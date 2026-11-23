// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// Publisher performs the one container mutation for a due batch.
type Publisher func(ctx context.Context, server *storage.Server, plan Plan) error

type fileSnap struct {
	target       string
	existed      bool
	backup       string
	recoveryRoot string
	size         int64
	sha256       string
}

// Sweep updates idle clocks and publishes batches whose window has arrived.
// A lock held by another lifecycle mutation is left for the next sweep.
// firstResolver allows existing in-package callers to omit the resolver; a nil
// resolver still resolves local/shared storage and fails closed for remote
// transport-only nodes. Production callers always pass their owned resolver.
func firstResolver(resolvers []*nodestorage.Resolver) *nodestorage.Resolver {
	if len(resolvers) > 0 {
		return resolvers[0]
	}
	return nil
}

func Sweep(ctx context.Context, store *storage.Store, log *logger.Logger, now time.Time, players func(serverID string) (int, bool), publish Publisher, resolvers ...*nodestorage.Resolver) error {
	resolver := firstResolver(resolvers)
	if store == nil {
		return nil
	}
	batches, err := store.ListOpenUpdateBatches(ctx)
	if err != nil {
		return err
	}
	var failed error
	for i := range batches {
		batch := batches[i]
		sample := PlayerSample{}
		if players != nil {
			count, known := players(batch.ServerID)
			sample = PlayerSample{Known: known, Count: count}
		}
		decision := Decide(now, batch, sample)
		if decision.SetIdle {
			if err := store.SetBatchIdleSince(ctx, batch.ID, decision.IdleSince); err != nil {
				failed = errors.Join(failed, err)
				continue
			}
		}
		if !decision.Due {
			continue
		}
		server, err := store.GetServer(ctx, batch.ServerID)
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		err = ApplyBatch(ctx, store, log, server, batch, publish, resolver)
		if lockHeld(err) {
			if log != nil {
				log.Warn("Staged batch %s for server %s waits because a lifecycle lock is held", batch.ID, server.ID)
			}
			continue
		}
		if err != nil {
			if log != nil {
				log.Error("Failed to apply staged batch %s for server %s: %v", batch.ID, server.ID, err)
			}
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

// ApplyBatch publishes one due batch. Files and config are restored when the
// container mutation fails, and the batch stays open. A live config field that
// moved after staging stops the apply before anything is written.
func ApplyBatch(ctx context.Context, store *storage.Store, log *logger.Logger, server *storage.Server, batch storage.ServerUpdateBatch, publish Publisher, resolvers ...*nodestorage.Resolver) error {
	resolver := firstResolver(resolvers)
	return operation.RunJournaled(ctx, store, log, server, "apply-batch", "", func() error {
		if err := reconcileOpenBatchState(ctx, store, batch.ID); err != nil {
			return err
		}
		if err := store.StagedConfigConflict(ctx, server.ID, batch.ID); err != nil {
			return err
		}
		configs, err := store.ListBatchConfig(ctx, batch.ID)
		if err != nil {
			return err
		}
		content, err := store.ListBatchContent(ctx, batch.ID)
		if err != nil {
			return err
		}
		if err := stagedModuleConflict(ctx, store, server, content, resolver); err != nil {
			return err
		}
		var restore func() error
		if len(content) > 0 {
			acc, accErr := openAccessor(ctx, store, server, resolver)
			if accErr != nil {
				return accErr
			}
			if err := validateStagedModCandidates(ctx, store, server, acc, content); err != nil {
				_ = acc.Close()
				return err
			}
			snaps, commitErr := commitFiles(ctx, acc, content)
			_ = acc.Close()
			restore = func() error {
				recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
				defer cancel()
				again, openErr := openAccessor(recoveryCtx, store, server, resolver)
				if openErr != nil {
					return openErr
				}
				defer func() { _ = again.Close() }()
				return restoreFiles(recoveryCtx, again, snaps)
			}
			if commitErr != nil {
				if restore != nil {
					return errors.Join(commitErr, restore())
				}
				return commitErr
			}
		}
		applied := make([]*storage.StagedConfigChange, 0, len(configs))
		for _, cfg := range configs {
			rec, applyErr := store.ApplyStagedConfigChange(ctx, cfg.ID)
			if applyErr != nil {
				return errors.Join(applyErr, undoApply(ctx, store, restore, applied, nil))
			}
			applied = append(applied, rec)
		}
		moduleIDs, undoModules, modErr := applyStagedModules(ctx, store, server, content, resolver)
		if modErr != nil {
			return errors.Join(modErr, undoApply(ctx, store, restore, applied, nil))
		}
		plan := PlanFor(server.Status, len(applied) > 0)
		plan.ModuleIDs = moduleIDs
		// A retry after publication succeeded but finalization failed finds no
		// staged rows left to publish. PlanFor still describes a running server
		// as needing a restart, so gate it on work this attempt actually did:
		// otherwise the retry would restart (or recreate) the container again
		// and discard the original publication's plan.
		pendingWork := len(applied) > 0 || len(content) > 0 || len(moduleIDs) > 0
		if pendingWork && plan.NeedsPublish() {
			if publish == nil {
				return errors.Join(errors.New("staged batch needs a container publish"), undoApply(ctx, store, moduleRestore(undoModules, restore), applied, nil))
			}
			if pubErr := publish(ctx, server, plan); pubErr != nil {
				return errors.Join(pubErr, undoApply(ctx, store, moduleRestore(undoModules, restore), applied, nil))
			}
		}
		for i := range content {
			if err := store.MarkStagedContentApplied(ctx, content[i].ID); err != nil {
				return err
			}
		}
		if err := store.CloseUpdateBatch(ctx, batch.ID, storage.UpdateBatchApplied); err != nil {
			return err
		}
		return cleanupBatchRecovery(ctx, store, server, batch.ID, resolver)
	})
}

func reconcileOpenBatchState(ctx context.Context, store *storage.Store, batchID string) error {
	if store == nil || batchID == "" {
		return nil
	}
	return store.RestageOpenBatchRolledBackConfigs(ctx, batchID)
}

// CommitNextRestartFiles copies content for a next-restart batch onto the live
// tree. A batch waiting for a time or an idle window is left untouched, so an
// incidental restart does not publish it. recreateModule runs for a staged
// module whose container spec changed. The returned function puts the files
// and module fields back and returns the rows to staged.
func CommitNextRestartFiles(ctx context.Context, store *storage.Store, server *storage.Server, recreateModule func(context.Context, string) error, resolvers ...*nodestorage.Resolver) (func(), error) {
	resolver := firstResolver(resolvers)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	if err != nil || batch == nil || batch.Window != string(storage.UpdateWindowNextRestart) {
		return nil, err
	}
	content, err := store.ListBatchContent(ctx, batch.ID)
	if err != nil || len(content) == 0 {
		return nil, err
	}
	if err := stagedModuleConflict(ctx, store, server, content, resolver); err != nil {
		return nil, err
	}
	acc, err := openAccessor(ctx, store, server, resolver)
	if err != nil {
		return nil, err
	}
	if err := validateStagedModCandidates(ctx, store, server, acc, content); err != nil {
		_ = acc.Close()
		return nil, err
	}
	snaps, err := commitFiles(ctx, acc, content)
	_ = acc.Close()
	restoreFilesNow := func() error {
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		again, openErr := openAccessor(recoveryCtx, store, server, resolver)
		if openErr != nil {
			return openErr
		}
		defer func() { _ = again.Close() }()
		return restoreFiles(recoveryCtx, again, snaps)
	}
	if err != nil {
		return nil, errors.Join(err, restoreFilesNow())
	}
	moduleIDs, undoModules, err := applyStagedModules(ctx, store, server, content, resolver)
	if err != nil {
		return nil, errors.Join(err, restoreFilesNow())
	}
	for _, id := range moduleIDs {
		if recreateModule == nil {
			return nil, errors.Join(errors.New("staged module needs a container recreate"), moduleRestore(undoModules, restoreFilesNow)())
		}
		if err := recreateModule(ctx, id); err != nil {
			return nil, errors.Join(err, moduleRestore(undoModules, restoreFilesNow)())
		}
	}
	applied := make([]string, 0, len(content))
	for i := range content {
		if markErr := store.MarkStagedContentApplied(ctx, content[i].ID); markErr != nil {
			return nil, errors.Join(markErr, undoApply(ctx, store, moduleRestore(undoModules, restoreFilesNow), nil, applied))
		}
		applied = append(applied, content[i].ID)
	}
	return func() {
		_ = undoApply(ctx, store, moduleRestore(undoModules, restoreFilesNow), nil, applied)
	}, nil
}

// FinishNextRestartBatch closes a next-restart batch once nothing in it is
// still staged. Time and idle batches are left open.
func FinishNextRestartBatch(ctx context.Context, store *storage.Store, serverID string, resolvers ...*nodestorage.Resolver) error {
	resolver := firstResolver(resolvers)
	batch, err := store.OpenUpdateBatch(ctx, serverID)
	if err != nil || batch == nil || batch.Window != string(storage.UpdateWindowNextRestart) {
		return err
	}
	content, err := store.ListBatchContent(ctx, batch.ID)
	if err != nil {
		return err
	}
	configs, err := store.ListBatchConfig(ctx, batch.ID)
	if err != nil {
		return err
	}
	if len(content) > 0 || len(configs) > 0 {
		return nil
	}
	if err := store.CloseUpdateBatch(ctx, batch.ID, storage.UpdateBatchApplied); err != nil {
		return err
	}
	server, err := store.GetServer(ctx, serverID)
	if err != nil {
		return err
	}
	return cleanupBatchRecovery(ctx, store, server, batch.ID, resolver)
}

func moduleRestore(undoModules func() error, restore func() error) func() error {
	return func() error {
		var failed error
		if undoModules != nil {
			failed = undoModules()
		}
		if restore != nil {
			failed = errors.Join(failed, restore())
		}
		return failed
	}
}

func stagedModuleConflict(ctx context.Context, store *storage.Store, server *storage.Server, rows []storage.StagedContentChange, resolver *nodestorage.Resolver) error {
	var moduleRows []storage.StagedContentChange
	for _, row := range rows {
		if row.Kind == storage.StagedKindModule {
			moduleRows = append(moduleRows, row)
		}
	}
	if len(moduleRows) == 0 {
		return nil
	}
	acc, err := openAccessor(ctx, store, server, resolver)
	if err != nil {
		return err
	}
	defer func() { _ = acc.Close() }()
	for _, row := range moduleRows {
		payload, err := readModulePayload(acc, row.StagePath)
		if err != nil {
			return err
		}
		module, err := store.GetModule(ctx, payload.ModuleID)
		if err != nil {
			return err
		}
		if module.ServerID != server.ID {
			return fmt.Errorf("staged module %s is not on this server", payload.ModuleID)
		}
		if err := checkModuleBase(module, payload.Fields); err != nil {
			return err
		}
	}
	return nil
}

func undoApply(ctx context.Context, store *storage.Store, restore func() error, configs []*storage.StagedConfigChange, contentIDs []string) error {
	var failed error
	if restore != nil {
		failed = restore()
	}
	for _, cfg := range configs {
		if _, err := store.RevertStagedConfigApplyToStaged(ctx, cfg.ID); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	for _, id := range contentIDs {
		if err := store.MarkStagedContentStaged(ctx, id); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

func commitFiles(ctx context.Context, acc nodestorage.Accessor, rows []storage.StagedContentChange) ([]fileSnap, error) {
	return commitStreamingFiles(ctx, acc, rows)
}

func openAccessor(ctx context.Context, store *storage.Store, server *storage.Server, resolver *nodestorage.Resolver) (nodestorage.Accessor, error) {
	if nodestorage.IsLegacyLocalNodeID(server.NodeID) {
		return nodestorage.NewLocal(server.DataPath)
	}
	node, err := store.GetNode(ctx, server.NodeID)
	if err != nil {
		return nil, err
	}
	return resolver.ForNode(ctx, node.StorageInfo(), server.DataPath)
}

func lockHeld(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, operation.ErrInProgress) {
		return true
	}
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE")
}
