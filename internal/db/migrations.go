// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package db

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/go-gormigrate/gormigrate/v2"
	"gorm.io/gorm"
)

func allModels() []any {
	return []any{
		&Server{},
		&ServerConfig{},
		&Mod{},
		&IndexedModpack{},
		&IndexedModpackFile{},
		&ModpackFavorite{},
		&ProxyConfig{},
		&ProxyListener{},
		&User{},
		&Role{},
		&UserRole{},
		&Session{},
		&APIToken{},
		&RegistrationInvite{},
		&ScheduledTask{},
		&TaskExecution{},
		&ActivityLog{},
		&ServerSubuser{},
		&ServerBlueprint{},
		&BackupRecord{},
		&ModuleTemplate{},
		&Module{},
		&SystemSetting{},
		&Node{},
		&nodeEnrollmentAuthorityState{},
		&ServerSnapshot{},
		&StagedConfigChange{},
		&ServerUpdateBatch{},
		&StagedContentChange{},
		&BackendPortLease{},
		&ServerOperation{},
		&ServerDisposition{},
		&IdempotencyKey{},
		&ArchiveImport{},
		&PlayerIdentity{},
		&PlayerAlias{},
		&PlayerSession{},
		&PlayerAccessState{},
		&PlayerModerationAction{},
		&WorldEntry{},
		&WorldRevision{},
		&WorldBinding{},
		&WorldImportJob{},
	}
}

func (s *Store) Migrate() error {
	if err := s.backupDB(); err != nil {
		return fmt.Errorf("pre-migration backup failed: %w", err)
	}

	// Rebuild the port-lease key before AutoMigrate so an existing
	// (node, port) table becomes (node, port, protocol) without GORM
	// guessing at the primary key.
	if err := ensurePortLeaseProtocolKey(s.db); err != nil {
		return fmt.Errorf("port lease migration failed: %w", err)
	}
	if err := ensurePortLeaseAddressKey(s.db); err != nil {
		return fmt.Errorf("port lease address migration failed: %w", err)
	}

	// Create all tables/columns
	if err := s.db.AutoMigrate(allModels()...); err != nil {
		return fmt.Errorf("schema migration failed: %w", err)
	}

	m := gormigrate.New(s.db, &gormigrate.Options{
		TableName:                 "migrations",
		IDColumnName:              "id",
		IDColumnSize:              200,
		UseTransaction:            true,
		ValidateUnknownMigrations: false,
	}, migrations())

	if err := m.Migrate(); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	if err := seeds(s); err != nil {
		return fmt.Errorf("seed failed: %w", err)
	}

	log.Println("[migrate] Migration complete")
	return nil
}

func seeds(s *Store) error {
	for _, seed := range []func() error{
		s.SeedSystemRoles,
		s.SeedGlobalSettings,
		// NOTE: SeedDefaultNode intentionally removed — fresh installs start
		// with zero nodes so no phantom "default" node exists in node settings.
	} {
		if err := seed(); err != nil {
			return err
		}
	}
	return nil
}

