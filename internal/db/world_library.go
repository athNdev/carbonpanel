// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// World Library control-plane records. World bytes are never stored here;
// these rows only describe immutable revisions and explicit bindings.

// WorldEntry is the logical identity of a library world. It outlives bindings.
type WorldEntry struct {
	ID                string    `gorm:"primaryKey;size:64" json:"id"`
	Name              string    `gorm:"not null;size:255" json:"name"`
	Edition           string    `gorm:"size:32" json:"edition"`
	Family            string    `gorm:"size:64" json:"family"`
	CurrentRevisionID string    `gorm:"size:64" json:"current_revision_id"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

// WorldRevision is an immutable, content-addressed snapshot. No RPC writes
// into its stored bytes; an edit always produces a new revision.
type WorldRevision struct {
	ID                 string    `gorm:"primaryKey;size:96" json:"id"`
	WorldEntryID       string    `gorm:"index;not null;size:64" json:"world_entry_id"`
	SHA256             string    `gorm:"not null;size:64;uniqueIndex:idx_world_revision_sha256" json:"sha256"`
	SizeBytes          int64     `json:"size_bytes"`
	EntryCount         int32     `json:"entry_count"`
	MCVersion          string    `gorm:"size:64" json:"mc_version"`
	DataVersion        string    `gorm:"size:64" json:"data_version"`
	ModLoader          string    `gorm:"size:64" json:"mod_loader"`
	Provenance         string    `gorm:"type:text" json:"provenance"`
	LicenseAttestation string    `gorm:"type:text" json:"license_attestation"`
	CreatedBy          string    `gorm:"size:128" json:"created_by"`
	NodeID             string    `gorm:"size:128" json:"node_id"`
	StoragePath        string    `gorm:"size:512" json:"storage_path"`
	CreatedAt          time.Time `json:"created_at"`
}

// WorldBinding is the authoritative answer to which world is live at a server
// level. The unique index on revision_id enforces the no-two-writers rule.
type WorldBinding struct {
	ID          string    `gorm:"primaryKey;size:64" json:"id"`
	ServerID    string    `gorm:"not null;size:128;uniqueIndex:idx_world_binding_server_level" json:"server_id"`
	LevelName   string    `gorm:"not null;size:128;uniqueIndex:idx_world_binding_server_level" json:"level_name"`
	RevisionID  string    `gorm:"not null;size:96;uniqueIndex:idx_world_binding_revision" json:"revision_id"`
	NodeID      string    `gorm:"size:128" json:"node_id"`
	Generation  int64     `json:"generation"`
	Consistency string    `gorm:"size:64" json:"consistency"`
	BoundAt     time.Time `json:"bound_at"`
}

// MatchesOwner fails closed when the server moved to another node or
// generation since the binding was made.
func (b *WorldBinding) MatchesOwner(nodeID string, generation int64) bool {
	return b != nil && b.Generation > 0 && generation > 0 &&
		b.Generation == generation && b.NodeID == nodeID
}

// WorldImportJob records one durable, one-shot import attempt.
type WorldImportJob struct {
	ID                 string    `gorm:"primaryKey;size:64" json:"id"`
	RequestID          string    `gorm:"size:128;uniqueIndex:idx_world_import_request" json:"request_id"`
	WorldEntryID       string    `gorm:"size:64" json:"world_entry_id"`
	SourcePlatform     string    `gorm:"size:64" json:"source_platform"`
	SourceURL          string    `gorm:"size:2048" json:"source_url"`
	Operator           string    `gorm:"size:128" json:"operator"`
	LicenseAttestation string    `gorm:"type:text" json:"license_attestation"`
	Phase              string    `gorm:"size:32" json:"phase"`
	ResultRevisionID   string    `gorm:"size:96" json:"result_revision_id"`
	Error              string    `gorm:"type:text" json:"error"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// World provenance is stored as JSON text so the columns stay portable.
type WorldProvenance struct {
	Platform     string `json:"platform,omitempty"`
	ProjectID    string `json:"project_id,omitempty"`
	FileID       string `json:"file_id,omitempty"`
	URL          string `json:"url,omitempty"`
	Creator      string `json:"creator,omitempty"`
	License      string `json:"license,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
	DownloadedAt string `json:"downloaded_at,omitempty"`
	Operator     string `json:"operator,omitempty"`
}

// EncodeWorldProvenance serialises provenance for a revision row.
func EncodeWorldProvenance(p WorldProvenance) (string, error) {
	if p == (WorldProvenance{}) {
		return "", nil
	}
	b, err := json.Marshal(p)
	return string(b), err
}

// DecodeWorldProvenance parses a revision's provenance column.
func DecodeWorldProvenance(raw string) (WorldProvenance, error) {
	var p WorldProvenance
	if raw == "" {
		return p, nil
	}
	err := json.Unmarshal([]byte(raw), &p)
	return p, err
}

var (
	// ErrWorldRevisionImmutable is returned when an existing revision id is reused.
	ErrWorldRevisionImmutable = errors.New("world revision is immutable")
	// ErrWorldRevisionBound is returned when a second writable binding is requested.
	ErrWorldRevisionBound = errors.New("world revision already has a writable binding")
	// ErrWorldBindingStale is returned when the node or generation no longer matches.
	ErrWorldBindingStale = errors.New("world binding owner is stale")
)

// CreateWorldEntry stores a new logical library identity.
func (s *Store) CreateWorldEntry(ctx context.Context, entry *WorldEntry) error {
	return s.db.WithContext(ctx).Create(entry).Error
}

// GetWorldEntry returns one library identity.
func (s *Store) GetWorldEntry(ctx context.Context, id string) (*WorldEntry, error) {
	var entry WorldEntry
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&entry).Error; err != nil {
		return nil, err
	}
	return &entry, nil
}

// ListWorldEntries returns library identities, optionally filtered.
func (s *Store) ListWorldEntries(ctx context.Context, query, edition string, limit int) ([]WorldEntry, error) {
	q := s.db.WithContext(ctx).Model(&WorldEntry{})
	if query != "" {
		q = q.Where("name LIKE ?", "%"+query+"%")
	}
	if edition != "" {
		q = q.Where("edition = ?", edition)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var entries []WorldEntry
	err := q.Order("updated_at DESC").Find(&entries).Error
	return entries, err
}

// PutWorldRevision stores a new revision. Reusing an existing id is refused so
// revision bytes and metadata can never be mutated in place.
func (s *Store) PutWorldRevision(ctx context.Context, revision *WorldRevision) error {
	if revision == nil || revision.ID == "" || revision.SHA256 == "" {
		return errors.New("world revision requires an id and sha256")
	}
	var existing WorldRevision
	err := s.db.WithContext(ctx).Select("id").Where("id = ? OR sha256 = ?", revision.ID, revision.SHA256).First(&existing).Error
	if err == nil {
		return ErrWorldRevisionImmutable
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.db.WithContext(ctx).Create(revision).Error
}

// GetWorldRevision returns one immutable revision.
func (s *Store) GetWorldRevision(ctx context.Context, id string) (*WorldRevision, error) {
	var revision WorldRevision
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&revision).Error; err != nil {
		return nil, err
	}
	return &revision, nil
}

// ListWorldRevisions returns every revision of one library entry.
func (s *Store) ListWorldRevisions(ctx context.Context, entryID string) ([]WorldRevision, error) {
	var revisions []WorldRevision
	err := s.db.WithContext(ctx).Where("world_entry_id = ?", entryID).Order("created_at DESC").Find(&revisions).Error
	return revisions, err
}

// CreateWorldBinding stores a binding. A second writable binding to the same
// revision is refused, as is a stale server-level binding.
func (s *Store) CreateWorldBinding(ctx context.Context, binding *WorldBinding) error {
	if binding == nil || binding.ServerID == "" || binding.LevelName == "" || binding.RevisionID == "" {
		return errors.New("world binding requires a server, level and revision")
	}
	if binding.Generation <= 0 || binding.NodeID == "" {
		return fmt.Errorf("%w: node and generation are required", ErrWorldBindingStale)
	}
	var existing WorldBinding
	err := s.db.WithContext(ctx).Select("id").Where("revision_id = ?", binding.RevisionID).First(&existing).Error
	if err == nil {
		return ErrWorldRevisionBound
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return s.db.WithContext(ctx).Create(binding).Error
}

// GetWorldBinding returns the active binding for one server level.
func (s *Store) GetWorldBinding(ctx context.Context, serverID, level string) (*WorldBinding, error) {
	var binding WorldBinding
	if err := s.db.WithContext(ctx).Where("server_id = ? AND level_name = ?", serverID, level).First(&binding).Error; err != nil {
		return nil, err
	}
	return &binding, nil
}

// ListWorldBindings returns bindings filtered by server and/or revision.
func (s *Store) ListWorldBindings(ctx context.Context, serverID, revisionID string) ([]WorldBinding, error) {
	q := s.db.WithContext(ctx).Model(&WorldBinding{})
	if serverID != "" {
		q = q.Where("server_id = ?", serverID)
	}
	if revisionID != "" {
		q = q.Where("revision_id = ?", revisionID)
	}
	var bindings []WorldBinding
	err := q.Order("bound_at DESC").Find(&bindings).Error
	return bindings, err
}

// SwapWorldBinding atomically replaces a server level's binding with a new
// revision. The row is changed in one transaction so the unique indexes on
// (server_id, level_name) and revision_id are enforced without a window in
// which no binding exists. A previous revision that is bound elsewhere is
// refused with ErrWorldRevisionBound; a previous row that changed concurrently
// is refused with ErrWorldBindingStale.
func (s *Store) SwapWorldBinding(ctx context.Context, previous, next *WorldBinding) error {
	if next == nil || next.ServerID == "" || next.LevelName == "" || next.RevisionID == "" {
		return errors.New("world binding requires a server, level and revision")
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var other WorldBinding
		lookup := tx.Where("revision_id = ?", next.RevisionID)
		if previous != nil {
			lookup = lookup.Where("id <> ?", previous.ID)
		}
		if err := lookup.Select("id").First(&other).Error; err == nil {
			return ErrWorldRevisionBound
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if previous == nil {
			return tx.Create(next).Error
		}
		var current WorldBinding
		if err := tx.Where("id = ? AND server_id = ? AND level_name = ? AND revision_id = ?",
			previous.ID, previous.ServerID, previous.LevelName, previous.RevisionID).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrWorldBindingStale
			}
			return err
		}
		updates := map[string]any{
			"revision_id": next.RevisionID,
			"node_id":     next.NodeID,
			"generation":  next.Generation,
			"consistency": next.Consistency,
			"bound_at":    next.BoundAt,
		}
		result := tx.Model(&WorldBinding{}).Where("id = ?", previous.ID).Updates(updates)
		if result.Error != nil {
			if isUniqueViolation(result.Error) {
				return ErrWorldRevisionBound
			}
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrWorldBindingStale
		}
		next.ID = previous.ID
		return nil
	})
	return err
}

// DeleteWorldBinding removes a binding row. It never touches world bytes.
func (s *Store) DeleteWorldBinding(ctx context.Context, id string) error {
	return s.db.WithContext(ctx).Where("id = ?", id).Delete(&WorldBinding{}).Error
}

// CreateWorldImportJob stores a new import job.
func (s *Store) CreateWorldImportJob(ctx context.Context, job *WorldImportJob) error {
	return s.db.WithContext(ctx).Create(job).Error
}

// GetWorldImportJob returns one import job.
func (s *Store) GetWorldImportJob(ctx context.Context, id string) (*WorldImportJob, error) {
	var job WorldImportJob
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&job).Error; err != nil {
		return nil, err
	}
	return &job, nil
}

// ListWorldImportJobs returns recent import jobs, optionally for one entry.
func (s *Store) ListWorldImportJobs(ctx context.Context, entryID string, limit int) ([]WorldImportJob, error) {
	q := s.db.WithContext(ctx).Model(&WorldImportJob{})
	if entryID != "" {
		q = q.Where("world_entry_id = ?", entryID)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var jobs []WorldImportJob
	err := q.Order("created_at DESC").Find(&jobs).Error
	return jobs, err
}

// UpdateWorldImportJob persists import state after a bounded detached context
// has been supplied by the caller.
func (s *Store) UpdateWorldImportJob(ctx context.Context, job *WorldImportJob) error {
	return s.db.WithContext(ctx).Save(job).Error
}
