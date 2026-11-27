// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AddStagedContent joins the server's one open batch and records a content
// edit. It does not copy bytes or touch the live tree; the caller writes the
// staged file first. A new batch uses the next-restart window and does not
// retarget a batch that is already open.
func (s *Store) AddStagedContent(ctx context.Context, row *StagedContentChange) (*StagedContentChange, error) {
	if row == nil || row.ServerID == "" || row.Kind == "" || row.Action == "" || row.TargetPath == "" {
		return nil, fmt.Errorf("staged content requires a server, kind, action, and target")
	}
	if row.Action != StagedActionDelete && row.StagePath == "" {
		return nil, fmt.Errorf("staged content file is required")
	}
	switch row.Kind {
	case StagedKindMod, StagedKindDatapack, StagedKindFile, StagedKindModpack, StagedKindModule:
	default:
		return nil, fmt.Errorf("unknown staged content kind %q", row.Kind)
	}
	switch row.Action {
	case StagedActionInstall, StagedActionReplace, StagedActionDelete:
	default:
		return nil, fmt.Errorf("unknown staged content action %q", row.Action)
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		batch, err := ensureOpenBatch(tx, row.ServerID, UpdateWindowNextRestart)
		if err != nil {
			return err
		}
		if err := requireBatchAdmission(tx, row.ServerID); err != nil {
			return err
		}
		if err := requireNoBatchCronConfig(tx, batch.ID); err != nil {
			return err
		}
		// Legacy config-only rows used apply-mode spellings as their batch
		// window. Once any cron row is gone, content can use the normal
		// next-restart batch path alongside compatible config.
		if batch.Window != string(UpdateWindowNextRestart) && batchWindowMatches(batch, UpdateWindowNextRestart, nil, 0) {
			if err := setBatchWindow(tx, batch, UpdateWindowNextRestart, StagedApplyOnRestart, nil, 0); err != nil {
				return err
			}
		}
		if row.ID == "" {
			row.ID = uuid.New().String()
		}
		row.BatchID = batch.ID
		row.Status = StagedStatusStaged
		return tx.Create(row).Error
	})
	if err != nil {
		return nil, err
	}
	return row, nil
}

// GetStagedContent loads one content row, including discarded rows.
func (s *Store) GetStagedContent(ctx context.Context, id string) (*StagedContentChange, error) {
	var row StagedContentChange
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ListStagedContent returns content still waiting to be applied.
func (s *Store) ListStagedContent(ctx context.Context, serverID string) ([]StagedContentChange, error) {
	var rows []StagedContentChange
	err := s.db.WithContext(ctx).
		Where("server_id = ? AND status = ?", serverID, StagedStatusStaged).
		Order("created_at asc").
		Find(&rows).Error
	return rows, err
}

// DiscardStagedContent marks a staged row discarded. The caller removes any
// staged bytes first; this does not touch the filesystem.
func (s *Store) DiscardStagedContent(ctx context.Context, id string) (*StagedContentChange, error) {
	var row StagedContentChange
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if err != nil {
		return nil, err
	}
	if row.Status != StagedStatusStaged {
		return nil, fmt.Errorf("staged content %s is %s", id, row.Status)
	}
	row.Status = StagedStatusDiscarded
	if err := s.db.WithContext(ctx).Save(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// ListOpenUpdateBatches returns every batch that can still receive or apply updates.
func (s *Store) ListOpenUpdateBatches(ctx context.Context) ([]ServerUpdateBatch, error) {
	var rows []ServerUpdateBatch
	err := s.db.WithContext(ctx).Where("status = ?", UpdateBatchOpen).Order("created_at asc").Find(&rows).Error
	return rows, err
}

// OpenUpdateBatch returns the server's open batch, or nil when there is none.
func (s *Store) OpenUpdateBatch(ctx context.Context, serverID string) (*ServerUpdateBatch, error) {
	return openUpdateBatch(s.db.WithContext(ctx), serverID)
}

func openUpdateBatch(tx *gorm.DB, serverID string) (*ServerUpdateBatch, error) {
	var batch ServerUpdateBatch
	err := tx.Where("open_key = ? AND status = ?", serverID, UpdateBatchOpen).First(&batch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &batch, nil
}

// ErrUpdateBatchConflict signals a batch cannot admit the requested stage.
var ErrUpdateBatchConflict = errors.New("staged update batch conflict")

// ensureOpenBatch starts with a write so admission reads and subsequent writes
// share the same SQLite writer transaction, even when the batch already exists.
func ensureOpenBatch(tx *gorm.DB, serverID string, window UpdateWindow) (*ServerUpdateBatch, error) {
	batch := ServerUpdateBatch{
		ID: uuid.NewString(), ServerID: serverID, OpenKey: &serverID,
		Window: string(window), Status: UpdateBatchOpen,
	}
	if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "open_key"}}, DoNothing: true}).Create(&batch).Error; err != nil {
		return nil, err
	}
	open, err := openUpdateBatch(tx, serverID)
	if err != nil {
		return nil, err
	}
	if open == nil {
		return nil, fmt.Errorf("%w: could not find the open batch for server %s", ErrUpdateBatchConflict, serverID)
	}
	return open, nil
}

