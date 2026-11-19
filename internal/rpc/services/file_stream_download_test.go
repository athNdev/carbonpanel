// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestControllerStagedDownloadStream exercises the typed OpenStream extension
// at the service seam: staged download bytes travel through the same bounded
// frames as node-path streams, with server, owner, cap and expiry binding.
func TestControllerStagedDownloadStream(t *testing.T) {
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()

	payload := bytes.Repeat([]byte("staged-download-0123456789"), 2000)[:48<<10]
	staged := filepath.Join(t.TempDir(), "owned-staged.bin")
	if err := os.WriteFile(staged, payload, 0o600); err != nil {
		t.Fatalf("seed staged fixture: %v", err)
	}
	session := svc.downloadManager.InitSessionFor(staged, "owned-staged.bin", int64(len(payload)), true, srv.ID, "u-owner")
	ctx := streamCtx("u-owner")

	before := len(svc.streamState().sessions)
	opened, err := svc.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, DownloadSessionId: session.ID, TotalSize: int64(len(payload)),
	}))
	if err != nil {
		t.Fatalf("OpenStream(staged): %v", err)
	}
	sid := opened.Msg.GetSessionId()
	if sid == "" || opened.Msg.GetSize() != int64(len(payload)) {
		t.Fatalf("open result = %+v", opened.Msg)
	}
	if len(svc.streamState().sessions) != before+1 {
		t.Fatal("download stream was not registered")
	}

	var got []byte
	digest := ""
	for i := int32(0); ; i++ {
		frame, err := svc.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
			SessionId: sid, FrameIndex: i, ServerId: srv.ID,
		}))
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		got = append(got, frame.Msg.GetData()...)
		if frame.Msg.GetDone() {
			digest = frame.Msg.GetSha256()
			if frame.Msg.GetOffset() != int64(len(payload)) {
				t.Fatalf("final offset = %d, want %d", frame.Msg.GetOffset(), len(payload))
			}
			break
		}
	}
	want := sha256.Sum256(payload)
	if !bytes.Equal(got, payload) || digest != hex.EncodeToString(want[:]) {
		t.Fatalf("staged transfer mismatch: bytes=%d sha=%s", len(got), digest)
	}
	if _, err := svc.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: sid, ServerId: srv.ID,
	})); err != nil {
		t.Fatalf("CloseStream: %v", err)
	}
	_ = workerRoot // staged bytes are controller-owned; no node tree is touched.
}

func TestControllerStagedDownloadStreamRefusals(t *testing.T) {
	store, srv, svc, _, _, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	other := &storage.Server{ID: "srv-other", Name: "srv-other", DataPath: filepath.Join(t.TempDir(), "other"), NodeID: "local"}
	if err := store.CreateServer(context.Background(), other); err != nil {
		t.Fatalf("CreateServer(other): %v", err)
	}

	payload := []byte("owned-refusal-fixture")
	staged := filepath.Join(t.TempDir(), "owned.bin")
	if err := os.WriteFile(staged, payload, 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	session := svc.downloadManager.InitSessionFor(staged, "owned.bin", int64(len(payload)), true, srv.ID, "u-owner")

	// Wrong server: refused before any handle is opened, no state change.
	before := len(svc.streamState().sessions)
	_, err := svc.OpenStream(streamCtx("u-owner"), connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: "srv-other", DownloadSessionId: session.ID, TotalSize: int64(len(payload)),
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || !errors.Is(err, errStreamCrossServer) {
		t.Fatalf("cross-server staged open was not refused: %v", err)
	}
	if len(svc.streamState().sessions) != before {
		t.Fatal("cross-server refusal changed stream state")
	}

	// Wrong owner: the staged file belongs to u-owner.
	_, err = svc.OpenStream(streamCtx("u-other"), connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, DownloadSessionId: session.ID, TotalSize: int64(len(payload)),
	}))
	if connect.CodeOf(err) != connect.CodePermissionDenied || !errors.Is(err, errStreamCrossOwner) {
		t.Fatalf("cross-owner staged open was not refused: %v", err)
	}

	// Over-declared cap and unknown/expired sessions are refused.
	_, err = svc.OpenStream(streamCtx("u-owner"), connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, DownloadSessionId: session.ID, TotalSize: int64(len(payload)) + 1,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !errors.Is(err, errStreamCap) {
		t.Fatalf("over-cap staged open was not refused: %v", err)
	}
	_, err = svc.OpenStream(streamCtx("u-owner"), connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, DownloadSessionId: "missing-session", TotalSize: int64(len(payload)),
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown staged session was not refused: %v", err)
	}

	expired := svc.downloadManager.InitSessionFor(staged, "owned.bin", int64(len(payload)), true, srv.ID, "u-owner")
	expired.ExpiresAt = time.Now().Add(-time.Minute)
	_, err = svc.OpenStream(streamCtx("u-owner"), connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, DownloadSessionId: expired.ID, TotalSize: int64(len(payload)),
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expired staged session was not refused: %v", err)
	}
}
