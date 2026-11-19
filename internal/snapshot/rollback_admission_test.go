// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/google/uuid"
)

// TestRollbackIsGated proves a rollback acquires the shared node gate before
// it can restart the container or replace any world files (OSS-405).
func TestRollbackIsGated(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	serverDir := t.TempDir()
	testFile := filepath.Join(serverDir, "server.properties")
	if err := os.WriteFile(testFile, []byte("port=25565\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Legacy local node keeps storage resolution self-contained in this seam.
	server := &db.Server{ID: uuid.NewString(), Name: "rollback-gated", NodeID: "", DataPath: serverDir, Status: db.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(store, nil, nil, logger.New(), Config{BackupDir: "", MaxSnapshots: 5})
	snap, err := engine.CreatePreUpdateSnapshot(ctx, server, "baseline")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testFile, []byte("port=25577\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	release, err := engine.admission.Acquire(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	gatedCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err = engine.Rollback(gatedCtx, server, snap.ID)
	if err == nil || !strings.Contains(err.Error(), "start admission") {
		t.Fatalf("Rollback error = %v, want node admission refusal", err)
	}
	content, readErr := os.ReadFile(testFile)
	if readErr != nil || string(content) != "port=25577\n" {
		t.Fatalf("refused rollback changed world file: %q, %v", content, readErr)
	}
}
