// SPDX-License-Identifier: MPL-2.0
package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestControllerStreamCancelDuringReadRetryIsIdempotent proves that a caller
// canceled during a blocked remote read does not lose a frame: the consumed
// frame is cached by index and an honest CodeCanceled tells the caller to
// retry the SAME index. The retry returns the cached bytes without advancing
// the offset a second time, and subsequent frames continue uninterrupted.
func TestControllerStreamCancelDuringReadRetryIsIdempotent(t *testing.T) {
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	payload := bytes.Repeat([]byte("r"), storageworker.ChunkBytes*2+17)
	if err := os.WriteFile(filepath.Join(workerRoot, "cancel.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}

	gate := make(chan struct{})
	entered := make(chan struct{})
	h := &countingHandle{readBlock: gate, readEntered: entered}
	svc.SetStorageResolver(nodestorage.NewResolver(&gatedTransport{root: workerRoot, handle: h}))

	ctx := streamCtx("u-cancel")
	opened, err := svc.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, Path: "cancel.bin", TotalSize: int64(len(payload)),
	}))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	sid := opened.Msg.GetSessionId()

	cancellable, cancel := context.WithCancel(ctx)
	type outcome struct {
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		_, err := svc.ReadStreamFrame(cancellable, connect.NewRequest(&v1.ReadStreamFrameRequest{
			SessionId: sid, FrameIndex: 0, ServerId: srv.ID,
		}))
		done <- outcome{err: err}
	}()
	<-entered // first read is in flight and blocked
	cancel()
	close(gate)

	select {
	case out := <-done:
		if connect.CodeOf(out.err) != connect.CodeCanceled {
			t.Fatalf("canceled read error code = %v, want canceled: %v", connect.CodeOf(out.err), out.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked read did not return after cancel")
	}

	// Exactly one frame was consumed; the offset advanced only once.
	state := svc.streamState()
	state.mu.Lock()
	session := state.sessions[sid]
	state.mu.Unlock()
	session.mu.Lock()
	next, offset, cached := session.nextFrame, session.offset, len(session.frames)
	session.mu.Unlock()
	if next != 1 || offset != storageworker.ChunkBytes || cached != 1 {
		t.Fatalf("cancel corrupted state: next=%d offset=%d cached=%d", next, offset, cached)
	}

	// Retry SAME index: cached frame returned, no second advance.
	retried, err := svc.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: sid, FrameIndex: 0, ServerId: srv.ID,
	}))
	if err != nil {
		t.Fatalf("same-index retry: %v", err)
	}
	if !bytes.Equal(retried.Msg.GetData(), payload[:storageworker.ChunkBytes]) ||
		retried.Msg.GetOffset() != storageworker.ChunkBytes {
		t.Fatalf("retry returned wrong frame: len=%d offset=%d", len(retried.Msg.GetData()), retried.Msg.GetOffset())
	}
	session.mu.Lock()
	next, offset = session.nextFrame, session.offset
	session.mu.Unlock()
	if next != 1 || offset != storageworker.ChunkBytes {
		t.Fatalf("retry double-advanced: next=%d offset=%d", next, offset)
	}

	// Subsequent frames continue: reconstruct the whole payload losslessly.
	got := append([]byte(nil), retried.Msg.GetData()...)
	for index := int32(1); ; index++ {
		frame, err := svc.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
			SessionId: sid, FrameIndex: index, ServerId: srv.ID,
		}))
		if err != nil {
			t.Fatalf("frame %d after retry: %v", index, err)
		}
		got = append(got, frame.Msg.GetData()...)
		if frame.Msg.GetDone() {
			break
		}
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch after cancel/retry: got %d want %d", len(got), len(payload))
	}
}

