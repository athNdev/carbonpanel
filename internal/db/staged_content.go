// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ErrDuplicateStagedTarget refuses a second live content row with the same
// normalized target inside one batch. Publication records one live post-image
// per target, so two rows would make an interrupted APPLYING snapshot
// unverifiable and permanently refuse recovery.
var ErrDuplicateStagedTarget = errors.New("staged target already exists in this batch")

// ErrModpackBatchConflict refuses a modpack row together with a separately
// staged file or reviewed-mod row in one batch. A modpack expands to arbitrary
// pack paths, so admission cannot prove the two will not write the same live
// target; requiring a separate batch is the smallest safe closure.
var ErrModpackBatchConflict = errors.New("a modpack update cannot share a batch with separately staged file or mod content")

// NormalizedStagedTarget matches the publication-time target identity: case,
// separator and trailing-separator aliases name the same live file.
func NormalizedStagedTarget(value string) string {
	return strings.TrimSuffix(strings.ToLower(path.Clean(strings.ReplaceAll(value, "\\", "/"))), "/")
}

// rejectDuplicateStagedTarget runs inside the admission transaction after the
// row insert, so a duplicate rolls back the insert instead of committing a
// batch publication cannot recover.
func rejectDuplicateStagedTarget(tx *gorm.DB, row *StagedContentChange, batchID string) error {
	var rows []StagedContentChange
	if err := tx.Select("id", "target_path").
		Where("batch_id = ? AND status = ?", batchID, StagedStatusStaged).
		Find(&rows).Error; err != nil {
		return err
	}
	target := NormalizedStagedTarget(row.TargetPath)
	for _, existing := range rows {
		if existing.ID == row.ID || NormalizedStagedTarget(existing.TargetPath) != target {
			continue
		}
		return fmt.Errorf("%w: %q is already staged in batch %s as row %s; discard that item before staging it again",
			ErrDuplicateStagedTarget, row.TargetPath, batchID, existing.ID)
	}
	return nil
}

// rejectModpackBatchMix refuses a modpack row alongside separately staged file
// or reviewed-mod rows. The modpack's expanded live targets are not known at
// admission, so accepting the mix could admit two rows for one live path.
func rejectModpackBatchMix(tx *gorm.DB, row *StagedContentChange, batchID string) error {
	if row.Kind != StagedKindModpack && row.Kind != StagedKindFile && row.Kind != StagedKindMod {
		return nil
	}
	var rows []StagedContentChange
	if err := tx.Select("id", "kind").
		Where("batch_id = ? AND status = ? AND id <> ?", batchID, StagedStatusStaged, row.ID).
		Find(&rows).Error; err != nil {
		return err
	}
	for _, existing := range rows {
		mixed := (row.Kind == StagedKindModpack && (existing.Kind == StagedKindFile || existing.Kind == StagedKindMod)) ||
			((row.Kind == StagedKindFile || row.Kind == StagedKindMod) && existing.Kind == StagedKindModpack)
		if !mixed {
			continue
		}
		return fmt.Errorf("%w: batch %s already has %s row %s; discard it or apply it in a separate batch before staging this %s row",
			ErrModpackBatchConflict, batchID, existing.Kind, existing.ID, row.Kind)
	}
	return nil
}

// ensureStagedModApprovalAdmission enforces durable operator approval on
// reviewed mod replacements. A reviewed row must carry the SHA-512 its staged
// bytes were verified against when it was admitted; a legacy row without that
// durable proof is refused instead of deriving approval from a mutable sidecar.
func ensureStagedModApprovalAdmission(row *StagedContentChange) error {
	if row == nil || row.Kind != StagedKindMod || row.ExpectedLiveSha512 == "" {
		return nil
	}
	if !validSHA512Hex(row.ApprovedSha512) {
		if row.ApprovedSha512 == "" {
			return ErrStagedModApprovalMissing
		}
		return ErrStagedModApprovalInvalid
	}
	row.ApprovedSha512 = strings.ToLower(row.ApprovedSha512)
	if err := validateStagedApprovedReleaseIdentity(row); err != nil {
		return err
	}
	return nil
}

