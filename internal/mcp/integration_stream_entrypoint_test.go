// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane: end-to-end evidence through the REGISTERED
// scoped-streaming MCP tools. No helper is called directly: every byte and
// every refusal below is produced by a real tools/call over the SDK stdio wire
// with a real dp_ token through the production auth interceptor.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var streamFixtureSeq atomic.Int64

type streamEntrypointFixture struct {
	store     *storage.Store
	enforcer  *rbac.Enforcer
	ts        *httptest.Server
	transfer  *StreamTransfer
	accessor  nodestorage.Accessor
	now       *time.Time
	clients   *Clients
	session   *sdkmcp.ClientSession
}

func newStreamEntrypointFixture(t *testing.T, capBytes int64) *streamEntrypointFixture {
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
	interceptor := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())

	mux := http.NewServeMux()
	mux.Handle("/carbonpanel.v1.AuthService/GetAuthStatus", connect.NewUnaryHandler(
		"/carbonpanel.v1.AuthService/GetAuthStatus",
		func(context.Context, *connect.Request[v1.GetAuthStatusRequest]) (*connect.Response[v1.GetAuthStatusResponse], error) {
			return connect.NewResponse(&v1.GetAuthStatusResponse{LocalAuthEnabled: true}), nil
		}, connect.WithInterceptors(interceptor)))
	mux.Handle("/carbonpanel.v1.FileService/InitFileDownload", connect.NewUnaryHandler(
		"/carbonpanel.v1.FileService/InitFileDownload",
		func(_ context.Context, req *connect.Request[v1.InitFileDownloadRequest]) (*connect.Response[v1.InitFileDownloadResponse], error) {
			invocationRecorder.record("/carbonpanel.v1.FileService/InitFileDownload", req.Msg)
			return connect.NewResponse(&v1.InitFileDownloadResponse{SessionId: "typed-download-1"}), nil
		}, connect.WithInterceptors(interceptor)))
	mux.Handle("/carbonpanel.v1.FileService/GetFile", connect.NewUnaryHandler(
		"/carbonpanel.v1.FileService/GetFile",
		func(_ context.Context, req *connect.Request[v1.GetFileRequest]) (*connect.Response[v1.GetFileResponse], error) {
			invocationRecorder.record("/carbonpanel.v1.FileService/GetFile", req.Msg)
			return connect.NewResponse(&v1.GetFileResponse{}), nil
		}, connect.WithInterceptors(interceptor)))
	// Authorized production commit path (typed upload RPCs behind the real
	// interceptor). These handlers record every controller-side write so the
	// tests can assert zero writes on refusal.
	mux.Handle("/carbonpanel.v1.UploadService/InitUpload", connect.NewUnaryHandler(
		"/carbonpanel.v1.UploadService/InitUpload",
		func(_ context.Context, req *connect.Request[v1.InitUploadRequest]) (*connect.Response[v1.InitUploadResponse], error) {
			invocationRecorder.record("/carbonpanel.v1.UploadService/InitUpload", req.Msg)
			return connect.NewResponse(&v1.InitUploadResponse{SessionId: "upload-1"}), nil
		}, connect.WithInterceptors(interceptor)))
	mux.Handle("/carbonpanel.v1.UploadService/UploadChunk", connect.NewUnaryHandler(
		"/carbonpanel.v1.UploadService/UploadChunk",
		func(_ context.Context, req *connect.Request[v1.UploadChunkRequest]) (*connect.Response[v1.UploadChunkResponse], error) {
			invocationRecorder.record("/carbonpanel.v1.UploadService/UploadChunk", req.Msg)
			return connect.NewResponse(&v1.UploadChunkResponse{SessionId: "upload-1"}), nil
		}, connect.WithInterceptors(interceptor)))
	mux.Handle("/carbonpanel.v1.FileService/SaveUploadedFile", connect.NewUnaryHandler(
		"/carbonpanel.v1.FileService/SaveUploadedFile",
		func(_ context.Context, req *connect.Request[v1.SaveUploadedFileRequest]) (*connect.Response[v1.SaveUploadedFileResponse], error) {
			invocationRecorder.record("/carbonpanel.v1.FileService/SaveUploadedFile", req.Msg)
			return connect.NewResponse(&v1.SaveUploadedFileResponse{Path: req.Msg.GetDestinationPath()}), nil
		}, connect.WithInterceptors(interceptor)))

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	root := filepath.Join(t.TempDir(), "data")
	acc, err := nodestorage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = acc.Close() })
	now := time.Now()
	transfer, err := NewStreamTransfer(acc, capBytes, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewStreamTransfer: %v", err)
	}

	f := &streamEntrypointFixture{
		store: store, enforcer: enforcer, ts: ts, transfer: transfer,
		accessor: acc, now: &now,
	}
	f.clients, f.session = f.newSession(t, fmt.Sprintf("u-stream-%d", streamFixtureSeq.Add(1)), "admin")
	return f
}

