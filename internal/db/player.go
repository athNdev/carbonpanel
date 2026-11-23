// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PlayerIdentity is a panel-scoped cache/index of a player. World files and
// gameplay state are owned by the game server; these rows are never
// authoritative for gameplay. A guessed UUID is never put in a URL: the
// identity key is opaque and stable.
type PlayerIdentity struct {
	ID            string `json:"id" gorm:"primaryKey;size:64"`
	PrimaryUUID   string `json:"primary_uuid" gorm:"index;size:36"`
	XUID          string `json:"xuid" gorm:"column:xuid;index;size:64"`
	FloodgateUUID string `json:"floodgate_uuid" gorm:"index;size:36"`
	CurrentName   string `json:"current_name" gorm:"size:64"`
	OfflineUUID   bool   `json:"offline_uuid"`
	// FirstSeenAt is when the panel first recorded this identity, whether
	// through an in-game observation or an operator action.
	FirstSeenAt time.Time
	// LastSeenAt is the last time the panel observed this identity in game
	// through roster, session or metrics ingestion. It is nil for identities
	// that were only registered — for example a manual allowlist add before
	// the first join — and is never backfilled with a plausible guess, because
	// an operator action is not a presence observation.
	LastSeenAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// PlayerAlias records only names this panel actually observed. Mojang removed
// the public name-history service in 2022, so no third-party lookup is used.
type PlayerAlias struct {
	ID             string `json:"id" gorm:"primaryKey"`
	IdentityID     string `json:"identity_id" gorm:"index;size:64;uniqueIndex:idx_player_alias"`
	Name           string `json:"name" gorm:"size:64;uniqueIndex:idx_player_alias"`
	SourceServerID string `json:"source_server_id" gorm:"size:64;uniqueIndex:idx_player_alias"`
	Source         string `json:"source" gorm:"size:32"`
	ObservedAt     time.Time
}

// PlayerSession joins an identity to a server for a join/leave observation.
type PlayerSession struct {
	ID         string    `json:"id" gorm:"primaryKey"`
	IdentityID string    `json:"identity_id" gorm:"index;size:64"`
	ServerID   string    `json:"server_id" gorm:"index;size:64"`
	JoinedAt   time.Time `gorm:"index"`
	LeftAt     *time.Time
	Source     string `json:"source" gorm:"size:32"`
	Estimate   bool   `json:"estimate"`
}

// PlayerAccessState is an observed access snapshot, not a source of truth for
// gameplay. Unknown fields stay empty; bools alone never mean "unverified".
type PlayerAccessState struct {
	IdentityID   string `json:"identity_id" gorm:"primaryKey;size:64"`
	ServerID     string `json:"server_id" gorm:"primaryKey;size:64"`
	Whitelisted  bool   `json:"whitelisted"`
	Operator     bool   `json:"operator"`
	OpLevel      int    `json:"op_level"`
	Banned       bool   `json:"banned"`
	BanReason    string `json:"ban_reason" gorm:"type:text"`
	BanExpiresAt *time.Time
	Mute         bool   `json:"mute"`
	Source       string `json:"source" gorm:"size:32"`
	Confidence   string `json:"confidence" gorm:"size:32"`
	ObservedAt   time.Time
}

// PlayerModerationAction is a small journal for typed player mutations. It is
// intentionally independent of the lifecycle ServerOperation journal: mod
// actions do not own a container or world and must not take its active lock.
// A request id is idempotent, and an uncertain outcome stays executing until
// it is reconciled -- it is never blindly retried.
type PlayerModerationAction struct {
	ID          string `json:"id" gorm:"primaryKey"`
	RequestID   string `json:"request_id" gorm:"uniqueIndex;size:128"`
	ServerID    string `json:"server_id" gorm:"index;size:64"`
	IdentityKey string `json:"identity_key" gorm:"index;size:64"`
	Action      string `json:"action" gorm:"index;size:32"`
	ActorID     string `json:"actor_id" gorm:"size:64"`
	ActorName   string `json:"actor_name" gorm:"size:128"`
	Reason      string `json:"reason" gorm:"type:text"`
	TargetCount int    `json:"target_count"`
	// PriorValue is a bounded JSON snapshot used only for reversible actions.
	PriorValue string `json:"prior_value" gorm:"type:text"`
	UndoToken  string `json:"undo_token" gorm:"uniqueIndex;size:128"`
	Undone     bool   `json:"undone"`
	Phase      string `json:"phase" gorm:"index;size:32"`
	LastError  string `json:"last_error" gorm:"type:text"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Player journal phases.
const (
	PlayerActionPrepared  = "prepared"
	PlayerActionExecuting = "executing"
	PlayerActionComplete  = "complete"
	PlayerActionFailed    = "failed"
)

// ErrPlayerActionInProgress is returned when a request id is already executing.
// The caller must reconcile the external state before trying again.
var ErrPlayerActionInProgress = errors.New("player action is still in progress")

// ErrPlayerActionChanged is returned when a request id is reused with a
// different action, server or target.
var ErrPlayerActionChanged = errors.New("player action request id reused for a different action")

// playerKey builds an opaque, non-guessable identity key.
func playerKey() string { return "pl_" + strings.ReplaceAll(uuid.NewString(), "-", "") }

// SourceManualAllowlist marks identities registered by an operator's manual
// allowlist add. It records the operator's intent, never proof that the
// player joined, so it does not update last_seen_at.
const SourceManualAllowlist = "manual_allowlist"

// ensurePlayerIdentity returns the identity for a UUID/name pair, creating or
// updating it as needed. When observed is true the call is treated as a live
// in-game observation and last_seen_at is stamped on create and update; when
// false the identity is only registered or refreshed and last_seen_at is left
// untouched (nil for a new row).
func (s *Store) ensurePlayerIdentity(ctx context.Context, primaryUUID, name, xuid, floodgateUUID string, offline, observed bool) (*PlayerIdentity, error) {
	uuidValue := strings.TrimSpace(primaryUUID)
	xuidValue := strings.TrimSpace(xuid)
	var identity PlayerIdentity
	q := s.db.WithContext(ctx)
	switch {
	case uuidValue != "":
		q = q.Where("primary_uuid = ?", uuidValue)
	case xuidValue != "":
		q = q.Where("xuid = ?", xuidValue)
	default:
		return nil, errors.New("player identity requires a uuid or xuid")
	}
	err := q.First(&identity).Error
	if err == nil {
		updates := map[string]any{}
		if observed {
			updates["last_seen_at"] = time.Now().UTC()
		}
		if name != "" {
			updates["current_name"] = name
		}
		if len(updates) > 0 {
			if err := s.db.WithContext(ctx).Model(&identity).Updates(updates).Error; err != nil {
				return nil, err
			}
		}
		if name != "" {
			if err := s.RecordAlias(ctx, identity.ID, name, "", "structured", time.Now().UTC()); err != nil {
				return nil, err
			}
		}
		return &identity, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	now := time.Now().UTC()
	identity = PlayerIdentity{
		ID:            playerKey(),
		PrimaryUUID:   strings.TrimSpace(primaryUUID),
		XUID:          strings.TrimSpace(xuid),
		FloodgateUUID: strings.TrimSpace(floodgateUUID),
		CurrentName:   strings.TrimSpace(name),
		OfflineUUID:   offline,
		FirstSeenAt:   now,
	}
	if observed {
		identity.LastSeenAt = &now
	}
	if err := s.db.WithContext(ctx).Create(&identity).Error; err != nil {
		return nil, err
	}
	if name != "" {
		if err := s.RecordAlias(ctx, identity.ID, name, "", "structured", now); err != nil {
			return nil, err
		}
	}
	return &identity, nil
}

// EnsurePlayerIdentity returns the identity for an observed UUID/name pair,
// creating one when needed. The call represents a live in-game observation
// (roster, session or metrics ingestion): last_seen_at is stamped on create
// and on every re-observation. Online UUIDs are authoritative; offline UUIDs
// are kept distinct and never merged by name alone.
func (s *Store) EnsurePlayerIdentity(ctx context.Context, primaryUUID, name, xuid, floodgateUUID string, offline bool) (*PlayerIdentity, error) {
	return s.ensurePlayerIdentity(ctx, primaryUUID, name, xuid, floodgateUUID, offline, true)
}

// EnsurePlayerIdentityRecorded registers or refreshes an identity WITHOUT
// claiming an in-game observation. It is used when the panel learns of a
// player through an operator action (a manual allowlist add) or an identity
// lookup refresh: last_seen_at is left untouched, so a player who never
// joined never reports a fabricated "last seen".
func (s *Store) EnsurePlayerIdentityRecorded(ctx context.Context, primaryUUID, name, xuid, floodgateUUID string, offline bool) (*PlayerIdentity, error) {
	return s.ensurePlayerIdentity(ctx, primaryUUID, name, xuid, floodgateUUID, offline, false)
}

// OfflineUUIDFor derives a stable, server-scoped pseudo UUID for a name that
// has no authoritative online UUID. It is explicitly marked offline and is
// never merged with an online identity: the same name on two servers is two
// distinct observations.
func OfflineUUIDFor(serverID, name string) string {
	sum := sha256.Sum256([]byte("carbonpanel:offline:" + serverID + ":" + strings.ToLower(strings.TrimSpace(name))))
	hexed := hex.EncodeToString(sum[:16])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// EnsureOfflinePlayerIdentity records a name-only observation from presence or
// metrics ingestion. The derived UUID is a local index key, not a Minecraft
// online id. A manual_allowlist source only registers the identity: an
// operator add is not an in-game observation, so last_seen_at stays unset
// instead of being stamped with a plausible time the player never earned.
func (s *Store) EnsureOfflinePlayerIdentity(ctx context.Context, name, serverID, source string) (*PlayerIdentity, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("player name is required")
	}
	observed := !strings.EqualFold(strings.TrimSpace(source), SourceManualAllowlist)
	identity, err := s.ensurePlayerIdentity(ctx, OfflineUUIDFor(serverID, name), name, "", "", true, observed)
	if err != nil {
		return nil, err
	}
	if err := s.RecordAlias(ctx, identity.ID, name, serverID, source, time.Now().UTC()); err != nil {
		return nil, err
	}
	return identity, nil
}

// EnsureOpenPlayerSession inserts an open session only when none exists. The
// NOT EXISTS clause runs inside the INSERT, so concurrent roster loads cannot
// both observe "no session" and create two open rows.
func (s *Store) EnsureOpenPlayerSession(ctx context.Context, identityID, serverID, source string, joinedAt time.Time) (bool, error) {
	if identityID == "" || serverID == "" {
		return false, errors.New("player session requires identity and server")
	}
	result := s.db.WithContext(ctx).Exec(`
		INSERT INTO player_sessions (id, identity_id, server_id, joined_at, source, estimate)
		SELECT ?, ?, ?, ?, ?, 0
		WHERE NOT EXISTS (
			SELECT 1 FROM player_sessions
			WHERE identity_id = ? AND server_id = ? AND left_at IS NULL
		)`, uuid.NewString(), identityID, serverID, joinedAt.UTC(), source, identityID, serverID)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected == 1, nil
}

// HasOpenPlayerSession reports whether an identity is currently observed as
// present on a server, so roster ingestion does not stack duplicate sessions.
func (s *Store) HasOpenPlayerSession(ctx context.Context, identityID, serverID string) (bool, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&PlayerSession{}).
		Where("identity_id = ? AND server_id = ? AND left_at IS NULL", identityID, serverID).
		Count(&count).Error
	return count > 0, err
}

// OpenPlayerSessions returns open sessions joined with their identity's current
// name, for closing observations that disappeared from a roster.
type OpenPlayerSession struct {
	SessionID  string
	IdentityID string
	Name       string
}

func (s *Store) OpenPlayerSessions(ctx context.Context, serverID string) ([]OpenPlayerSession, error) {
	var rows []OpenPlayerSession
	err := s.db.WithContext(ctx).Model(&PlayerSession{}).
		Select("player_sessions.id as session_id, player_sessions.identity_id as identity_id, player_identities.current_name as name").
		Joins("JOIN player_identities ON player_identities.id = player_sessions.identity_id").
		Where("player_sessions.server_id = ? AND player_sessions.left_at IS NULL", serverID).
		Find(&rows).Error
	return rows, err
}

// GetPlayerIdentity loads one identity by opaque key.
func (s *Store) GetPlayerIdentity(ctx context.Context, key string) (*PlayerIdentity, error) {
	var identity PlayerIdentity
	if err := s.db.WithContext(ctx).Where("id = ?", key).First(&identity).Error; err != nil {
		return nil, err
	}
	return &identity, nil
}

// playerIdentityOrder sorts identities with the most recent in-game
// observation first. Identities without any observation (last_seen_at IS
// NULL) always sort after observed ones, so a freshly registered identity
// never floats above players who were actually seen in game.
const playerIdentityOrder = "last_seen_at IS NULL, last_seen_at DESC"

// ListPlayerIdentities returns identities matching a bounded search. A server
// filter is applied through observed sessions so the RBAC object scope holds.
func (s *Store) ListPlayerIdentities(ctx context.Context, serverID, query string, limit int) ([]PlayerIdentity, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := s.db.WithContext(ctx).Model(&PlayerIdentity{})
	if serverID != "" {
		q = q.Where("id IN (SELECT identity_id FROM player_sessions WHERE server_id = ?) OR id IN (SELECT identity_id FROM player_access_states WHERE server_id = ?)", serverID, serverID)
	}
	if needle := strings.TrimSpace(query); needle != "" {
		like := "%" + needle + "%"
		q = q.Where("id = ? OR current_name LIKE ? OR primary_uuid LIKE ? OR xuid LIKE ? OR id IN (SELECT identity_id FROM player_aliases WHERE name LIKE ?)", needle, like, like, like, like)
	}
	var out []PlayerIdentity
	err := q.Order(playerIdentityOrder).Limit(limit).Find(&out).Error
	return out, err
}

// FindPlayerIdentitiesByExact returns identities whose primary UUID or current
// name exactly matches the supplied values. It is used by exact-identity lookup
// to detect a unique local observation or a genuine name collision; it is never
// a fuzzy search. Both values are trimmed; an empty value is ignored.
func (s *Store) FindPlayerIdentitiesByExact(ctx context.Context, uuid, name string) ([]PlayerIdentity, error) {
	uuid = strings.TrimSpace(uuid)
	name = strings.TrimSpace(name)
	if uuid == "" && name == "" {
		return nil, nil
	}
	q := s.db.WithContext(ctx).Model(&PlayerIdentity{})
	var conditions []string
	var args []any
	if uuid != "" {
		conditions = append(conditions, "primary_uuid = ?")
		args = append(args, uuid)
	}
	if name != "" {
		conditions = append(conditions, "lower(current_name) = lower(?)")
		args = append(args, name)
	}
	q = q.Where(strings.Join(conditions, " OR "), args...)
	var out []PlayerIdentity
	err := q.Order(playerIdentityOrder).Limit(50).Find(&out).Error
	return out, err
}

// RecordAlias stores an observed name with its source; repeats are idempotent.
func (s *Store) RecordAlias(ctx context.Context, identityID, name, serverID, source string, observedAt time.Time) error {
	if identityID == "" || strings.TrimSpace(name) == "" {
		return nil
	}
	row := PlayerAlias{
		ID:             uuid.NewString(),
		IdentityID:     identityID,
		Name:           strings.TrimSpace(name),
		SourceServerID: serverID,
		Source:         source,
		ObservedAt:     observedAt,
	}
	err := s.db.WithContext(ctx).Create(&row).Error
	if err == nil || !isUniqueViolation(err) {
		return err
	}
	return s.db.WithContext(ctx).Model(&PlayerAlias{}).
		Where("identity_id = ? AND name = ? AND source_server_id = ?", identityID, row.Name, serverID).
		Update("observed_at", observedAt).Error
}

// StartPlayerSession records a join. Returns the session id so the matching
// leave can close it without guessing.
func (s *Store) StartPlayerSession(ctx context.Context, identityID, serverID, source string, joinedAt time.Time) (string, error) {
	row := PlayerSession{
		ID:         uuid.NewString(),
		IdentityID: identityID,
		ServerID:   serverID,
		JoinedAt:   joinedAt,
		Source:     source,
	}
	return row.ID, s.db.WithContext(ctx).Create(&row).Error
}

// EndPlayerSession closes the most recent open session for an identity/server.
func (s *Store) EndPlayerSession(ctx context.Context, identityID, serverID string, leftAt time.Time) error {
	var session PlayerSession
	err := s.db.WithContext(ctx).
		Where("identity_id = ? AND server_id = ? AND left_at IS NULL", identityID, serverID).
		Order("joined_at DESC").First(&session).Error
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&session).Updates(map[string]any{"left_at": leftAt}).Error
}

// EndPlayerSessionById closes a specific open session during roster ingestion.
func (s *Store) EndPlayerSessionById(ctx context.Context, sessionID string, leftAt time.Time) error {
	return s.db.WithContext(ctx).Model(&PlayerSession{}).
		Where("id = ? AND left_at IS NULL", sessionID).
		Update("left_at", leftAt).Error
}

// ListPlayerSessions returns newest-first sessions for one identity.
func (s *Store) ListPlayerSessions(ctx context.Context, identityID, serverID string, limit int) ([]PlayerSession, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Where("identity_id = ?", identityID)
	if serverID != "" {
		q = q.Where("server_id = ?", serverID)
	}
	var out []PlayerSession
	err := q.Order("joined_at DESC").Limit(limit).Find(&out).Error
	return out, err
}

// RecordPlayerAccess stores the latest observed access snapshot for a server.
// Re-observing a server replaces the prior snapshot rather than stacking rows.
func (s *Store) RecordPlayerAccess(ctx context.Context, state PlayerAccessState) error {
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "identity_id"}, {Name: "server_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"whitelisted", "operator", "op_level", "banned", "ban_reason",
			"ban_expires_at", "mute", "source", "confidence", "observed_at",
		}),
	}).Create(&state).Error
}

// ListPlayerAccess returns observed access states for one identity.
func (s *Store) ListPlayerAccess(ctx context.Context, identityID string) ([]PlayerAccessState, error) {
	var out []PlayerAccessState
	err := s.db.WithContext(ctx).Where("identity_id = ?", identityID).Order("server_id").Find(&out).Error
	return out, err
}

// BeginPlayerAction persists intent before any external mutation. A repeated
// request id with the same identity is idempotent; a completed action returns
// its journal row, while an executing action returns ErrPlayerActionInProgress.
func (s *Store) BeginPlayerAction(ctx context.Context, requestID, serverID, identityKey, action, actorID, actorName, reason, priorValue string, targetCount int) (*PlayerModerationAction, bool, error) {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return nil, true, errors.New("player action requires a request id")
	}
	if len(requestID) > 128 {
		return nil, true, errors.New("request_id must be at most 128 characters")
	}
	var row PlayerModerationAction
	err := s.db.WithContext(ctx).Where("request_id = ?", requestID).First(&row).Error
	if err == nil {
		if row.Action != action || row.ServerID != serverID || row.IdentityKey != identityKey {
			return nil, false, ErrPlayerActionChanged
		}
		if row.Phase == PlayerActionExecuting || row.Phase == PlayerActionPrepared {
			return &row, false, ErrPlayerActionInProgress
		}
		return &row, false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}
	row = PlayerModerationAction{
		ID:          uuid.NewString(),
		RequestID:   requestID,
		ServerID:    serverID,
		IdentityKey: identityKey,
		Action:      action,
		ActorID:     actorID,
		ActorName:   actorName,
		Reason:      reason,
		TargetCount: targetCount,
		PriorValue:  priorValue,
		UndoToken:   "undo_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Phase:       PlayerActionPrepared,
	}
	if err := s.db.WithContext(ctx).Create(&row).Error; err != nil {
		if isUniqueViolation(err) {
			return nil, false, ErrPlayerActionInProgress
		}
		return nil, false, err
	}
	return &row, true, nil
}

// AdvancePlayerAction moves a journal row to a terminal or executing phase.
func (s *Store) AdvancePlayerAction(ctx context.Context, id, from, to, detail string) error {
	r := s.db.WithContext(ctx).Model(&PlayerModerationAction{}).
		Where("id = ? AND phase = ?", id, from).
		Updates(map[string]any{"phase": to, "last_error": detail, "updated_at": time.Now().UTC()})
	if r.Error != nil {
		return r.Error
	}
	if r.RowsAffected != 1 {
		return fmt.Errorf("player action %s phase changed; reconcile before retry", id)
	}
	return nil
}

// ClaimPlayerActionUndo atomically marks an undo token as consumed before the
// external inverse mutation, so concurrent or repeated undos cannot both apply.
func (s *Store) ClaimPlayerActionUndo(ctx context.Context, token string) (bool, error) {
	r := s.db.WithContext(ctx).Model(&PlayerModerationAction{}).
		Where("undo_token = ? AND undone = ? AND prior_value <> ? AND prior_value <> ?", token, false, "", "{}").
		Updates(map[string]any{"undone": true, "updated_at": time.Now().UTC()})
	if r.Error != nil {
		return false, r.Error
	}
	return r.RowsAffected == 1, nil
}

// GetPlayerActionByUndoToken loads a reversible action for undo.
func (s *Store) GetPlayerActionByUndoToken(ctx context.Context, token string) (*PlayerModerationAction, error) {
	var row PlayerModerationAction
	if err := s.db.WithContext(ctx).Where("undo_token = ?", token).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ListPlayerHistory returns audit rows for one identity, newest first.
func (s *Store) ListPlayerHistory(ctx context.Context, identityKey, serverID string, limit int) ([]PlayerModerationAction, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := s.db.WithContext(ctx).Model(&PlayerModerationAction{})
	if identityKey != "" {
		q = q.Where("identity_key = ?", identityKey)
	}
	if serverID != "" {
		q = q.Where("server_id = ?", serverID)
	}
	var out []PlayerModerationAction
	err := q.Order("created_at DESC").Limit(limit).Find(&out).Error
	return out, err
}
