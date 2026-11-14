// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
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
