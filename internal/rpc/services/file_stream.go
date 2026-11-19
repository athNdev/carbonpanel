// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// OSS-387: controller-owned bounded byte-stream sessions. The gateway never
// opens node storage; it holds only an opaque session handle and reads framed
// bytes through these authenticated RPCs.
//
// Session/permission policy (documented here and tested in
// internal/mcp/integration_stream_entrypoint_test.go and
// internal/rpc/services/file_stream_test.go):
//
//   - Authorization is NOT cached in the session. Every RPC is re-authorized
//     by the production rpc.NewAuthInterceptor against the caller's current
//     dp_ token and the procedure's live RBAC mapping. A scope revoked between
//     open and a later frame/commit is refused on that call with zero bytes
//     read and zero controller writes.
//   - Ownership AND server binding are enforced on every call: a handle
//     opened for server A is refused for a request scoped to server B even
//     when the authenticated principal is identical. The interceptor scopes
//     all three stream RPCs to files/read[server_id]; the service repeats the
//     check against the session's immutable serverID before any read, close
//     or state mutation.
//   - The session owns a REAL bounded context (context.WithTimeout) derived
//     from the service-owned job context. It is deliberately not the opening
//     HTTP request's context: that request ends as soon as OpenStream returns
//     and a handle bound to it would be canceled before the first frame. It is
//     also not context.WithoutCancel: the deadline is real, the context is
//     canceled exactly once on close/expiry/service-stop, and a bounded
//     reaper evicts stale registry entries so the map cannot grow without
//     bound.
package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const (
	streamFrameBytes      = storageworker.ChunkBytes
	streamSessionTTL      = 5 * time.Minute
	streamMaxPerPrincipal = 16
	streamMaxTotal        = 64
	streamReapInterval    = 30 * time.Second
)

var (
	errStreamExpired     = errors.New("stream session expired")
	errStreamCanceled    = errors.New("stream session canceled")
	errStreamCrossOwner  = errors.New("stream session belongs to another principal")
	errStreamCrossServer = errors.New("stream session belongs to another server")
	errStreamNotFound    = errors.New("stream session not found")
	errStreamCap         = errors.New("stream frame or total size exceeds bound")
	errStreamFrameLost   = errors.New("stream frame is no longer cached; close and reopen the session")
	errStreamTerminal    = errors.New("stream session terminated after a partial or over-cap read; close and reopen")
)

// cachedFrame makes a frame read idempotent by frame_index. A caller whose
// context is canceled while a remote read is in flight can safely retry the
// same index: the frame was consumed from the handle exactly once and is
// returned from this small bounded cache instead of being lost.
type cachedFrame struct {
	data   []byte
	offset int64
	done   bool
	sha    string
}

const streamFrameCache = 2

type controllerStreamSession struct {
	// mu serializes ONLY this session's frame state and handle lifecycle. It
	// is never the shared registry lock: a slow remote read cannot block
	// lookups or frames for other sessions. Lock order is always
	// state.mu -> session.mu, never the reverse.
	mu       sync.Mutex
	owner    string
	serverID string
	path     string
	handle   storageworker.ReadHandle
	accessor nodestorage.Accessor
	ctx      context.Context    // bounded session lifetime, service-owned
	cancel   context.CancelFunc // releases the handle's context exactly once
	expires  time.Time
	closed   bool
	failed   bool
	total    int64
	nextFrame int32
	offset    int64
	hash      hash.Hash
	frames    map[int32]cachedFrame // bounded retry cache (idempotent frame_index)
}

type controllerStreamState struct {
	mu             sync.Mutex
	sessions       map[string]*controllerStreamSession
	opening        int            // in-flight opens, counted against the total cap
	openingByOwner map[string]int // in-flight opens, counted against the per-owner cap
}

func (s *FileService) streamState() *controllerStreamState {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	if s.streams == nil {
		s.streams = &controllerStreamState{
			sessions: map[string]*controllerStreamSession{},
			openingByOwner: map[string]int{},
		}
	}
	return s.streams
}

func streamOwner(ctx context.Context) (string, error) {
	u := auth.GetUserFromContext(ctx)
	if u == nil || u.ID == "" {
		return "", connect.NewError(connect.CodeUnauthenticated, errors.New("no authenticated principal for stream session"))
	}
	return u.ID, nil
}

func newStreamID() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

