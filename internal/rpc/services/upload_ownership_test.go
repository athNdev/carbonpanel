// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
)

func ownershipUploadService(t *testing.T) (*UploadService, *upload.Manager) {
	t.Helper()
	m := upload.NewManager(t.TempDir(), time.Hour, 0, logger.New())
	t.Cleanup(m.Stop)
	cfg := &config.Config{Upload: config.UploadConfig{DefaultChunkSize: 4, MaxChunkSize: 8}}
	return NewUploadService(m, cfg, logger.New()), m
}

func userCtx(id string) context.Context {
	return auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: id, Username: id, Roles: []string{"admin"}})
}

// InitUpload binds the session to the authenticated user; session-ID-only
// access by a different user must fail on every follow-up RPC.
func TestUploadRPCSessionOwnership(t *testing.T) {
	svc, mgr := ownershipUploadService(t)

	initResp, err := svc.InitUpload(userCtx("user-a"), connect.NewRequest(&v1.InitUploadRequest{
		Filename: "payload.bin", TotalSize: 8, ChunkSize: 4,
	}))
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	id := initResp.Msg.SessionId
	if _, err := mgr.GetSessionFor(id, "user-a"); err != nil {
		t.Fatalf("session was not bound to owner: %v", err)
	}

	chunkReq := &v1.UploadChunkRequest{SessionId: id, ChunkIndex: 0, Data: []byte("abcd")}
	if _, err := svc.UploadChunk(userCtx("user-b"), connect.NewRequest(chunkReq)); err == nil {
		t.Fatal("cross-user UploadChunk was accepted")
	} else if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Errorf("cross-user UploadChunk code = %v, want permission_denied", code)
	}

	for name, call := range map[string]func(context.Context) error{
		"status": func(ctx context.Context) error {
			_, err := svc.GetUploadStatus(ctx, connect.NewRequest(&v1.GetUploadStatusRequest{SessionId: id}))
			return err
		},
		"cancel": func(ctx context.Context) error {
			_, err := svc.CancelUpload(ctx, connect.NewRequest(&v1.CancelUploadRequest{SessionId: id}))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(userCtx("user-b")); err == nil {
				t.Fatal("cross-user operation was accepted")
			} else if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
				t.Errorf("cross-user code = %v, want permission_denied", code)
			}
		})
	}

	stored, err := mgr.GetSessionFor(id, "user-a")
	if err != nil {
		t.Fatalf("owner lookup after refusals: %v", err)
	}
	if stored.BytesReceived != 0 {
		t.Fatalf("cross-user write changed BytesReceived to %d", stored.BytesReceived)
	}
}

// The real owner can still complete a resumable upload through the same RPCs.
func TestUploadRPCOwnerCanComplete(t *testing.T) {
	svc, mgr := ownershipUploadService(t)
	ctx := userCtx("user-a")

	initResp, err := svc.InitUpload(ctx, connect.NewRequest(&v1.InitUploadRequest{
		Filename: "payload.bin", TotalSize: 8, ChunkSize: 4,
	}))
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	id := initResp.Msg.SessionId

	if _, err := svc.UploadChunk(ctx, connect.NewRequest(&v1.UploadChunkRequest{
		SessionId: id, ChunkIndex: 0, Data: []byte("abcd"),
	})); err != nil {
		t.Fatalf("owner UploadChunk: %v", err)
	}
	status, err := svc.GetUploadStatus(ctx, connect.NewRequest(&v1.GetUploadStatusRequest{SessionId: id}))
	if err != nil {
		t.Fatalf("owner GetUploadStatus: %v", err)
	}
	if status.Msg.BytesReceived != 4 || status.Msg.ChunksReceived != 1 {
		t.Fatalf("unexpected owner status: %+v", status.Msg)
	}
	if _, _, err := mgr.GetTempPathFor(id, "user-a"); err == nil {
		t.Error("incomplete session must not expose a temp path")
	}
}
