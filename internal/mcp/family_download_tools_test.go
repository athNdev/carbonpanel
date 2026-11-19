// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370 download family): executable tools/call
// evidence for the five pending download operations. A REAL FileService (with
// node-aware storage, a download manager and the production auth interceptor)
// is mounted behind an SDK stdio session; the gateway holds no storage handle
// and writes no file. All bytes travel through typed controller frame RPCs.
package mcp

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

type downloadFixture struct {
	store           *storage.Store
	enforcer        *rbac.Enforcer
	ts              *httptest.Server
	root            string
	root2           string
	downloadManager *download.Manager
}

func newDownloadFixture(t *testing.T, ttl time.Duration, extra ...connect.Interceptor) *downloadFixture {
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

	base := t.TempDir()
	root := filepath.Join(base, "srv1-data")
	root2 := filepath.Join(base, "srv2-data")
	for _, p := range []string{root, root2} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s): %v", p, err)
		}
	}
	for _, srv := range []*storage.Server{
		{ID: "srv-1", Name: "srv-1", DataPath: root, NodeID: "local"},
		{ID: "srv-2", Name: "srv-2", DataPath: root2, NodeID: "local"},
	} {
		if err := store.CreateServer(context.Background(), srv); err != nil {
			t.Fatalf("CreateServer(%s): %v", srv.ID, err)
		}
	}

	log := logger.New()
	uploadManager := upload.NewManager(filepath.Join(base, "upload"), time.Hour, 64<<20, log)
	downloadManager := download.NewManager(filepath.Join(base, "download"), ttl, log)
	fileService := services.NewFileService(store, nil, uploadManager, downloadManager, log)
	fileService.SetStorageResolver(nodestorage.NewResolver(nil))

	chain := []connect.Interceptor{ic, recordingStreamInterceptor()}
	chain = append(chain, extra...)
	mux := http.NewServeMux()
	filePath, fileHandler := carbonpanelv1connect.NewFileServiceHandler(fileService, connect.WithInterceptors(chain...))
	mux.Handle(filePath, fileHandler)
	ts := httptest.NewServer(mux)
	t.Cleanup(func() {
		ts.Close()
		fileService.Stop()
		uploadManager.Stop()
		downloadManager.Stop()
	})
	return &downloadFixture{
		store: store, enforcer: enforcer, ts: ts,
		root: root, root2: root2, downloadManager: downloadManager,
	}
}

func (f *downloadFixture) session(t *testing.T, userID, role string, perms ...rbac.Permission) *sdkmcp.ClientSession {
	t.Helper()
	token := seedToken(t, f.store, f.enforcer, userID, role, perms)
	clients, err := NewClients(f.ts.Client(), f.ts.URL, token)
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	s := NewServer(clients)
	RegisterDownloadTools(s, clients)
	return connectStdioSession(t, s, context.Background())
}

func downloadRecord[T proto.Message](t *testing.T, procedure string) T {
	t.Helper()
	for _, rec := range invocationRecorder.snapshot() {
		if rec.Procedure == procedure {
			if m, ok := rec.Msg.Interface().(T); ok {
				return m
			}
		}
	}
	t.Fatalf("no recorded typed invocation for %s", procedure)
	var zero T
	return zero
}

func downloadBytes(t *testing.T, out ToolOutput) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(resultString(out, "content"))
	if err != nil {
		t.Fatalf("content base64: %v", err)
	}
	return raw
}

func assertDownloadBytes(t *testing.T, tool string, out ToolOutput, want []byte) {
	t.Helper()
	got := downloadBytes(t, out)
	sum := sha256.Sum256(want)
	if !bytes.Equal(got, want) {
		t.Fatalf("%s: byte transfer mismatch: got %d bytes, want %d", tool, len(got), len(want))
	}
	if resultInt64(out, "size") != int64(len(want)) {
		t.Fatalf("%s: size = %d, want %d", tool, resultInt64(out, "size"), len(want))
	}
	if sumHex := hex.EncodeToString(sum[:]); resultString(out, "sha256") != sumHex {
		t.Fatalf("%s: sha256 = %q, want %q", tool, resultString(out, "sha256"), sumHex)
	}
}

