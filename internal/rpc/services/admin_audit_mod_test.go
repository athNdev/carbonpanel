// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"github.com/athNdev/carbon-panel/internal/activity"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestImportUploadedModWritesAuditRow covers A51: the uploaded-mod install path
// records exactly one server-scoped mod.install row, including on the staged
// path that does not touch the live mods directory.
func TestImportUploadedModWritesAuditRow(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "audit-upload", storage.StatusStopped)
	jar := zipArchiveBytes(t, map[string]string{
		"fabric.mod.json": `{"environment":"server"}`,
	})
	session := uploadSession(t, um, "audited.jar", jar)
	svc := NewModService(store, nil, um, logger.New())

	if _, err := svc.ImportUploadedMod(ctx, connect.NewRequest(&v1.ImportUploadedModRequest{
		ServerId:        server.ID,
		UploadSessionId: session,
		Stage:           true,
	})); err != nil {
		t.Fatalf("ImportUploadedMod: %v", err)
	}

	rows := adminAuditRows(t, store, activity.EventModInstall)
	if len(rows) != 1 {
		t.Fatalf("mod.install audit rows = %d, want exactly one", len(rows))
	}
	if rows[0].SubjectTyp != "server" || rows[0].SubjectID != server.ID {
		t.Fatalf("mod.install subject = (%q, %q), want server/%s", rows[0].SubjectTyp, rows[0].SubjectID, server.ID)
	}
}

// TestUpdateModWritesAuditRow covers A52: flipping a mod's enabled state writes
// a mod.update row for the owning server.
func TestUpdateModWritesAuditRow(t *testing.T) {
	ctx := context.Background()
	store, server, _ := newContentStageFixture(t, "audit-update", storage.StatusStopped)
	modsDir := filepath.Join(server.DataPath, "mods")
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const fileName = "audited.jar"
	if err := os.WriteFile(filepath.Join(modsDir, fileName), []byte("jar-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	modID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(server.ID+fileName)).String()
	svc := NewModService(store, nil, nil, logger.New())
	disabled := false

	if _, err := svc.UpdateMod(ctx, connect.NewRequest(&v1.UpdateModRequest{
		ServerId: server.ID,
		ModId:    modID,
		Enabled:  &disabled,
	})); err != nil {
		t.Fatalf("UpdateMod: %v", err)
	}

	rows := adminAuditRows(t, store, activity.EventModUpdate)
	if len(rows) != 1 {
		t.Fatalf("mod.update audit rows = %d, want exactly one", len(rows))
	}
	if rows[0].SubjectTyp != "server" || rows[0].SubjectID != server.ID {
		t.Fatalf("mod.update subject = (%q, %q), want server/%s", rows[0].SubjectTyp, rows[0].SubjectID, server.ID)
	}
}

// TestModBrowseServiceInstallWritesAuditRow covers A55: installing from the
// generated browse service delegates to the shared install operation, which
// writes the server-scoped mod.install row.
func TestModBrowseServiceInstallWritesAuditRow(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := seedModBrowseServer(t, store)
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("jar-bytes"))
	}))
	defer download.Close()

	manager := handlers.NewModOnlineManager(store, logger.New(), nil, nil)
	svc := NewModBrowseService(store, manager)
	if _, err := svc.InstallMods(ctx, connect.NewRequest(&v1.InstallModsRequest{
		ServerId: server.ID,
		Items: []*v1.InstallModEntry{{
			Name: "Example", FileName: "example.jar", DownloadUrl: download.URL,
		}},
	})); err != nil {
		t.Fatalf("InstallMods: %v", err)
	}

	rows := adminAuditRows(t, store, activity.EventModInstall)
	if len(rows) != 1 {
		t.Fatalf("mod.install audit rows = %d, want exactly one", len(rows))
	}
	if rows[0].SubjectTyp != "server" || rows[0].SubjectID != server.ID {
		t.Fatalf("mod.install subject = (%q, %q), want server/%s", rows[0].SubjectTyp, rows[0].SubjectID, server.ID)
	}
}
