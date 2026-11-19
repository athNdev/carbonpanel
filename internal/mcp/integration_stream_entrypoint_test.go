// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-387): executable tools/call evidence for
// the controller-owned authenticated byte-stream transport. A REAL controller
// FileService (with node-aware storage) is mounted behind rpc.NewAuthInterceptor;
// the gateway holds no storage handle.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/pkg/download"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/athNdev/carbon-panel/pkg/upload"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"
)

type controllerStreamFixture struct {
	store    *storage.Store
	enforcer *rbac.Enforcer
	ts       *httptest.Server
	root     string
}

// recordingStreamInterceptor captures the real typed RPCs that reach the
// controller behind the production auth interceptor. This is executable
// evidence: a tools/call that never reaches the controller cannot appear.
func recordingStreamInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if m, ok := req.Any().(proto.Message); ok {
				invocationRecorder.record(req.Spec().Procedure, m)
			}
			return next(ctx, req)
		}
	}
}

func newControllerStreamFixture(t *testing.T, extra ...connect.Interceptor) *controllerStreamFixture {
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
	ic := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())
	log := logger.New()
	root := filepath.Join(t.TempDir(), "data")
	if err := store.CreateServer(context.Background(), &storage.Server{
		ID: "srv-1", Name: "srv-1", DataPath: root, NodeID: "local",
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	uploadManager := upload.NewManager(t.TempDir(), time.Hour, 64<<20, log)
	downloadManager := download.NewManager(t.TempDir(), time.Hour, log)
	fileService := services.NewFileService(store, nil, uploadManager, downloadManager, log)
	fileService.SetStorageResolver(nodestorage.NewResolver(nil))
	uploadService := services.NewUploadService(uploadManager, &config.Config{
		Upload: config.UploadConfig{DefaultChunkSize: 1 << 20, MaxChunkSize: 1 << 20},
	}, log)

	// The auth interceptor and the recording interceptor both run on every
	// controller RPC: real scope enforcement plus real invocation evidence.
	chain := []connect.Interceptor{ic, recordingStreamInterceptor()}
	chain = append(chain, extra...)
	interceptors := connect.WithInterceptors(chain...)
	mux := http.NewServeMux()
	mux.Handle("/carbonpanel.v1.AuthService/GetAuthStatus", connect.NewUnaryHandler(
		"/carbonpanel.v1.AuthService/GetAuthStatus",
		func(context.Context, *connect.Request[v1.GetAuthStatusRequest]) (*connect.Response[v1.GetAuthStatusResponse], error) {
			return connect.NewResponse(&v1.GetAuthStatusResponse{LocalAuthEnabled: true}), nil
		}, interceptors))
	filePath, fileHandler := carbonpanelv1connect.NewFileServiceHandler(fileService, interceptors)
	mux.Handle(filePath, fileHandler)
	uploadPath, uploadHandler := carbonpanelv1connect.NewUploadServiceHandler(uploadService, interceptors)
	mux.Handle(uploadPath, uploadHandler)
	ts := httptest.NewServer(mux)
	// Stop the real services before TempDir cleanup so any still-open stream
	// handles are released (no locked files on Windows, no leaked goroutines).
	t.Cleanup(func() {
		ts.Close()
		fileService.Stop()
		uploadManager.Stop()
		downloadManager.Stop()
	})
	return &controllerStreamFixture{store: store, enforcer: enforcer, ts: ts, root: root}
}

func (f *controllerStreamFixture) session(t *testing.T, userID, role string, perms ...rbac.Permission) *sdkmcp.ClientSession {
	t.Helper()
	token := seedToken(t, f.store, f.enforcer, userID, role, perms)
	clients, err := NewClients(f.ts.Client(), f.ts.URL, token)
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	return connectStdioSession(t, NewServer(clients), context.Background())
}

func resultString(out ToolOutput, key string) string {
	m, _ := out.Result.(map[string]any)
	s, _ := m[key].(string)
	return s
}

func resultInt64(out ToolOutput, key string) int64 {
	m, _ := out.Result.(map[string]any)
	switch v := m[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func assertToolError(t *testing.T, out ToolOutput, tool string) {
	t.Helper()
	if out.Status != "error" || out.Error == nil {
		t.Fatalf("%s: expected refusal, got %+v", tool, out)
	}
}

func TestIntegrationControllerStreamEntryPoint(t *testing.T) {
	f := newControllerStreamFixture(t)
	payload := bytes.Repeat([]byte("carbonpanel-controller-stream-0123456789"), 20000)[:300<<10]
	want := sha256.Sum256(payload)
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer func() { _ = acc.Close() }()
	if err := acc.WriteFile("worlds/large.bin", payload); err != nil {
		t.Fatalf("seed: %v", err)
	}

	session := f.session(t, "u-stream", "admin")
	invocationRecorder.enable()
	defer invocationRecorder.disable()
	before := len(invocationRecorder.snapshot())

	opened := callTool(t, session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/large.bin", "total_size": len(payload),
	})
	assertSuccess(t, opened, "scoped_stream_open")
	handle := resultString(opened, "session_handle")
	if handle == "" {
		t.Fatalf("open did not return a handle: %+v", opened)
	}
	got := []byte{}
	digest := ""
	for {
		chunkOut := callTool(t, session, "scoped_stream_chunk", map[string]any{"session_handle": handle})
		assertSuccess(t, chunkOut, "scoped_stream_chunk")
		raw, err := base64.StdEncoding.DecodeString(resultString(chunkOut, "data"))
		if err != nil {
			t.Fatalf("chunk base64: %v", err)
		}
		got = append(got, raw...)
		m, _ := chunkOut.Result.(map[string]any)
		if done, _ := m["done"].(bool); done {
			digest = resultString(chunkOut, "sha256")
			break
		}
	}
	if !bytes.Equal(got, payload) || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("controller stream mismatch: got %d bytes sha=%s", len(got), digest)
	}
	commit := callTool(t, session, "scoped_stream_commit", map[string]any{
		"session_handle": handle, "destination": "worlds", "filename": "copy.bin", "confirm": true,
	})
	assertSuccess(t, commit, "scoped_stream_commit")
	if resultInt64(commit, "bytes") != int64(len(payload)) || resultString(commit, "sha256") != digest {
		t.Fatalf("commit mismatch: %+v", commit)
	}
	reached := map[string]bool{}
	for _, rec := range invocationRecorder.snapshot()[before:] {
		reached[rec.Procedure] = true
	}
	for _, proc := range []string{
		"/carbonpanel.v1.FileService/OpenStream",
		"/carbonpanel.v1.FileService/ReadStreamFrame",
		"/carbonpanel.v1.UploadService/InitUpload",
		"/carbonpanel.v1.UploadService/UploadChunk",
		"/carbonpanel.v1.FileService/SaveUploadedFile",
	} {
		if !reached[proc] {
			t.Errorf("controller RPC not reached: %s", proc)
		}
	}
	if reached["/carbonpanel.v1.FileService/GetFile"] {
		t.Error("frame path re-read the whole file with unary GetFile")
	}
	t.Logf("OSS-387 entry point: %d bytes, sha=%s, controller RPCs reached", len(got), digest)
}

func TestIntegrationControllerStreamRefusals(t *testing.T) {
	f := newControllerStreamFixture(t)
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer func() { _ = acc.Close() }()
	payload := bytes.Repeat([]byte("x"), 300<<10)
	if err := acc.WriteFile("worlds/refuse.bin", payload); err != nil {
		t.Fatalf("seed: %v", err)
	}
	admin := f.session(t, "u-admin-stream", "admin")

	opened := callTool(t, admin, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	})
	assertSuccess(t, opened, "scoped_stream_open")
	handle := resultString(opened, "session_handle")
	assertSuccess(t, callTool(t, admin, "scoped_stream_chunk", map[string]any{"session_handle": handle}), "first chunk")

	// Cancel mid-transfer.
	assertSuccess(t, callTool(t, admin, "scoped_stream_cancel", map[string]any{"session_handle": handle, "confirm": true}), "cancel")
	assertToolError(t, callTool(t, admin, "scoped_stream_chunk", map[string]any{"session_handle": handle}), "canceled chunk")

	// Cap refusal.
	assertToolError(t, callTool(t, admin, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": (64 << 30) + 1,
	}), "oversize open")

	// Cross-owner refusal via a second authenticated principal.
	other := f.session(t, "u-other-stream", "admin")
	assertToolError(t, callTool(t, other, "scoped_stream_chunk", map[string]any{"session_handle": handle}), "cross-owner chunk")

	// Cross-server refusal via an object-scoped token.
	srv2 := f.session(t, "u-srv2-stream", "srv2-reader",
		rbac.Permission{Resource: "files", Action: "read", ObjectID: "srv-2"})
	assertToolError(t, callTool(t, srv2, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	}), "cross-server open")

	// Read-only principal: may open/read, but commit must be refused with zero
	// controller writes (fresh files:create[server_id] check).
	ro := f.session(t, "u-readonly-stream", "files-reader",
		rbac.Permission{Resource: "files", Action: "read", ObjectID: "srv-1"},
		rbac.Permission{Resource: "uploads", Action: "create", ObjectID: "*"})
	roOpen := callTool(t, ro, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	})
	assertSuccess(t, roOpen, "read-only open")
	roHandle := resultString(roOpen, "session_handle")
	assertSuccess(t, callTool(t, ro, "scoped_stream_chunk", map[string]any{"session_handle": roHandle}), "read-only chunk")
	assertToolError(t, callTool(t, ro, "scoped_stream_commit", map[string]any{
		"session_handle": roHandle, "destination": "worlds", "filename": "blocked.bin", "confirm": true,
	}), "read-only commit")
	if _, err := os.Stat(filepath.Join(f.root, "worlds", "blocked.bin")); !os.IsNotExist(err) {
		t.Fatalf("read-only commit wrote bytes despite refusal: %v", err)
	}

	// Permission revoked between open and a subsequent chunk: every frame is
	// re-authorized by the controller interceptor, so it must be refused.
	revoked := f.session(t, "u-revoke-stream", "revoked-reader",
		rbac.Permission{Resource: "files", Action: "read", ObjectID: "srv-1"})
	revOpen := callTool(t, revoked, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	})
	assertSuccess(t, revOpen, "pre-revocation open")
	revHandle := resultString(revOpen, "session_handle")
	if err := f.enforcer.SetPermissionsForRole("revoked-reader", nil); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	assertToolError(t, callTool(t, revoked, "scoped_stream_chunk", map[string]any{"session_handle": revHandle}), "revoked chunk")
	assertToolError(t, callTool(t, revoked, "scoped_stream_commit", map[string]any{
		"session_handle": revHandle, "destination": "worlds", "filename": "blocked.bin", "confirm": true,
	}), "revoked commit")
	if _, err := os.Stat(filepath.Join(f.root, "worlds", "blocked.bin")); !os.IsNotExist(err) {
		t.Fatalf("revoked commit wrote bytes despite refusal: %v", err)
	}

	t.Log("OSS-387 refusals via tools/call: cancel, cap, cross-owner, cross-server, read-only commit, revoked mid-session, zero writes")
}

