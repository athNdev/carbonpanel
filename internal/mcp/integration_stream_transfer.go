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

// Gateway staging is deliberately bounded. The gateway is a stdio process, not
// a storage tier: it may hold only a small bounded working set of frames while
// a composed upload/commit is assembled. Larger transfers must use the
// controller-owned download/upload REST flow instead.
const (
	maxGatewaySessions       = 64
	maxGatewaySessionBytes   = 8 << 20  // per session staged bytes
	maxGatewayAggregateBytes = 64 << 20 // across all gateway sessions
)

var (
	ErrStreamCanceled       = errors.New("stream transfer was canceled")
	ErrStreamExpired        = errors.New("stream session expired")
	ErrStreamCrossScope     = errors.New("stream session belongs to a different owner or server")
	ErrStreamTooLarge       = errors.New("stream exceeds the bounded transfer cap")
	ErrStreamNotFound       = errors.New("stream session not found")
	ErrStreamNotComplete    = errors.New("stream is not complete: all frames must be read before commit")
	ErrStreamDigestMismatch = errors.New("stream digest does not match the controller's final SHA-256")
	ErrStreamAdmission      = errors.New("gateway stream admission limit reached")
	ErrStreamCommitBusy     = errors.New("stream has an in-flight commit; retry after it finishes")
	ErrCommitOutcomeUnknown = errors.New("SaveUploadedFile outcome UNKNOWN: verify the destination before any retry")

)

// CommitOutcome distinguishes a definitely-safe retry from an ambiguous
// post-dispatch save whose bytes may already be durable.
type CommitOutcome int

const (
	CommitSucceeded CommitOutcome = iota
	CommitRetryable
	CommitUnknown
)

// StreamTransfer is the gateway-side session registry. The real byte handle
// lives on the controller; this struct only tracks opaque ids, offsets and
// SHA-256 state for the bounded tools/call workflow.
//
// Locking: t.mu protects the registry, the aggregate byte counter and the
// per-handle staged-byte accounting. It is NEVER held across a controller RPC.
// Each session has its own mu serializing frame/state operations for THAT
// session only, including the (potentially slow) frame RPC. No shared registry
// lock is held across a slow read. Lock order, where both are needed, is
// session.mu -> t.mu; release helpers never take t.mu while waiting for a
// session lock, so there is no lock cycle with the TTL timer.
type StreamTransfer struct {
	client   carbonpanelv1connect.FileServiceClient
	ttl      time.Duration
	mu       sync.Mutex
	sessions  map[string]*streamSession
	staged    map[string]int  // bytes staged per handle, guarded by t.mu
	pinned    map[string]bool // handles with an in-flight commit lease, guarded by t.mu
	aggregate int             // total staged bytes, guarded by t.mu
}

type streamSession struct {
	// mu serializes all mutable state for this session. Immutable identity
	// fields below are set once at open and never modified.
	mu           sync.Mutex
	handle       string
	controllerID string
	owner        string
	serverID     string
	path         string
	total        int64
	nextFrame    int32
	offset       int64
	actualSize   int64
	expires      time.Time
	canceled     bool
	completed    bool
	committing   bool // one-at-a-time commit lease
	commitPinned bool // budget is pinned until FinishCommit
	controllerSHA string
	hash         hash.Hash
	data         []byte
	timer        *time.Timer
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
	return &StreamTransfer{
		client: client, ttl: ttl,
		sessions: map[string]*streamSession{}, staged: map[string]int{}, pinned: map[string]bool{},
	}, nil
}