// TestControllerStreamCancelWhileWaitingForLockDoesNotMutate proves that a
// caller canceled while queued behind a slow frame performs NO storage read,
// NO offset advance and NO state mutation.
func TestControllerStreamCancelWhileWaitingForLockDoesNotMutate(t *testing.T) {
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	seedStreamFile(t, workerRoot, "waitlock.bin", storageworker.ChunkBytes)
	block := make(chan struct{})
	close(block)
	h := &countingHandle{readBlock: block}
	svc.SetStorageResolver(nodestorage.NewResolver(&gatedTransport{root: workerRoot, handle: h}))
	ctx := streamCtx("u-waitlock")
	opened, err := svc.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, Path: "waitlock.bin", TotalSize: storageworker.ChunkBytes,
	}))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	sid := opened.Msg.GetSessionId()

	state := svc.streamState()
	state.mu.Lock()
	session := state.sessions[sid]
	state.mu.Unlock()
	session.mu.Lock() // hold the session lock so the caller must wait

	cancellable, cancel := context.WithCancel(ctx)
	type outcome struct{ err error }
	done := make(chan outcome, 1)
	go func() {
		_, err := svc.ReadStreamFrame(cancellable, connect.NewRequest(&v1.ReadStreamFrameRequest{
			SessionId: sid, FrameIndex: 0, ServerId: srv.ID,
		}))
		done <- outcome{err: err}
	}()
	time.Sleep(50 * time.Millisecond) // caller is now queued on session.mu
	cancel()
	session.mu.Unlock()

	select {
	case out := <-done:
		if connect.CodeOf(out.err) != connect.CodeCanceled {
			t.Fatalf("canceled waiter code = %v, want canceled: %v", connect.CodeOf(out.err), out.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled waiter did not return")
	}
	session.mu.Lock()
	next, offset, cached := session.nextFrame, session.offset, len(session.frames)
	session.mu.Unlock()
	if next != 0 || offset != 0 || cached != 0 {
		t.Fatalf("canceled waiter mutated state: next=%d offset=%d cached=%d", next, offset, cached)
	}
	if h.reads() != 0 {
		t.Fatalf("canceled waiter performed %d storage reads", h.reads())
	}
}

// blockingOpenTransport holds Open in flight so admission bounds can be
// observed while opens are pending, not merely after they complete.
type blockingOpenTransport struct {
	root    string
	entered chan struct{}
	release chan struct{}
}

func (t *blockingOpenTransport) Open(ctx context.Context, _ nodestorage.NodeInfo, _ string) (nodestorage.Accessor, error) {
	t.entered <- struct{}{}
	select {
	case <-t.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return nodestorage.NewLocal(t.root)
}

// TestControllerStreamAdmissionCountsInFlightOpens proves the per-owner cap
// is enforced against opens in flight. With streamMaxPerPrincipal opens blocked
// in the transport, one more open is refused with ResourceExhausted before any
// storage handle is created.
func TestControllerStreamAdmissionCountsInFlightOpens(t *testing.T) {
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	seedStreamFile(t, workerRoot, "cap.bin", 128)
	transport := &blockingOpenTransport{
		root: workerRoot, entered: make(chan struct{}, streamMaxPerPrincipal), release: make(chan struct{}),
	}
	svc.SetStorageResolver(nodestorage.NewResolver(transport))
	ctx := streamCtx("u-cap")
	req := func() *connect.Request[v1.OpenStreamRequest] {
		return connect.NewRequest(&v1.OpenStreamRequest{
			ServerId: srv.ID, Path: "cap.bin", TotalSize: 128,
		})
	}

	type outcome struct {
		id  string
		err error
	}
	results := make(chan outcome, streamMaxPerPrincipal+1)
	for i := 0; i < streamMaxPerPrincipal; i++ {
		go func() {
			res, err := svc.OpenStream(ctx, req())
			id := ""
			if res != nil {
				id = res.Msg.GetSessionId()
			}
			results <- outcome{id: id, err: err}
		}()
	}
	for i := 0; i < streamMaxPerPrincipal; i++ {
		select {
		case <-transport.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight opens did not reach the transport")
		}
	}
	// Every cap slot is reserved by an in-flight open; the next must be refused.
	if _, err := svc.OpenStream(ctx, req()); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("over-cap in-flight open code = %v, want resource_exhausted: %v", connect.CodeOf(err), err)
	}
	close(transport.release)

	succeeded := 0
	for i := 0; i < streamMaxPerPrincipal; i++ {
		out := <-results
		if out.err != nil {
			t.Fatalf("reserved open failed: %v", out.err)
		}
		if out.id != "" {
			succeeded++
		}
	}
	if succeeded != streamMaxPerPrincipal {
		t.Fatalf("succeeded %d opens, want %d", succeeded, streamMaxPerPrincipal)
	}
	state := svc.streamState()
	state.mu.Lock()
	opening, total := state.opening, len(state.sessions)
	state.mu.Unlock()
	if opening != 0 || total != streamMaxPerPrincipal {
		t.Fatalf("cap leak: opening=%d sessions=%d", opening, total)
	}
}

// TestControllerStreamStopReleasesEverything proves service shutdown cancels
// the owned reaper goroutine and releases all live session handles.
func TestControllerStreamStopReleasesEverything(t *testing.T) {
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	seedStreamFile(t, workerRoot, "stop.bin", storageworker.ChunkBytes)
	block := make(chan struct{})
	close(block)
	h := &countingHandle{readBlock: block}
	svc.SetStorageResolver(nodestorage.NewResolver(&gatedTransport{root: workerRoot, handle: h}))
	ctx := streamCtx("u-stop")
	opened, err := svc.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, Path: "stop.bin", TotalSize: storageworker.ChunkBytes,
	}))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	sid := opened.Msg.GetSessionId()
	state := svc.streamState()
	state.mu.Lock()
	sessionCtx := state.sessions[sid].ctx
	state.mu.Unlock()

	svc.Stop()
	select {
	case <-svc.reaperDone:
	case <-time.After(5 * time.Second):
		t.Fatal("reaper goroutine did not stop on service shutdown")
	}
	if sessionCtx.Err() == nil {
		t.Fatal("shutdown did not cancel the session context")
	}
	if h.closed.Load() != 1 {
		t.Fatalf("shutdown closed handle %d times, want 1", h.closed.Load())
	}
	state.mu.Lock()
	remaining := len(state.sessions)
	state.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("shutdown left %d sessions in the registry", remaining)
	}
}