var _ = errors.New

// TestIntegrationControllerStreamServerBinding is a REAL authenticated
// tools-layer/RPC test: the same principal opens a session on srv-1, then
// presents server_id=srv-2 on frame and close. The controller must refuse with
// no bytes and no state change; the correct server still completes the stream.
func TestIntegrationControllerStreamServerBinding(t *testing.T) {
	f := newControllerStreamFixture(t)
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer func() { _ = acc.Close() }()
	payload := bytes.Repeat([]byte("b"), 64<<10)
	if err := acc.WriteFile("worlds/bind.bin", payload); err != nil {
		t.Fatalf("seed: %v", err)
	}

	token := seedToken(t, f.store, f.enforcer, "u-bind", "admin", nil)
	clients, err := NewClients(f.ts.Client(), f.ts.URL, token)
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	ctx := context.Background()
	opened, err := clients.File.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: "srv-1", Path: "worlds/bind.bin", TotalSize: int64(len(payload)),
	}))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	sid := opened.Msg.GetSessionId()

	badFrame := connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: sid, FrameIndex: 0, ServerId: "srv-2",
	})
	if _, err := clients.File.ReadStreamFrame(ctx, badFrame); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("cross-server frame was not denied: code=%v err=%v", connect.CodeOf(err), err)
	}
	// No bytes were consumed: the frame at index 0 still works for srv-1.
	frame, err := clients.File.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: sid, FrameIndex: 0, ServerId: "srv-1",
	}))
	if err != nil {
		t.Fatalf("correct-server frame after refusal: %v", err)
	}
	if !bytes.Equal(frame.Msg.GetData(), payload[:len(frame.Msg.GetData())]) || len(frame.Msg.GetData()) == 0 {
		t.Fatalf("cross-server refusal altered stream data: %d bytes", len(frame.Msg.GetData()))
	}
	if _, err := clients.File.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: sid, ServerId: "srv-2",
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition && connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("cross-server close was not denied: code=%v err=%v", connect.CodeOf(err), err)
	}
	if _, err := clients.File.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: sid, ServerId: "srv-1",
	})); err != nil {
		t.Fatalf("correct-server close: %v", err)
	}
	t.Log("OSS-387 server binding: mismatched server_id denied on frame and close with zero bytes and no state change")
}

