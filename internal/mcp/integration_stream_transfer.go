// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-387): gateway-side view of CONTROLLER-OWNED
// bounded byte-stream sessions. The gateway holds only an opaque session id;
// every frame travels through an authenticated typed Connect RPC. Panel/node
// storage is never shared with the gateway process.
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

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

var (
	ErrStreamCanceled   = errors.New("stream transfer was canceled")
	ErrStreamExpired    = errors.New("stream session expired")
	ErrStreamCrossScope = errors.New("stream session belongs to a different owner or server")
	ErrStreamTooLarge   = errors.New("stream exceeds the bounded transfer cap")
	ErrStreamNotFound   = errors.New("stream session not found")
)

// StreamTransfer is the gateway-side session registry. The real byte handle
// lives on the controller; this struct only tracks opaque ids, offsets and
// SHA-256 state for the bounded tools/call workflow.
type StreamTransfer struct {
	client carbonpanelv1connect.FileServiceClient
	ttl    time.Duration
	mu     sync.Mutex
	sessions map[string]*streamSession
}

type streamSession struct {
	handle       string
	controllerID string
	owner        string
	serverID     string
	path         string
	total        int64
	nextFrame    int32
	offset       int64
	expires      time.Time
	canceled     bool
	hash         hash.Hash
	data         []byte
}

// NewStreamTransfer builds the gateway session registry over the authenticated
// controller FileService client. There is no storage filesystem parameter: the
// controller owns storage by construction.
func NewStreamTransfer(client carbonpanelv1connect.FileServiceClient, ttl time.Duration) (*StreamTransfer, error) {
	if client == nil {
		return nil, errors.New("mcp: nil controller FileService client")
	}
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &StreamTransfer{client: client, ttl: ttl, sessions: map[string]*streamSession{}}, nil
}

func newStreamHandle() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// OpenWithSize asks the controller to open a bounded read handle. owner is the
// authenticated principal derived from the caller's token, never a tool arg.
func (t *StreamTransfer) OpenWithSize(ctx context.Context, owner, serverID, path string, size int64) (string, error) {
	if size < 0 || size > storageworker.MaxStreamBytes {
		return "", fmt.Errorf("%w: size %d", ErrStreamTooLarge, size)
	}
	res, err := t.client.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: serverID, Path: path, TotalSize: size,
	}))
	if err != nil {
		return "", err
	}
	handle := newStreamHandle()
	t.mu.Lock()
	t.sessions[handle] = &streamSession{
		handle: handle, controllerID: res.Msg.GetSessionId(),
		owner: owner, serverID: serverID, path: path, total: size,
		expires: time.Now().Add(t.ttl), hash: sha256.New(),
	}
	t.mu.Unlock()
	return handle, nil
}

func (t *StreamTransfer) session(owner, handle string) (*streamSession, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[handle]
	if !ok {
		return nil, ErrStreamNotFound
	}
	if s.owner != owner {
		return nil, ErrStreamCrossScope
	}
	if s.canceled {
		return nil, ErrStreamCanceled
	}
	if !time.Now().Before(s.expires) {
		return nil, ErrStreamExpired
	}
	return s, nil
}

// Lookup returns a session bound to the authenticated owner.
func (t *StreamTransfer) Lookup(owner, handle string) (*streamSession, error) {
	return t.session(owner, handle)
}

func (s *streamSession) ServerID() string { return s.serverID }
func (s *streamSession) Path() string     { return s.path }

// ReadChunk reads exactly one controller frame. ctx keeps the owning tools/call
// deadline and cancellation: no context.WithoutCancel is used.
func (t *StreamTransfer) ReadChunk(ctx context.Context, owner, handle string) ([]byte, string, int64, error) {
	s, err := t.session(owner, handle)
	if err != nil {
		return nil, "", 0, err
	}
	res, err := t.client.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: s.controllerID, FrameIndex: s.nextFrame, ServerId: s.serverID,
	}))
	if err != nil {
		return nil, "", 0, err
	}
	frame := res.Msg.GetData()
	if int64(len(frame)) > storageworker.ChunkBytes {
		return nil, "", 0, fmt.Errorf("%w: frame %d", ErrStreamTooLarge, len(frame))
	}
	s.data = append(s.data, frame...)
	_, _ = s.hash.Write(frame)
	s.offset += int64(len(frame))
	s.nextFrame++
	if res.Msg.GetDone() {
		// The controller's cumulative offset is the authoritative ACTUAL byte
		// count. Prefer it over locally accumulated bytes; they must agree.
		actual := res.Msg.GetOffset()
		if actual != s.offset {
			return nil, "", 0, fmt.Errorf("%w: controller offset %d, local %d", ErrStreamTooLarge, actual, s.offset)
		}
		return frame, res.Msg.GetSha256(), actual, io.EOF
	}
	return frame, "", s.offset, nil
}

// Frames returns the accumulated bytes and cumulative SHA-256 for the authorized
// commit step. It never writes to storage.
func (t *StreamTransfer) Frames(owner, handle string) ([]byte, string, error) {
	s, err := t.session(owner, handle)
	if err != nil {
		return nil, "", err
	}
	return s.data, hex.EncodeToString(s.hash.Sum(nil)), nil
}

// Cancel asks the controller to abort and release only this session.
func (t *StreamTransfer) Cancel(ctx context.Context, owner, handle string) error {
	s, err := t.session(owner, handle)
	if err != nil {
		return err
	}
	if _, err := t.client.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: s.controllerID, Cancel: true, ServerId: s.serverID,
	})); err != nil {
		return err
	}
	t.mu.Lock()
	s.canceled = true
	t.mu.Unlock()
	return nil
}

// Close releases a finished session on the controller.
func (t *StreamTransfer) Close(ctx context.Context, owner, handle string) error {
	s, err := t.session(owner, handle)
	if err != nil {
		return err
	}
	if _, err := t.client.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: s.controllerID, ServerId: s.serverID,
	})); err != nil {
		return err
	}
	t.mu.Lock()
	delete(t.sessions, handle)
	t.mu.Unlock()
	return nil
}