func assertOwnedZip(t *testing.T, data []byte, wantEntry string) {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip reader: %v", err)
	}
	for _, f := range zr.File {
		if f.Name == wantEntry {
			return
		}
	}
	t.Fatalf("owned zip does not contain %q", wantEntry)
}

func TestDownloadToolsTypedMappingAndBytes(t *testing.T) {
	f := newDownloadFixture(t, time.Hour)
	payload := bytes.Repeat([]byte("carbonpanel-download-0123456789"), 12000)[:300<<10]
	cfg := []byte("owned-config: true\n")
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	defer func() { _ = acc.Close() }()
	if err := acc.WriteFile("config.yml", cfg); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	if err := acc.WriteFile("worlds/owned.dat", payload); err != nil {
		t.Fatalf("seed owned fixture: %v", err)
	}

	session := f.session(t, "u-download", "admin")
	invocationRecorder.enable()
	defer invocationRecorder.disable()

	// 1. GetFile: exact typed field mapping plus real bytes, size and SHA.
	getOut := callTool(t, session, "download_get_file", map[string]any{
		"server_id": "srv-1", "path": "config.yml",
	})
	assertSuccess(t, getOut, "download_get_file")
	getReq := downloadRecord[*v1.GetFileRequest](t, "/carbonpanel.v1.FileService/GetFile")
	if getReq.GetServerId() != "srv-1" || getReq.GetPath() != "config.yml" {
		t.Fatalf("GetFile typed mapping = %+v", getReq)
	}
	assertDownloadBytes(t, "download_get_file", getOut, cfg)
	if resultString(getOut, "mime_type") == "" {
		t.Fatal("download_get_file returned no MIME type")
	}

	// 2. InitFileDownload (session RPC) + download_stream (composed byte path).
	initOut := callTool(t, session, "init_file_download", map[string]any{
		"server_id": "srv-1", "path": "worlds/owned.dat",
	})
	assertSuccess(t, initOut, "init_file_download")
	initReq := downloadRecord[*v1.InitFileDownloadRequest](t, "/carbonpanel.v1.FileService/InitFileDownload")
	if initReq.GetServerId() != "srv-1" || initReq.GetPath() != "worlds/owned.dat" {
		t.Fatalf("InitFileDownload typed mapping = %+v", initReq)
	}
	sid := resultString(initOut, "session_id")
	size := resultInt64(initOut, "total_size")
	if sid == "" || size != int64(len(payload)) || resultString(initOut, "filename") != "owned.dat" {
		t.Fatalf("InitFileDownload outcome = %+v", initOut.Result)
	}
	streamOut := callTool(t, session, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": sid, "total_size": size,
	})
	assertSuccess(t, streamOut, "download_stream")
	assertDownloadBytes(t, "download_stream", streamOut, payload)
	openReq := downloadRecord[*v1.OpenStreamRequest](t, "/carbonpanel.v1.FileService/OpenStream")
	if openReq.GetDownloadSessionId() != sid || openReq.GetServerId() != "srv-1" || openReq.GetTotalSize() != size || openReq.GetPath() != "" {
		t.Fatalf("OpenStream typed mapping = %+v", openReq)
	}
	for _, rec := range invocationRecorder.snapshot() {
		if rec.Procedure == "/carbonpanel.v1.FileService/ReadStreamFrame" {
			fr := rec.Msg.Interface().(*v1.ReadStreamFrameRequest)
			if fr.GetServerId() != "srv-1" || fr.GetSessionId() == "" {
				t.Fatalf("ReadStreamFrame typed mapping = %+v", fr)
			}
		}
		if rec.Procedure == "/carbonpanel.v1.FileService/CloseStream" {
			cl := rec.Msg.Interface().(*v1.CloseStreamRequest)
			if cl.GetServerId() != "srv-1" || cl.GetSessionId() == "" {
				t.Fatalf("CloseStream typed mapping = %+v", cl)
			}
		}
	}

	// 3. Single-call composed init tool uses the same bounded byte path.
	initBundle := callTool(t, session, "download_init_file", map[string]any{
		"server_id": "srv-1", "path": "worlds/owned.dat",
	})
	assertSuccess(t, initBundle, "download_init_file")
	assertDownloadBytes(t, "download_init_file", initBundle, payload)

	// 4. DownloadArchive: repeated paths map exactly; the staged zip is framed.
	paths := []string{"config.yml", "worlds/owned.dat"}
	archOut := callTool(t, session, "download_archive", map[string]any{
		"server_id": "srv-1", "paths": paths,
	})
	assertSuccess(t, archOut, "download_archive")
	archReq := downloadRecord[*v1.DownloadArchiveRequest](t, "/carbonpanel.v1.FileService/DownloadArchive")
	if archReq.GetServerId() != "srv-1" || !reflect.DeepEqual(archReq.GetPaths(), paths) {
		t.Fatalf("DownloadArchive typed mapping = %+v", archReq)
	}
	archSID := resultString(archOut, "session_id")
	archStream := callTool(t, session, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": archSID, "total_size": resultInt64(archOut, "total_size"),
	})
	assertSuccess(t, archStream, "download_stream(archive)")
	archBytes := downloadBytes(t, archStream)
	assertOwnedZip(t, archBytes, "config.yml")
	if resultInt64(archStream, "size") != resultInt64(archOut, "total_size") {
		t.Fatalf("archive streamed size %d != staged total %d",
			resultInt64(archStream, "size"), resultInt64(archOut, "total_size"))
	}

	// 5. Single-call composed archive bundle.
	bundle := callTool(t, session, "download_archive_bundle", map[string]any{
		"server_id": "srv-1", "paths": paths,
	})
	assertSuccess(t, bundle, "download_archive_bundle")
	assertOwnedZip(t, downloadBytes(t, bundle), "config.yml")

	// 6. ExportWorld: server id maps exactly, data_path is returned, staged zip
	// travels through bounded frames with no user world touched.
	exportOut := callTool(t, session, "export_world", map[string]any{"server_id": "srv-1"})
	assertSuccess(t, exportOut, "export_world")
	exportReq := downloadRecord[*v1.ExportWorldRequest](t, "/carbonpanel.v1.FileService/ExportWorld")
	if exportReq.GetServerId() != "srv-1" {
		t.Fatalf("ExportWorld typed mapping = %+v", exportReq)
	}
	if resultString(exportOut, "data_path") != f.root {
		t.Fatalf("ExportWorld data_path = %q, want %q", resultString(exportOut, "data_path"), f.root)
	}
	exportSID := resultString(exportOut, "session_id")
	exportStream := callTool(t, session, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": exportSID, "total_size": resultInt64(exportOut, "total_size"),
	})
	assertSuccess(t, exportStream, "download_stream(world)")
	assertOwnedZip(t, downloadBytes(t, exportStream), "config.yml")
	if resultInt64(exportStream, "size") != resultInt64(exportOut, "total_size") {
		t.Fatalf("world export streamed size %d != staged total %d",
			resultInt64(exportStream, "size"), resultInt64(exportOut, "total_size"))
	}

	// 7. Single-call composed world export bundle.
	worldBundle := callTool(t, session, "download_export_world_bundle", map[string]any{"server_id": "srv-1"})
	assertSuccess(t, worldBundle, "download_export_world_bundle")
	assertOwnedZip(t, downloadBytes(t, worldBundle), "config.yml")

	reached := map[string]bool{}
	for _, rec := range invocationRecorder.snapshot() {
		reached[rec.Procedure] = true
	}
	for _, proc := range []string{
		"/carbonpanel.v1.FileService/GetFile",
		"/carbonpanel.v1.FileService/InitFileDownload",
		"/carbonpanel.v1.FileService/DownloadArchive",
		"/carbonpanel.v1.FileService/ExportWorld",
		"/carbonpanel.v1.FileService/OpenStream",
		"/carbonpanel.v1.FileService/ReadStreamFrame",
		"/carbonpanel.v1.FileService/CloseStream",
	} {
		if !reached[proc] {
			t.Errorf("download lane did not reach typed RPC %s", proc)
		}
	}
	t.Logf("OSS-370 download lane: %d-byte framed transfer; all 5 pending download ops reached typed RPCs", len(payload))
}