// TestProductionShutdownStopsFileService guards the real production wiring:
// the controller's Shutdown must call FileService.Stop so OSS-387 sessions,
// handles and the reaper are not left behind on a normal server shutdown.
func TestProductionShutdownStopsFileService(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(src), "func (s *Server) Shutdown()")
	if start < 0 {
		t.Fatal("Server.Shutdown not found")
	}
	end := strings.Index(string(src[start:]), "\n}")
	if end < 0 {
		t.Fatal("end of Shutdown not found")
	}
	body := string(src[start : start+end])
	if !strings.Contains(body, "s.fileService.Stop()") {
		t.Fatal("production Server.Shutdown does not stop the FileService (stream sessions/reaper would leak)")
	}
}

// assertTerminalStream asserts that a stream whose handle consumed bytes and
// then failed is evicted, its handle closed exactly once, and a later call
// gets a clear terminal refusal rather than a frame at a skipped offset.
func assertTerminalStream(t *testing.T, svc *FileService, srvID, sid string, h *countingHandle, firstCode connect.Code) {
	t.Helper()
	ctx := streamCtx("u-partial")
	_, err := svc.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: sid, FrameIndex: 0, ServerId: srvID,
	}))
	if connect.CodeOf(err) != firstCode {
		t.Fatalf("partial read code = %v, want %v: %v", connect.CodeOf(err), firstCode, err)
	}
	state := svc.streamState()
	state.mu.Lock()
	_, present := state.sessions[sid]
	opening := state.opening
	state.mu.Unlock()
	if present || opening != 0 {
		t.Fatalf("terminal session still registered: present=%v opening=%d", present, opening)
	}
	if h.closed.Load() != 1 {
		t.Fatalf("terminal handle closed %d times, want 1", h.closed.Load())
	}
	// A retry is refused with a terminal error; it must NOT succeed at an
	// advanced offset that would skip the consumed bytes.
	retried, err := svc.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: sid, FrameIndex: 0, ServerId: srvID,
	}))
	if err == nil || retried != nil {
		t.Fatalf("retry after terminal failure succeeded: %+v %v", retried, err)
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !errors.Is(err, errStreamNotFound) {
		t.Fatalf("retry error is not a clear terminal refusal: code=%v err=%v", connect.CodeOf(err), err)
	}
	if h.closed.Load() != 1 {
		t.Fatalf("terminal retry closed handle again: %d", h.closed.Load())
	}
}

func partialReadSession(t *testing.T, readErr error, declared int64) (*FileService, string, string, *countingHandle, func()) {
	t.Helper()
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	const fileSize = 4096
	seedStreamFile(t, workerRoot, "partial.bin", fileSize)
	gate := make(chan struct{})
	close(gate)
	h := &countingHandle{
		readBlock: gate,
		override: func(p []byte) (int, error) {
			n := len(p)
			if n > fileSize {
				n = fileSize
			}
			for i := range p[:n] {
				p[i] = 'q'
			}
			return n, readErr
		},
	}
	svc.SetStorageResolver(nodestorage.NewResolver(&gatedTransport{root: workerRoot, handle: h}))
	opened, err := svc.OpenStream(streamCtx("u-partial"), connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, Path: "partial.bin", TotalSize: declared,
	}))
	if err != nil {
		t.Fatal(err)
	}
	return svc, srv.ID, opened.Msg.GetSessionId(), h, cleanup
}

// TestControllerStreamPartialReadIsTerminal proves a short read carrying a
// non-EOF error terminates the session. Bytes consumed from the handle are
// never replayed at a stale index and no frame is silently skipped.
func TestControllerStreamPartialReadIsTerminal(t *testing.T) {
	svc, srvID, sid, h, cleanup := partialReadSession(t, io.ErrUnexpectedEOF, 4096)
	defer cleanup()
	assertTerminalStream(t, svc, srvID, sid, h, connect.CodeInternal)
}

// TestControllerStreamPartialCustomErrorIsTerminal covers an arbitrary remote
// error returned together with n>0 bytes.
func TestControllerStreamPartialCustomErrorIsTerminal(t *testing.T) {
	svc, srvID, sid, h, cleanup := partialReadSession(t, errors.New("remote pipe reset"), 4096)
	defer cleanup()
	assertTerminalStream(t, svc, srvID, sid, h, connect.CodeInternal)
}

// TestControllerStreamOverCapReadIsTerminal proves a frame that would exceed
// the declared/staged cap terminates the session rather than advancing the
// offset and returning a retryable error.
func TestControllerStreamOverCapReadIsTerminal(t *testing.T) {
	svc, srvID, sid, h, cleanup := partialReadSession(t, nil, 10) // declared 10, handle returns 4096
	defer cleanup()
	assertTerminalStream(t, svc, srvID, sid, h, connect.CodeResourceExhausted)
}
