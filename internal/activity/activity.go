// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package activity

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	storage "github.com/athNdev/carbon-panel/internal/db"
)

// Event names written by the audit trail (MINE-141, SEC-3).
const (
	EventAuthLogin                 = "auth.login"
	EventAuthLoginFailed           = "auth.login_failed"
	EventAuthDenied                = "auth.denied"
	EventAuthRecoveryUsed          = "auth.recovery_used"
	EventAuthTokenRevoked          = "auth.token_revoked"
	EventAuthLogout                = "auth.logout"
	EventServerStart               = "server.start"
	EventServerStop                = "server.stop"
	EventServerRestart             = "server.restart"
	EventServerCreate              = "server.create"
	EventServerUpdate              = "server.update"
	EventServerImage               = "server.image"
	EventServerDelete              = "server.delete"
	EventServerExport              = "server.export"
	EventServerPrivilegedOverrides = "server.privileged_overrides"
	EventServerOperationResolve    = "server.operation.resolve"
	EventModInstall                = "mod.install"
	EventModUpdate                 = "mod.update"
	EventModRemove                 = "mod.remove"

	// EventServerCommand records one attempted console command, including a
	// failed send. Properties carry the bounded command text, the silent flag
	// and the send outcome.
	EventServerCommand = "server.command"

	// Player management actions (OSS-345). Each typed moderation action, its
	// undo and each bulk job carries a request id linking it to the player
	// action journal. Subject type is "player" and subject id is the opaque
	// pl_* identity key. ServerID carries the action's scope independently.
	EventPlayerKick            = "player.kick"
	EventPlayerBan             = "player.ban"
	EventPlayerPardon          = "player.pardon"
	EventPlayerOp              = "player.op"
	EventPlayerDeop            = "player.deop"
	EventPlayerWhitelistAdd    = "player.whitelist.add"
	EventPlayerWhitelistRemove = "player.whitelist.remove"
	EventPlayerBulk            = "player.bulk"
	EventPlayerUndo            = "player.undo"
	EventPlayerSkinRefresh     = "player.skin.refresh"

	// World Library bind and unbind operations (OSS-362). Subject type is
	// "server" and subject id is the owning server. RequestID is the
	// journaled operation key so retries and crash recovery emit exactly one
	// event per operation.
	EventWorldBind   = "world.bind"
	EventWorldUnbind = "world.unbind"
)

// Entry is a single audit record.
type Entry struct {
	ActorID    string
	ActorName  string
	IP         string
	Event      string
	SubjectTyp string
	SubjectID  string
	ServerID   string
	Properties string
	RequestID  string
}

// Log appends one audit record. Failures are returned; callers log and
// continue — auditing must never break the operation being audited.
func Log(db *gorm.DB, e Entry) error {
	return db.Create(&storage.ActivityLog{
		ID:         uuid.New().String(),
		ActorID:    e.ActorID,
		ActorName:  e.ActorName,
		IP:         e.IP,
		Event:      e.Event,
		SubjectTyp: e.SubjectTyp,
		SubjectID:  e.SubjectID,
		ServerID:   e.ServerID,
		Properties: e.Properties,
		RequestID:  e.RequestID,
	}).Error
}

// LogOnce appends an audit record only when no record with the same request
// id, event and subject already exists. A non-empty request id is required to
// make the operation-owned event idempotent across retries and crash recovery.
func LogOnce(db *gorm.DB, e Entry) error {
	if e.RequestID == "" {
		return Log(db, e)
	}
	var count int64
	if err := db.Model(&storage.ActivityLog{}).
		Where("request_id = ? AND event = ? AND subject_id = ?", e.RequestID, e.Event, e.SubjectID).
		Limit(1).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	return Log(db, e)
}

// Prune deletes records older than olderThan, always keeping at least
// minKeep newest rows. Returns rows deleted.
func Prune(db *gorm.DB, olderThan time.Time, minKeep int) (int64, error) {
	var keepIDs []string
	if err := db.Model(&storage.ActivityLog{}).
		Order("created_at DESC").
		Limit(minKeep).
		Pluck("id", &keepIDs).Error; err != nil {
		return 0, err
	}
	res := db.Where("created_at < ?", olderThan)
	if len(keepIDs) > 0 {
		res = res.Where("id NOT IN ?", keepIDs)
	}
	res = res.Delete(&storage.ActivityLog{})
	return res.RowsAffected, res.Error
}

// List returns newest-first records filtered by server, actor and time.
func List(db *gorm.DB, serverID, actor string, since time.Time, limit int) ([]storage.ActivityLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := db.Model(&storage.ActivityLog{}).Order("created_at DESC, id DESC").Limit(limit)
	if serverID != "" {
		q = q.Where(`(server_id = ? OR
			((server_id IS NULL OR server_id = '') AND subject_type = ? AND subject_id = ?))`,
			serverID, "server", serverID)
	}
	if actor != "" {
		q = q.Where("actor_id = ? OR actor_name = ?", actor, actor)
	}
	if !since.IsZero() {
		q = q.Where("created_at >= ?", since)
	}
	var out []storage.ActivityLog
	return out, q.Find(&out).Error
}
