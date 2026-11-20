// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
)

// TestMigrateCreatesVerifiedPreMigrationBackupPreservingData asserts the
// datastore-migration durability journey: an upgrade pass over an existing,
// populated file-backed database writes a verified pre-migration backup of the
// prior state before applying schema work, and leaves the live data intact.
// The backup must be a restorable snapshot, not just a file that happened to
// be written.
func TestMigrateCreatesVerifiedPreMigrationBackupPreservingData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "panel.sqlite")
	cfg := &config.Config{Database: config.DatabaseConfig{Path: dbPath}}

	store, err := NewSQLiteStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.db.Create(&SystemSetting{Key: "durability.probe", Value: "before-migrate"}).Error; err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewSQLiteStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate on existing database: %v", err)
	}

	backupPath := dbPath + ".pre-migrate.bak"
	if _, err := os.Stat(backupPath); err != nil {
		t.Fatalf("pre-migration backup missing at %s: %v", backupPath, err)
	}
	if err := sqliteIntegrity(backupPath); err != nil {
		t.Fatalf("pre-migration backup must be integrity-checked: %v", err)
	}

	backupDB, err := sql.Open("sqlite", sqliteReadOnlyDSN(backupPath))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backupDB.Close() }()
	var backedUp string
	if err := backupDB.QueryRow(`SELECT value FROM system_settings WHERE key = ?`, "durability.probe").Scan(&backedUp); err != nil {
		t.Fatalf("pre-migration backup must preserve prior data: %v", err)
	}
	if backedUp != "before-migrate" {
		t.Fatalf("pre-migration backup value = %q, want %q", backedUp, "before-migrate")
	}

	var live string
	if err := store.db.Raw(`SELECT value FROM system_settings WHERE key = ?`, "durability.probe").Scan(&live).Error; err != nil {
		t.Fatalf("live database must remain readable after migration: %v", err)
	}
	if live != "before-migrate" {
		t.Fatalf("live value after migration = %q, want %q", live, "before-migrate")
	}
}