// cleanupSession releases one session's context and storage resources exactly
// once. The caller must NOT hold state.mu. It blocks until any in-flight frame
// on the same session completes (session.mu), so a close never races a read.
func cleanupSession(session *controllerStreamSession) {
	// Cancel FIRST, before taking the session lock. A remote read in flight
	// holds session.mu; an ctx-aware transport can only unblock that read once
	// the owned session context is canceled. Deferring cancel until after the
	// lock would deadlock shutdown behind a blocked remote frame.
	session.cancel()
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed {
		return
	}
	session.closed = true
	if session.handle != nil {
		_ = session.handle.Close()
	}
	// A staged-download stream owns a controller temp file handle, not a node
	// storage accessor. Only node-path streams have an accessor to release.
	if session.accessor != nil {
		_ = session.accessor.Close()
	}
}

// terminateSession permanently fails a session after a read consumed bytes but
// could not be completed honestly (partial read with an error, or over-cap).
// The handle is evicted and closed so a retry can never skip bytes at an
// advanced offset. Call only AFTER releasing session.mu.
func (s *FileService) terminateSession(id string, session *controllerStreamSession) {
	state := s.streamState()
	state.mu.Lock()
	delete(state.sessions, id)
	state.mu.Unlock()
	session.mu.Lock()
	session.failed = true
	session.mu.Unlock()
	cleanupSession(session)
}

// lookupSession resolves a session for an authenticated owner and the exact
// server named by the request. It holds the registry lock only for the map
// lookup and the immutable-field checks, never across a storage read.
// Expired sessions are evicted (state change) before the error is returned;
// a server mismatch is refused with NO state change and NO bytes.
func (s *FileService) lookupSession(owner, serverID, id string) (*controllerStreamSession, error) {
	state := s.streamState()
	state.mu.Lock()
	session, ok := state.sessions[id]
	if !ok {
		state.mu.Unlock()
		return nil, errStreamNotFound
	}
	if session.owner != owner {
		state.mu.Unlock()
		return nil, errStreamCrossOwner
	}
	if serverID == "" || serverID != session.serverID {
		state.mu.Unlock()
		return nil, errStreamCrossServer
	}
	if !time.Now().Before(session.expires) || session.ctx.Err() != nil {
		delete(state.sessions, id)
		state.mu.Unlock()
		cleanupSession(session)
		return nil, errStreamExpired
	}
	session.mu.Lock()
	failed := session.failed
	session.mu.Unlock()
	if failed {
		state.mu.Lock()
		delete(state.sessions, id)
		state.mu.Unlock()
		cleanupSession(session)
		return nil, errStreamTerminal
	}
	state.mu.Unlock()
	return session, nil
}

// OpenStream opens a read handle on the owning node through the shared node
// storage resolver. No path outside the server data root and no process-global
// transport is used. The handle is bound to a bounded, service-owned session
// context, not to the short-lived opening request.
func (s *FileService) OpenStream(ctx context.Context, req *connect.Request[v1.OpenStreamRequest]) (*connect.Response[v1.OpenStreamResponse], error) {
	owner, err := streamOwner(ctx)
	if err != nil {
		return nil, err
	}
	msg := req.Msg
	if msg.GetServerId() == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server_id is required"))
	}
	// A staged-download stream carries a download_session_id instead of a
	// server-relative path. The controller opens the download's owned temp file;
	// no node path is ever accepted from the gateway for that flow.
	if msg.GetDownloadSessionId() == "" {
		if msg.GetPath() == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path is required"))
		}
		if strings.Contains(msg.GetPath(), "..") || filepath.IsAbs(msg.GetPath()) {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("path must be server-relative and bounded"))
		}
	}
	if msg.GetTotalSize() < 0 || msg.GetTotalSize() > storageworker.MaxStreamBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errStreamCap)
	}
	server, err := s.store.GetServer(ctx, msg.GetServerId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}
	if dsid := msg.GetDownloadSessionId(); dsid != "" {
		return s.openStagedDownloadStream(ctx, owner, server, dsid, msg.GetTotalSize())
	}

	state := s.streamState()
	// Reserve a slot BEFORE the (potentially slow) storage open. Both completed
	// sessions and in-flight opens count against the per-owner and global caps;
	// otherwise concurrent opens can all pass a pre-check and over-admit.
	state.mu.Lock()
	perPrincipal, total := 0, len(state.sessions)
	for _, session := range state.sessions {
		if session.owner == owner {
			perPrincipal++
		}
	}
	if perPrincipal+state.openingByOwner[owner] >= streamMaxPerPrincipal ||
		total+state.opening >= streamMaxTotal {
		state.mu.Unlock()
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many open stream sessions"))
	}
	state.opening++
	state.openingByOwner[owner]++
	state.mu.Unlock()

	releaseReservation := func() {
		state.mu.Lock()
		state.opening--
		state.openingByOwner[owner]--
		state.mu.Unlock()
	}

	sessionCtx, cancel := context.WithTimeout(s.jobCtx, streamSessionTTL)
	_, accessor, err := s.openReadOnlyStorage(sessionCtx, server)
	if err != nil {
		releaseReservation()
		cancel()
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	handle, err := accessor.OpenRead(sessionCtx, msg.GetPath())
	if err != nil {
		releaseReservation()
		cancel()
		_ = accessor.Close()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("stream source not available"))
	}

	state.mu.Lock()
	// The reservation guarantees a slot is still available: release it as the
	// established session is inserted, keeping the cap invariant continuous.
	state.opening--
	state.openingByOwner[owner]--
	id := newStreamID()
	state.sessions[id] = &controllerStreamSession{
		owner: owner, serverID: msg.GetServerId(), path: msg.GetPath(),
		handle: handle, accessor: accessor, ctx: sessionCtx, cancel: cancel,
		expires: time.Now().Add(streamSessionTTL), hash: sha256.New(), total: msg.GetTotalSize(),
		frames: make(map[int32]cachedFrame, streamFrameCache),
	}
	state.mu.Unlock()
	return connect.NewResponse(&v1.OpenStreamResponse{SessionId: id, Size: msg.GetTotalSize()}), nil
}

