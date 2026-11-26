// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

// errModuleRecoveryAmbiguous refuses to restore a recovery snapshot when a
// staged module may already have changed its database row or container. Unlike
// files, that mutation has no APPLIED marker before it happens.
var errModuleRecoveryAmbiguous = errors.New("staged recovery belongs to an attempt whose module runtime mutation may have begun; resolve the retained publication before retrying")

// publicationAttempt binds a staging operation to the durable, batch-bound
// publication record. Recovery reads the controller-owned snapshot from this
// record, never the mutable manifest inside the game data filesystem.
type publicationAttempt struct {
	store  *storage.Store
	record *storage.ServerUpdatePublication
}

func newPublicationAttempt(store *storage.Store, record *storage.ServerUpdatePublication) *publicationAttempt {
	if store == nil || record == nil {
		return nil
	}
	return &publicationAttempt{store: store, record: record}
}

func (p *publicationAttempt) phase() storage.PublicationPhase {
	if p == nil || p.record == nil {
		return ""
	}
	return p.record.Phase
}

func (p *publicationAttempt) attempt() int {
	if p == nil || p.record == nil {
		return 0
	}
	return p.record.Attempt
}

func (p *publicationAttempt) recordSnapshot(ctx context.Context, entries []undoEntry) error {
	body, err := json.Marshal(entries)
	if err != nil {
		return err
	}
	if err := p.store.RecordUpdatePublicationSnapshot(ctx, p.record.BatchID, p.record.Attempt, string(body)); err != nil {
		return err
	}
	p.record.Phase = storage.PublicationPrepared
	return nil
}

func (p *publicationAttempt) markApplying(ctx context.Context) error {
	if err := p.store.RecordUpdatePublicationApplying(ctx, p.record.BatchID, p.record.Attempt); err != nil {
		return err
	}
	p.record.Phase = storage.PublicationApplying
	return nil
}

func (p *publicationAttempt) recordModuleIntent(ctx context.Context, modules []string) error {
	if len(modules) == 0 {
		return nil
	}
	body, err := json.Marshal(modules)
	if err != nil {
		return err
	}
	return p.store.RecordUpdatePublicationModuleIntent(ctx, p.record.BatchID, p.record.Attempt, string(body))
}

func (p *publicationAttempt) beginNext(ctx context.Context) error {
	record, err := p.store.BeginUpdatePublication(ctx, p.record.BatchID, p.record.ServerID, p.record.BaselineContainerID)
	if err != nil {
		return err
	}
	p.record = record
	return nil
}

// preparePublicationRecovery decides what may be done with an existing recovery
// tree from the durable phase alone:
//   - preparing: no live write began; discard a partial tree and reuse the tree.
//   - prepared: no applying marker, so no live write began; discard and start
//     the next attempt.
//   - applying: restore the exact recorded snapshot, restage its applied rows,
//     then start the next attempt. Module intent makes the runtime ambiguous.
//   - dispatched/verified/cleaned: never reuse; resolve the runtime first.
func preparePublicationRecovery(ctx context.Context, acc nodestorage.Accessor, pub *publicationAttempt, batchID, recovery string) (bool, error) {
	_, statErr := acc.Stat(recovery)
	exists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) {
		return false, statErr
	}
	switch pub.phase() {
	case storage.PublicationPreparing:
		if exists {
			if err := acc.RemoveAll(recovery); err != nil {
				return false, err
			}
		}
		return false, nil
	case storage.PublicationPrepared:
		if pub.record.ModuleIntent != "" {
			return false, errModuleRecoveryAmbiguous
		}
		// The applying marker is durable before the first live write, so a
		// prepared record proves none began. A complete tree is safe to discard.
		if exists {
			if err := acc.RemoveAll(recovery); err != nil {
				return false, err
			}
		}
		return false, pub.beginNext(ctx)
	case storage.PublicationApplying:
		if pub.record.ModuleIntent != "" {
			return false, errModuleRecoveryAmbiguous
		}
		if !exists {
			return false, fmt.Errorf("batch %s recorded live writes but its recovery snapshot is missing; retry the lifecycle after inspecting the batch", batchID)
		}
		owned, err := pub.store.ListBatchContentAll(ctx, batchID)
		if err != nil {
			return false, err
		}
		if err := restoreRecordedRecovery(ctx, acc, pub.store, pub.record, recovery, owned); err != nil {
			return false, err
		}
		return true, pub.beginNext(ctx)
	case storage.PublicationDispatched, storage.PublicationVerified, storage.PublicationCleaned:
		return false, fmt.Errorf("batch %s has a recorded runtime dispatch (%s); resolve it before reusing recovery", batchID, pub.phase())
	default:
		return false, fmt.Errorf("batch %s has no durable publication phase", batchID)
	}
}

