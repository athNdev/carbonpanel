// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/pkg/download"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/athNdev/carbon-panel/pkg/upload"
)

// tenantStreamEnv mounts the REAL controller FileService (including the
// controller-owned bounded stream sessions) behind rpc.NewAuthInterceptor and
// a generated Connect client. Two synthetic principals hold disjoint
// server-scoped grants. The only fixtures are local temp files, an in-memory
// SQLite store and a mock-free loopback HTTP server.
type tenantStreamEnv struct {
	store        *storage.Store
	enforcer     *rbac.Enforcer
	ts           *httptest.Server
	client       carbonpanelv1connect.FileServiceClient
	rootA        string
	rootB        string
	tokenA       string
	tokenB       string
	payloadA     []byte
	payloadB     []byte
	handlerCalls atomic.Int64
}

func newTenantStreamEnv(t *testing.T) *tenantStreamEnv {
	t.Helper()
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	authManager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local: config.LocalConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	env := &tenantStreamEnv{store: store, enforcer: enforcer}
	env.payloadA = bytes.Repeat([]byte("tenant-a-secret-payload-0123456789"), 40000)
	env.payloadB = bytes.Repeat([]byte("tenant-b-secret-payload-9876543210"), 40000)
	env.rootA = filepath.Join(t.TempDir(), "tenant-a-data")
	env.rootB = filepath.Join(t.TempDir(), "tenant-b-data")
	for _, tc := range []struct {
		serverID string
		root     string
		payload  []byte
	}{
		{"srv-a", env.rootA, env.payloadA},
		{"srv-b", env.rootB, env.payloadB},
	} {
		if err := store.CreateServer(context.Background(), &storage.Server{
			ID: tc.serverID, Name: tc.serverID, DataPath: tc.root, NodeID: "local",
		}); err != nil {
			t.Fatalf("CreateServer(%s): %v", tc.serverID, err)
		}
		acc, err := nodestorage.NewLocal(tc.root)
		if err != nil {
			t.Fatalf("NewLocal(%s): %v", tc.serverID, err)
		}
		if err := acc.WriteFile("worlds/tenant.bin", tc.payload); err != nil {
			t.Fatalf("seed(%s): %v", tc.serverID, err)
		}
		_ = acc.Close()
	}

	log := logger.New()
	uploadManager := upload.NewManager(t.TempDir(), time.Hour, 64<<20, log)
	downloadManager := download.NewManager(t.TempDir(), time.Hour, log)
	fileService := services.NewFileService(store, nil, uploadManager, downloadManager, log)
	fileService.SetStorageResolver(nodestorage.NewResolver(nil))

	// The production auth interceptor is first: an authorization refusal must
	// never even reach the counting interceptor or the FileService handler.
	// The counter therefore distinguishes an interceptor refusal (zero handler
	// work) from a service-level ownership refusal (handler runs but returns no
	// bytes).
	authInterceptor := NewAuthInterceptor(authManager, enforcer, store, log)
	counting := func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			env.handlerCalls.Add(1)
			return next(ctx, req)
		}
	}
	path, handler := carbonpanelv1connect.NewFileServiceHandler(fileService,
		connect.WithInterceptors(authInterceptor, connect.UnaryInterceptorFunc(counting)))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	env.ts = httptest.NewServer(mux)
	env.client = carbonpanelv1connect.NewFileServiceClient(env.ts.Client(), env.ts.URL)

	grantA := []rbac.Permission{{Resource: rbac.ResourceFiles, Action: rbac.ActionRead, ObjectID: "srv-a"}}
	grantB := []rbac.Permission{{Resource: rbac.ResourceFiles, Action: rbac.ActionRead, ObjectID: "srv-b"}}
	env.tokenA, env.tokenB = seedTenantToken(t, store, enforcer, "u-tenant-a", "tenant-a-reader", grantA),
		seedTenantToken(t, store, enforcer, "u-tenant-b", "tenant-b-reader", grantB)

	t.Cleanup(func() {
		env.ts.Close()
		fileService.Stop()
		uploadManager.Stop()
		downloadManager.Stop()
	})
	return env
}

