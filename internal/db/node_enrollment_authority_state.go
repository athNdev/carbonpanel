// SPDX-License-Identifier: MPL-2.0

package db

import (
	"context"
	"errors"
	"math"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const nodeEnrollmentAuthorityID = 1

type nodeEnrollmentAuthorityState struct {
	ID       int    `gorm:"primaryKey;autoIncrement:false"`
	Revision int64  `gorm:"not null"`
	Snapshot []byte `gorm:"not null"`
}

func (nodeEnrollmentAuthorityState) TableName() string {
	return "node_enrollment_authority_state"
}

// NodeEnrollmentStateStore uses the controller's migrated SQLite database.
type NodeEnrollmentStateStore struct {
	db *gorm.DB
}

func (s *Store) NodeEnrollmentStateStore() *NodeEnrollmentStateStore {
	return &NodeEnrollmentStateStore{db: s.db}
}

func (s *NodeEnrollmentStateStore) Load(ctx context.Context) (revision int64, snapshot []byte, err error) {
	var state nodeEnrollmentAuthorityState
	if err := s.db.WithContext(ctx).Take(&state, nodeEnrollmentAuthorityID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, nil, nil
		}
		return 0, nil, err
	}
	return state.Revision, state.Snapshot, nil
}

func (s *NodeEnrollmentStateStore) Commit(ctx context.Context, expectedRevision int64, snapshot []byte) (int64, error) {
	if expectedRevision < 0 || expectedRevision == math.MaxInt64 || len(snapshot) == 0 {
		return 0, errors.New("invalid node enrollment state commit")
	}
	next := expectedRevision + 1
	query := s.db.WithContext(ctx)
	var result *gorm.DB
	if expectedRevision == 0 {
		result = query.Clauses(clause.OnConflict{DoNothing: true}).Create(&nodeEnrollmentAuthorityState{
			ID: nodeEnrollmentAuthorityID, Revision: next, Snapshot: snapshot,
		})
	} else {
		result = query.Model(&nodeEnrollmentAuthorityState{}).
			Where("id = ? AND revision = ?", nodeEnrollmentAuthorityID, expectedRevision).
			UpdateColumns(map[string]any{"revision": next, "snapshot": snapshot})
	}
	if result.Error != nil {
		return 0, result.Error
	}
	if result.RowsAffected != 1 {
		return 0, errors.New("node enrollment state revision changed")
	}
	return next, nil
}