func newStreamHandle() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// OpenWithSize asks the controller to open a bounded read handle. owner is the
// authenticated principal derived from the caller's token, never a tool arg.
// Declared size is an ADMISSION UPPER BOUND, not an exact byte count.
func (t *StreamTransfer) OpenWithSize(ctx context.Context, owner, serverID, path string, size int64) (string, error) {
	if size < 0 || size > maxGatewaySessionBytes || size > storageworker.MaxStreamBytes {
		return "", fmt.Errorf("%w: size %d", ErrStreamTooLarge, size)
	}
	t.mu.Lock()
	if len(t.sessions) >= maxGatewaySessions || t.aggregate >= maxGatewayAggregateBytes {
		t.mu.Unlock()
		return "", ErrStreamAdmission
	}
	t.mu.Unlock()

	res, err := t.client.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: serverID, Path: path, TotalSize: size,
	}))
	if err != nil {
		return "", err
	}
	handle := newStreamHandle()
	t.mu.Lock()
	// Re-check after the RPC so concurrent opens cannot over-admit.
	if len(t.sessions) >= maxGatewaySessions || t.aggregate >= maxGatewayAggregateBytes {
		t.mu.Unlock()
		// Cleanup is independent of a canceled/expired tools/call context so the
		// controller-owned handle is not leaked on an admission race.
		closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, _ = t.client.CloseStream(closeCtx, connect.NewRequest(&v1.CloseStreamRequest{
			SessionId: res.Msg.GetSessionId(), Cancel: true, ServerId: serverID,
		}))
		cancel()
		return "", ErrStreamAdmission
	}
	s := &streamSession{
		handle: handle, controllerID: res.Msg.GetSessionId(),
		owner: owner, serverID: serverID, path: path, total: size,
		expires: time.Now().Add(t.ttl), hash: sha256.New(),
	}
	// Proactive TTL release: the timer removes the entry and frees staged bytes
	// even if no further tools/call ever touches this session. Install it before
	// publishing the session so the callback never races the timer assignment.
	s.timer = time.AfterFunc(t.ttl, func() { t.expire(handle) })
	t.sessions[handle] = s
	t.staged[handle] = 0
	t.mu.Unlock()
	return handle, nil
}

// acquire validates an owner-bound session and returns it with its session
// mutex HELD. The caller must unlock it. Expired sessions are proactively
// evicted on access as well as by their timer.
func (t *StreamTransfer) acquire(owner, handle string) (*streamSession, error) {
	t.mu.Lock()
	s, ok := t.sessions[handle]
	t.mu.Unlock()
	if !ok {
		return nil, ErrStreamNotFound
	}
	if s.owner != owner {
		return nil, ErrStreamCrossScope
	}
	s.mu.Lock()
	if s.canceled {
		s.mu.Unlock()
		return nil, ErrStreamCanceled
	}
	if !time.Now().Before(s.expires) {
		s.mu.Unlock()
		t.release(handle)
		return nil, ErrStreamExpired
	}
	return s, nil
}

// releaseLocked removes registry bookkeeping. The caller holds t.mu. Session
// state is cleared only AFTER t.mu is dropped (see release/expire) to avoid a
// lock cycle with a frame in flight.
func (t *StreamTransfer) releaseLocked(handle string) {
	n := t.staged[handle]
	delete(t.staged, handle)
	t.aggregate -= n
	delete(t.sessions, handle)
}

func (t *StreamTransfer) release(handle string) {
	t.mu.Lock()
	s, ok := t.sessions[handle]
	pinned := t.pinned[handle]
	// An in-flight commit still holds a reference to the staged slice. Keep
	// its budget accounted and its registry entry until FinishCommit runs.
	if ok && !pinned {
		t.releaseLocked(handle)
	}
	t.mu.Unlock()
	if ok {
		if s.timer != nil {
			s.timer.Stop()
		}
		s.mu.Lock()
		s.canceled = true
		if !pinned {
			s.data = nil
			s.hash = nil
			s.completed = false
		}
		s.mu.Unlock()
	}
}

// expire is the TTL timer callback. It removes the session and frees staged
// bytes without waiting on t.mu while holding a session lock.
func (t *StreamTransfer) expire(handle string) {
	t.release(handle)
}