// newSession builds a real dp_ token for the user and returns a full MCP
// session whose scoped tools share the fixture's W17 transfer.
func (f *streamEntrypointFixture) newSession(t *testing.T, userID, role string, perms ...rbac.Permission) (*Clients, *sdkmcp.ClientSession) {
	t.Helper()
	token := seedToken(t, f.store, f.enforcer, userID, role, perms)
	clients, err := NewClients(f.ts.Client(), f.ts.URL, token)
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	clients.SetStreamTransfer(f.transfer)
	return clients, connectStdioSession(t, NewServer(clients), context.Background())
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

// TestIntegrationRegisteredStreamEntryPoint drives the full scoped transfer
// through tools/call only: registered tool -> authenticated token -> typed RPC
// -> W17 streaming filesystem -> bytes back through tools/call.
func TestIntegrationRegisteredStreamEntryPoint(t *testing.T) {
	invocationRecorder.enable()
	defer invocationRecorder.disable()
	f := newStreamEntrypointFixture(t, 1<<20)
	payload := bytes.Repeat([]byte("carbonpanel-scoped-mcp-0123456789abcdef"), 20000)[:300<<10]
	want := sha256.Sum256(payload)
	if err := f.accessor.WriteFile("worlds/large.bin", payload); err != nil {
		t.Fatalf("seed: %v", err)
	}

	before := len(invocationRecorder.snapshot())
	opened := callTool(t, f.session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/large.bin", "total_size": len(payload),
	})
	assertSuccess(t, opened, "scoped_stream_open")
	handle := resultString(opened, "session_handle")
	if handle == "" {
		t.Fatalf("open did not return a session handle: %+v", opened)
	}

	got := []byte{}
	digest := ""
	for {
		chunkOut := callTool(t, f.session, "scoped_stream_chunk", map[string]any{"session_handle": handle})
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
		t.Fatalf("tools/call stream mismatch: got %d bytes sha=%s", len(got), digest)
	}
	commit := callTool(t, f.session, "scoped_stream_commit", map[string]any{
		"session_handle": handle, "destination": "worlds", "filename": "copy.bin", "confirm": true,
	})
	assertSuccess(t, commit, "scoped_stream_commit")
	if resultInt64(commit, "bytes") != int64(len(payload)) || resultString(commit, "sha256") != digest {
		t.Fatalf("commit outcome mismatch: %+v", commit)
	}
	if resultString(commit, "authorization") != "production typed upload + server-scoped save" {
		t.Fatalf("commit did not use the production authorization path: %+v", commit)
	}
	// The write went through the typed SaveUploadedFile RPC, not a helper.
	saveReached := false
	for _, rec := range invocationRecorder.snapshot()[before:] {
		if rec.Procedure == "/carbonpanel.v1.FileService/SaveUploadedFile" {
			saveReached = true
		}
	}
	if !saveReached {
		t.Fatal("commit did not reach the typed server-scoped SaveUploadedFile RPC")
	}
	reached := map[string]bool{}
	for _, rec := range invocationRecorder.snapshot()[before:] {
		reached[rec.Procedure] = true
	}
	if !reached["/carbonpanel.v1.FileService/InitFileDownload"] || !reached["/carbonpanel.v1.FileService/SaveUploadedFile"] {
		t.Fatalf("registered tools did not reach typed methods: %+v", reached)
	}
	if reached["/carbonpanel.v1.FileService/GetFile"] {
		t.Fatal("frames re-read the full file with unary GetFile instead of using the owned session")
	}
	t.Logf("registered stream entry point: %d bytes, sha=%s, typed methods reached", len(got), digest)
}

