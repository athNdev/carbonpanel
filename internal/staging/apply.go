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
	"sync"
	"time"

	"github.com/athNdev/carbon-panel/internal/datawrite"
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
	mutated      bool
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
		// Hold the data path from before the undo snapshot through publication
		// and any rollback. Ordinary panel file mutations take the shared side
		// of this gate, so they cannot straddle snapshot and commit.
		write, err := datawrite.AcquireExclusive(ctx, server.ID)
		if err != nil {
			return err
		}
		defer write.Release()
		// Admission can retarget the batch after Sweep decides it is due but
		// before this journal owns it. Let the next sweep evaluate any change.
		current, err := store.OpenUpdateBatch(ctx, server.ID)
		if err != nil {
			return err
		}
		if current == nil || current.ID != batch.ID || current.Window != batch.Window || !current.UpdatedAt.Equal(batch.UpdatedAt) {
			return nil
		}
		if err := reconcileOpenBatchState(ctx, store, batch.ID); err != nil {
			return err
		}
		configs, err := store.ListBatchConfig(ctx, batch.ID)
		if err != nil {
			return err
		}
		dueConfigIDs := make([]string, 0, len(configs))
		for _, config := range configs {
			if config.ApplyMode != storage.StagedApplyScheduled {
				dueConfigIDs = append(dueConfigIDs, config.ID)
			}
		}
		if err := store.StagedConfigConflictForIDs(ctx, server.ID, dueConfigIDs); err != nil {
			return err
		}
		content, err := store.ListBatchContent(ctx, batch.ID)
		if err != nil {
			return err
		}
		// Applied rows survive a failed cleanup. They are the durable evidence
		// that this batch already created file recovery, so a retry owes the
		// idempotent cleanup even though no staged row remains to republish.
		appliedContent, err := store.CountBatchContentByStatus(ctx, batch.ID, storage.StagedStatusApplied)
		if err != nil {
			return err
		}
		hadContent := len(content) > 0 || appliedContent > 0
		if err := stagedModuleConflict(ctx, store, server, content, resolver); err != nil {
			return err
		}
		if err := requireReviewedModRuntimeConsistency(ctx, store, server, content); err != nil {
			return err
		}
		var restore func() error
		if len(content) > 0 {
			acc, accErr := openAccessor(ctx, store, server, resolver)
			if accErr != nil {
				return accErr
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
		// Independently scheduled (cron) config rows are not part of this
		// timed batch publication. Either entry point refuses to create this
		// mixed state, but legacy data may already contain it: never run a
		// cron row early just because a mod update's window came due.
		dueConfigs := make([]*storage.StagedConfigChange, 0, len(configs))
		var scheduledConfigs []*storage.StagedConfigChange
		for _, cfg := range configs {
			if cfg.ApplyMode == storage.StagedApplyScheduled {
				scheduledConfigs = append(scheduledConfigs, cfg)
			} else {
				dueConfigs = append(dueConfigs, cfg)
			}
		}
		applied := make([]*storage.StagedConfigChange, 0, len(dueConfigs))
		for _, cfg := range dueConfigs {
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
			// Known limit: RunJournaled records this publisher error as definite.
			// A transport-ambiguous Docker outcome can therefore reach undoApply
			// after the daemon accepted the mutation. See
			// knowledge/failures/staged-publication-timed-outcome.md; automatic
			// compensation is claimed only for the typed next-restart paths.
			if pubErr := publish(ctx, server, plan); pubErr != nil {
				return errors.Join(pubErr, undoApply(ctx, store, moduleRestore(undoModules, restore), applied, nil))
			}
		}
		for i := range content {
			if err := store.MarkStagedContentApplied(ctx, content[i].ID); err != nil {
				return err
			}
		}
		if len(scheduledConfigs) > 0 {
			// Keep the cron rows and their batch for their own trigger. Clean up
			// the applied timed content before parking the batch: next_restart is
			// not a retry trigger, so a failed cleanup would otherwise forfeit the
			// due window that brought us here.
			if hadContent {
				if err := cleanupBatchRecovery(ctx, store, server, batch.ID, resolver); err != nil {
					return err
				}
			}
			if _, err := store.SetOpenBatchWindow(ctx, server.ID, storage.UpdateWindowNextRestart, nil, 0); err != nil {
				return err
			}
			return nil
		}
		// A config-only batch has no undo tree to remove. Do not open file
		// storage for it, and clean up before closing so a cleanup failure is
		// retryable instead of hiding behind an already-applied batch.
		if hadContent {
			if err := cleanupBatchRecovery(ctx, store, server, batch.ID, resolver); err != nil {
				return err
			}
		}
		return store.CloseUpdateBatch(ctx, batch.ID, storage.UpdateBatchApplied)
	})
}

