// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// PublicationPhase is the durable phase of one batch's runtime publication.
// The phases are ordered: preparing is the only phase in which no live write
// may have started, applying proves the snapshot exists but not that a runtime
// dispatch happened, and dispatched is the only phase from which a retry may
// verify a runtime outcome.
type PublicationPhase string

const (
	// PublicationPreparing means the attempt owns the batch but has not
	// recorded a complete recovery snapshot. No live write may have started, so
	// a partial recovery tree may be discarded and prepared again.
	PublicationPreparing PublicationPhase = "preparing"
	// PublicationPrepared means the exact snapshot (owned targets, backup
	// references and digests) is durable and complete. Live writes have not
	// started yet; the next transition marks applying before the first one.
	PublicationPrepared PublicationPhase = "prepared"
	// PublicationApplying means live files, module rows or config may have been
	// written. A runtime dispatch has not been recorded, so recovery may
	// restore the recorded snapshot but must never infer a dispatched runtime.
	PublicationApplying PublicationPhase = "applying"
	// PublicationDispatched means a runtime mutation intent was recorded
	// durably before the Docker call. The outcome is unverified until the
	// recorded expected replacement identity is observed again.
	PublicationDispatched PublicationPhase = "dispatched"
	// PublicationIdentityUnresolved means a runtime dispatch created a
	// replacement whose exact identity could not be persisted. The runtime may
	// contain the batch, so nothing is compensated or cleaned up. A retry may
	// adopt the canonical container created by that dispatch as the expected
	// identity, then verify it like any other dispatch.
	PublicationIdentityUnresolved PublicationPhase = "identity_unresolved"
	// PublicationVerified means a positive runtime outcome was recorded. A
	// retry owes cleanup and bookkeeping only.
	PublicationVerified PublicationPhase = "verified"
	// PublicationCleaned means recovery cleanup ran for a verified publication.
	PublicationCleaned PublicationPhase = "cleaned"
	// PublicationAbandoned means the attempt was fully compensated before any
	// runtime dispatch. It is not evidence about the runtime.
	PublicationAbandoned PublicationPhase = "abandoned"
)

// ServerUpdatePublication is the small, batch-bound record for one staged
// batch's publication. It separates the prepared snapshot, the dispatch
// intent, the positively verified runtime outcome and cleanup. It never stores
// a token, path outside the server data root, or game-authored metadata.
type ServerUpdatePublication struct {
	BatchID string `json:"batch_id" gorm:"primaryKey;column:batch_id"`
	// Attempt increases for each preparation attempt. Runtime identity captured
	// for one attempt is never valid evidence for another.
	Attempt  int              `json:"attempt" gorm:"not null;column:attempt"`
	ServerID string           `json:"server_id" gorm:"not null;index;column:server_id"`
	Phase    PublicationPhase `json:"phase" gorm:"not null;index;column:phase"`
	// Snapshot is the controller-owned recovery manifest: exact owned row ids,
	// targets, backup references and digests. Recovery never reads the mutable
	// game-data manifest for this decision.
	Snapshot string `gorm:"type:text;column:snapshot"`
	// ModuleIntent is set durably before a staged module row is allowed to
	// change its database row or container. A resume that finds module intent
	// without a dispatch cannot know whether a module runtime mutation began.
	ModuleIntent string `gorm:"type:text;column:module_intent"`
	// AppliedConfigIDs and AppliedContentIDs are the exact row identities this
	// dispatch covers. A row applied after dispatch is not covered by it.
	AppliedConfigIDs    string `gorm:"type:text;column:applied_config_ids"`
	AppliedContentIDs   string `gorm:"type:text;column:applied_content_ids"`
	BaselineContainerID string `gorm:"column:baseline_container_id"`
	// ExpectedContainerID is the replacement identity captured at creation.
	// Reconcile/adoption of any other container is identity repair, not
	// publication evidence.
	ExpectedContainerID string     `gorm:"column:expected_container_id"`
	VerifiedContainerID string     `gorm:"column:verified_container_id"`
	VerifiedAt          *time.Time `gorm:"column:verified_at"`
	CreatedAt           time.Time  `gorm:"autoCreateTime"`
	UpdatedAt           time.Time  `gorm:"autoUpdateTime"`
}

// ErrPublicationTransition refuses a phase change that the durable record does
// not permit. Callers must retain and seal rather than overwrite evidence.
var ErrPublicationTransition = errors.New("staged publication transition refused")

// ErrPublicationStaleAttempt refuses an update written for an older attempt.
var ErrPublicationStaleAttempt = errors.New("staged publication attempt is stale")

