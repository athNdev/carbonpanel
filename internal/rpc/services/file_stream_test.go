// SPDX-License-Identifier: MPL-2.0
package services

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
)

type fileStreamZeros struct{}

func (fileStreamZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestBinaryFileRPCsExceedSmallFileLimit(t *testing.T) {
	_, server, svc, _, workerRoot, phantomRoot, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()
	const size int64 = 12 << 20
	um := upload.NewManager(t.TempDir(), time.Minute, size, logger.New())
	defer um.Stop()
	svc.uploadManager = um
	session, err := um.InitSession("large.jar", size, int32(size))
	if err != nil {
		t.Fatal(err)
	}
	if _, done, err := um.WriteStream(session.ID, io.LimitReader(fileStreamZeros{}, size), 0); err != nil || !done {
		t.Fatalf("upload done=%v %v", done, err)
	}
	uploadPath, _, err := um.GetTempPath(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SaveUploadedFile(ctx, connect.NewRequest(&v1.SaveUploadedFileRequest{ServerId: server.ID, UploadSessionId: session.ID, DestinationPath: "mods"})); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(workerRoot, "mods", "large.jar")); err != nil || fi.Size() != size {
		t.Fatalf("uploaded file %v %v", fi, err)
	}
	if _, err := os.Stat(uploadPath); !os.IsNotExist(err) {
		t.Fatalf("upload source not cleaned after streaming: %v", err)
	}
	if _, err := svc.CopyFile(ctx, connect.NewRequest(&v1.CopyFileRequest{ServerId: server.ID, SourcePath: "mods/large.jar", DestinationPath: "mods/copied.jar"})); err != nil {
		t.Fatal(err)
	}
	response, err := svc.InitFileDownload(ctx, connect.NewRequest(&v1.InitFileDownloadRequest{ServerId: server.ID, Path: "mods/copied.jar"}))
	if err != nil {
		t.Fatal(err)
	}
	download, err := svc.downloadManager.GetSession(response.Msg.SessionId)
	if err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(download.FilePath); err != nil || fi.Size() != size {
		t.Fatalf("download staging %v %v", fi, err)
	}
	if response.Msg.TotalSize != size {
		t.Fatal("download size changed")
	}
	if _, err := svc.GetFile(ctx, connect.NewRequest(&v1.GetFileRequest{ServerId: server.ID, Path: "mods/large.jar"})); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("editor did not expose its limit: %v", err)
	}
	assertNoControllerLocalTree(t, phantomRoot, server.DataPath)
}

type metadataOnlyTransport struct {
	root  string
	reads int
}

func (p *metadataOnlyTransport) Open(context.Context, nodestorage.NodeInfo, string) (nodestorage.Accessor, error) {
	local, err := nodestorage.NewLocal(p.root)
	if err != nil {
		return nil, err
	}
	return metadataOnlyAccessor{Accessor: local, owner: p}, nil
}

type metadataOnlyAccessor struct {
	nodestorage.Accessor
	owner *metadataOnlyTransport
}

func (a metadataOnlyAccessor) ReadFile(string) ([]byte, error) {
	a.owner.reads++
	return nil, errors.New("listing read body")
}
func (a metadataOnlyAccessor) OpenRead(context.Context, string) (storageworker.ReadHandle, error) {
	a.owner.reads++
	return nil, errors.New("listing opened body")
}

func TestFileListingDoesNotFetchBodies(t *testing.T) {
	_, server, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	transport := &metadataOnlyTransport{root: workerRoot}
	svc.SetStorageResolver(nodestorage.NewResolver(transport))
	response, err := svc.ListFiles(context.Background(), connect.NewRequest(&v1.ListFilesRequest{ServerId: server.ID, Path: "."}))
	if err != nil {
		t.Fatal(err)
	}
	if transport.reads != 0 {
		t.Fatalf("metadata listing fetched %d bodies", transport.reads)
	}
	if len(response.Msg.Files) != 1 || !response.Msg.Files[0].IsEditable {
		t.Fatalf("filename editor affordance lost: %v", response.Msg.Files)
	}
}

// --- OSS-387 controller stream lifecycle / ownership tests ----------------

func streamCtx(id string) context.Context {
	return auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: id, Username: id})
}

func seedStreamFile(t *testing.T, root, name string, n int) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), bytes.Repeat([]byte("z"), n), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestControllerStreamServerBinding proves that a handle opened for one server
// cannot be read or closed through a request bound to another server, even for
// the same authenticated principal. A refusal consumes no bytes and leaves the
// session state untouched, so the correct server can still complete the stream.
func TestControllerStreamServerBinding(t *testing.T) {
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	seedStreamFile(t, workerRoot, "bind.bin", 4096)
	ctx := streamCtx("u-binding")

	opened, err := svc.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, Path: "bind.bin", TotalSize: 4096,
	}))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	sid := opened.Msg.GetSessionId()

	// Wrong server on a frame: denied, no bytes consumed, no state change.
	bad := connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: sid, FrameIndex: 0, ServerId: "srv-other",
	})
	if _, err := svc.ReadStreamFrame(ctx, bad); connect.CodeOf(err) != connect.CodeFailedPrecondition ||
		!errors.Is(err, errStreamCrossServer) {
		t.Fatalf("cross-server frame was not refused: %v", err)
	}
	state := svc.streamState()
	state.mu.Lock()
	next := state.sessions[sid].nextFrame
	state.mu.Unlock()
	if next != 0 {
		t.Fatalf("cross-server refusal consumed a frame: nextFrame=%d", next)
	}

	// Wrong server on close: denied, session still present and usable.
	if _, err := svc.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: sid, ServerId: "srv-other",
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition && connect.CodeOf(err) != connect.CodePermissionDenied &&
		!errors.Is(err, errStreamCrossServer) {
		t.Fatalf("cross-server close was not refused: %v", err)
	}
	state.mu.Lock()
	_, stillOpen := state.sessions[sid]
	state.mu.Unlock()
	if !stillOpen {
		t.Fatal("cross-server close removed the session")
	}

	// Correct server still works end to end.
	frame, err := svc.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: sid, FrameIndex: 0, ServerId: srv.ID,
	}))
	if err != nil || len(frame.Msg.GetData()) == 0 {
		t.Fatalf("correct-server frame failed: data=%d err=%v", len(frame.Msg.GetData()), err)
	}
	if _, err := svc.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: sid, ServerId: srv.ID,
	})); err != nil {
		t.Fatalf("correct-server close: %v", err)
	}
}