func TestDownloadToolsResourceScopeDenial(t *testing.T) {
	f := newDownloadFixture(t, time.Hour)
	payload := []byte("scope-owned-bytes")
	acc, _ := nodestorage.NewLocal(f.root)
	defer func() { _ = acc.Close() }()
	if err := acc.WriteFile("config.yml", payload); err != nil {
		t.Fatalf("seed: %v", err)
	}
	admin := f.session(t, "u-download-admin", "admin")
	initOut := callTool(t, admin, "init_file_download", map[string]any{
		"server_id": "srv-1", "path": "config.yml",
	})
	assertSuccess(t, initOut, "admin init")
	foreignSID := resultString(initOut, "session_id")

	// A token scoped to srv-2 only must be refused for srv-1 on every op, and
	// must not be able to frame another principal's srv-1 download session.
	scoped := f.session(t, "u-download-srv2", "download-srv2",
		rbac.Permission{Resource: "files", Action: "read", ObjectID: "srv-2"})
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"download_get_file", map[string]any{"server_id": "srv-1", "path": "config.yml"}},
		{"download_init_file", map[string]any{"server_id": "srv-1", "path": "config.yml"}},
		{"download_archive_bundle", map[string]any{"server_id": "srv-1", "paths": []string{"config.yml"}}},
		{"download_export_world_bundle", map[string]any{"server_id": "srv-1"}},
		{"download_stream", map[string]any{"server_id": "srv-1", "session_id": foreignSID, "total_size": len(payload)}},
		// Lying about server_id cannot cross the session's immutable binding.
		{"download_stream", map[string]any{"server_id": "srv-2", "session_id": foreignSID, "total_size": len(payload)}},
	}
	for _, tc := range cases {
		out := callTool(t, scoped, tc.tool, tc.args)
		if out.Status != "error" || out.Error == nil {
			t.Fatalf("%s: expected scope denial, got %+v", tc.tool, out)
		}
		if out.Error.Kind != ErrKindPermissionDenied {
			t.Fatalf("%s: denial kind = %q, want %q (%s)", tc.tool, out.Error.Kind, ErrKindPermissionDenied, out.Error.Message)
		}
		if len(downloadBytesOrEmpty(t, out)) != 0 {
			t.Fatalf("%s: denial returned bytes", tc.tool)
		}
	}

	// A real active token with no grants is denied too (fail-closed).
	noperms := f.session(t, "u-download-none", "download-none")
	noGrant := callTool(t, noperms, "download_get_file", map[string]any{
		"server_id": "srv-1", "path": "config.yml",
	})
	_ = assertStructuredError(t, noGrant, "download_get_file", ErrKindPermissionDenied)
	t.Log("OSS-370 download lane: cross-server and no-grant tools/call denials observed with zero bytes")
}