func seedTenantToken(t *testing.T, store *storage.Store, enforcer *rbac.Enforcer, userID, role string, perms []rbac.Permission) string {
	t.Helper()
	ctx := context.Background()
	if err := enforcer.SetPermissionsForRole(role, perms); err != nil {
		t.Fatalf("SetPermissionsForRole(%s): %v", role, err)
	}
	raw := "dp_" + userID + "-token"
	if err := store.CreateUser(ctx, &storage.User{ID: userID, Username: userID, AuthProvider: "local", IsActive: true}); err != nil {
		t.Fatalf("CreateUser(%s): %v", userID, err)
	}
	if err := store.AssignRole(ctx, userID, role, "manual"); err != nil {
		t.Fatalf("AssignRole(%s): %v", userID, err)
	}
	sum := sha256.Sum256([]byte(raw))
	if err := store.CreateAPIToken(ctx, &storage.APIToken{
		UserID: userID, Name: "tenant-stream-test", TokenHash: hex.EncodeToString(sum[:]),
	}); err != nil {
		t.Fatalf("CreateAPIToken(%s): %v", userID, err)
	}
	return raw
}

func (e *tenantStreamEnv) open(t *testing.T, bearer, serverID, path string, size int64) (string, *connect.Error) {
	t.Helper()
	req := connect.NewRequest(&v1.OpenStreamRequest{ServerId: serverID, Path: path, TotalSize: size})
	req.Header().Set("Authorization", "Bearer "+bearer)
	res, err := e.client.OpenStream(context.Background(), req)
	if err != nil {
		connErr, ok := err.(*connect.Error)
		if !ok {
			t.Fatalf("OpenStream returned %T: %v", err, err)
		}
		return "", connErr
	}
	return res.Msg.GetSessionId(), nil
}

func (e *tenantStreamEnv) openOK(t *testing.T, bearer, serverID, path string, size int64) string {
	t.Helper()
	handle, connErr := e.open(t, bearer, serverID, path, size)
	if connErr != nil {
		t.Fatalf("OpenStream(%s,%s): %v", bearer, serverID, connErr)
	}
	if handle == "" {
		t.Fatal("OpenStream returned an empty handle")
	}
	return handle
}

func (e *tenantStreamEnv) frame(t *testing.T, bearer, serverID, handle string, index int32) (*v1.ReadStreamFrameResponse, *connect.Error) {
	t.Helper()
	req := connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: handle, FrameIndex: index, ServerId: serverID,
	})
	req.Header().Set("Authorization", "Bearer "+bearer)
	res, err := e.client.ReadStreamFrame(context.Background(), req)
	if err != nil {
		connErr, ok := err.(*connect.Error)
		if !ok {
			t.Fatalf("ReadStreamFrame returned %T: %v", err, err)
		}
		return nil, connErr
	}
	return res.Msg, nil
}

func (e *tenantStreamEnv) frameOK(t *testing.T, bearer, serverID, handle string, index int32) *v1.ReadStreamFrameResponse {
	t.Helper()
	res, connErr := e.frame(t, bearer, serverID, handle, index)
	if connErr != nil {
		t.Fatalf("ReadStreamFrame(%s,%s,%d): %v", bearer, serverID, index, connErr)
	}
	return res
}

func assertPermissionDenied(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected permission_denied, got success")
	}
	connErr, ok := err.(*connect.Error)
	if !ok || connErr.Code() != connect.CodePermissionDenied {
		t.Fatalf("expected permission_denied, got %T: %v", err, err)
	}
}