// ensureReviewedModRuntimeAdmission refuses a reviewed mod row while the same
// batch already carries a pending runtime configuration change the review was
// not verified against. It also records the normalized effective runtime
// target on the row, or refuses a caller-supplied target that no longer
// matches, so publication can revalidate the exact target the candidate was
// verified against. It runs after the transaction's first write so the check
// cannot straddle a concurrent config admission.
func ensureReviewedModRuntimeAdmission(tx *gorm.DB, row *StagedContentChange, batchID string) error {
	if row == nil || row.Kind != StagedKindMod || row.ExpectedLiveSha512 == "" {
		return nil
	}
	if err := requireNoPendingReviewedModRuntimeChanges(tx, row.ServerID, batchID); err != nil {
		return err
	}
	loader, game, err := reviewedRuntimeTargetTx(tx, row.ServerID)
	if err != nil {
		if errors.Is(err, ErrReviewedModRuntimeUnknown) {
			return fmt.Errorf("%w: %v; review the update when the Docker runtime target is known", ErrStagedModRuntimeTargetChanged, err)
		}
		return err
	}
	if row.ReviewedRuntimeLoader != "" || row.ReviewedRuntimeVersion != "" {
		if row.ReviewedRuntimeLoader != string(loader) || row.ReviewedRuntimeVersion != game {
			return fmt.Errorf("%w: candidate was reviewed for %s/%s but the effective target is %s/%s; review the update again",
				ErrStagedModRuntimeTargetChanged, row.ReviewedRuntimeLoader, row.ReviewedRuntimeVersion, loader, game)
		}
		return nil
	}
	row.ReviewedRuntimeLoader = string(loader)
	row.ReviewedRuntimeVersion = game
	return nil
}

// hasStagedReviewedModCandidates reports whether the server has a staged
// reviewed mod replacement whose live digest precondition identifies a provider
// candidate.
func hasStagedReviewedModCandidates(tx *gorm.DB, serverID string) (bool, error) {
	var rows []StagedContentChange
	if err := tx.Select("expected_live_sha512").
		Where("server_id = ? AND kind = ? AND status = ?", serverID, StagedKindMod, StagedStatusStaged).
		Find(&rows).Error; err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.ExpectedLiveSha512 != "" {
			return true, nil
		}
	}
	return false, nil
}

// HasStagedReviewedModCandidates reports whether a reviewed mod replacement is
// staged for the server. Callers use it before changing the Docker runtime
// target, which would invalidate the review context.
func (s *Store) HasStagedReviewedModCandidates(ctx context.Context, serverID string) (bool, error) {
	return hasStagedReviewedModCandidates(s.db.WithContext(ctx), serverID)
}

func ensureNoStagedReviewedModCandidatesTx(tx *gorm.DB, serverID string) error {
	has, err := hasStagedReviewedModCandidates(tx, serverID)
	if err != nil {
		return err
	}
	if has {
		return ErrStagedModVersionChange
	}
	return nil
}

func validSHA512Hex(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha512.Size
}

// validateStagedApprovedReleaseIdentity requires the durable provider release
// identity that admission records with an approved digest. An older reviewed
// row may carry only the digest; publication cannot prove which release the
// operator approved, so it is refused with re-review guidance instead of
// trusting the mutable sidecar.
func validateStagedApprovedReleaseIdentity(row *StagedContentChange) error {
	if row == nil {
		return ErrStagedModApprovalIdentityMissing
	}
	row.ApprovedProvider = strings.ToLower(strings.TrimSpace(row.ApprovedProvider))
	row.ApprovedProjectID = strings.TrimSpace(row.ApprovedProjectID)
	row.ApprovedVersionID = strings.TrimSpace(row.ApprovedVersionID)
	row.ApprovedFilename = strings.TrimSpace(row.ApprovedFilename)
	if row.ApprovedProvider == "" || row.ApprovedProjectID == "" || row.ApprovedVersionID == "" || row.ApprovedFilename == "" {
		return ErrStagedModApprovalIdentityMissing
	}
	if strings.ContainsAny(row.ApprovedFilename, "/\\") ||
		!strings.HasSuffix(strings.ToLower(row.ApprovedFilename), ".jar") {
		return fmt.Errorf("%w: approved filename %q is not a bare JAR name", ErrStagedModApprovalInvalid, row.ApprovedFilename)
	}
	return nil
}