func TestIntegrationRegisteredStreamRefusals(t *testing.T) {
	f := newStreamEntrypointFixture(t, 1<<20)
	payload := bytes.Repeat([]byte("x"), 300<<10)
	if err := f.accessor.WriteFile("worlds/refuse.bin", payload); err != nil {
		t.Fatalf("seed: %v", err)
	}
	opened := callTool(t, f.session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	})
	assertSuccess(t, opened, "scoped_stream_open")
	handle := resultString(opened, "session_handle")

	first := callTool(t, f.session, "scoped_stream_chunk", map[string]any{"session_handle": handle})
	assertSuccess(t, first, "scoped_stream_chunk")
	cancel := callTool(t, f.session, "scoped_stream_cancel", map[string]any{"session_handle": handle, "confirm": true})
	assertSuccess(t, cancel, "scoped_stream_cancel")
	assertToolError(t, callTool(t, f.session, "scoped_stream_chunk", map[string]any{"session_handle": handle}), "canceled chunk")

	overCap := callTool(t, f.session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": (1 << 20) + 1,
	})
	assertToolError(t, overCap, "oversize open")

	h2Out := callTool(t, f.session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	})
	assertSuccess(t, h2Out, "scoped_stream_open")
	h2 := resultString(h2Out, "session_handle")
	*f.now = f.now.Add(2 * time.Minute)
	assertToolError(t, callTool(t, f.session, "scoped_stream_chunk", map[string]any{"session_handle": h2}), "expired chunk")
	*f.now = time.Now()

	// Cross-owner refusal: a different real dp_ token cannot use the handle.
	_, other := f.newSession(t, "u-other", "admin")
	assertToolError(t, callTool(t, other, "scoped_stream_chunk", map[string]any{"session_handle": h2}), "cross-owner chunk")

	// Cross-server refusal: a scoped token for srv-2 cannot open a srv-1
	// session because the typed RPC interceptor checks the object scope.
	_, srv2 := f.newSession(t, "u-srv2", "srv2-reader",
		rbac.Permission{Resource: "files", Action: "read", ObjectID: "srv-2"})
	assertToolError(t, callTool(t, srv2, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	}), "cross-server open")

	// Read-only principal: can open/chunk with files:read, but commit must be
	// refused by the production upload path with ZERO controller writes.
	_, readOnly := f.newSession(t, "u-readonly", "files-reader",
		rbac.Permission{Resource: "files", Action: "read", ObjectID: "srv-1"})
	roOpen := callTool(t, readOnly, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	})
	assertSuccess(t, roOpen, "read-only open")
	roHandle := resultString(roOpen, "session_handle")
	_ = callTool(t, readOnly, "scoped_stream_chunk", map[string]any{"session_handle": roHandle})
	writesBefore := len(invocationRecorder.snapshot())
	roCommit := callTool(t, readOnly, "scoped_stream_commit", map[string]any{
		"session_handle": roHandle, "destination": "worlds", "filename": "blocked.bin", "confirm": true,
	})
	assertToolError(t, roCommit, "read-only commit")
	for _, rec := range invocationRecorder.snapshot()[writesBefore:] {
		if rec.Procedure == "/carbonpanel.v1.FileService/SaveUploadedFile" ||
			rec.Procedure == "/carbonpanel.v1.UploadService/InitUpload" {
			t.Fatalf("read-only commit reached a write RPC: %s", rec.Procedure)
		}
	}

	// Revoked scope: no grants at all, so even open is refused.
	_, revoked := f.newSession(t, "u-revoked", "revoked")
	assertToolError(t, callTool(t, revoked, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/refuse.bin", "total_size": len(payload),
	}), "revoked open")

	t.Log("registered refusals observed via tools/call: cancel, cap, expiry, cross-owner, cross-server, read-only commit (zero writes), revoked scope")
}

// runRegisteredStreamEvidence is called by the unified evidence harness so a
// VERIFIED label on download/upload surfaces requires real tools/call entry
// point evidence — not a direct call to the transfer helper.
func runRegisteredStreamEvidence(t *testing.T) bool {
	t.Helper()
	verified := false
	t.Run("registered-stream-entrypoint-evidence", func(st *testing.T) {
		TestIntegrationRegisteredStreamEntryPoint(st)
		TestIntegrationRegisteredStreamRefusals(st)
		verified = !st.Failed()
	})
	return verified
}

// TestIntegrationStreamToolsProductionWiring proves the scoped tools are
// mounted by the production NewServer constructor, with no fixture-only
// injection seam, and that the policy allowlist is bound before registration.
func TestIntegrationStreamToolsProductionWiring(t *testing.T) {
	clients, err := NewClients(nil, "http://controller.invalid", "dp_productiontoken")
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	server := NewServer(clients)
	allowed := IntegrationAllowedTools(server)
	for _, name := range []string{"scoped_stream_open", "scoped_stream_chunk", "scoped_stream_cancel", "scoped_stream_commit"} {
		if !allowed[name] {
			t.Errorf("production allowlist omits stream tool %s", name)
		}
	}
	// Secret-bearing / excluded tools must never be in the production set.
	for _, name := range []string{"send_command", "create_api_token", "list_nodes", "upload_to_mclogs"} {
		if allowed[name] {
			t.Errorf("excluded tool %s entered the production allowlist", name)
		}
	}
	session := connectStdioSession(t, server, context.Background())
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	found := map[string]bool{}
	for _, tool := range listed.Tools {
		found[tool.Name] = true
	}
	for _, name := range []string{"scoped_stream_open", "scoped_stream_chunk", "scoped_stream_cancel", "scoped_stream_commit"} {
		if !found[name] {
			t.Errorf("production server did not mount %s", name)
		}
	}
}