func downloadBytesOrEmpty(t *testing.T, out ToolOutput) []byte {
	t.Helper()
	if out.Result == nil {
		return nil
	}
	m, ok := out.Result.(map[string]any)
	if !ok {
		return nil
	}
	s, _ := m["content"].(string)
	if s == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("content base64: %v", err)
	}
	return b
}

func TestDownloadToolRefusals(t *testing.T) {
	f := newDownloadFixture(t, time.Hour)
	payload := bytes.Repeat([]byte("z"), 96<<10)
	acc, _ := nodestorage.NewLocal(f.root)
	defer func() { _ = acc.Close() }()
	if err := acc.WriteFile("owned.bin", payload); err != nil {
		t.Fatalf("seed: %v", err)
	}
	admin := f.session(t, "u-download-refuse", "admin")
	initOut := callTool(t, admin, "init_file_download", map[string]any{
		"server_id": "srv-1", "path": "owned.bin",
	})
	assertSuccess(t, initOut, "init")
	sid := resultString(initOut, "session_id")
	size := resultInt64(initOut, "total_size")

	// Cap: declared bound above the gateway working set is refused locally,
	// before a controller stream is opened.
	overCap := callTool(t, admin, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": sid, "total_size": int64(downloadMaxBytes) + 1,
	})
	_ = assertStructuredError(t, overCap, "download_stream", ErrKindInvalidArguments)

	// Cap: an under-declared bound is refused by the controller frame path with
	// no partial bytes presented as success.
	underCap := callTool(t, admin, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": sid, "total_size": 16,
	})
	if underCap.Status != "error" || underCap.Error == nil {
		t.Fatalf("under-declared cap not refused: %+v", underCap)
	}
	if len(downloadBytesOrEmpty(t, underCap)) != 0 {
		t.Fatal("under-declared cap refusal returned bytes")
	}

	// Digest mismatch: a wrong expected digest is reported, never a success.
	mismatch := callTool(t, admin, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": sid, "total_size": size,
		"expected_sha256": strings.Repeat("0", 64),
	})
	if mismatch.Status != "error" || mismatch.Error == nil {
		t.Fatalf("digest mismatch not refused: %+v", mismatch)
	}

	// Argument validation stays fail-closed before any typed RPC.
	badPath := callTool(t, admin, "download_init_file", map[string]any{
		"server_id": "srv-1", "path": "../secrets",
	})
	_ = assertStructuredError(t, badPath, "download_init_file", ErrKindInvalidArguments)
	badCount := callTool(t, admin, "download_archive_bundle", map[string]any{
		"server_id": "srv-1", "paths": []string{},
	})
	_ = assertStructuredError(t, badCount, "download_archive_bundle", ErrKindInvalidArguments)

	// Reusing the same download session still works after the previous stream
	// released its controller handle (no leaked-cap refusal).
	again := callTool(t, admin, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": sid, "total_size": size,
	})
	assertSuccess(t, again, "reopen after release")
	assertDownloadBytes(t, "reopen after release", again, payload)
}