// requireBatchAdmission runs after the transaction's first write, so checking
// ownership and inserting cannot straddle a publish or discard snapshot.
func requireBatchAdmission(tx *gorm.DB, serverID string) error {
	var active int64
	if err := tx.Model(&ServerOperation{}).
		Where("active_server = ? AND kind IN ?", serverID, []string{"apply-batch", "discard-update-batch"}).
		Count(&active).Error; err != nil {
		return err
	}
	if active != 0 {
		return fmt.Errorf("%w: the batch is being published or discarded; retry after it finishes", ErrUpdateBatchConflict)
	}
	return nil
}

func hasBatchContent(tx *gorm.DB, batchID string) (bool, error) {
	var count int64
	err := tx.Model(&StagedContentChange{}).
		Where("batch_id = ? AND status = ?", batchID, StagedStatusStaged).Count(&count).Error
	return count > 0, err
}

func hasBatchConfig(tx *gorm.DB, batchID string) (bool, error) {
	var count int64
	err := tx.Model(&StagedConfigChange{}).
		Where("batch_id = ? AND status = ?", batchID, StagedStatusStaged).Count(&count).Error
	return count > 0, err
}

func hasBatchCronConfig(tx *gorm.DB, batchID string) (bool, error) {
	var count int64
	err := tx.Model(&StagedConfigChange{}).
		Where("batch_id = ? AND status = ? AND apply_mode = ?", batchID, StagedStatusStaged, StagedApplyScheduled).Count(&count).Error
	return count > 0, err
}

func requireNoBatchCronConfig(tx *gorm.DB, batchID string) error {
	cron, err := hasBatchCronConfig(tx, batchID)
	if err != nil {
		return err
	}
	if cron {
		return fmt.Errorf("%w: open batch %s has an independently scheduled config row; apply or discard that config before staging content", ErrUpdateBatchConflict, batchID)
	}
	return nil
}

func sameApplyInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func batchWindowMatches(batch *ServerUpdateBatch, window UpdateWindow, applyAt *time.Time, idleMinutes int) bool {
	stored := UpdateWindow(batch.Window)
	switch stored {
	case UpdateWindow(StagedApplyOnRestart), UpdateWindow(StagedApplyScheduled):
		// Older config-only batches used the first row's apply mode as the
		// window. A cron row has its own trigger; it defines no timed window.
		stored = UpdateWindowNextRestart
	}
	return stored == window && batch.IdleMinutes == idleMinutes && sameApplyInstant(batch.ApplyAt, applyAt)
}

func batchWindowConflict(batch *ServerUpdateBatch, window UpdateWindow, applyAt *time.Time, idleMinutes int) error {
	return fmt.Errorf("%w: open batch %s has window %q (at %v, idle %d) with reviewed changes; refusing conflicting window %q (at %v, idle %d)",
		ErrUpdateBatchConflict, batch.ID, batch.Window, batch.ApplyAt, batch.IdleMinutes, window, applyAt, idleMinutes)
}