// GetUpdatePublication returns the batch's publication record, or nil when the
// batch predates durable publication records.
func (s *Store) GetUpdatePublication(ctx context.Context, batchID string) (*ServerUpdatePublication, error) {
	if batchID == "" {
		return nil, nil
	}
	var record ServerUpdatePublication
	err := s.db.WithContext(ctx).Where("batch_id = ?", batchID).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// BeginUpdatePublication starts the next preparation attempt. It refuses while
// the record proves an unverified runtime dispatch: that attempt must be
// resolved before a new snapshot may replace its evidence.
func (s *Store) BeginUpdatePublication(ctx context.Context, batchID, serverID, baselineContainerID string) (*ServerUpdatePublication, error) {
	if batchID == "" {
		return nil, fmt.Errorf("%w: batch id is required", ErrPublicationTransition)
	}
	var out *ServerUpdatePublication
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing ServerUpdatePublication
		err := tx.Where("batch_id = ?", batchID).First(&existing).Error
		missing := errors.Is(err, gorm.ErrRecordNotFound)
		if err != nil && !missing {
			return err
		}
		attempt := 1
		if !missing {
			switch existing.Phase {
			case PublicationDispatched, PublicationIdentityUnresolved, PublicationVerified, PublicationCleaned:
				return fmt.Errorf("%w: batch %s is %s; resolve the recorded runtime outcome first", ErrPublicationTransition, batchID, existing.Phase)
			}
			attempt = existing.Attempt + 1
		}
		record := ServerUpdatePublication{
			BatchID: batchID, ServerID: serverID, Attempt: attempt,
			Phase: PublicationPreparing, BaselineContainerID: baselineContainerID,
		}
		if !missing {
			if err := tx.Model(&ServerUpdatePublication{}).Where("batch_id = ?", batchID).Updates(map[string]any{
				"attempt": attempt, "server_id": serverID, "phase": PublicationPreparing,
				"snapshot": "", "module_intent": "", "applied_config_ids": "",
				"applied_content_ids": "", "baseline_container_id": baselineContainerID,
				"expected_container_id": "", "verified_container_id": "", "verified_at": nil,
			}).Error; err != nil {
				return err
			}
			record.CreatedAt = existing.CreatedAt
		} else if err := tx.Create(&record).Error; err != nil {
			return err
		}
		out = &record
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RecordUpdatePublicationSnapshot stores the complete recovery snapshot and
// moves preparing -> prepared. It is the last durable write before live work
// may start, so a missing snapshot always means no live write began.
func (s *Store) RecordUpdatePublicationSnapshot(ctx context.Context, batchID string, attempt int, snapshot string) error {
	return s.publishTransition(ctx, batchID, attempt,
		[]PublicationPhase{PublicationPreparing}, PublicationPrepared,
		map[string]any{"snapshot": snapshot})
}

// RecordUpdatePublicationModuleIntent durably records that staged module rows
// are about to change module rows or containers. A resume that sees this
// intent without dispatch cannot prove the runtime was untouched.
func (s *Store) RecordUpdatePublicationModuleIntent(ctx context.Context, batchID string, attempt int, intent string) error {
	return s.publishTransition(ctx, batchID, attempt,
		[]PublicationPhase{PublicationPrepared, PublicationApplying}, "",
		map[string]any{"module_intent": intent})
}

// RecordUpdatePublicationApplying marks the first live write. It must be
// durable before that write; only a missing applying marker proves that no live
// write began.
func (s *Store) RecordUpdatePublicationApplying(ctx context.Context, batchID string, attempt int) error {
	return s.publishTransition(ctx, batchID, attempt,
		[]PublicationPhase{PublicationPrepared, PublicationApplying}, PublicationApplying, nil)
}

// RecordUpdatePublicationDispatch stores the exact applied row identities and
// moves prepared/applying -> dispatched. Callers must do this before the Docker
// call; the runtime outcome is not evidence until it is verified.
func (s *Store) RecordUpdatePublicationDispatch(ctx context.Context, batchID string, attempt int, configIDs, contentIDs string) error {
	return s.publishTransition(ctx, batchID, attempt,
		[]PublicationPhase{PublicationPrepared, PublicationApplying}, PublicationDispatched,
		map[string]any{"applied_config_ids": configIDs, "applied_content_ids": contentIDs})
}

// RecordUpdatePublicationReplacement stores the replacement identity returned
// at creation time. It is idempotent for the same identity and refuses a
// different one: only the identity captured for this attempt is evidence.
func (s *Store) RecordUpdatePublicationReplacement(ctx context.Context, batchID string, attempt int, expected string) error {
	if expected == "" {
		return fmt.Errorf("%w: replacement identity is required", ErrPublicationTransition)
	}
	var record ServerUpdatePublication
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("batch_id = ?", batchID).First(&record).Error; err != nil {
			return err
		}
		if record.Attempt != attempt {
			return fmt.Errorf("%w: batch %s attempt %d is not %d", ErrPublicationStaleAttempt, batchID, record.Attempt, attempt)
		}
		if record.Phase != PublicationDispatched {
			return fmt.Errorf("%w: batch %s is %s, not dispatched", ErrPublicationTransition, batchID, record.Phase)
		}
		if record.ExpectedContainerID != "" {
			if record.ExpectedContainerID == expected {
				return nil
			}
			return fmt.Errorf("%w: batch %s already recorded replacement %s", ErrPublicationTransition, batchID, record.ExpectedContainerID)
		}
		return tx.Model(&ServerUpdatePublication{}).
			Where("batch_id = ? AND attempt = ? AND phase = ?", batchID, attempt, PublicationDispatched).
			Update("expected_container_id", expected).Error
	})
	if err != nil {
		return err
	}
	return nil
}