// Lookup returns a session bound to the authenticated owner. The returned
// session has no lock held; this is used only for cheap identity checks.
func (t *StreamTransfer) Lookup(owner, handle string) (*streamSession, error) {
	s, err := t.acquire(owner, handle)
	if err != nil {
		return nil, err
	}
	s.mu.Unlock()
	return s, nil
}

func (s *streamSession) ServerID() string { return s.serverID }
func (s *streamSession) Path() string     { return s.path }

// ReadChunk reads exactly one controller frame. The session mutex is held for
// the RPC and the frame-state update, so parallel tools/call on one session can
// never interleave nextFrame/offset/SHA mutations. ctx keeps the owning
// tools/call deadline and cancellation: no context.WithoutCancel is used.
func (t *StreamTransfer) ReadChunk(ctx context.Context, owner, handle string) ([]byte, string, int64, error) {
	s, err := t.acquire(owner, handle)
	if err != nil {
		return nil, "", 0, err
	}
	var failed bool
	var closeOnFail bool
	defer func() {
		if failed {
			cid, sid := s.controllerID, s.serverID
			s.mu.Unlock()
			t.release(handle)
			if closeOnFail {
				// Best-effort cleanup independent of the caller's context: a
				// terminal gateway-side failure must not leave a controller-owned
				// handle open until TTL expiry.
				closeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, _ = t.client.CloseStream(closeCtx, connect.NewRequest(&v1.CloseStreamRequest{
					SessionId: cid, Cancel: true, ServerId: sid,
				}))
				cancel()
			}
			return
		}
		s.mu.Unlock()
	}()
	if s.completed {
		return nil, "", 0, errors.New("stream already completed")
	}
	res, err := t.client.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: s.controllerID, FrameIndex: s.nextFrame, ServerId: s.serverID,
	}))
	if err != nil {
		return nil, "", 0, err
	}
	frame := res.Msg.GetData()
	if int64(len(frame)) > storageworker.ChunkBytes {
		failed = true
		closeOnFail = true
		return nil, "", 0, fmt.Errorf("%w: frame %d", ErrStreamTooLarge, len(frame))
	}
	if len(s.data)+len(frame) > maxGatewaySessionBytes {
		failed = true
		closeOnFail = true
		return nil, "", 0, ErrStreamTooLarge
	}

	t.mu.Lock()
	if t.staged[handle]+len(frame) > maxGatewayAggregateBytes {
		t.mu.Unlock()
		failed = true
		closeOnFail = true
		return nil, "", 0, ErrStreamTooLarge
	}
	t.staged[handle] += len(frame)
	t.aggregate += len(frame)
	t.mu.Unlock()

	s.data = append(s.data, frame...)
	_, _ = s.hash.Write(frame)
	s.offset += int64(len(frame))
	s.nextFrame++
	if res.Msg.GetDone() {
		// The controller's offset and SHA are authoritative. Both must agree
		// with locally accumulated bytes before a commit is even possible.
		actual := res.Msg.GetOffset()
		if actual != s.offset {
			failed = true
			closeOnFail = true
			return nil, "", 0, fmt.Errorf("%w: controller offset %d, local %d", ErrStreamTooLarge, actual, s.offset)
		}
		local := hex.EncodeToString(s.hash.Sum(nil))
		if res.Msg.GetSha256() != local {
			failed = true
			closeOnFail = true
			return nil, "", 0, fmt.Errorf("%w: controller %q local %q", ErrStreamDigestMismatch, res.Msg.GetSha256(), local)
		}
		s.controllerSHA = local
		s.actualSize = actual
		s.completed = true
		return frame, local, actual, io.EOF
	}
	return frame, "", s.offset, nil
}