func migrations() []*gormigrate.Migration {
	return []*gormigrate.Migration{
		{
			ID: "20260306_001_retry_backfill_user_roles",
			Migrate: func(tx *gorm.DB) error {
				// Find users that have no entry in user_roles
				var usersWithoutRoles []User
				if err := tx.Where("id NOT IN (SELECT DISTINCT user_id FROM user_roles)").
					Order("created_at ASC").
					Find(&usersWithoutRoles).Error; err != nil {
					return err
				}

				if len(usersWithoutRoles) == 0 {
					return nil
				}

				var adminCount int64
				tx.Model(&UserRole{}).Where("role_name = ?", "admin").Count(&adminCount)

				for i, user := range usersWithoutRoles {
					roleName := "user"
					if i == 0 && adminCount == 0 {
						roleName = "admin"
					}
					ur := UserRole{
						ID:       user.ID + "-" + roleName,
						UserID:   user.ID,
						RoleName: roleName,
						Source:   "migration",
					}
					if err := tx.Create(&ur).Error; err != nil {
						return fmt.Errorf("failed to assign role %s to user %s: %w", roleName, user.Username, err)
					}
					log.Printf("[migrate] Assigned role '%s' to user '%s'", roleName, user.Username)
				}

				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return tx.Where("source = ?", "migration").Delete(&UserRole{}).Error
			},
		},
		{
			ID: "20260307_001_multinode_default_node",
			Migrate: func(tx *gorm.DB) error {
				// Historical backfill for databases created while the seeded
				// "default" node existed. Fresh installs no longer seed it.
				if err := tx.Model(&Server{}).Where("node_id IS NULL OR node_id = ''").Update("node_id", "default").Error; err != nil {
					return err
				}
				if err := tx.Model(&Module{}).Where("node_id IS NULL OR node_id = ''").Update("node_id", "default").Error; err != nil {
					return err
				}
				return nil
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		{
			ID: "20261028_001_port_lease_protocol",
			Migrate: func(tx *gorm.DB) error {
				if err := ensurePortLeaseProtocolKey(tx); err != nil {
					return err
				}
				return ensurePortLeaseAddressKey(tx)
			},
			Rollback: func(tx *gorm.DB) error {
				return nil
			},
		},
		{
			ID: nodeControlContractMigrationID,
			Migrate: func(tx *gorm.DB) error {
				return applyNodeControlContractColumns(tx)
			},
			Rollback: func(tx *gorm.DB) error {
				return rollbackNodeControlContractColumns(tx)
			},
		},
		{
			ID: playerLastSeenObservationMigrationID,
			Migrate: func(tx *gorm.DB) error {
				return backfillPlayerLastSeenObservations(tx)
			},
			Rollback: func(tx *gorm.DB) error {
				// Forward-only: the nulls written by the backfill are the
				// corrected state. A pre-fix last_seen value on a session-less
				// identity cannot be distinguished from a real observation's
				// timestamp, so there is nothing safe to restore.
				return nil
			},
		},
	}
}

const playerLastSeenObservationMigrationID = "20261123_001_player_last_seen_observation"

// backfillPlayerLastSeenObservations clears last_seen_at for identities that
// have no session rows. Before observation sources were separated, a manual
// allowlist add stamped last_seen_at with the time of the add even though the
// player had never been observed in game, and that fabricated timestamp then
// surfaced as presence and sorted the never-joined player to the top of the
// roster. A session row is the evidence that an observation was real, so rows
// without one are reset to "never observed". The statement is idempotent and
// touches only the player identity index, never world or server data.
func backfillPlayerLastSeenObservations(tx *gorm.DB) error {
	if err := tx.Exec(`
		UPDATE player_identities SET last_seen_at = NULL
		WHERE NOT EXISTS (
			SELECT 1 FROM player_sessions WHERE player_sessions.identity_id = player_identities.id
		)`).Error; err != nil {
		return fmt.Errorf("backfill player last-seen observations: %w", err)
	}
	return nil
}

const nodeControlContractMigrationID = "20261118_001_node_control_contract"

// nodeControlContractColumns are metadata for the versioned node-control
// contract (ADR 0019). No column stores a token, key or other secret: only
// fingerprints, generations, a version and a versioned capability snapshot.
var nodeControlContractColumns = []struct {
	name string
	ddl  string
}{
	{"transport", "TEXT NOT NULL DEFAULT 'legacy'"},
	{"enrollment_state", "TEXT NOT NULL DEFAULT 'legacy'"},
	{"enrollment_id", "TEXT NOT NULL DEFAULT ''"},
	{"enrollment_expires_at", "DATETIME"},
	{"protocol_version", "INTEGER NOT NULL DEFAULT 0"},
	{"capabilities_json", "TEXT NOT NULL DEFAULT ''"},
	{"capability_owner", "TEXT NOT NULL DEFAULT ''"},
	{"credential_generation", "INTEGER NOT NULL DEFAULT 0"},
	{"key_fingerprint", "TEXT NOT NULL DEFAULT ''"},
	{"token_fingerprint", "TEXT NOT NULL DEFAULT ''"},
}

// applyNodeControlContractColumns is idempotent: AutoMigrate may already have
// created the columns from the Node model, in which case only the legacy-state
// backfill is needed. Existing rows stay legacy; no node is auto-enrolled.
func applyNodeControlContractColumns(tx *gorm.DB) error {
	for _, column := range nodeControlContractColumns {
		if tx.Migrator().HasColumn(&Node{}, column.name) {
			continue
		}
		if err := tx.Exec("ALTER TABLE nodes ADD COLUMN " + column.name + " " + column.ddl).Error; err != nil {
			return fmt.Errorf("add node control column %s: %w", column.name, err)
		}
	}
	backfills := map[string]string{
		"transport":        NodeTransportLegacy,
		"enrollment_state": NodeEnrollmentStateLegacy,
	}
	for column, value := range backfills {
		if err := tx.Exec("UPDATE nodes SET "+column+" = ? WHERE "+column+" IS NULL OR "+column+" = ''",
			value).Error; err != nil {
			return fmt.Errorf("backfill node control column %s: %w", column, err)
		}
	}
	return nil
}

// rollbackNodeControlContractColumns removes only the columns this migration
// added. It never touches worlds, backups, server rows or other node data.
func rollbackNodeControlContractColumns(tx *gorm.DB) error {
	// Use native ALTER TABLE DROP COLUMN rather than GORM's SQLite rebuild:
	// the rebuild derives its schema from the current Node model and would
	// re-add the other contract columns on every iteration. Column names are
	// compile-time constants from nodeControlContractColumns.
	for _, column := range nodeControlContractColumns {
		if !tx.Migrator().HasColumn(&Node{}, column.name) {
			continue
		}
		if err := tx.Exec("ALTER TABLE nodes DROP COLUMN " + column.name).Error; err != nil {
			return fmt.Errorf("drop node control column %s: %w", column.name, err)
		}
	}
	return nil
}

// isInMemorySQLitePath reports whether path addresses an in-memory SQLite
// database (the bare ":memory:" DSN, or a "file:...?mode=memory..." DSN,
// with or without a shared cache) rather than a real file on disk. A
// pre-migration VACUUM INTO backup is meaningless - and fails outright,
// since sqlite rejects a cache-mode query parameter on the backup's target
// path - for any of these.
func isInMemorySQLitePath(path string) bool {
	if path == "" || path == ":memory:" {
		return true
	}
	lower := strings.ToLower(path)
	return strings.Contains(lower, ":memory:") || strings.Contains(lower, "mode=memory")
}

func (s *Store) backupDB() error {
	if isInMemorySQLitePath(s.cfg.Database.Path) {
		return nil
	}

	var count int
	row := s.db.Raw("SELECT count(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'").Row()
	if err := row.Scan(&count); err != nil || count == 0 {
		return nil
	}

	backupPath := s.cfg.Database.Path + ".pre-migrate.bak"
	_ = os.Remove(backupPath)
	if err := s.db.Exec("VACUUM INTO ?", backupPath).Error; err != nil {
		_ = os.Remove(backupPath)
		return fmt.Errorf("VACUUM INTO %s: %w", backupPath, err)
	}
	if err := sqliteIntegrity(backupPath); err != nil {
		_ = os.Remove(backupPath)
		return fmt.Errorf("pre-migration backup integrity check failed: %w", err)
	}

	log.Printf("[migrate] Database backed up to %s", backupPath)
	return nil
}