// MarkUpdatePublicationIdentityUnresolved records that a dispatch created a
// replacement whose exact identity could not be persisted. It is idempotent
// and only applies to that dispatch attempt; the record then proves a retry may
// adopt the canonical container created by this dispatch.
func (s *Store) MarkUpdatePublicationIdentityUnresolved(ctx context.Context, batchID string, attempt int) error {
	return s.publishTransition(ctx, batchID, attempt,
		[]PublicationPhase{PublicationDispatched}, PublicationIdentityUnresolved, nil)
}

// AdoptUpdatePublicationIdentity records the canonical container a retry
// observed as the expected identity of an identity-unresolved dispatch, and
// returns the record to the dispatched phase so normal identity verification
// can acknowledge it. It refuses an empty or baseline container: those do not
// prove this dispatch created a replacement. It is idempotent for the same
// identity.
func (s *Store) AdoptUpdatePublicationIdentity(ctx context.Context, batchID string, attempt int, containerID string) error {
	if strings.TrimSpace(containerID) == "" {
		return fmt.Errorf("%w: an observed canonical container identity is required", ErrPublicationTransition)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record ServerUpdatePublication
		if err := tx.Where("batch_id = ?", batchID).First(&record).Error; err != nil {
			return err
		}
		if record.Attempt != attempt {
			return fmt.Errorf("%w: batch %s attempt %d is not %d", ErrPublicationStaleAttempt, batchID, record.Attempt, attempt)
		}
		if record.ExpectedContainerID != "" {
			if record.ExpectedContainerID == containerID {
				return nil
			}
			return fmt.Errorf("%w: batch %s already recorded replacement %s", ErrPublicationTransition, batchID, record.ExpectedContainerID)
		}
		if record.Phase != PublicationIdentityUnresolved {
			return fmt.Errorf("%w: batch %s is %s, not identity-unresolved", ErrPublicationTransition, batchID, record.Phase)
		}
		if record.BaselineContainerID != "" && record.BaselineContainerID == containerID {
			return fmt.Errorf("%w: observed container %s is the recorded baseline, not a replacement", ErrPublicationTransition, containerID)
		}
		return tx.Model(&ServerUpdatePublication{}).
			Where("batch_id = ? AND attempt = ? AND phase = ?", batchID, attempt, PublicationIdentityUnresolved).
			Updates(map[string]any{"phase": PublicationDispatched, "expected_container_id": containerID}).Error
	})
}

// MarkUpdatePublicationVerified records the positive runtime outcome and the
// batch acknowledgement in one transaction. It is idempotent. A failure leaves
// both the phase and the batch unacknowledged so a retry cannot run cleanup
// over an unproven publication.
func (s *Store) MarkUpdatePublicationVerified(ctx context.Context, batchID string, attempt int, containerID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record ServerUpdatePublication
		if err := tx.Where("batch_id = ?", batchID).First(&record).Error; err != nil {
			return err
		}
		now := time.Now()
		switch record.Phase {
		case PublicationVerified, PublicationCleaned:
			return nil
		case PublicationDispatched:
		default:
			return fmt.Errorf("%w: batch %s is %s, not dispatched", ErrPublicationTransition, batchID, record.Phase)
		}
		if record.Attempt != attempt {
			return fmt.Errorf("%w: batch %s attempt %d is not %d", ErrPublicationStaleAttempt, batchID, record.Attempt, attempt)
		}
		if err := tx.Model(&ServerUpdatePublication{}).
			Where("batch_id = ? AND attempt = ? AND phase = ?", batchID, attempt, PublicationDispatched).
			Updates(map[string]any{
				"phase": PublicationVerified, "verified_at": now, "verified_container_id": containerID,
			}).Error; err != nil {
			return err
		}
		return tx.Model(&ServerUpdateBatch{}).
			Where("id = ? AND status = ? AND published_at IS NULL", batchID, UpdateBatchOpen).
			Updates(map[string]any{"published_at": now, "sealed_at": now}).Error
	})
}