func TestDownloadToolExpiry(t *testing.T) {
	f := newDownloadFixture(t, 120*time.Millisecond)
	acc, _ := nodestorage.NewLocal(f.root)
	defer func() { _ = acc.Close() }()
	if err := acc.WriteFile("owned.bin", []byte("expire-me")); err != nil {
		t.Fatalf("seed: %v", err)
	}
	admin := f.session(t, "u-download-expiry", "admin")
	initOut := callTool(t, admin, "init_file_download", map[string]any{
		"server_id": "srv-1", "path": "owned.bin",
	})
	assertSuccess(t, initOut, "init")
	time.Sleep(250 * time.Millisecond)
	late := callTool(t, admin, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": resultString(initOut, "session_id"),
		"total_size": resultInt64(initOut, "total_size"),
	})
	if late.Status != "error" || late.Error == nil {
		t.Fatalf("expired session not refused: %+v", late)
	}
	if len(downloadBytesOrEmpty(t, late)) != 0 {
		t.Fatal("expired session refusal returned bytes")
	}
	t.Log("OSS-370 download lane: expired staged session refused by tools/call with zero bytes")
}

// delayFrameInterceptor makes a frame RPC slow enough for a tools/call
// cancellation to be observed mid-transfer rather than before dispatch.
func delayFrameInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().Procedure == "/carbonpanel.v1.FileService/ReadStreamFrame" {
				time.Sleep(40 * time.Millisecond)
			}
			return next(ctx, req)
		}
	}
}