// TestIntegrationControllerStreamSizeContract proves the declared total_size
// is an admission UPPER BOUND, not an exact byte count. A short source still
// completes with Done/SHA over the ACTUAL bytes, and the composed upload/commit
// uses that actual count rather than echoing the declared bound. A zero-byte
// stream yields Done with an empty digest but an explicit commit refusal.
func TestIntegrationControllerStreamSizeContract(t *testing.T) {
	f := newControllerStreamFixture(t)
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer func() { _ = acc.Close() }()
	payload := bytes.Repeat([]byte("sz"), 32<<10) // 64 KiB actual
	want := sha256.Sum256(payload)
	if err := acc.WriteFile("worlds/short.bin", payload); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := acc.WriteFile("worlds/empty.bin", nil); err != nil {
		t.Fatalf("seed empty: %v", err)
	}

	session := f.session(t, "u-size", "admin")
	invocationRecorder.enable()
	defer invocationRecorder.disable()
	before := len(invocationRecorder.snapshot())

	// Declare 1 MiB for a 64 KiB source: the bound admits the open, and the
	// final frame reports the true byte count and digest.
	opened := callTool(t, session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/short.bin", "total_size": 1 << 20,
	})
	assertSuccess(t, opened, "scoped_stream_open")
	handle := resultString(opened, "session_handle")
	got := []byte{}
	digest := ""
	actualOffset := int64(0)
	for {
		out := callTool(t, session, "scoped_stream_chunk", map[string]any{"session_handle": handle})
		assertSuccess(t, out, "scoped_stream_chunk")
		raw, err := base64.StdEncoding.DecodeString(resultString(out, "data"))
		if err != nil {
			t.Fatalf("chunk base64: %v", err)
		}
		got = append(got, raw...)
		m, _ := out.Result.(map[string]any)
		if done, _ := m["done"].(bool); done {
			digest = resultString(out, "sha256")
			actualOffset = resultInt64(out, "offset")
			break
		}
	}
	if !bytes.Equal(got, payload) || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("short source mismatch: got=%d sha=%s", len(got), digest)
	}
	if actualOffset != int64(len(payload)) {
		t.Fatalf("final offset %d is not the actual size %d", actualOffset, len(payload))
	}
	commit := callTool(t, session, "scoped_stream_commit", map[string]any{
		"session_handle": handle, "destination": "worlds", "filename": "short-copy.bin", "confirm": true,
	})
	assertSuccess(t, commit, "scoped_stream_commit")
	if resultInt64(commit, "bytes") != int64(len(payload)) || resultInt64(commit, "actual_size") != int64(len(payload)) {
		t.Fatalf("commit did not use the actual transferred size: %+v", commit)
	}

	reached := map[string]bool{}
	for _, rec := range invocationRecorder.snapshot()[before:] {
		reached[rec.Procedure] = true
	}
	for _, proc := range []string{
		"/carbonpanel.v1.FileService/OpenStream",
		"/carbonpanel.v1.FileService/ReadStreamFrame",
		"/carbonpanel.v1.UploadService/InitUpload",
		"/carbonpanel.v1.FileService/SaveUploadedFile",
	} {
		if !reached[proc] {
			t.Errorf("size-contract path did not reach %s", proc)
		}
	}

	// Zero-byte source: open and read succeed with done=true and the empty
	// digest, but commit is explicitly refused (no bytes to commit).
	zeroOpen := callTool(t, session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/empty.bin", "total_size": 0,
	})
	assertSuccess(t, zeroOpen, "zero-byte open")
	zeroHandle := resultString(zeroOpen, "session_handle")
	zeroChunk := callTool(t, session, "scoped_stream_chunk", map[string]any{"session_handle": zeroHandle})
	assertSuccess(t, zeroChunk, "zero-byte chunk")
	zm, _ := zeroChunk.Result.(map[string]any)
	if done, _ := zm["done"].(bool); !done {
		t.Fatalf("zero-byte chunk did not report done: %+v", zeroChunk)
	}
	zeroCommit := callTool(t, session, "scoped_stream_commit", map[string]any{
		"session_handle": zeroHandle, "destination": "worlds", "filename": "empty.bin", "confirm": true,
	})
	_ = assertStructuredError(t, zeroCommit, "scoped_stream_commit", ErrKindInvalidArguments)
	t.Logf("OSS-387 size contract: declared 1 MiB, actual %d bytes, SHA and commit used actual bytes; zero-byte stream refused at commit", len(payload))
}