// openStagedDownloadStream opens a bounded frame stream over a download session
// staged by InitFileDownload, DownloadArchive or ExportWorld. The bytes live in
// the controller's owned temp directory; the gateway only ever sends the opaque
// download session id and reads framed bytes through ReadStreamFrame. Authorization
// is unchanged: the production interceptor scopes OpenStream to
// files/read[server_id], and the session's immutable server binding is checked
// here before a handle is opened. There is no remote->controller fallback and no
// gateway-visible storage path.
func (s *FileService) openStagedDownloadStream(ctx context.Context, owner string, server *storage.Server,
	downloadSessionID string, declared int64) (*connect.Response[v1.OpenStreamResponse], error) {
	if s.downloadManager == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("download manager is not configured"))
	}
	download, err := s.downloadManager.GetSession(downloadSessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("download session not found or expired"))
	}
	if download.ServerID == "" || download.ServerID != server.ID {
		return nil, connect.NewError(connect.CodePermissionDenied, errStreamCrossServer)
	}
	// The staged file is bound to the principal that created it. A legacy
	// session without a recorded creator keeps the historical server-scoped
	// behavior; a recorded creator is enforced fail-closed here.
	if download.CreatorUserID != "" && download.CreatorUserID != owner {
		return nil, connect.NewError(connect.CodePermissionDenied, errStreamCrossOwner)
	}
	if declared < 0 || declared > download.TotalSize || download.TotalSize > storageworker.MaxStreamBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, errStreamCap)
	}

	state := s.streamState()
	// Reserve before opening the owned file. In-flight opens count against the
	// same per-owner/global caps as node-path streams so concurrent opens cannot
	// over-admit the registry.
	state.mu.Lock()
	perPrincipal, total := 0, len(state.sessions)
	for _, session := range state.sessions {
		if session.owner == owner {
			perPrincipal++
		}
	}
	if perPrincipal+state.openingByOwner[owner] >= streamMaxPerPrincipal ||
		total+state.opening >= streamMaxTotal {
		state.mu.Unlock()
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many open stream sessions"))
	}
	state.opening++
	state.openingByOwner[owner]++
	state.mu.Unlock()
	releaseReservation := func() {
		state.mu.Lock()
		state.opening--
		state.openingByOwner[owner]--
		state.mu.Unlock()
	}

	sessionCtx, cancel := context.WithTimeout(s.jobCtx, streamSessionTTL)
	file, err := os.Open(download.FilePath)
	if err != nil {
		releaseReservation()
		cancel()
		return nil, connect.NewError(connect.CodeNotFound, errors.New("download file not available"))
	}

	state.mu.Lock()
	state.opening--
	state.openingByOwner[owner]--
	id := newStreamID()
	// accessor stays nil: this session owns a controller temp file, not a node
	// storage transport. cleanupSession nil-guards the accessor release.
	state.sessions[id] = &controllerStreamSession{
		owner: owner, serverID: server.ID, path: download.Filename,
		handle: file, ctx: sessionCtx, cancel: cancel,
		expires: time.Now().Add(streamSessionTTL), hash: sha256.New(), total: declared,
		frames: make(map[int32]cachedFrame, streamFrameCache),
	}
	state.mu.Unlock()
	return connect.NewResponse(&v1.OpenStreamResponse{SessionId: id, Size: declared}), nil
}

