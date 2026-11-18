// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/go-gormigrate/gormigrate/v2"
)

func nodeControlContractMigration(t *testing.T) *gormigrate.Migration {
	t.Helper()
	for _, migration := range migrations() {
		if migration.ID == nodeControlContractMigrationID {
			return migration
		}
	}
	t.Fatalf("migration %s is not registered", nodeControlContractMigrationID)
	return nil
}

// TestNodeControlContractMigrationApplyRollback runs the real migration
// functions against an owned on-disk SQLite database that first holds only the
// previous nodes schema. It proves a forward-only apply, idempotent re-apply,
// legacy backfill and a rollback that removes only the added columns while
// preserving the existing node row.
func TestNodeControlContractMigrationApplyRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node-control-contract.db")
	store, err := NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{
		Path:           path,
		AutoMigrate:    false,
		MaxConnections: 2,
	}})
	if err != nil {
		t.Fatalf("owned store failed: %v", err)
	}
	defer func() { _ = store.Close() }()
	gdb := store.DB()

	if err := gdb.Exec(`CREATE TABLE nodes (
		id text PRIMARY KEY,
		name text NOT NULL,
		host text NOT NULL,
		status text NOT NULL DEFAULT 'offline',
		endpoint_generation integer NOT NULL DEFAULT 1
	)`).Error; err != nil {
		t.Fatalf("previous schema fixture failed: %v", err)
	}
	if err := gdb.Exec(`INSERT INTO nodes (id, name, host) VALUES (?, ?, ?)`,
		"legacy-node", "legacy", "tcp://192.0.2.1:2376").Error; err != nil {
		t.Fatalf("legacy node fixture failed: %v", err)
	}

	migration := nodeControlContractMigration(t)

	if err := migration.Migrate(gdb); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	for _, column := range nodeControlContractColumns {
		if !gdb.Migrator().HasColumn(&Node{}, column.name) {
			t.Errorf("column %s missing after apply", column.name)
		}
	}
	var transport, state string
	row := gdb.Raw(`SELECT transport, enrollment_state FROM nodes WHERE id = ?`, "legacy-node").Row()
	if err := row.Scan(&transport, &state); err != nil {
		t.Fatalf("backfill read failed: %v", err)
	}
	if transport != NodeTransportLegacy || state != NodeEnrollmentStateLegacy {
		t.Fatalf("existing node was not backfilled to legacy: transport=%q state=%q", transport, state)
	}

	// Re-applying an idempotent migration must not fail on existing columns.
	if err := migration.Migrate(gdb); err != nil {
		t.Fatalf("idempotent re-apply failed: %v", err)
	}

	if err := migration.Rollback(gdb); err != nil {
		t.Fatalf("rollback failed: %v", err)
	}
	for _, column := range nodeControlContractColumns {
		if gdb.Migrator().HasColumn(&Node{}, column.name) {
			t.Errorf("column %s still present after rollback", column.name)
		}
	}
	var count int64
	if err := gdb.Raw(`SELECT count(*) FROM nodes`).Scan(&count).Error; err != nil {
		t.Fatalf("node count after rollback failed: %v", err)
	}
	if count != 1 {
		t.Fatalf("rollback changed node data: got %d nodes, want 1", count)
	}
}

// TestNodeControlContractFreshInstallDefaults proves a normal migration and
// node create leaves new nodes in the legacy, unenrolled state with no
// capability or credential material invented.
func TestNodeControlContractFreshInstallDefaults(t *testing.T) {
	store, err := NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{
		Path:           "file:node_control_contract_fresh?mode=memory&cache=shared",
		AutoMigrate:    true,
		MaxConnections: 3,
	}})
	if err != nil {
		t.Fatalf("owned store failed: %v", err)
	}
	defer func() { _ = store.Close() }()

	node := &Node{Name: "fresh-endpoint", Host: "tcp://192.0.2.2:2376"}
	if err := store.CreateNode(context.Background(), node); err != nil {
		t.Fatalf("CreateNode failed: %v", err)
	}
	if node.Transport != NodeTransportLegacy || node.EnrollmentState != NodeEnrollmentStateLegacy {
		t.Fatalf("fresh node must be legacy and unenrolled: %+v", node)
	}
	if node.ProtocolVersion != 0 || node.CredentialGeneration != 0 || node.CapabilitiesJSON != "" || node.TokenFingerprint != "" || node.KeyFingerprint != "" {
		t.Fatalf("fresh node invented contract metadata: %+v", node)
	}

	stored, err := store.GetNode(context.Background(), node.ID)
	if err != nil {
		t.Fatalf("GetNode failed: %v", err)
	}
	if stored.Transport != NodeTransportLegacy || stored.EnrollmentState != NodeEnrollmentStateLegacy {
		t.Fatalf("stored node is not legacy: %+v", stored)
	}
}