// TestTwoTenantControllerStreamRevocationAndDelegation is the two-principal
// revocation-during-stream gap. A holds srv-a, B holds srv-b. While A's
// authenticated frame stream is live, A's scope is revoked and delegated to B.
// The controller must immediately fail A closed with zero bytes, refuse to let
// B take over A's live handle (no cross-tenant read), and leave B's own tenant
// unaffected.
func TestTwoTenantControllerStreamRevocationAndDelegation(t *testing.T) {
	e := newTenantStreamEnv(t)

	// A opens a live stream over its own tenant's file and reads one frame.
	handleA := e.openOK(t, e.tokenA, "srv-a", "worlds/tenant.bin", int64(len(e.payloadA)))
	frame0 := e.frameOK(t, e.tokenA, "srv-a", handleA, 0)
	if len(frame0.GetData()) == 0 || !bytes.HasPrefix(frame0.GetData(), []byte("tenant-a-secret")) {
		t.Fatalf("A's first frame is not A's tenant data: %d bytes", len(frame0.GetData()))
	}
	remaining := e.payloadA[len(frame0.GetData()):]

	// Cross-tenant open is denied before any revocation: B cannot read srv-a.
	if _, connErr := e.open(t, e.tokenB, "srv-a", "worlds/tenant.bin", int64(len(e.payloadA))); connErr == nil {
		t.Fatal("B opened A's server with only a srv-b grant")
	} else {
		assertPermissionDenied(t, connErr)
	}

	// B's own tenant stream is live too, so we can prove it survives.
	handleB := e.openOK(t, e.tokenB, "srv-b", "worlds/tenant.bin", int64(len(e.payloadB)))

	// Revoke A's scope and delegate it to B while both streams are open.
	grantA := []rbac.Permission{{Resource: rbac.ResourceFiles, Action: rbac.ActionRead, ObjectID: "srv-a"}}
	grantABoth := []rbac.Permission{
		{Resource: rbac.ResourceFiles, Action: rbac.ActionRead, ObjectID: "srv-b"},
		{Resource: rbac.ResourceFiles, Action: rbac.ActionRead, ObjectID: "srv-a"},
	}
	if err := e.enforcer.SetPermissionsForRole("tenant-a-reader", nil); err != nil {
		t.Fatalf("revoke A: %v", err)
	}
	if err := e.enforcer.SetPermissionsForRole("tenant-b-reader", grantABoth); err != nil {
		t.Fatalf("delegate A's scope to B: %v", err)
	}

	// A's next frame is refused by the production interceptor with zero bytes.
	before := e.handlerCalls.Load()
	respA, connErrA := e.frame(t, e.tokenA, "srv-a", handleA, 1)
	assertPermissionDenied(t, connErrA)
	if respA != nil && len(respA.GetData()) != 0 {
		t.Fatalf("revoked A received %d bytes", len(respA.GetData()))
	}
	if got := e.handlerCalls.Load(); got != before {
		t.Fatalf("refused frame reached the FileService handler %d time(s)", got-before)
	}
	// A cannot open a fresh stream while revoked either.
	if _, connErr := e.open(t, e.tokenA, "srv-a", "worlds/tenant.bin", int64(len(e.payloadA))); connErr == nil {
		t.Fatal("revoked A opened a new srv-a stream")
	} else {
		assertPermissionDenied(t, connErr)
	}

	// Delegation is effective at the authorization boundary: B can open a
	// BRAND NEW srv-a stream after the grant transfer. That does not make A's
	// live session transferable.
	delegatedHandle := e.openOK(t, e.tokenB, "srv-a", "worlds/tenant.bin", int64(len(e.payloadA)))
	if got := e.frameOK(t, e.tokenB, "srv-a", delegatedHandle, 0); len(got.GetData()) == 0 {
		t.Fatal("delegated grant did not allow a fresh B-owned stream")
	}
	if delegatedHandle == handleA {
		t.Fatal("delegation reused A's live session handle")
	}

	// B now holds A's scope but must not inherit A's live session: the handle
	// is bound to its owner, so B gets zero bytes and no cross-tenant read.
	respB, connErrB := e.frame(t, e.tokenB, "srv-a", handleA, 1)
	if connErrB == nil {
		t.Fatal("B read A's live frame stream with the delegated scope")
	}
	if !strings.Contains(connErrB.Message(), "another principal") {
		t.Fatalf("refusal is not the cross-owner check: %v", connErrB)
	}
	if respB != nil && len(respB.GetData()) != 0 {
		t.Fatalf("cross-tenant takeover received %d bytes", len(respB.GetData()))
	}
	// A delegated handle cannot be closed by B either.
	_, connErrClose := e.client.CloseStream(context.Background(), withBearer(
		&v1.CloseStreamRequest{SessionId: handleA, ServerId: "srv-a"}, e.tokenB))
	assertPermissionDenied(t, connErrClose)

	// B's own tenant stream is unaffected by the revocation/delegation.
	bFrame := e.frameOK(t, e.tokenB, "srv-b", handleB, 0)
	if len(bFrame.GetData()) == 0 || !bytes.HasPrefix(bFrame.GetData(), []byte("tenant-b-secret")) {
		t.Fatalf("B's own tenant frame changed: %d bytes", len(bFrame.GetData()))
	}

	// Restore A's grant and retry the SAME frame index: it must still return
	// the next intact slice, proving the refused calls consumed zero bytes.
	if err := e.enforcer.SetPermissionsForRole("tenant-a-reader", grantA); err != nil {
		t.Fatalf("restore A: %v", err)
	}
	frame1 := e.frameOK(t, e.tokenA, "srv-a", handleA, 1)
	want := remaining
	if len(frame1.GetData()) < len(want) {
		want = want[:len(frame1.GetData())]
	}
	if !bytes.Equal(frame1.GetData(), want) {
		t.Fatalf("frame 1 changed after refused calls: got %d bytes want %d", len(frame1.GetData()), len(want))
	}
}

func withBearer[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
}