// ReadStreamFrame returns at most one W17-bounded frame. It never re-reads the
// whole file: the controller owns the read handle and advances its offset. The
// per-call ctx carries the caller's auth and cancellation; every frame is
// independently re-authorized by the interceptor before this handler runs.
// Only the session lock (not the registry lock) is held across the read.
func (s *FileService) ReadStreamFrame(ctx context.Context, req *connect.Request[v1.ReadStreamFrameRequest]) (*connect.Response[v1.ReadStreamFrameResponse], error) {
	owner, err := streamOwner(ctx)
	if err != nil {
		return nil, err
	}
	// Preserve caller cancellation/deadline: a canceled frame request never
	// touches storage state.
	if err := ctx.Err(); err != nil {
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	msg := req.Msg
	sid := msg.GetSessionId()
	session, err := s.lookupSession(owner, msg.GetServerId(), sid)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}

	// This defer is registered BEFORE the unlock defer, so at return the
	// session lock is released first and only then is a failed session evicted
	// and its handle closed. No cleanup ever runs while the lock is held.
	var terminal bool
	defer func() {
		if terminal {
			s.terminateSession(sid, session)
		}
	}()
	session.mu.Lock()
	defer session.mu.Unlock()
	// Recheck the CALLER context after acquiring the session lock: a caller
	// canceled while waiting for a slow frame must not mutate session state.
	if err := ctx.Err(); err != nil {
		return nil, connect.NewError(connect.CodeCanceled, err)
	}
	if session.closed {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errStreamCanceled)
	}
	if session.failed {
		terminal = true
		return nil, connect.NewError(connect.CodeFailedPrecondition, errStreamTerminal)
	}

	index := msg.GetFrameIndex()
	if index < session.nextFrame {
		// Idempotent same-index retry: the frame was already consumed from the
		// handle exactly once. Returning the cached copy never double-advances.
		if frame, ok := session.frames[index]; ok {
			return connect.NewResponse(&v1.ReadStreamFrameResponse{
				Data: frame.data, Offset: frame.offset, Done: frame.done, Sha256: frame.sha,
			}), nil
		}
		terminal = true
		return nil, connect.NewError(connect.CodeFailedPrecondition, errStreamFrameLost)
	}
	if index != session.nextFrame {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("frame index out of order"))
	}
	// The read is bounded by the session deadline: an expired session cannot
	// keep consuming bytes from a storage handle.
	if err := session.ctx.Err(); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errStreamExpired)
	}

	buf := make([]byte, streamFrameBytes)
	n, readErr := session.handle.Read(buf)
	frame := buf[:n]
	// A short read WITH a non-EOF error, or a read that would exceed the
	// declared/staged cap, has already consumed bytes from the handle. Retrying
	// at the same index against the advanced handle would skip those bytes, so
	// the session is TERMINATED (evicted + handle closed) instead.
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		if n > 0 {
			// Bytes were consumed before the error: replaying at the same index
			// against the advanced handle would skip them. Terminate.
			terminal = true
			return nil, connect.NewError(connect.CodeInternal,
				errors.New("partial stream read: session terminated; close and reopen"))
		}
		// No bytes consumed: a retry is safe. Preserve the caller/session
		// cancellation semantics instead of mislabeling it a partial read.
		switch {
		case errors.Is(readErr, context.Canceled):
			return nil, connect.NewError(connect.CodeCanceled, readErr)
		case errors.Is(readErr, context.DeadlineExceeded):
			return nil, connect.NewError(connect.CodeDeadlineExceeded, readErr)
		default:
			return nil, connect.NewError(connect.CodeInternal, readErr)
		}
	}
	if int64(n) > storageworker.MaxStreamBytes ||
		session.offset+int64(n) > storageworker.MaxStreamBytes ||
		session.offset+int64(n) > session.total {
		terminal = true
		return nil, connect.NewError(connect.CodeResourceExhausted, errStreamCap)
	}
	_, _ = session.hash.Write(frame)
	session.offset += int64(n)
	session.nextFrame++
	response := &v1.ReadStreamFrameResponse{Data: frame, Offset: session.offset}
	if errors.Is(readErr, io.EOF) {
		response.Done = true
		response.Sha256 = hex.EncodeToString(session.hash.Sum(nil))
	}
	// Commit the frame to the bounded retry cache before looking at cancellation
	// again. If the caller was canceled during the remote read, the bytes are
	// still safe: retrying the SAME frame_index now returns this cached frame.
	session.frames[index] = cachedFrame{
		data: frame, offset: response.GetOffset(), done: response.GetDone(), sha: response.GetSha256(),
	}
	if len(session.frames) > streamFrameCache {
		delete(session.frames, index-streamFrameCache)
	}
	if err := ctx.Err(); err != nil {
		return nil, connect.NewError(connect.CodeCanceled,
			errors.New("caller canceled during frame read; retry the same frame_index is safe"))
	}
	if err := session.ctx.Err(); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errStreamExpired)
	}
	return connect.NewResponse(response), nil
}