func reconcileOpenBatchState(ctx context.Context, store *storage.Store, batchID string) error {
	if store == nil || batchID == "" {
		return nil
	}
	return store.RestageOpenBatchRolledBackConfigs(ctx, batchID)
}

// NextRestartCommit owns the server data path after files for a next-restart
// batch have been published. The start/restart call site is not itself
// journaled, so the caller must keep this handle until the container operation
// resolves: Undo restores the files and releases the gate, Finish keeps the
// published bytes and releases it. Both methods are idempotent.
type NextRestartCommit struct {
	mu             sync.Mutex
	undo           func() error
	complete       func() error
	seal           func(context.Context) error
	acknowledge    func(context.Context) error
	undoErr        error
	pendingContent bool
	published      bool
	configs        []*storage.StagedConfigChange
	priorConfigs   map[string]bool
	release        func()
	undone         bool
	released       bool
}

// configApplier is the config subset a next-restart commit needs to publish its
// own snapshot. It is an interface so lifecycle tests can use a fake store.
type configApplier interface {
	ApplyStagedConfigChange(context.Context, string) (*storage.StagedConfigChange, error)
}

// ApplyConfigs applies only the config rows selected in this commit's snapshot.
// Rows already applied after the last start are returned for rollback on a safe
// failure but are not applied again.
func (c *NextRestartCommit) ApplyConfigs(ctx context.Context, store configApplier) ([]*storage.StagedConfigChange, error) {
	if c == nil {
		return nil, nil
	}
	applied := make([]*storage.StagedConfigChange, 0, len(c.configs))
	for _, cfg := range c.configs {
		if cfg.Status == storage.StagedStatusApplied {
			applied = append(applied, cfg)
			if c.priorConfigs == nil {
				c.priorConfigs = make(map[string]bool)
			}
			c.priorConfigs[cfg.ID] = true
			continue
		}
		rec, err := store.ApplyStagedConfigChange(ctx, cfg.ID)
		if err != nil {
			return applied, err
		}
		applied = append(applied, rec)
	}
	return applied, nil
}

// IsPriorConfig reports whether a snapshot config row was already applied
// before this commit. Such a row may be embedded in a replacement from an
// earlier attempt, so a failure in this attempt must not revert it.
func (c *NextRestartCommit) IsPriorConfig(id string) bool {
	if c == nil || id == "" {
		return false
	}
	return c.priorConfigs[id]
}

// ConfigSnapshot reports how many config rows were selected for this restart.
func (c *NextRestartCommit) ConfigSnapshot() int {
	if c == nil {
		return 0
	}
	return len(c.configs)
}

// Undo restores published files, module fields and staged rows, then releases
// the data-write gate. It is a no-op after Finish.
func (c *NextRestartCommit) Undo() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.undone || c.released {
		return c.undoErr
	}
	c.undone = true
	if c.undo != nil {
		c.undoErr = c.undo()
	}
	c.released = true
	if c.release != nil {
		c.release()
	}
	return c.undoErr
}