// validateStagedContentAdmission enforces the invariants shared by every
// staged-content write: a known server, kind, action and target, plus staged
// bytes for anything that is not a delete. Both admission paths use it so a
// caller cannot bypass one set of rules by choosing the other entry point.
func validateStagedContentAdmission(row *StagedContentChange) error {
	if row == nil || row.ServerID == "" || row.Kind == "" || row.Action == "" || row.TargetPath == "" {
		return fmt.Errorf("staged content requires a server, kind, action, and target")
	}
	if row.Action != StagedActionDelete && row.StagePath == "" {
		return fmt.Errorf("staged content file is required")
	}
	switch row.Kind {
	case StagedKindMod, StagedKindDatapack, StagedKindFile, StagedKindModpack, StagedKindModule:
	default:
		return fmt.Errorf("unknown staged content kind %q", row.Kind)
	}
	switch row.Action {
	case StagedActionInstall, StagedActionReplace, StagedActionDelete:
	default:
		return fmt.Errorf("unknown staged content action %q", row.Action)
	}
	return nil
}

// AddStagedContent joins the server's one open batch and records a content
// edit. It does not copy bytes or touch the live tree; the caller writes the
// staged file first. A new batch uses the next-restart window and does not
// retarget a batch that is already open.
func (s *Store) AddStagedContent(ctx context.Context, row *StagedContentChange) (*StagedContentChange, error) {
	if err := validateStagedContentAdmission(row); err != nil {
		return nil, err
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
		if err := ensureStagedModApprovalAdmission(row); err != nil {
			return err
		}
		if err := ensureReviewedModRuntimeAdmission(tx, row, batch.ID); err != nil {
			return err
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		if err := rejectDuplicateStagedTarget(tx, row, batch.ID); err != nil {
			return err
		}
		return rejectModpackBatchMix(tx, row, batch.ID)
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
	batch, err := openUpdateBatch(tx, serverID)
	if err != nil {
		return err
	}
	if batch != nil && (batch.SealedAt != nil || batch.PublishedAt != nil) {
		return fmt.Errorf("%w: the open batch %s has a retained publication; retry the lifecycle operation to resolve it before staging more", ErrUpdateBatchConflict, batch.ID)
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

// WindowMatches reports whether the stored window denotes window, including
// the legacy config-only spellings that map onto the next-restart window. It
// lets the restart selector reuse the same normalization as admission instead
// of comparing the raw stored string.
func (b *ServerUpdateBatch) WindowMatches(window UpdateWindow, applyAt *time.Time, idleMinutes int) bool {
	if b == nil {
		return false
	}
	return batchWindowMatches(b, window, applyAt, idleMinutes)
}

func batchWindowConflict(batch *ServerUpdateBatch, window UpdateWindow, applyAt *time.Time, idleMinutes int) error {
	return fmt.Errorf("%w: open batch %s has window %q (at %v, idle %d) with reviewed changes; refusing conflicting window %q (at %v, idle %d)",
		ErrUpdateBatchConflict, batch.ID, batch.Window, batch.ApplyAt, batch.IdleMinutes, window, applyAt, idleMinutes)
}

// CheckReviewedContentWindow checks admission before the caller fetches payloads.
// PrepareReviewedContentBatch and AddStagedContentToBatch enforce it at writes.
func (s *Store) CheckReviewedContentWindow(ctx context.Context, serverID string, window UpdateWindow, applyAt *time.Time, idleMinutes int) error {
	tx := s.db.WithContext(ctx)
	batch, err := openUpdateBatch(tx, serverID)
	if err != nil || batch == nil {
		return err
	}
	return checkReviewedContentWindow(tx, batch, window, applyAt, idleMinutes)
}

func checkReviewedContentWindow(tx *gorm.DB, batch *ServerUpdateBatch, window UpdateWindow, applyAt *time.Time, idleMinutes int) error {
	if err := requireNoBatchCronConfig(tx, batch.ID); err != nil {
		return err
	}
	if batchWindowMatches(batch, window, applyAt, idleMinutes) {
		return nil
	}
	content, err := hasBatchContent(tx, batch.ID)
	if err != nil {
		return err
	}
	configs, err := hasBatchConfig(tx, batch.ID)
	if err != nil {
		return err
	}
	if content || configs {
		return batchWindowConflict(batch, window, applyAt, idleMinutes)
	}
	return nil
}

// AddStagedContentToBatch inserts only into the reviewed, still-open batch
// with the same window and no independently scheduled config row. It applies
// the same admission rules as AddStagedContent and always persists the row as
// staged: a caller cannot orphan it with a terminal status.
func (s *Store) AddStagedContentToBatch(ctx context.Context, row *StagedContentChange, batchID string, window UpdateWindow, applyAt *time.Time, idleMinutes int) error {
	if err := validateStagedContentAdmission(row); err != nil {
		return err
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Write first to serialize with config admission. Nothing is visible
		// until commit, and a refusal rolls this insert back.
		row.BatchID = batchID
		row.Status = StagedStatusStaged
		if err := ensureStagedModApprovalAdmission(row); err != nil {
			return err
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
		if err := rejectDuplicateStagedTarget(tx, row, batchID); err != nil {
			return err
		}
		if err := rejectModpackBatchMix(tx, row, batchID); err != nil {
			return err
		}
		if err := requireBatchAdmission(tx, row.ServerID); err != nil {
			return err
		}
		batch, err := openUpdateBatch(tx, row.ServerID)
		if err != nil {
			return err
		}
		if batch == nil || batch.ID != batchID || !batchWindowMatches(batch, window, applyAt, idleMinutes) {
			return fmt.Errorf("%w: chosen batch %s closed or changed window before staging", ErrUpdateBatchConflict, batchID)
		}
		if err := requireNoBatchCronConfig(tx, batchID); err != nil {
			return err
		}
		if err := ensureReviewedModRuntimeAdmission(tx, row, batch.ID); err != nil {
			return err
		}
		if row.Kind == StagedKindMod && row.ExpectedLiveSha512 != "" &&
			(row.ReviewedRuntimeLoader != "" || row.ReviewedRuntimeVersion != "") {
			return tx.Model(row).Updates(map[string]any{
				"reviewed_runtime_loader":  row.ReviewedRuntimeLoader,
				"reviewed_runtime_version": row.ReviewedRuntimeVersion,
			}).Error
		}
		return nil
	})
}

// SetBatchIdleSince records when the current idle stretch started.
// A nil value clears it, which is what a player joining does.
func (s *Store) SetBatchIdleSince(ctx context.Context, id string, since *time.Time) error {
	return s.db.WithContext(ctx).Model(&ServerUpdateBatch{}).Where("id = ?", id).Update("idle_since", since).Error
}

// PrepareReviewedContentBatch commits the window before content is written,
// serializing the counterpart check and any window change with config staging.
func (s *Store) PrepareReviewedContentBatch(ctx context.Context, serverID string, window UpdateWindow, applyAt *time.Time, idleMinutes int) (*ServerUpdateBatch, error) {
	return s.setOpenBatchWindow(ctx, serverID, window, applyAt, idleMinutes, true)
}

// SetOpenBatchWindow moves an open batch and its pending non-cron configs.
// Batch publication uses it to park legacy cron rows after due work finishes.
func (s *Store) SetOpenBatchWindow(ctx context.Context, serverID string, window UpdateWindow, applyAt *time.Time, idleMinutes int) (*ServerUpdateBatch, error) {
	return s.setOpenBatchWindow(ctx, serverID, window, applyAt, idleMinutes, false)
}

func (s *Store) setOpenBatchWindow(ctx context.Context, serverID string, window UpdateWindow, applyAt *time.Time, idleMinutes int, reviewed bool) (*ServerUpdateBatch, error) {
	mode, err := modeForWindow(window, applyAt, idleMinutes)
	if err != nil {
		return nil, err
	}
	var batch *ServerUpdateBatch
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		batch, err = ensureOpenBatch(tx, serverID, window)
		if err != nil {
			return err
		}
		if reviewed {
			if err := requireBatchAdmission(tx, serverID); err != nil {
				return err
			}
			if err := checkReviewedContentWindow(tx, batch, window, applyAt, idleMinutes); err != nil {
				return err
			}
			if err := requireNoPendingReviewedModRuntimeChanges(tx, serverID, batch.ID); err != nil {
				return err
			}
		}
		return setBatchWindow(tx, batch, window, mode, applyAt, idleMinutes)
	})
	if err != nil {
		return nil, err
	}
	return batch, nil
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

// SealUpdateBatch stops admission into a batch whose publication left durable
// applied rows without a confirmed lifecycle outcome. The batch stays open so
// the retry can resolve the original snapshot. It is idempotent.
func (s *Store) SealUpdateBatch(ctx context.Context, id string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&ServerUpdateBatch{}).
		Where("id = ? AND status = ? AND sealed_at IS NULL", id, UpdateBatchOpen).
		Update("sealed_at", now).Error
}

// ClaimUpdateBatchForPublication durably seals an open, unacknowledged batch
// before a next-restart attempt copies live files or applies config. Admission
// checks the same row inside its own transaction, so a DB config stage cannot
// join the snapshot's recovery tree after this claim. It reports whether this
// call set the seal, so a caller that fully compensates knows it may release it.
func (s *Store) ClaimUpdateBatchForPublication(ctx context.Context, id string) (bool, error) {
	res := s.db.WithContext(ctx).Model(&ServerUpdateBatch{}).
		Where("id = ? AND status = ? AND sealed_at IS NULL AND published_at IS NULL", id, UpdateBatchOpen).
		Update("sealed_at", time.Now())
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// ReleaseUpdateBatchSeal clears a seal when an attempt fully compensated and
// left no durable applied evidence. It never clears PublishedAt, so an
// acknowledged batch stays closed to admission. It is idempotent.
func (s *Store) ReleaseUpdateBatchSeal(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Model(&ServerUpdateBatch{}).
		Where("id = ? AND status = ? AND published_at IS NULL", id, UpdateBatchOpen).
		Update("sealed_at", nil).Error
}

// MarkUpdateBatchPublished records that a lifecycle accepted the batch's
// runtime publication. A later retry may finish metadata and recovery cleanup,
// but must not perform another container publication. It also seals admission
// because appending new work after acceptance would reuse the recovery tree.
// It is idempotent and does not change the first acknowledgement time.
func (s *Store) MarkUpdateBatchPublished(ctx context.Context, id string) error {
	now := time.Now()
	return s.db.WithContext(ctx).Model(&ServerUpdateBatch{}).
		Where("id = ? AND status = ? AND published_at IS NULL", id, UpdateBatchOpen).
		Updates(map[string]any{"published_at": now, "sealed_at": now}).Error
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

// CountBatchContentByStatus counts one batch's content rows in a status.
// Applied rows are durable evidence that a publication copied its bytes even
// after ListBatchContent no longer returns them, so finalization can retry an
// idempotent recovery cleanup without republishing.
func (s *Store) CountBatchContentByStatus(ctx context.Context, batchID string, status StagedStatus) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&StagedContentChange{}).
		Where("batch_id = ? AND status = ?", batchID, status).Count(&count).Error
	return count, err
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
