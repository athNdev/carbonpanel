// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
)

func newTransferFixture(t *testing.T, capBytes int64, ttl time.Duration) (*StreamTransfer, nodestorage.Accessor, *time.Time) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "data")
	acc, err := nodestorage.NewLocal(root)
	if err != nil {
		t.Fatalf("NewLocal: %v", err)
	}
	t.Cleanup(func() { _ = acc.Close() })
	now := time.Now()
	transfer, err := NewStreamTransfer(acc, capBytes, ttl, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewStreamTransfer: %v", err)
	}
	return transfer, acc, &now
}

// TestIntegrationScopedStreamTransfer proves the composed download workflow
// genuinely moves bytes through the W17 streaming contract, not just a
// session id, and enforces cancellation, caps, expiry and cross-scope refusal.
func TestIntegrationScopedStreamTransfer(t *testing.T) {
	ctx := context.Background()
	transfer, acc, now := newTransferFixture(t, 1<<20, time.Minute)

	// A file larger than one W17 chunk but under the composed cap.
	payload := bytes.Repeat([]byte("carbonpanel-stream-0123456789abcdef"), 30000) // ~1.02 MB? trimmed below
	payload = payload[:300<<10] // 300 KiB: multiple 256 KiB chunks
	sum := sha256.Sum256(payload)
	// Write via the shared accessor exposed by the transfer fixture (same fs).
	if err := acc.WriteFile("worlds/download.bin", payload); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	handle, err := transfer.OpenWithSize(ctx, "u-1", "srv-1", "worlds/download.bin", int64(len(payload)))
	if err != nil {
		t.Fatalf("OpenWithSize: %v", err)
	}
	got := []byte{}
	var final string
	for {
		chunk, sha, readErr := transfer.ReadChunk(ctx, "u-1", "srv-1", handle)
		got = append(got, chunk...)
		if errors.Is(readErr, io.EOF) {
			final = sha
			break
		}
		if readErr != nil {
			t.Fatalf("ReadChunk: %v", readErr)
		}
	}
	if !bytes.Equal(got, payload) || final != hex.EncodeToString(sum[:]) {
		t.Fatalf("stream bytes/SHA mismatch: got %d bytes sha=%s", len(got), final)
	}
	if _, _, err := transfer.Commit(ctx, "u-1", "srv-1", handle, "worlds/copy.bin"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	committed, err := acc.ReadFile("worlds/copy.bin")
	if err != nil || !bytes.Equal(committed, payload) {
		t.Fatalf("committed bytes mismatch: %d bytes err=%v", len(committed), err)
	}
	t.Logf("scoped transfer: %d bytes across %d-byte frames, sha=%s", len(got), storageworker.ChunkBytes, final)

	// Cancellation mid-transfer: refuse the next frame immediately.
	h2, err := transfer.OpenWithSize(ctx, "u-1", "srv-1", "worlds/download.bin", int64(len(payload)))
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	if _, _, err := transfer.ReadChunk(ctx, "u-1", "srv-1", h2); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if err := transfer.Cancel("u-1", "srv-1", h2); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if _, _, err := transfer.ReadChunk(ctx, "u-1", "srv-1", h2); !errors.Is(err, ErrStreamCanceled) {
		t.Fatalf("canceled session was not refused: %v", err)
	}

	// Size cap refusal before bytes move.
	if _, err := transfer.OpenWithSize(ctx, "u-1", "srv-1", "worlds/download.bin", (1<<20)+1); !errors.Is(err, ErrStreamTooLarge) {
		t.Fatalf("oversize session was not refused: %v", err)
	}

	// Session expiry refusal without sleeping: advance the injected clock.
	h3, err := transfer.OpenWithSize(ctx, "u-1", "srv-1", "worlds/download.bin", int64(len(payload)))
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	*now = now.Add(2 * time.Minute)
	if _, _, err := transfer.ReadChunk(ctx, "u-1", "srv-1", h3); !errors.Is(err, ErrStreamExpired) {
		t.Fatalf("expired session was not refused: %v", err)
	}

	// Cross-scope refusal: another owner / another server cannot use the handle.
	*now = time.Now()
	h4, err := transfer.OpenWithSize(ctx, "u-1", "srv-1", "worlds/download.bin", int64(len(payload)))
	if err != nil {
		t.Fatalf("fourth open: %v", err)
	}
	if _, _, err := transfer.ReadChunk(ctx, "u-2", "srv-1", h4); !errors.Is(err, ErrStreamCrossScope) {
		t.Fatalf("wrong owner was not refused: %v", err)
	}
	if _, _, err := transfer.ReadChunk(ctx, "u-1", "srv-2", h4); !errors.Is(err, ErrStreamCrossScope) {
		t.Fatalf("wrong server was not refused: %v", err)
	}
}

// Compile-time guard that the shared stream contract is the W17 one.
var _ storageworker.StreamingFS = (nodestorage.Accessor)(nil)