// MarkUpdatePublicationCleaned records that recovery cleanup finished for a
// verified publication.
func (s *Store) MarkUpdatePublicationCleaned(ctx context.Context, batchID string) error {
	return s.db.WithContext(ctx).Model(&ServerUpdatePublication{}).
		Where("batch_id = ? AND phase = ?", batchID, PublicationVerified).
		Update("phase", PublicationCleaned).Error
}

// AbandonUpdatePublication releases a preparation attempt that never recorded a
// runtime dispatch. It refuses once dispatch intent exists, because the runtime
// outcome is unverified and only identity verification may settle it.
func (s *Store) AbandonUpdatePublication(ctx context.Context, batchID string, attempt int) error {
	res := s.db.WithContext(ctx).Model(&ServerUpdatePublication{}).
		Where("batch_id = ? AND attempt = ? AND phase IN ?", batchID, attempt,
			[]PublicationPhase{PublicationPreparing, PublicationPrepared, PublicationApplying}).
		Update("phase", PublicationAbandoned)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 0 {
		return nil
	}
	var record ServerUpdatePublication
	if err := s.db.WithContext(ctx).Where("batch_id = ?", batchID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: batch %s has no publication record", ErrPublicationTransition, batchID)
		}
		return err
	}
	if record.Attempt == attempt && record.Phase == PublicationAbandoned {
		return nil
	}
	return fmt.Errorf("%w: batch %s attempt %d is %s and cannot be abandoned", ErrPublicationTransition, batchID, record.Attempt, record.Phase)
}

// AbandonPreflightDispatch releases a dispatched attempt only after a typed
// Docker preflight proved that no container mutation began. AbandonUpdatePublication
// refuses the dispatched phase generically because an unverified or ambiguous
// dispatch must be settled by identity verification; this method exists only for
// that provable no-mutation case and is idempotent for an already abandoned
// attempt.
func (s *Store) AbandonPreflightDispatch(ctx context.Context, batchID string, attempt int) error {
	res := s.db.WithContext(ctx).Model(&ServerUpdatePublication{}).
		Where("batch_id = ? AND attempt = ? AND phase = ?", batchID, attempt, PublicationDispatched).
		Update("phase", PublicationAbandoned)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 0 {
		return nil
	}
	var record ServerUpdatePublication
	if err := s.db.WithContext(ctx).Where("batch_id = ?", batchID).First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: batch %s has no publication record", ErrPublicationTransition, batchID)
		}
		return err
	}
	if record.Attempt == attempt && record.Phase == PublicationAbandoned {
		return nil
	}
	return fmt.Errorf("%w: batch %s attempt %d is %s, not a preflight-refused dispatch", ErrPublicationTransition, batchID, record.Attempt, record.Phase)
}

func (s *Store) publishTransition(ctx context.Context, batchID string, attempt int, from []PublicationPhase, to PublicationPhase, fields map[string]any) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var record ServerUpdatePublication
		if err := tx.Where("batch_id = ?", batchID).First(&record).Error; err != nil {
			return err
		}
		if record.Attempt != attempt {
			return fmt.Errorf("%w: batch %s attempt %d is not %d", ErrPublicationStaleAttempt, batchID, record.Attempt, attempt)
		}
		for _, allowed := range from {
			if record.Phase == allowed {
				updates := map[string]any{}
				for key, value := range fields {
					updates[key] = value
				}
				if to != "" {
					updates["phase"] = to
				}
				return tx.Model(&ServerUpdatePublication{}).
					Where("batch_id = ? AND attempt = ? AND phase = ?", batchID, attempt, record.Phase).
					Updates(updates).Error
			}
		}
		// An identical repeat is idempotent; anything else is a refused
		// transition that must leave the recorded evidence intact.
		if to != "" && record.Phase == to {
			return nil
		}
		return fmt.Errorf("%w: batch %s is %s, not %v", ErrPublicationTransition, batchID, record.Phase, from)
	})
}

// ListBatchContentAll returns every content row for a batch, including applied
// rows. Recovery uses the ids to prove that each snapshot target is owned by
// this batch before mutating it.
func (s *Store) ListBatchContentAll(ctx context.Context, batchID string) ([]StagedContentChange, error) {
	var rows []StagedContentChange
	err := s.db.WithContext(ctx).
		Where("batch_id = ?", batchID).
		Order("created_at asc").
		Find(&rows).Error
	return rows, err
}
