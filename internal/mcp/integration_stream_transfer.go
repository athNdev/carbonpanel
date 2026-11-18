// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): scoped streaming download transfer.
//
// The raw REST byte channels stay OUT of the MCP tool catalog. This file
// supplies the bounded, composed transfer behind the typed workflow: it
// reuses the W17 storageworker StreamingFS contract, chunk bound, total bound
// and atomic-write semantics rather than defining a second protocol. Sessions
// are bound to one owner and one server id, expire, carry a running SHA-256,
// and can be cancelled mid-transfer.
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"sync"
	"time"

	"github.com/athNdev/carbon-panel/internal/storageworker"
)

var (
	ErrStreamCanceled  = errors.New("stream transfer was canceled")
	ErrStreamExpired   = errors.New("stream session expired")
	ErrStreamCrossScope = errors.New("stream session belongs to a different owner or server")
	ErrStreamTooLarge  = errors.New("stream exceeds the bounded transfer cap")
	ErrStreamNotFound  = errors.New("stream session not found")
)

// StreamTransfer is a scoped, bounded download session manager. The fs is the
// shared W17 streaming filesystem contract (local or remote accessor); no new
// wire protocol is introduced here.
type StreamTransfer struct {
	fs      storageworker.StreamingFS
	ttl     time.Duration
	now     func() time.Time
	cap     int64

	mu       sync.Mutex
	sessions map[string]*streamSession
}

type streamSession struct {
	owner    string
	serverID string
	path     string
	rel      string
	handle   storageworker.ReadHandle
	size     int64
	offset   int64
	expires  time.Time
	canceled bool
	hash     hash.Hash
	data     []byte
}