func TestDownloadToolCancellation(t *testing.T) {
	f := newDownloadFixture(t, time.Hour, delayFrameInterceptor())
	acc, _ := nodestorage.NewLocal(f.root)
	defer func() { _ = acc.Close() }()
	if err := acc.WriteFile("owned.bin", bytes.Repeat([]byte("c"), 512<<10)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	admin := f.session(t, "u-download-cancel", "admin")
	initOut := callTool(t, admin, "init_file_download", map[string]any{
		"server_id": "srv-1", "path": "owned.bin",
	})
	assertSuccess(t, initOut, "init")

	invocationRecorder.enable()
	defer invocationRecorder.disable()
	ctx, cancel := context.WithCancel(context.Background())
	cancelObserved := make(chan struct{})
	go func() {
		defer close(cancelObserved)
		defer cancel()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, rec := range invocationRecorder.snapshot() {
				if rec.Procedure == "/carbonpanel.v1.FileService/ReadStreamFrame" {
					return // cancel while the delayed frame read is in flight
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()
	out, err := callToolRaw(ctx, admin, "download_stream", map[string]any{
		"server_id": "srv-1", "session_id": resultString(initOut, "session_id"),
		"total_size": resultInt64(initOut, "total_size"),
	})
	if err == nil && out.Status != "error" {
		t.Fatalf("canceled tools/call did not fail: %+v", out)
	}
	<-cancelObserved
	if len(downloadBytesOrEmpty(t, out)) != 0 {
		t.Fatal("canceled tools/call returned bytes")
	}
	// The call really reached the controller and cleanup was independent of the
	// canceled tools/call context: a cancel-close must have been dispatched.
	opened, canceledClose := false, false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && (!opened || !canceledClose) {
		opened, canceledClose = false, false
		for _, rec := range invocationRecorder.snapshot() {
			switch rec.Procedure {
			case "/carbonpanel.v1.FileService/OpenStream":
				opened = true
			case "/carbonpanel.v1.FileService/CloseStream":
				if rec.Msg.Interface().(*v1.CloseStreamRequest).GetCancel() {
					canceledClose = true
				}
			}
		}
		if !canceledClose {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !opened || !canceledClose {
		t.Fatalf("cancellation cleanup evidence missing: opened=%v cancelClose=%v", opened, canceledClose)
	}
	t.Log("OSS-370 download lane: tools/call cancellation reached controller frames and cancel-closed the owned handle")
}

func TestDownloadDescriptorsMatchSpecs(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("family_download_descriptors.json"))
	if err != nil {
		t.Fatalf("read descriptors: %v", err)
	}
	var fileDescriptors []DownloadToolDescriptor
	if err := json.Unmarshal(raw, &fileDescriptors); err != nil {
		t.Fatalf("unmarshal descriptors: %v", err)
	}
	goDescriptors := DownloadToolDescriptors()
	if !reflect.DeepEqual(fileDescriptors, goDescriptors) {
		t.Fatalf("descriptor JSON drifted from DownloadToolDescriptors():\n file=%+v\n go=%+v", fileDescriptors, goDescriptors)
	}
	specs := DownloadToolSpecs()
	if len(specs) != len(goDescriptors) {
		t.Fatalf("specs=%d descriptors=%d", len(specs), len(goDescriptors))
	}
	specByName := make(map[string]ToolSpec, len(specs))
	for _, spec := range specs {
		specByName[spec.Name] = spec
	}
	for _, desc := range goDescriptors {
		spec, ok := specByName[desc.Tool]
		if !ok {
			t.Errorf("descriptor tool %s has no exported spec", desc.Tool)
			continue
		}
		if spec.Procedure != desc.Procedure || spec.Class != desc.Class {
			t.Errorf("descriptor %s procedure/class %s/%s != spec %s/%s",
				desc.Tool, desc.Procedure, desc.Class, spec.Procedure, spec.Class)
		}
	}
}
