// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): executable behavior checks for the
// two workflows that a registration/fixture count cannot prove: API-token
// logout semantics and end-to-end byte transport on the scoped streaming
// equivalents. These tests observe what the controller actually does and keep
// the tool descriptions and coverage claims honest.
package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func newAuthInterceptorForBytes(t *testing.T) (connect.UnaryInterceptorFunc, string) {
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
	token := seedToken(t, store, enforcer, "u-bytes", "admin", nil)
	return interceptor, token
}

// TestIntegrationStreamingBytesEndToEnd drives the composed upload/download
// workflows over tools/call and asserts that the bytes — not just a session
// handle — reach the typed controller path and come back on a scoped read.
func TestIntegrationStreamingBytesEndToEnd(t *testing.T) {
	ic, token := newAuthInterceptorForBytes(t)
	payload := []byte("carbonpanel-scoped-bytes-0123456789")
	uploaded := []byte{}
	savedName := ""
	downloadSession := ""

	mux := http.NewServeMux()
	// Upload byte channel: the typed UploadChunk RPC carries base64 data that
	// Connect decodes into []byte before it reaches this handler.
	mux.Handle("/carbonpanel.v1.UploadService/InitUpload", connect.NewUnaryHandler(
		"/carbonpanel.v1.UploadService/InitUpload",
		func(context.Context, *connect.Request[v1.InitUploadRequest]) (*connect.Response[v1.InitUploadResponse], error) {
			return connect.NewResponse(&v1.InitUploadResponse{SessionId: "up-bytes-1"}), nil
		}, connect.WithInterceptors(ic)))
	mux.Handle("/carbonpanel.v1.UploadService/UploadChunk", connect.NewUnaryHandler(
		"/carbonpanel.v1.UploadService/UploadChunk",
		func(_ context.Context, req *connect.Request[v1.UploadChunkRequest]) (*connect.Response[v1.UploadChunkResponse], error) {
			if req.Msg.GetSessionId() != "up-bytes-1" {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("wrong upload session"))
			}
			uploaded = append(uploaded, req.Msg.GetData()...)
			return connect.NewResponse(&v1.UploadChunkResponse{}), nil
		}, connect.WithInterceptors(ic)))
	mux.Handle("/carbonpanel.v1.FileService/SaveUploadedFile", connect.NewUnaryHandler(
		"/carbonpanel.v1.FileService/SaveUploadedFile",
		func(_ context.Context, req *connect.Request[v1.SaveUploadedFileRequest]) (*connect.Response[v1.SaveUploadedFileResponse], error) {
			if req.Msg.GetUploadSessionId() != "up-bytes-1" {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
			}
			savedName = req.Msg.GetFilename()
			return connect.NewResponse(&v1.SaveUploadedFileResponse{}), nil
		}, connect.WithInterceptors(ic)))
	// Download byte channel: the raw GET is excluded; the typed scoped reads
	// below are the supported equivalent and return the actual bytes.
	mux.Handle("/carbonpanel.v1.FileService/InitFileDownload", connect.NewUnaryHandler(
		"/carbonpanel.v1.FileService/InitFileDownload",
		func(context.Context, *connect.Request[v1.InitFileDownloadRequest]) (*connect.Response[v1.InitFileDownloadResponse], error) {
			downloadSession = "dl-bytes-1"
			return connect.NewResponse(&v1.InitFileDownloadResponse{SessionId: downloadSession}), nil
		}, connect.WithInterceptors(ic)))
	mux.Handle("/carbonpanel.v1.FileService/GetFile", connect.NewUnaryHandler(
		"/carbonpanel.v1.FileService/GetFile",
		func(context.Context, *connect.Request[v1.GetFileRequest]) (*connect.Response[v1.GetFileResponse], error) {
			return connect.NewResponse(&v1.GetFileResponse{Content: payload, MimeType: "application/octet-stream"}), nil
		}, connect.WithInterceptors(ic)))

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	clients, err := NewClients(ts.Client(), ts.URL, token)
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	session := connectStdioSession(t, NewServer(clients), context.Background())

	initUpload := callTool(t, session, "init_upload", map[string]any{
		"filename": "bytes.bin", "total_size": len(payload), "chunk_size": 256, "confirm": true,
	})
	assertSuccess(t, initUpload, "init_upload")
	chunk := callTool(t, session, "upload_chunk", map[string]any{
		"session_id": "up-bytes-1", "chunk_index": 0,
		"data": base64.StdEncoding.EncodeToString(payload), "confirm": true,
	})
	assertSuccess(t, chunk, "upload_chunk")
	if !bytes.Equal(uploaded, payload) {
		t.Fatalf("typed upload chunk did not carry the bytes: got %q want %q", uploaded, payload)
	}
	save := callTool(t, session, "save_uploaded_file", map[string]any{
		"server_id": "srv-1", "upload_session_id": "up-bytes-1",
		"destination_path": ".", "filename": "bytes.bin", "confirm": true,
	})
	assertSuccess(t, save, "save_uploaded_file")
	if savedName != "bytes.bin" {
		t.Fatalf("save_uploaded_file did not map the filename: got %q", savedName)
	}

	initDownload := callTool(t, session, "init_file_download", map[string]any{
		"server_id": "srv-1", "path": "bytes.bin",
	})
	assertSuccess(t, initDownload, "init_file_download")
	if downloadSession != "dl-bytes-1" {
		t.Fatalf("init_file_download did not reach the typed handler")
	}
	download := callTool(t, session, "get_file", map[string]any{
		"server_id": "srv-1", "path": "bytes.bin",
	})
	assertSuccess(t, download, "get_file")
	result, ok := download.Result.(map[string]any)
	if !ok {
		t.Fatalf("get_file result is not an object: %+v", download.Result)
	}
	encoded, _ := result["content"].(string)
	got, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("scoped typed read did not carry the bytes: got %q err=%v", got, err)
	}

	// Exercise the dedicated typed stream client set as well: the composed
	// equivalents share the same token and interceptor, with no raw HTTP path.
	stream, err := clients.streamClients()
	if err != nil {
		t.Fatalf("streamClients: %v", err)
	}
	if stream == nil || stream.File == nil || stream.Upload == nil {
		t.Fatal("typed stream clients were not constructed")
	}
	t.Logf("streaming byte evidence: upload=%d bytes, download=%d bytes, session=%s", len(uploaded), len(got), downloadSession)
}
