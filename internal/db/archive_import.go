// SPDX-License-Identifier: MPL-2.0

package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Archive import phases. A claimed intent has no destination yet. A staged
// intent owns a destination directory proven by the ownership marker. A
// committed intent has a server row and its data must never be reclaimed by
// recovery.
const (
	ArchiveImportClaimed   = "claimed"
	ArchiveImportStaged    = "staged"
	ArchiveImportCommitted = "committed"
)

// ErrArchiveImportUnproven reports an idempotency claim that has no durable
// archive intent. This is the pre-recovery format: the controller cannot prove
// which destination, if any, the interrupted attempt created.
var ErrArchiveImportUnproven = errors.New("interrupted server archive import has no durable destination record")

// ArchiveImport binds one server archive submission to the destination it was
// allowed to allocate. It exists before the server row so a crash can be
// distinguished from a directory that some other process created.
type ArchiveImport struct {
	RequestKey string `gorm:"primaryKey;size:128"`
	Kind       string `gorm:"not null"`
	ServerID   string `gorm:"not null"`
	NodeID     string
	DataPath   string `gorm:"not null"`
	// ClaimToken is the local ownership proof stored in the destination marker.
	ClaimToken string `gorm:"not null"`
	Phase      string `gorm:"not null"`
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ClaimArchiveImport records the idempotency key and the archive destination in
// one transaction, so a crash cannot leave a request claim without the durable
// record needed to recover its destination. A repeat of the same key returns
// the existing intent and claimed=false.
func (s *Store) ClaimArchiveImport(ctx context.Context, requestKey, kind, serverID, dataPath string) (*ArchiveImport, bool, error) {
	requestKey = strings.TrimSpace(requestKey)
	if requestKey == "" || kind == "" || serverID == "" || dataPath == "" {
		return nil, false, errors.New("archive import claim requires a request key, kind, server and destination")
	}
	if len(requestKey) > maxRequestIDLen {
		return nil, false, ErrRequestIDTooLong
	}

	var intent ArchiveImport
	claimed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		lookupErr := tx.Where("request_key = ?", requestKey).First(&intent).Error
		if lookupErr == nil {
			if intent.Kind != kind {
				return fmt.Errorf("%w: %s", ErrRequestIDReused, intent.Kind)
			}
			return nil
		}
		if !errors.Is(lookupErr, gorm.ErrRecordNotFound) {
			return lookupErr
		}

		row := IdempotencyKey{RequestID: requestKey, ServerID: serverID, Kind: kind}
		if err := tx.Create(&row).Error; err != nil {
			if !isUniqueViolation(err) {
				return err
			}
			// Another attempt won the claim. Its intent must be readable or the
			// submission cannot be recovered without guessing at ownership.
			if err := tx.Where("request_key = ?", requestKey).First(&intent).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrArchiveImportUnproven
				}
				return err
			}
			if intent.Kind != kind {
				return fmt.Errorf("%w: %s", ErrRequestIDReused, intent.Kind)
			}
			return nil
		}

		intent = ArchiveImport{
			RequestKey: requestKey,
			Kind:       kind,
			ServerID:   serverID,
			DataPath:   dataPath,
			ClaimToken: uuid.NewString(),
			Phase:      ArchiveImportClaimed,
		}
		claimed = true
		return tx.Create(&intent).Error
	})
	if err != nil {
		return nil, false, err
	}
	return &intent, claimed, nil
}

// GetArchiveImport returns the durable destination record for requestKey.
func (s *Store) GetArchiveImport(ctx context.Context, requestKey string) (*ArchiveImport, error) {
	requestKey = strings.TrimSpace(requestKey)
	var intent ArchiveImport
	err := s.db.WithContext(ctx).Where("request_key = ?", requestKey).First(&intent).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrArchiveImportUnproven
	}
	if err != nil {
		return nil, err
	}
	return &intent, nil
}

// SetArchiveImportNode records the node chosen for an already claimed intent.
func (s *Store) SetArchiveImportNode(ctx context.Context, requestKey, nodeID string) error {
	return s.db.WithContext(ctx).Model(&ArchiveImport{}).
		Where("request_key = ?", strings.TrimSpace(requestKey)).
		Updates(map[string]any{"node_id": nodeID, "updated_at": time.Now().UTC()}).Error
}

// SetArchiveImportPhase advances the durable phase without changing ownership.
func (s *Store) SetArchiveImportPhase(ctx context.Context, requestKey, phase string) error {
	return s.db.WithContext(ctx).Model(&ArchiveImport{}).
		Where("request_key = ?", strings.TrimSpace(requestKey)).
		Updates(map[string]any{"phase": phase, "updated_at": time.Now().UTC()}).Error
}

// DeleteArchiveImport forgets a settled intent. The idempotency key remains the
// durable record of a committed server.
func (s *Store) DeleteArchiveImport(ctx context.Context, requestKey string) error {
	return s.db.WithContext(ctx).Where("request_key = ?", strings.TrimSpace(requestKey)).
		Delete(&ArchiveImport{}).Error
}