// NewStreamTransfer builds a transfer manager over the shared streaming
// filesystem. cap bounds a single composed transfer; it may not exceed the
// W17 maximum stream bound.
func NewStreamTransfer(fs storageworker.StreamingFS, capBytes int64, ttl time.Duration, now func() time.Time) (*StreamTransfer, error) {
	if fs == nil {
		return nil, errors.New("mcp: nil streaming filesystem")
	}
	if capBytes <= 0 || capBytes > storageworker.MaxStreamBytes {
		return nil, fmt.Errorf("mcp: transfer cap %d outside W17 bounds", capBytes)
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	if now == nil {
		now = time.Now
	}
	return &StreamTransfer{fs: fs, cap: capBytes, ttl: ttl, now: now, sessions: map[string]*streamSession{}}, nil
}

func newStreamHandle() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// Open starts a scoped download session for one server-relative path. The
// owner and server id are recorded and checked on every later operation.
func (t *StreamTransfer) Open(ctx context.Context, owner, serverID, rel string) (string, error) {
	return t.OpenWithSize(ctx, owner, serverID, rel, t.cap)
}

// OpenWithSize starts a session with a known bounded size (from the typed
// download metadata). Oversize sources are refused before any bytes move.
func (t *StreamTransfer) OpenWithSize(ctx context.Context, owner, serverID, rel string, size int64) (string, error) {
	if size < 0 || size > t.cap {
		return "", fmt.Errorf("%w: size %d cap %d", ErrStreamTooLarge, size, t.cap)
	}
	h, err := t.fs.OpenRead(ctx, rel)
	if err != nil {
		return "", err
	}
	handle := newStreamHandle()
	t.mu.Lock()
	t.sessions[handle] = &streamSession{
		owner: owner, serverID: serverID, path: rel, rel: rel, handle: h,
		size: size, expires: t.now().Add(t.ttl), hash: sha256.New(),
	}
	t.mu.Unlock()
	return handle, nil
}

// Frames returns the bytes accumulated through ReadChunk and the cumulative
// SHA-256. The caller must send them through an authorized production write
// path; this helper never writes to the controller.
func (t *StreamTransfer) Frames(owner, handle string) ([]byte, string, error) {
	s, err := t.session(owner, "", handle)
	if err != nil {
		return nil, "", err
	}
	return s.data, hex.EncodeToString(s.hash.Sum(nil)), nil
}

// Lookup returns a scoped session by its opaque handle. owner is always the
// authenticated principal derived from the caller's token, never a tool arg.
func (t *StreamTransfer) Lookup(owner, handle string) (*streamSession, error) {
	return t.session(owner, "", handle)
}

// ServerID is exposed for tool handlers to pass the bound server to typed RPCs.
func (s *streamSession) ServerID() string { return s.serverID }

// Path is the bound server-relative source path.
func (s *streamSession) Path() string { return s.path }

func (t *StreamTransfer) session(owner, serverID, handle string) (*streamSession, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[handle]
	if !ok {
		return nil, ErrStreamNotFound
	}
	if s.owner != owner || (serverID != "" && s.serverID != serverID) {
		return nil, ErrStreamCrossScope
	}
	if s.canceled {
		return nil, ErrStreamCanceled
	}
	if !t.now().Before(s.expires) {
		return nil, ErrStreamExpired
	}
	return s, nil
}

// ReadChunk returns the next bounded frame and its cumulative SHA-256. It
// reuses the W17 ChunkBytes bound and enforces the session's total cap.
func (t *StreamTransfer) ReadChunk(ctx context.Context, owner, serverID, handle string) ([]byte, string, error) {
	s, err := t.session(owner, serverID, handle)
	if err != nil {
		return nil, "", err
	}
	buf := make([]byte, storageworker.ChunkBytes)
	n, readErr := s.handle.Read(buf)
	if readErr != nil && readErr != io.EOF {
		return nil, "", readErr
	}
	chunk := buf[:n]
	if s.offset+int64(n) > t.cap {
		return nil, "", fmt.Errorf("%w: cap %d", ErrStreamTooLarge, t.cap)
	}
	_, _ = s.hash.Write(chunk)
	s.data = append(s.data, chunk...)
	s.offset += int64(n)
	if readErr == io.EOF {
		return chunk, hex.EncodeToString(s.hash.Sum(nil)), io.EOF
	}
	return chunk, "", nil
}

// Cancel aborts a session mid-transfer and closes only its owned handle.
func (t *StreamTransfer) Cancel(owner, serverID, handle string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[handle]
	if !ok {
		return ErrStreamNotFound
	}
	if s.owner != owner || s.serverID != serverID {
		return ErrStreamCrossScope
	}
	s.canceled = true
	// Keep the tombstone so every later read reports the cancellation rather
	// than looking like an unknown handle. Close (not Cancel) releases it.
	return s.handle.Close()
}

// Close releases a finished or abandoned session without committing bytes.
func (t *StreamTransfer) Close(owner, serverID, handle string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[handle]
	if !ok {
		return ErrStreamNotFound
	}
	if s.owner != owner || s.serverID != serverID {
		return ErrStreamCrossScope
	}
	err := s.handle.Close()
	delete(t.sessions, handle)
	return err
}

// Commit writes the full session to one controlled destination through the
// W17 AtomicWriter and returns the byte count and cumulative SHA-256.
func (t *StreamTransfer) Commit(ctx context.Context, owner, serverID, handle, dest string) (int64, string, error) {
	s, err := t.session(owner, serverID, handle)
	if err != nil {
		return 0, "", err
	}
	w, err := t.fs.BeginWrite(ctx, dest, s.size)
	if err != nil {
		return 0, "", err
	}
	if int64(len(s.data)) > t.cap || int64(len(s.data)) > s.size {
		_ = w.Close()
		return 0, "", fmt.Errorf("%w: cap %d", ErrStreamTooLarge, t.cap)
	}
	// Write the bytes the session actually received through ReadChunk. The
	// AtomicWriter promotes only on Commit; any failure aborts its own temp.
	if len(s.data) != 0 {
		if _, err := w.Write(s.data); err != nil {
			_ = w.Close()
			return 0, "", err
		}
	}
	if err := w.Commit(); err != nil {
		return 0, "", err
	}
	return int64(len(s.data)), hex.EncodeToString(s.hash.Sum(nil)), nil
}