// restoreRecordedRecovery restores exactly the targets and backups recorded in
// the controller-owned snapshot. It validates ownership and every backup
// reference before mutating anything, restages the rows it restored, and only
// then removes the recovery tree.
func restoreRecordedRecovery(ctx context.Context, acc nodestorage.Accessor, store *storage.Store, record *storage.ServerUpdatePublication, recovery string, owned []storage.StagedContentChange) error {
	if record.Snapshot == "" {
		return fmt.Errorf("batch %s has no recorded recovery snapshot", record.BatchID)
	}
	var entries []undoEntry
	if err := json.Unmarshal([]byte(record.Snapshot), &entries); err != nil {
		return fmt.Errorf("batch %s has a corrupt recorded recovery snapshot: %w", record.BatchID, err)
	}
	byID := make(map[string]storage.StagedContentChange, len(owned))
	for _, row := range owned {
		byID[row.ID] = row
	}
	if len(entries) == 0 {
		return fmt.Errorf("batch %s recorded an empty recovery snapshot", record.BatchID)
	}
	for i, entry := range entries {
		owner, ok := byID[entry.RowID]
		if !ok {
			return fmt.Errorf("recovery entry %d names row %q which is not owned by batch %s", i, entry.RowID, record.BatchID)
		}
		if err := validateRecordedEntry(entry, owner, recovery, i); err != nil {
			return err
		}
	}
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !entry.Existed {
			if err := acc.Remove(entry.Target); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("recovery could not remove %q: %w", entry.Target, err)
			}
			continue
		}
		want := &undoEntry{Target: entry.Target, Size: entry.Size, SHA256: entry.SHA256}
		if _, err := copyStream(ctx, acc, entry.Backup, entry.Target, want, "", false, entry.Size); err != nil {
			return fmt.Errorf("recovery could not restore %q: %w", entry.Target, err)
		}
	}
	restaged := map[string]bool{}
	for _, entry := range entries {
		if restaged[entry.RowID] {
			continue
		}
		row := byID[entry.RowID]
		if row.Status != storage.StagedStatusApplied {
			continue
		}
		if err := store.MarkStagedContentStaged(ctx, row.ID); err != nil {
			return fmt.Errorf("recovery restored %q but could not restage row %s: %w", row.TargetPath, row.ID, err)
		}
		restaged[entry.RowID] = true
	}
	return acc.RemoveAll(recovery)
}

func validateRecordedEntry(entry undoEntry, owner storage.StagedContentChange, recovery string, index int) error {
	target := normalizeStagedTarget(entry.Target)
	if target == "" || target == "." || target == undoRoot || strings.HasPrefix(target, undoRoot+"/") {
		return fmt.Errorf("recovery entry %d has a reserved target %q", index, entry.Target)
	}
	if path.IsAbs(target) || target == ".." || strings.HasPrefix(target, "../") {
		return fmt.Errorf("recovery entry %d has a target outside the server data root: %q", index, entry.Target)
	}
	switch owner.Kind {
	case storage.StagedKindModpack:
		// A modpack owns paths derived from its staged tree: they may be any
		// safe relative path, but never a reserved panel tree.
		if _, err := modpackLivePath(target); err != nil {
			return fmt.Errorf("recovery entry %d is not an owned modpack target: %w", index, err)
		}
	default:
		if target != normalizeStagedTarget(owner.TargetPath) {
			return fmt.Errorf("recovery entry %d target %q is not the owned target %q", index, entry.Target, owner.TargetPath)
		}
	}
	if entry.Existed {
		want := path.Join(recovery, fmt.Sprintf("%06d.data", index))
		if entry.Backup != want {
			return fmt.Errorf("recovery entry %d backup %q is not the recorded backup %q", index, entry.Backup, want)
		}
		if entry.Size < 0 {
			return fmt.Errorf("recovery entry %d has a negative size", index)
		}
		return nil
	}
	if entry.Backup != "" {
		return fmt.Errorf("recovery entry %d references a backup for a target that did not exist", index)
	}
	return nil
}
