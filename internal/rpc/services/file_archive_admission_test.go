// SPDX-License-Identifier: MPL-2.0

package services

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// A direct panel file write participates in the same admission as module
// lifecycles: while an export owns the server, the write is refused and cannot
// create a false stopped-consistency guarantee during the archive read.
func TestDirectFileWriteIsRefusedDuringArchiveExport(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	dataRoot := t.TempDir()
	server := &storage.Server{
		ID: "file-export-server", Name: "Export", NodeID: "local",
		Status: storage.StatusStopped, DataPath: dataRoot,
	}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	live := filepath.Join(dataRoot, "server.properties")
	if err := os.WriteFile(live, []byte("motd=original\n"), 0o644); err != nil {
		t.Fatalf("write live file: %v", err)
	}

	files := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(files.Stop)
	fence, err := module.TryBeginServerModuleArchive(server.ID)
	if err != nil {
		t.Fatalf("archive fence: %v", err)
	}

	_, err = files.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
		ServerId: server.ID, Path: "server.properties", Content: []byte("motd=changed\n"),
	}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("UpdateFile code = %v, want FailedPrecondition (%v)", got, err)
	}
	body, readErr := os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("read live file: %v", readErr)
	}
	if string(body) != "motd=original\n" {
		t.Fatalf("file changed during export: %q", body)
	}

	fence.Release()
	if _, err := files.UpdateFile(ctx, connect.NewRequest(&v1.UpdateFileRequest{
		ServerId: server.ID, Path: "server.properties", Content: []byte("motd=changed\n"),
	})); err != nil {
		t.Fatalf("UpdateFile after export: %v", err)
	}
	body, readErr = os.ReadFile(live)
	if readErr != nil {
		t.Fatalf("read live file after export: %v", readErr)
	}
	if string(body) != "motd=changed\n" {
		t.Fatalf("write after export = %q", body)
	}
}
