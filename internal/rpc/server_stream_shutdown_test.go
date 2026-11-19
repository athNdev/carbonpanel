// SPDX-License-Identifier: MPL-2.0
package rpc

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
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// shutdownHandle blocks until its owning (session) context is canceled, then
// returns an error. It models a remote block read whose transport honors the
// context the controller gave it.
type shutdownHandle struct {
	ctx       context.Context
	real      storageworker.ReadHandle
	entered   chan struct{}
	closed    atomic.Int32
	completed atomic.Int32
	once      sync.Once
}

func (h *shutdownHandle) Read(p []byte) (int, error) {
	h.once.Do(func() { close(h.entered) })
	select {
	case <-h.ctx.Done():
		h.completed.Add(1)
		return 0, h.ctx.Err()
	case <-time.After(10 * time.Second):
		h.completed.Add(1)
		return 0, io.ErrUnexpectedEOF
	}
}
func (h *shutdownHandle) ReadAt(p []byte, off int64) (int, error) { return h.real.ReadAt(p, off) }
func (h *shutdownHandle) Close() error {
	h.closed.Add(1)
	return h.real.Close()
}

type shutdownTransport struct {
	root   string
	handle *shutdownHandle
}

func (t *shutdownTransport) Open(ctx context.Context, _ nodestorage.NodeInfo, _ string) (nodestorage.Accessor, error) {
	local, err := nodestorage.NewLocal(t.root)
	if err != nil {
		return nil, err
	}
	t.handle.ctx = ctx
	return shutdownAccessor{Accessor: local, handle: t.handle}, nil
}

type shutdownAccessor struct {
	nodestorage.Accessor
	handle *shutdownHandle
}

func (a shutdownAccessor) OpenRead(ctx context.Context, path string) (storageworker.ReadHandle, error) {
	real, err := a.Accessor.OpenRead(ctx, path)
	if err != nil {
		return nil, err
	}
	a.handle.real = real
	return a.handle, nil
}

// TestServerShutdownUnblocksRemoteStreamRead is a RUNTIME seam test: a real
// FileService has a stream frame blocked in a remote read, the production
// Server.Shutdown path is invoked, and we observe that StopStreams cancels the
// owned context, unblocks the read, evicts the session and closes the handle.
// No source-string assertion is used; the evidence is the observable events.
func TestServerShutdownUnblocksRemoteStreamRead(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "blocked.bin"), bytes.Repeat([]byte("s"), 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	// A transport-only node forces the controller through the injected
	// nodestorage.Transport instead of a mapped local filesystem path.
	if err := store.CreateNode(ctx, &storage.Node{
		ID: "remote-node", Name: "remote-node", Enabled: true,
		Host: "tcp://10.0.0.50:2375", StorageRoot: filepath.Join(t.TempDir(), "phantom-root"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateServer(ctx, &storage.Server{
		ID: "srv-stream", Name: "srv-stream", NodeID: "remote-node",
		DataPath: filepath.Join("var", "data", "servers", "srv-stream"),
	}); err != nil {
		t.Fatal(err)
	}

	h := &shutdownHandle{entered: make(chan struct{})}
	svc := services.NewFileService(store, nil, nil, nil, logger.New())
	svc.SetStorageResolver(nodestorage.NewResolver(&shutdownTransport{root: root, handle: h}))
	t.Cleanup(svc.Stop)

	authCtx := auth.WithUser(ctx, &auth.AuthenticatedUser{ID: "u-shutdown", Username: "u-shutdown"})
	opened, err := svc.OpenStream(authCtx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: "srv-stream", Path: "blocked.bin", TotalSize: 4096,
	}))
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	sid := opened.Msg.GetSessionId()

	type outcome struct{ err error }
	readDone := make(chan outcome, 1)
	go func() {
		_, err := svc.ReadStreamFrame(authCtx, connect.NewRequest(&v1.ReadStreamFrameRequest{
			SessionId: sid, FrameIndex: 0, ServerId: "srv-stream",
		}))
		readDone <- outcome{err: err}
	}()
	select {
	case <-h.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("remote read never entered")
	}

	// Production shutdown path, exercised at runtime.
	s := &Server{fileService: svc}
	shutdownDone := make(chan struct{})
	go func() {
		s.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(5 * time.Second):
		t.Fatal("Server.Shutdown blocked behind the in-flight remote read")
	}
	select {
	case out := <-readDone:
		if out.err == nil {
			t.Fatal("blocked read unexpectedly succeeded after shutdown")
		}
		if !errors.Is(out.err, context.Canceled) && connect.CodeOf(out.err) != connect.CodeCanceled &&
			connect.CodeOf(out.err) != connect.CodeFailedPrecondition {
			t.Fatalf("blocked read error after shutdown: %v", out.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked read did not unblock on shutdown")
	}
	if h.completed.Load() != 1 {
		t.Fatalf("blocked read completed %d times, want 1", h.completed.Load())
	}
	if h.closed.Load() != 1 {
		t.Fatalf("shutdown closed handle %d times, want 1", h.closed.Load())
	}
	if _, err := svc.ReadStreamFrame(authCtx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: sid, FrameIndex: 0, ServerId: "srv-stream",
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("evicted session after shutdown is still readable: %v", err)
	}
	t.Log("runtime shutdown evidence: Server.Shutdown -> StopStreams canceled session ctx, unblocked read, closed handle, evicted session")
}
