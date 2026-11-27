// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/datawrite"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

// reserveExclusivePublication starts a staged-publication claim that must wait
// for the request's already-admitted shared lease. The drain reservation makes a
// second shared acquisition in the same request fail, so this exposes a
// duplicate lease without changing production code.
func reserveExclusivePublication(t *testing.T, serverID string) (chan *datawrite.Lease, chan error) {
	t.Helper()
	lease := make(chan *datawrite.Lease, 1)
	errs := make(chan error, 1)
	go func() {
		acquired, err := datawrite.AcquireExclusive(context.Background(), serverID)
		if err != nil {
			errs <- err
			return
		}
		lease <- acquired
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		probe, err := datawrite.TryShared(serverID)
		if err != nil {
			require.ErrorIs(t, err, datawrite.ErrExclusiveHeld)
			return lease, errs
		}
		probe.Release()
		if time.Now().After(deadline) {
			t.Fatal("waiting publication never reserved the data path")
		}
		time.Sleep(time.Millisecond)
	}
}

func awaitExclusivePublication(t *testing.T, lease chan *datawrite.Lease, errs chan error) {
	t.Helper()
	select {
	case acquired := <-lease:
		acquired.Release()
	case err := <-errs:
		t.Fatalf("exclusive publication failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive publication did not proceed after the request released its lease")
	}
}

// A non-staged delete takes one shared lease before it opens storage. A
// publication that reserves the drain while the request is opening storage must
// not make the already-admitted request fail with Aborted.
func TestNonStagedDeleteHoldsOneDataWriteLease(t *testing.T) {
	_, server, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, os.WriteFile(filepath.Join(workerRoot, "gone.txt"), []byte("x"), 0o644))

	transport := &blockingOpenTransport{
		root: workerRoot, entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	svc.SetStorageResolver(nodestorage.NewResolver(transport))

	done := make(chan error, 1)
	go func() {
		_, err := svc.DeleteFile(ctx, connect.NewRequest(&v1.DeleteFileRequest{
			ServerId: server.ID, Path: "gone.txt",
		}))
		done <- err
	}()
	select {
	case <-transport.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("delete never reached storage")
	}

	lease, errs := reserveExclusivePublication(t, server.ID)
	close(transport.release)

	require.NoError(t, <-done, "an admitted request must not acquire the shared lease twice")
	awaitExclusivePublication(t, lease, errs)
	_, err := os.Stat(filepath.Join(workerRoot, "gone.txt"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

// A remote archive creation also takes one shared lease for the whole request,
// including the transport decision and the accessor write.
func TestRemoteCreateArchiveHoldsOneDataWriteLease(t *testing.T) {
	_, server, svc, _, workerRoot, _, cleanup := transportOnlyFixture(t)
	defer cleanup()
	ctx := context.Background()
	require.NoError(t, os.WriteFile(filepath.Join(workerRoot, "archive-me.txt"), []byte("payload"), 0o644))

	transport := &blockingOpenTransport{
		root: workerRoot, entered: make(chan struct{}, 1), release: make(chan struct{}),
	}
	svc.SetStorageResolver(nodestorage.NewResolver(transport))

	type outcome struct {
		res *connect.Response[v1.CreateArchiveResponse]
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := svc.CreateArchive(ctx, connect.NewRequest(&v1.CreateArchiveRequest{
			ServerId: server.ID, Paths: []string{"archive-me.txt"}, ArchiveName: "bundle.zip",
		}))
		done <- outcome{res: res, err: err}
	}()
	select {
	case <-transport.entered:
	case got := <-done:
		t.Fatalf("archive creation returned before storage: %v", got.err)
	case <-time.After(5 * time.Second):
		t.Fatal("archive creation never reached storage")
	}

	lease, errs := reserveExclusivePublication(t, server.ID)
	close(transport.release)

	got := <-done
	require.NoError(t, got.err, "an admitted archive request must not acquire the shared lease twice")
	require.Equal(t, "bundle.zip", got.res.Msg.GetArchivePath())
	awaitExclusivePublication(t, lease, errs)
	require.FileExists(t, filepath.Join(workerRoot, "bundle.zip"))
}