// Finish keeps the published files and releases the data-write gate. It does
// not undo; a caller that needs rollback must call Undo before Finish. Calling
// Undo after Finish is deliberately a no-op so success cannot be reversed
// without fresh ownership.
func (c *NextRestartCommit) Finish() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return
	}
	c.released = true
	if c.release != nil {
		c.release()
	}
}

// CommitNextRestartFiles selects and copies the content and config of one
// next-restart batch onto the live tree. A batch waiting for a time or an idle
// window is left untouched, so an incidental restart does not publish it;
// legacy unbatched on-restart rows are still included. recreateModule runs for
// a staged module whose container spec changed. The returned handle owns the
// exclusive data-write gate, and holds the selection of both content and config
// rows, until the caller calls Undo or Finish. Config rows are applied through
// ApplyConfigs so a row staged after this selection cannot enter the restart.
func CommitNextRestartFiles(ctx context.Context, store *storage.Store, server *storage.Server, recreateModule func(context.Context, string) error, resolvers ...*nodestorage.Resolver) (*NextRestartCommit, error) {
	resolver := firstResolver(resolvers)
	write, err := datawrite.AcquireExclusive(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	owned := false
	defer func() {
		if !owned {
			write.Release()
		}
	}()
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	batchID := ""
	published := false
	var content []storage.StagedContentChange
	if batch != nil && batch.Window == string(storage.UpdateWindowNextRestart) {
		batchID = batch.ID
		published = batch.PublishedAt != nil
		content, err = store.ListBatchContent(ctx, batch.ID)
		if err != nil {
			return nil, err
		}
	}
	// Select configs under the same lease as the content, from this batch plus
	// legacy rows that predate batches. Another window's on-restart row is not
	// part of this restart.
	configs, err := store.ListNextRestartConfigSnapshot(ctx, server.ID, batchID, server.LastStarted)
	if err != nil {
		return nil, err
	}
	if batchID == "" && len(configs) == 0 {
		return nil, nil
	}
	commit := &NextRestartCommit{release: write.Release, configs: configs, published: published}
	if batchID != "" {
		batchForFinish := batchID
		commit.complete = func() error {
			finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
			defer cancel()
			return finishNextRestartBatch(finishCtx, store, server, batchForFinish, resolver, commit.HasContent())
		}
		commit.seal = func(sealCtx context.Context) error {
			return store.SealUpdateBatch(sealCtx, batchForFinish)
		}
		commit.acknowledge = func(ackCtx context.Context) error {
			return store.MarkUpdateBatchPublished(ackCtx, batchForFinish)
		}
		// An earlier attempt may already have published some content rows. The
		// count is durable evidence independent of staged rows still waiting, of
		// server status, and of whether the attempt's start outcome was observed.
		count, err := store.CountBatchContentByStatus(ctx, batchID, storage.StagedStatusApplied)
		if err != nil {
			return nil, err
		}
		commit.pendingContent = count > 0
	}
	if len(content) > 0 {
		if err := stagedModuleConflict(ctx, store, server, content, resolver); err != nil {
			return nil, err
		}
		if err := requireReviewedModRuntimeConsistency(ctx, store, server, content); err != nil {
			return nil, err
		}
		commit.undo, err = commitNextRestartFilesHeld(ctx, store, server, content, recreateModule, resolver)
		if err != nil {
			return nil, err
		}
	}
	owned = true
	return commit, nil
}

// HasContent reports whether this batch has content work: files copied by this
// attempt or applied rows retained from an earlier one. It is also the durable
// evidence that recovery cleanup may be owed.
func (c *NextRestartCommit) HasContent() bool { return c != nil && (c.undo != nil || c.pendingContent) }

// NeedsRuntimePublication separates work that still owes a container publish
// from a publication an earlier lifecycle already accepted. Files copied by
// this attempt always need the publish. Retained applied rows need it only
// while the batch has no durable acknowledgement; after that, a retry owes
// cleanup alone and must not recreate the container again.
func (c *NextRestartCommit) NeedsRuntimePublication() bool {
	if c == nil {
		return false
	}
	if c.undo != nil {
		return true
	}
	return c.pendingContent && !c.published
}

// sealRetained makes the durable applied rows, and their recovery tree,
// unreachable to new admissions. A later lifecycle must resolve them first.
func (c *NextRestartCommit) sealRetained(ctx context.Context) error {
	if c == nil || c.seal == nil {
		return nil
	}
	sealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return c.seal(sealCtx)
}

// markPublished records a positively acknowledged runtime publication before
// finalization. If persisting it fails, the retry conservatively repeats the
// publication rather than closing over an unproven one.
func (c *NextRestartCommit) markPublished(ctx context.Context) error {
	if c == nil || c.acknowledge == nil || c.published {
		return nil
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.acknowledge(ackCtx); err != nil {
		return err
	}
	c.published = true
	return nil
}

// HasPriorRuntimeState reports that an earlier attempt already applied this
// snapshot's content or config. The evidence is durable persisted rows, not the
// mutable server status. Such a row may be embedded in a replacement container,
// so compensation must leave it applied; only work this attempt applied is
// reverted. Resolve uses the per-row evidence directly.
func (c *NextRestartCommit) HasPriorRuntimeState() bool {
	if c == nil {
		return false
	}
	if c.pendingContent {
		return true
	}
	for _, cfg := range c.configs {
		if cfg.Status == storage.StagedStatusApplied {
			return true
		}
	}
	return false
}

// commitNextRestartFilesHeld publishes the already-selected next-restart rows.
// The caller owns the exclusive data-write gate through the returned undo.
func commitNextRestartFilesHeld(ctx context.Context, store *storage.Store, server *storage.Server, content []storage.StagedContentChange, recreateModule func(context.Context, string) error, resolver *nodestorage.Resolver) (func() error, error) {
	acc, err := openAccessor(ctx, store, server, resolver)
	if err != nil {
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
	return func() error {
		return undoApply(ctx, store, moduleRestore(undoModules, restoreFilesNow), nil, applied)
	}, nil
}

// The commit owns the server data gate and the original batch identity.
func finishNextRestartBatch(ctx context.Context, store *storage.Store, server *storage.Server, batchID string, resolver *nodestorage.Resolver, hadContent bool) error {
	content, err := store.ListBatchContent(ctx, batchID)
	if err != nil {
		return err
	}
	configs, err := store.ListBatchConfig(ctx, batchID)
	if err != nil {
		return err
	}
	if len(content) > 0 || len(configs) > 0 {
		return nil
	}
	// A config-only batch never created recovery storage. Opening a node
	// accessor here would make a successful finalize depend on file storage (and
	// hold node admission behind that IO). Cleanup runs before the close so a
	// real cleanup failure leaves the batch open and retryable.
	if hadContent {
		if err := cleanupBatchRecovery(ctx, store, server, batchID, resolver); err != nil {
			return err
		}
	}
	return store.CloseUpdateBatch(ctx, batchID, storage.UpdateBatchApplied)
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

// requireReviewedModRuntimeConsistency refuses a legacy mixed batch whose
// pending config rows would change the Docker runtime target while a reviewed
// mod candidate is also staged in that batch. The insert path rejects this
// state, so only pre-existing data reaches the check.
func requireReviewedModRuntimeConsistency(ctx context.Context, store *storage.Store, server *storage.Server, rows []storage.StagedContentChange) error {
	for _, row := range rows {
		if row.Kind == storage.StagedKindMod && row.ExpectedLiveSha512 != "" {
			return store.RequireNoPendingReviewedModRuntimeChanges(ctx, server.ID, row.BatchID)
		}
	}
	return nil
}

func undoApply(ctx context.Context, store *storage.Store, restore func() error, configs []*storage.StagedConfigChange, contentIDs []string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	var failed error
	if restore != nil {
		failed = restore()
	}
	for i := len(configs) - 1; i >= 0; i-- {
		if _, err := store.RevertStagedConfigApplyToStaged(ctx, configs[i].ID); err != nil {
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