// SetBatchIdleSince records when the current idle stretch started.
// A nil value clears it, which is what a player joining does.
func (s *Store) SetBatchIdleSince(ctx context.Context, id string, since *time.Time) error {
	return s.db.WithContext(ctx).Model(&ServerUpdateBatch{}).Where("id = ?", id).Update("idle_since", since).Error
}

func setBatchWindow(tx *gorm.DB, batch *ServerUpdateBatch, window UpdateWindow, mode StagedApplyMode, applyAt *time.Time, idleMinutes int) error {
	changed := !batchWindowMatches(batch, window, applyAt, idleMinutes)
	if batch.Window != string(window) || changed {
		updates := map[string]any{"window": string(window), "apply_at": applyAt, "idle_minutes": idleMinutes}
		if changed {
			updates["idle_since"] = nil
		}
		if err := tx.Model(batch).Updates(updates).Error; err != nil {
			return err
		}
		batch.Window, batch.ApplyAt, batch.IdleMinutes = string(window), applyAt, idleMinutes
	}
	// Repair older non-cron rows with a different tuple, leaving matching
	// rows and independent cron schedules untouched.
	var configs []StagedConfigChange
	if err := tx.Where("batch_id = ? AND status = ? AND apply_mode <> ?", batch.ID, StagedStatusStaged, StagedApplyScheduled).
		Find(&configs).Error; err != nil {
		return err
	}
	var stale []string
	for _, config := range configs {
		if config.ApplyMode != mode || config.IdleMinutes != idleMinutes || !sameApplyInstant(config.ApplyAt, applyAt) {
			stale = append(stale, config.ID)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return tx.Model(&StagedConfigChange{}).Where("id IN ?", stale).Updates(map[string]any{
		"apply_mode": string(mode), "apply_at": applyAt, "idle_minutes": idleMinutes,
	}).Error
}

// CloseUpdateBatch finishes the open batch so another one can be recorded.
func (s *Store) CloseUpdateBatch(ctx context.Context, id, status string) error {
	return s.db.WithContext(ctx).Model(&ServerUpdateBatch{}).Where("id = ? AND status = ?", id, UpdateBatchOpen).Updates(map[string]any{
		"status":   status,
		"open_key": nil,
	}).Error
}

// ListBatchContent returns staged content still waiting in one batch.
func (s *Store) ListBatchContent(ctx context.Context, batchID string) ([]StagedContentChange, error) {
	var rows []StagedContentChange
	err := s.db.WithContext(ctx).
		Where("batch_id = ? AND status = ?", batchID, StagedStatusStaged).
		Order("created_at asc").
		Find(&rows).Error
	return rows, err
}

// MarkStagedContentApplied records that a content row was committed to the live tree.
func (s *Store) MarkStagedContentApplied(ctx context.Context, id string) error {
	now := time.Now()
	res := s.db.WithContext(ctx).Model(&StagedContentChange{}).
		Where("id = ? AND status = ?", id, StagedStatusStaged).
		Updates(map[string]any{"status": StagedStatusApplied, "applied_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 1 {
		return nil
	}
	var row StagedContentChange
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return fmt.Errorf("staged content %s is not waiting to be applied", id)
	}
	if row.Status == StagedStatusApplied {
		return nil
	}
	return fmt.Errorf("staged content %s is not waiting to be applied", id)
}

// MarkStagedContentStaged returns an applied content row to staged after a failed publish.
func (s *Store) MarkStagedContentStaged(ctx context.Context, id string) error {
	res := s.db.WithContext(ctx).Model(&StagedContentChange{}).
		Where("id = ? AND status = ?", id, StagedStatusApplied).
		Updates(map[string]any{"status": StagedStatusStaged, "applied_at": nil})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 1 {
		return nil
	}
	var row StagedContentChange
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&row).Error; err != nil {
		return fmt.Errorf("staged content %s is not applied", id)
	}
	if row.Status == StagedStatusStaged {
		return nil
	}
	return fmt.Errorf("staged content %s is not applied", id)
}