// CloseStream releases only the session's owned handle, accessor and context.
// A caller can never close or cancel another principal's or another server's
// session. The request server_id must match the session's immutable binding.
func (s *FileService) CloseStream(ctx context.Context, req *connect.Request[v1.CloseStreamRequest]) (*connect.Response[v1.CloseStreamResponse], error) {
	owner, err := streamOwner(ctx)
	if err != nil {
		return nil, err
	}
	state := s.streamState()
	state.mu.Lock()
	session, ok := state.sessions[req.Msg.GetSessionId()]
	if !ok {
		state.mu.Unlock()
		return nil, connect.NewError(connect.CodeNotFound, errStreamNotFound)
	}
	if session.owner != owner {
		state.mu.Unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errStreamCrossOwner)
	}
	if req.Msg.GetServerId() == "" || req.Msg.GetServerId() != session.serverID {
		state.mu.Unlock()
		return nil, connect.NewError(connect.CodePermissionDenied, errStreamCrossServer)
	}
	delete(state.sessions, req.Msg.GetSessionId())
	state.mu.Unlock()
	cleanupSession(session)
	return connect.NewResponse(&v1.CloseStreamResponse{}), nil
}

// reapExpiredStreams proactively evicts expired sessions and caps the total
// registry size. It is called by the owned reaper goroutine and directly by
// deterministic tests. Cleanup happens after the registry lock is released so
// a slow close never blocks other sessions' lookups.
func (s *FileService) reapExpiredStreams() int {
	state := s.streamState()
	state.mu.Lock()
	now := time.Now()
	var stale []*controllerStreamSession
	for id, session := range state.sessions {
		if !now.Before(session.expires) || session.ctx.Err() != nil {
			delete(state.sessions, id)
			stale = append(stale, session)
		}
	}
	// Capacity bound: evict the earliest-expiring sessions beyond the cap.
	for len(state.sessions) > streamMaxTotal {
		var oldestID string
		var oldest *controllerStreamSession
		for id, session := range state.sessions {
			if oldest == nil || session.expires.Before(oldest.expires) {
				oldestID, oldest = id, session
			}
		}
		delete(state.sessions, oldestID)
		stale = append(stale, oldest)
	}
	state.mu.Unlock()
	for _, session := range stale {
		cleanupSession(session)
	}
	return len(stale)
}

// reapStreams is the owned, bounded reaper. It exits on service stop or job
// context cancellation; it never leaks a goroutine per FileService.
func (s *FileService) reapStreams() {
	ticker := time.NewTicker(streamReapInterval)
	defer ticker.Stop()
	defer close(s.reaperDone)
	for {
		select {
		case <-s.cleanupStop:
			return
		case <-s.jobCtx.Done():
			return
		case <-ticker.C:
			s.reapExpiredStreams()
		}
	}
}

// StopStreams releases every remaining session's context and handles. It is
// idempotent and safe to call more than once.
func (s *FileService) StopStreams() {
	state := s.streamState()
	state.mu.Lock()
	var all []*controllerStreamSession
	for id, session := range state.sessions {
		delete(state.sessions, id)
		all = append(all, session)
	}
	state.mu.Unlock()
	for _, session := range all {
		cleanupSession(session)
	}
}