// countingHandle/accessor prove cleanup releases storage resources exactly once.
type countingHandle struct {
	real        storageworker.ReadHandle
	closed      atomic.Int32
	readCount   atomic.Int32
	readBlock   chan struct{}
	readEntered chan struct{}
	override    func([]byte) (int, error)
	once        sync.Once
}

func (h *countingHandle) reads() int { return int(h.readCount.Load()) }

func (h *countingHandle) Read(p []byte) (int, error) {
	h.readCount.Add(1)
	if h.readEntered != nil {
		h.once.Do(func() { close(h.readEntered) })
	}
	if h.readBlock != nil {
		<-h.readBlock
	}
	if h.override != nil {
		return h.override(p)
	}
	return h.real.Read(p)
}
func (h *countingHandle) ReadAt(p []byte, off int64) (int, error) { return h.real.ReadAt(p, off) }
func (h *countingHandle) Close() error {
	h.closed.Add(1)
	return h.real.Close()
}

type gatedTransport struct {
	root   string
	handle *countingHandle
}

func (g *gatedTransport) Open(_ context.Context, _ nodestorage.NodeInfo, _ string) (nodestorage.Accessor, error) {
	local, err := nodestorage.NewLocal(g.root)
	if err != nil {
		return nil, err
	}
	return gatedAccessor{Accessor: local, handle: g.handle}, nil
}

type gatedAccessor struct {
	nodestorage.Accessor
	handle *countingHandle
}

func (a gatedAccessor) OpenRead(ctx context.Context, path string) (storageworker.ReadHandle, error) {
	real, err := a.Accessor.OpenRead(ctx, path)
	if err != nil {
		return nil, err
	}
	a.handle.real = real
	return a.handle, nil
}

// TestControllerStreamReapEvictsAndReleases proves TTL reclamation evicts the
// registry entry, cancels the owned context and closes the storage handle.
func TestControllerStreamReapEvictsAndReleases(t *testing.T) {
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	seedStreamFile(t, workerRoot, "reap.bin", 4096)
	block := make(chan struct{})
	close(block) // reads complete immediately
	h := &countingHandle{readBlock: block}
	svc.SetStorageResolver(nodestorage.NewResolver(&gatedTransport{root: workerRoot, handle: h}))
	ctx := streamCtx("u-reap")

	opened, err := svc.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, Path: "reap.bin", TotalSize: 4096,
	}))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	sid := opened.Msg.GetSessionId()
	state := svc.streamState()
	state.mu.Lock()
	session := state.sessions[sid]
	sessionCtx := session.ctx
	session.expires = time.Now().Add(-time.Second)
	state.mu.Unlock()

	if n := svc.reapExpiredStreams(); n != 1 {
		t.Fatalf("reaped %d sessions, want 1", n)
	}
	state.mu.Lock()
	_, present := state.sessions[sid]
	state.mu.Unlock()
	if present {
		t.Fatal("expired session was not evicted from the registry")
	}
	if sessionCtx.Err() == nil {
		t.Fatal("reaper did not cancel the owned session context")
	}
	if got := h.closed.Load(); got != 1 {
		t.Fatalf("storage handle closed %d times, want 1", got)
	}
	// A second reap must not double-close or resurrect anything.
	if n := svc.reapExpiredStreams(); n != 0 {
		t.Fatalf("second reap removed %d sessions", n)
	}
	if got := h.closed.Load(); got != 1 {
		t.Fatalf("second reap closed handle again: %d", got)
	}
}

// TestControllerStreamRegistryLockNotHeldAcrossRead proves a slow remote frame
// on one session does not hold the shared registry mutex. Another session's
// lookup/close and registry introspection remain possible while a read is in
// flight; only the affected session is serialized.
func TestControllerStreamRegistryLockNotHeldAcrossRead(t *testing.T) {
	_, srv, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	seedStreamFile(t, workerRoot, "lock.bin", 4096)
	block := make(chan struct{})
	entered := make(chan struct{})
	h := &countingHandle{readBlock: block, readEntered: entered}
	svc.SetStorageResolver(nodestorage.NewResolver(&gatedTransport{root: workerRoot, handle: h}))
	ctx := streamCtx("u-lock")

	opened, err := svc.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: srv.ID, Path: "lock.bin", TotalSize: 4096,
	}))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	sid := opened.Msg.GetSessionId()

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		_, err := svc.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
			SessionId: sid, FrameIndex: 0, ServerId: srv.ID,
		}))
		done <- result{err: err}
	}()
	<-entered // the read is now in flight, blocked on the gate

	state := svc.streamState()
	if !state.mu.TryLock() {
		t.Fatal("shared registry lock was held across a slow remote read")
	}
	state.mu.Unlock()

	close(block) // let the in-flight frame finish
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("gated frame: %v", r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gated frame did not complete after the gate opened")
	}
}
