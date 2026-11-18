// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
)

// A session created by an authenticated RPC must be consumable by that same
// user through a downstream import RPC. This pins the full init -> stream ->
// consume flow, which previously broke when ownership was introduced.
func TestOwnedUploadSessionIsConsumableByOwner(t *testing.T) {
	svc, mgr := ownershipUploadService(t)
	ctx := userCtx("user-a")

	initResp, err := svc.InitUpload(ctx, connect.NewRequest(&v1.InitUploadRequest{
		Filename: "payload.jar", TotalSize: 4, ChunkSize: 4,
	}))
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	id := initResp.Msg.SessionId
	if _, _, err := mgr.WriteStreamFor(id, "user-a", bytes.NewReader([]byte("data")), 0); err != nil {
		t.Fatalf("WriteStreamFor: %v", err)
	}

	// GetTempPathFor is the exact seam used by save/import consumers.
	path, name, err := mgr.GetTempPathFor(id, "user-a")
	if err != nil {
		t.Fatalf("owner GetTempPathFor: %v", err)
	}
	if name != "payload.jar" || path == "" {
		t.Fatalf("temp path/name = %q/%q", path, name)
	}

	// A different user cannot read or clean up that session through the
	// consume seam, even with the correct session ID.
	if _, _, err := mgr.GetTempPathFor(id, "user-b"); !errors.Is(err, upload.ErrOwnerMismatch) {
		t.Errorf("cross-user GetTempPathFor err = %v, want ErrOwnerMismatch", err)
	}
	if err := mgr.CleanupSessionFor(id, "user-b"); !errors.Is(err, upload.ErrOwnerMismatch) {
		t.Errorf("cross-user CleanupSessionFor err = %v, want ErrOwnerMismatch", err)
	}
	if _, err := mgr.GetSessionFor(id, "user-a"); err != nil {
		t.Fatalf("cross-user attempts disturbed owner's session: %v", err)
	}
}

// Full RPC consume seam: the owner can save an owned upload, while a
// different authenticated user holding the same session ID is denied.
func TestSaveUploadedFileEnforcesSessionOwner(t *testing.T) {
	_, server, svc, _, workerRoot, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()

	um := upload.NewManager(t.TempDir(), time.Hour, 0, logger.New())
	t.Cleanup(um.Stop)
	svc.uploadManager = um
	uploadSvc := NewUploadService(um, &config.Config{Upload: config.UploadConfig{
		DefaultChunkSize: 4, MaxChunkSize: 8,
	}}, logger.New())

	ctx := userCtx("user-a")
	initResp, err := uploadSvc.InitUpload(ctx, connect.NewRequest(&v1.InitUploadRequest{
		Filename: "owned.jar", TotalSize: 4, ChunkSize: 4,
	}))
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	id := initResp.Msg.SessionId
	if _, _, err := um.WriteStreamFor(id, "user-a", bytes.NewReader([]byte("data")), 0); err != nil {
		t.Fatalf("WriteStreamFor: %v", err)
	}

	req := &v1.SaveUploadedFileRequest{ServerId: server.ID, UploadSessionId: id, DestinationPath: "mods"}
	_, err = svc.SaveUploadedFile(userCtx("user-b"), connect.NewRequest(req))
	if err == nil {
		t.Fatal("cross-user consume was accepted")
	} else if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Errorf("cross-user consume code = %v, want permission_denied", code)
	}

	_, err = svc.SaveUploadedFile(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("owner consume: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(workerRoot, "mods", "owned.jar"))
	if err != nil || string(got) != "data" {
		t.Fatalf("saved file = %q, %v", got, err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

// The no-auth synthetic admin has a real identity. A session created by that
// principal must still be consumable by the same identity on a later RPC.
func TestNoAuthAdminSessionIsConsumable(t *testing.T) {
	mgr := upload.NewManager(t.TempDir(), time.Hour, 0, logger.New())
	t.Cleanup(mgr.Stop)
	cfg := &config.Config{Upload: config.UploadConfig{DefaultChunkSize: 4, MaxChunkSize: 8}}
	svc := NewUploadService(mgr, cfg, logger.New())

	initResp, err := svc.InitUpload(userCtx("admin"), connect.NewRequest(&v1.InitUploadRequest{
		Filename: "payload.jar", TotalSize: 4, ChunkSize: 4,
	}))
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	id := initResp.Msg.SessionId
	if _, _, err := mgr.WriteStreamFor(id, "admin", bytes.NewReader([]byte("data")), 0); err != nil {
		t.Fatalf("WriteStreamFor: %v", err)
	}
	if _, _, err := mgr.GetTempPathFor(id, "admin"); err != nil {
		t.Fatalf("admin consume: %v", err)
	}
	if err := mgr.CleanupSessionFor(id, "admin"); err != nil {
		t.Fatalf("admin cleanup: %v", err)
	}
}

// Anonymous sessions are bound to the synthetic anonymous identity, not left
// unowned, so another principal cannot take over a public-panel upload.
func TestAnonymousSessionIsBound(t *testing.T) {
	mgr := upload.NewManager(t.TempDir(), time.Hour, 0, logger.New())
	t.Cleanup(mgr.Stop)
	cfg := &config.Config{Upload: config.UploadConfig{DefaultChunkSize: 4, MaxChunkSize: 8}}
	svc := NewUploadService(mgr, cfg, logger.New())

	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "anonymous", Roles: []string{"anonymous"}})
	initResp, err := svc.InitUpload(ctx, connect.NewRequest(&v1.InitUploadRequest{
		Filename: "payload.jar", TotalSize: 4, ChunkSize: 4,
	}))
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	sess, err := mgr.GetSessionFor(initResp.Msg.SessionId, "anonymous")
	if err != nil {
		t.Fatalf("anonymous session not bound: %v", err)
	}
	if sess.OwnerUserID != "anonymous" {
		t.Fatalf("OwnerUserID = %q, want anonymous", sess.OwnerUserID)
	}
}