// ClaimCommit atomically takes the one-at-a-time commit lease for a completed,
// digest-validated stream. It pins the staged bytes in the gateway budget for
// the whole upload: TTL/cancel/release cannot free memory the uploader still
// references. A second concurrent claim is refused with ErrStreamCommitBusy
// before any destination mutation. The caller MUST call FinishCommit exactly
// once (normally via defer) with success=true only after SaveUploadedFile.
func (t *StreamTransfer) ClaimCommit(owner, handle string) ([]byte, string, error) {
	s, err := t.acquire(owner, handle)
	if err != nil {
		return nil, "", err
	}
	defer s.mu.Unlock()
	if !s.completed {
		return nil, "", ErrStreamNotComplete
	}
	local := hex.EncodeToString(s.hash.Sum(nil))
	if s.controllerSHA == "" || s.controllerSHA != local {
		return nil, "", ErrStreamDigestMismatch
	}
	if s.actualSize != int64(len(s.data)) {
		return nil, "", ErrStreamTooLarge
	}
	if s.committing {
		return nil, "", ErrStreamCommitBusy
	}
	s.committing = true
	s.commitPinned = true
	t.mu.Lock()
	t.pinned[handle] = true
	t.mu.Unlock()
	return s.data, s.controllerSHA, nil
}

// FinishCommit releases the commit lease. On success (or after TTL/cancel),
// the staged bytes and registry entry are freed exactly once. On an ordinary
// upload failure the bytes stay staged and the lease is released so the same
// handle can be retried deterministically with identical bytes/digest.
func (t *StreamTransfer) FinishCommit(owner, handle string, outcome CommitOutcome) {
	t.mu.Lock()
	s, ok := t.sessions[handle]
	pinned := t.pinned[handle]
	t.mu.Unlock()
	if !ok || !pinned {
		return
	}
	s.mu.Lock()
	// Retryable keeps the bytes staged; success and UNKNOWN both terminate the
	// session so an ambiguous save can never be blindly re-dispatched.
	terminal := outcome != CommitRetryable || s.canceled || !time.Now().Before(s.expires)
	s.committing = false
	s.commitPinned = false
	if terminal {
		s.data = nil
		s.hash = nil
		s.completed = false
	}
	s.mu.Unlock()

	if s.timer != nil {
		s.timer.Stop()
	}
	t.mu.Lock()
	delete(t.pinned, handle)
	if terminal {
		if _, still := t.sessions[handle]; still {
			t.releaseLocked(handle)
		}
	}
	t.mu.Unlock()
}

// Cancel asks the controller to abort and immediately frees all gateway state.
func (t *StreamTransfer) Cancel(ctx context.Context, owner, handle string) error {
	s, err := t.acquire(owner, handle)
	if err != nil {
		return err
	}
	defer s.mu.Unlock()
	if _, err := t.client.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: s.controllerID, Cancel: true, ServerId: s.serverID,
	})); err != nil {
		return err
	}
	t.mu.Lock()
	_, present := t.sessions[handle]
	pinned := t.pinned[handle]
	if present && !pinned {
		t.releaseLocked(handle)
	}
	t.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.canceled = true
	if !pinned {
		s.data = nil
		s.hash = nil
		s.completed = false
	}
	return nil
}

// Close releases a finished session on the controller and frees gateway state.
func (t *StreamTransfer) Close(ctx context.Context, owner, handle string) error {
	s, err := t.acquire(owner, handle)
	if err != nil {
		return err
	}
	defer s.mu.Unlock()
	if _, err := t.client.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: s.controllerID, ServerId: s.serverID,
	})); err != nil {
		return err
	}
	t.mu.Lock()
	_, present := t.sessions[handle]
	pinned := t.pinned[handle]
	if present && !pinned {
		t.releaseLocked(handle)
	}
	t.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
	}
	s.canceled = true
	if !pinned {
		s.data = nil
		s.hash = nil
		s.completed = false
	}
	return nil
}

// Release frees gateway-side state for a session with no in-flight commit.
// Committed streams use FinishCommit instead so their budget is held until the
// upload actually completes.
func (t *StreamTransfer) Release(owner, handle string) {
	t.release(handle)
}
