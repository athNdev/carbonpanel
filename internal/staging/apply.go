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
		if current == nil || current.ID != batch.ID || current.Window != batch.Window ||
			!current.UpdatedAt.Equal(batch.UpdatedAt) || !sameApplyInstant(current.ApplyAt, batch.ApplyAt) || current.IdleMinutes != batch.IdleMinutes {
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

// sameApplyInstant compares the window's absolute deadline without requiring
// the same pointer identity.
func sameApplyInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
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
	store          *storage.Store
	publication    *publicationAttempt
	batchID        string
	undo           func() error
	complete       func() error
	seal           func(context.Context) error
	acknowledge    func(context.Context) error
	unseal         func(context.Context) error
	undoErr        error
	pendingContent bool
	published      bool
	appliedConfigs int
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
	if c.publication != nil {
		switch c.publication.phase() {
		case storage.PublicationDispatched, storage.PublicationVerified, storage.PublicationCleaned:
			// A row staged after the dispatch cannot be covered by that
			// runtime. The durable seal normally prevents it; legacy mixed data
			// is refused instead of applied silently.
			if hasStagedConfig(c.configs) {
				return nil, fmt.Errorf("batch %s has a retained publication; discard the newly staged config rows and retry", c.batchID)
			}
			applied := appliedConfigsOnly(c.configs)
			for _, cfg := range applied {
				if c.priorConfigs == nil {
					c.priorConfigs = make(map[string]bool)
				}
				c.priorConfigs[cfg.ID] = true
			}
			return applied, nil
		}
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
		c.appliedConfigs++
		applied = append(applied, rec)
	}
	return applied, nil
}

// AppliedConfigsThisAttempt reports how many snapshot config rows this attempt
// applied. Rows that were already applied before the snapshot are prior
// evidence of an earlier publication and do not count, so an acknowledged
// retry is finalize-only instead of rebuilding the container again.
func (c *NextRestartCommit) AppliedConfigsThisAttempt() int {
	if c == nil {
		return 0
	}
	return c.appliedConfigs
}

// NeedsIdentityVerification reports that a durable dispatch intent was recorded
// but its runtime outcome was not verified. The caller must observe the exact
// expected replacement identity before treating the batch as published; a
// different or equal canonical container only repairs the tracked ID and is
// never publication evidence.
func (c *NextRestartCommit) NeedsIdentityVerification() bool {
	if c == nil || c.publication == nil {
		return false
	}
	switch c.publication.phase() {
	case storage.PublicationDispatched, storage.PublicationIdentityUnresolved:
		return true
	default:
		return false
	}
}

// ExpectedReplacementID is the replacement identity captured at creation for
// the current attempt. It is empty when the dispatch did not create a new
// container or its identity could not be persisted.
func (c *NextRestartCommit) ExpectedReplacementID() string {
	if c == nil || c.publication == nil || c.publication.record == nil {
		return ""
	}
	return c.publication.record.ExpectedContainerID
}

// PublicationPhase reports the durable publication phase for diagnostics and
// conservative caller decisions. The zero value means no record exists.
func (c *NextRestartCommit) PublicationPhase() storage.PublicationPhase {
	if c == nil || c.publication == nil {
		return ""
	}
	return c.publication.phase()
}

// VerifyExpectedReplacement acknowledges the batch only when the canonical
// container is exactly the replacement identity captured at creation for this
// attempt. A replacement that rolled back to the baseline, an empty identity, or
// an unrelated/same canonical container is not evidence.
func (c *NextRestartCommit) VerifyExpectedReplacement(ctx context.Context, canonicalID string) (bool, error) {
	if !c.NeedsIdentityVerification() {
		return false, nil
	}
	baseline := ""
	if c.publication.record != nil {
		baseline = c.publication.record.BaselineContainerID
	}
	expected := c.ExpectedReplacementID()
	if expected == "" && c.publication.phase() == storage.PublicationIdentityUnresolved {
		// The identity write for a created replacement failed. A retry may
		// adopt the canonical container this dispatch created as the expected
		// identity, but only from that explicit durable marker and never the
		// baseline container (an in-place dispatch, which stays sealed).
		if canonicalID == "" || canonicalID == baseline {
			return false, nil
		}
		if err := c.store.AdoptUpdatePublicationIdentity(ctx, c.batchID, c.publication.attempt(), canonicalID); err != nil {
			return false, err
		}
		c.publication.record.Phase = storage.PublicationDispatched
		c.publication.record.ExpectedContainerID = canonicalID
		expected = canonicalID
	}
	if canonicalID == "" || expected == "" || expected == baseline || canonicalID != expected {
		return false, nil
	}
	if err := c.store.MarkUpdatePublicationVerified(ctx, c.batchID, c.publication.attempt(), canonicalID); err != nil {
		return false, err
	}
	c.publication.record.Phase = storage.PublicationVerified
	c.published = true
	return true, nil
}

// BeginDispatch durably records the exact applied row identities and the
// dispatch intent. Callers must invoke it before the first runtime mutation.
// It is a no-op for a batch with no publication record.
func (c *NextRestartCommit) BeginDispatch(ctx context.Context, appliedConfigs []*storage.StagedConfigChange) error {
	if c == nil || c.publication == nil || c.published {
		return nil
	}
	if c.publication.phase() == storage.PublicationDispatched {
		return nil
	}
	configIDs := make([]string, 0, len(appliedConfigs))
	for _, cfg := range appliedConfigs {
		if cfg != nil && cfg.ID != "" {
			configIDs = append(configIDs, cfg.ID)
		}
	}
	rows, err := c.store.ListBatchContentAll(ctx, c.batchID)
	if err != nil {
		return err
	}
	contentIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Status == storage.StagedStatusApplied {
			contentIDs = append(contentIDs, row.ID)
		}
	}
	configBody, err := json.Marshal(configIDs)
	if err != nil {
		return err
	}
	contentBody, err := json.Marshal(contentIDs)
	if err != nil {
		return err
	}
	if err := c.store.RecordUpdatePublicationDispatch(ctx, c.batchID, c.publication.attempt(), string(configBody), string(contentBody)); err != nil {
		return err
	}
	c.publication.record.Phase = storage.PublicationDispatched
	c.publication.record.AppliedConfigIDs = string(configBody)
	c.publication.record.AppliedContentIDs = string(contentBody)
	return nil
}

// RecordReplacement stores the replacement identity Docker returned at
// creation. The recorded value is what a retry may independently verify; it is
// never inferred from later container discovery.
func (c *NextRestartCommit) RecordReplacement(ctx context.Context, containerID string) error {
	if c == nil || c.publication == nil || containerID == "" || c.published {
		return nil
	}
	if c.publication.phase() != storage.PublicationDispatched {
		return nil
	}
	return c.store.RecordUpdatePublicationReplacement(ctx, c.batchID, c.publication.attempt(), containerID)
}

// markIdentityUnresolved durably records that this dispatch created a
// replacement whose exact identity could not be persisted. The marker is what
// authorizes a retry to adopt the created canonical container; without it the
// dispatch would stay at an empty expected identity and refuse every retry.
func (c *NextRestartCommit) markIdentityUnresolved(ctx context.Context) error {
	if c == nil || c.publication == nil || c.publication.phase() != storage.PublicationDispatched {
		return nil
	}
	markerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := c.store.MarkUpdatePublicationIdentityUnresolved(markerCtx, c.batchID, c.publication.attempt()); err != nil {
		return fmt.Errorf("batch %s could not record its unresolved replacement identity; inspect the runtime before retrying the lifecycle: %w", c.batchID, err)
	}
	c.publication.record.Phase = storage.PublicationIdentityUnresolved
	return nil
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
	write, err := acquireRecoveryLease(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	owned := false
	batchID := ""
	defer func() {
		if owned {
			return
		}
		// An interrupted attempt whose durable phase is APPLYING may have
		// written live files and owes a restore. Keep ordinary writes out until
		// the retry owns the path again; releasing here would let a save land
		// under the recorded snapshot and be erased by the restore.
		if publicationRecoveryPending(ctx, store, batchID) {
			retainRecoveryLease(server.ID, write)
			return
		}
		write.Release()
	}()
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	published := false
	if batch.WindowMatches(storage.UpdateWindowNextRestart, nil, 0) {
		batchID = batch.ID
		published = batch.PublishedAt != nil
		// Claim admission before reading the snapshot and before any live
		// write. DB config staging checks this same durable seal in its own
		// transaction, so it cannot join a recovery tree that this attempt is
		// about to write. A no-op below releases the claim.
		if !published {
			if _, err := store.ClaimUpdateBatchForPublication(ctx, batch.ID); err != nil {
				return nil, err
			}
		}
	}
	var content []storage.StagedContentChange
	if batchID != "" {
		content, err = store.ListBatchContent(ctx, batchID)
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
	var appliedContent int64
	if batchID != "" {
		// An earlier attempt may already have published some content rows. The
		// count is durable evidence independent of staged rows still waiting, of
		// server status, and of whether the attempt's start outcome was observed.
		appliedContent, err = store.CountBatchContentByStatus(ctx, batchID, storage.StagedStatusApplied)
		if err != nil {
			return nil, err
		}
	}
	if batchID == "" {
		if len(configs) == 0 {
			return nil, nil
		}
		// Legacy unbatched rows have no batch-bound publication record. They
		// keep the pre-existing lifecycle behavior and are never auto-restored.
		commit := &NextRestartCommit{release: write.Release, configs: configs}
		owned = true
		return commit, nil
	}

	publicationRecord, err := store.GetUpdatePublication(ctx, batchID)
	if err != nil {
		return nil, err
	}
	if publicationRecord != nil {
		switch publicationRecord.Phase {
		case storage.PublicationDispatched, storage.PublicationIdentityUnresolved, storage.PublicationVerified, storage.PublicationCleaned:
			if len(content) > 0 || hasStagedConfig(configs) {
				// A row joined a batch whose runtime outcome is already
				// recorded. It cannot be covered by that dispatch.
				return nil, fmt.Errorf("open batch %s has a retained publication; discard the newly staged rows and retry the lifecycle to resolve the retained publication", batchID)
			}
			if err := requirePublicationCoverage(ctx, store, publicationRecord, batchID, configs); err != nil {
				return nil, err
			}
			commit := &NextRestartCommit{
				store: store, release: write.Release, configs: appliedConfigsOnly(configs),
				published: publicationRecord.Phase != storage.PublicationDispatched,
				batchID:   batchID, publication: newPublicationAttempt(store, publicationRecord),
			}
			commit.pendingContent = appliedContent > 0
			commit.setBatchActions(ctx, store, server, batchID, resolver)
			owned = true
			return commit, nil
		}
	}
	if !published && (publicationRecord == nil || publicationRecord.Phase == storage.PublicationAbandoned) {
		// The first preparation attempt owns the batch before any live write.
		publicationRecord, err = store.BeginUpdatePublication(ctx, batchID, server.ID, server.ContainerID)
		if err != nil {
			return nil, err
		}
	}
	if appliedContent == 0 && len(content) == 0 && len(configs) == 0 &&
		(publicationRecord == nil || publicationRecord.Phase == storage.PublicationPreparing || publicationRecord.Phase == storage.PublicationAbandoned) {
		// Nothing to publish or resolve: do not leave a seal that would block
		// valid later admission until another lifecycle happens to run. This
		// also releases a seal left by an interrupted but write-free attempt.
		if err := store.ReleaseUpdateBatchSeal(ctx, batchID); err != nil {
			return nil, err
		}
		return nil, nil
	}
	commit := &NextRestartCommit{
		store: store, release: write.Release, configs: configs, published: published,
		batchID: batchID, publication: newPublicationAttempt(store, publicationRecord),
	}
	commit.pendingContent = appliedContent > 0
	if commit.publication != nil && commit.publication.phase() == storage.PublicationPreparing && len(content) == 0 {
		// A config-only attempt has no file snapshot. Record the empty
		// snapshot durably so the dispatch transition starts from a complete
		// prepared phase instead of the no-live-write phase.
		body, marshalErr := json.Marshal([]undoEntry{})
		if marshalErr != nil {
			return nil, marshalErr
		}
		if snapErr := store.RecordUpdatePublicationSnapshot(ctx, batchID, commit.publication.attempt(), string(body)); snapErr != nil {
			return nil, snapErr
		}
		commit.publication.record.Phase = storage.PublicationPrepared
	}
	commit.setBatchActions(ctx, store, server, batchID, resolver)
	if len(content) > 0 {
		if err := stagedModuleConflict(ctx, store, server, content, resolver); err != nil {
			return nil, err
		}
		if err := requireReviewedModRuntimeConsistency(ctx, store, server, content); err != nil {
			return nil, err
		}
		// The durable publication phase, not an APPLIED row alone, decides
		// whether a recovery tree may be restored. A prepared/applying snapshot
		// is validated against its controller-owned entries.
		commit.undo, err = commitNextRestartFilesHeld(ctx, store, server, content, recreateModule, commit.publication, resolver)
		if err != nil {
			if errors.Is(err, errModuleRecoveryAmbiguous) {
				// The durable record cannot prove the module runtime was
				// untouched. Seal rather than restore or clean up.
				_ = store.SealUpdateBatch(ctx, batchID)
			}
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

func (c *NextRestartCommit) setBatchActions(ctx context.Context, store *storage.Store, server *storage.Server, batchID string, resolver *nodestorage.Resolver) {
	c.complete = func() error {
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
		defer cancel()
		return finishNextRestartBatch(finishCtx, store, server, batchID, resolver, c.HasContent())
	}
	c.seal = func(sealCtx context.Context) error {
		return store.SealUpdateBatch(sealCtx, batchID)
	}
	c.acknowledge = func(ackCtx context.Context) error {
		return store.MarkUpdateBatchPublished(ackCtx, batchID)
	}
	c.unseal = func(unsealCtx context.Context) error {
		return store.ReleaseUpdateBatchSeal(unsealCtx, batchID)
	}
}

func hasStagedConfig(configs []*storage.StagedConfigChange) bool {
	for _, cfg := range configs {
		if cfg != nil && cfg.Status == storage.StagedStatusStaged {
			return true
		}
	}
	return false
}

func appliedConfigsOnly(configs []*storage.StagedConfigChange) []*storage.StagedConfigChange {
	out := make([]*storage.StagedConfigChange, 0, len(configs))
	for _, cfg := range configs {
		if cfg != nil && cfg.Status == storage.StagedStatusApplied {
			out = append(out, cfg)
		}
	}
	return out
}

func moduleRowIDs(rows []storage.StagedContentChange) []string {
	var ids []string
	for _, row := range rows {
		if row.Kind == storage.StagedKindModule {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

// requirePublicationCoverage refuses a resume when a batch-owned row was
// applied after the recorded dispatch. The older runtime is not evidence for
// that row, so treating it as covered would hide a real, unexpected effect.
func requirePublicationCoverage(ctx context.Context, store *storage.Store, record *storage.ServerUpdatePublication, batchID string, configs []*storage.StagedConfigChange) error {
	var coveredConfigs, coveredContent []string
	if record.AppliedConfigIDs != "" {
		if err := json.Unmarshal([]byte(record.AppliedConfigIDs), &coveredConfigs); err != nil {
			return fmt.Errorf("batch %s has a corrupt covered config identity: %w", batchID, err)
		}
	}
	if record.AppliedContentIDs != "" {
		if err := json.Unmarshal([]byte(record.AppliedContentIDs), &coveredContent); err != nil {
			return fmt.Errorf("batch %s has a corrupt covered content identity: %w", batchID, err)
		}
	}
	configSet := make(map[string]bool, len(coveredConfigs))
	for _, id := range coveredConfigs {
		configSet[id] = true
	}
	for _, cfg := range configs {
		if cfg == nil || cfg.BatchID != batchID || cfg.Status != storage.StagedStatusApplied {
			continue
		}
		if !configSet[cfg.ID] {
			return fmt.Errorf("batch %s has config row %s applied after its recorded publication; the older runtime does not cover it, so the lifecycle must be resolved by an operator", batchID, cfg.ID)
		}
	}
	contentSet := make(map[string]bool, len(coveredContent))
	for _, id := range coveredContent {
		contentSet[id] = true
	}
	rows, err := store.ListBatchContentAll(ctx, batchID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Status == storage.StagedStatusApplied && !contentSet[row.ID] {
			return fmt.Errorf("batch %s has content row %s applied after its recorded publication; the older runtime does not cover it, so the lifecycle must be resolved by an operator", batchID, row.ID)
		}
	}
	return nil
}

// NeedsRuntimePublication separates work that still owes a container publish
// from a publication an earlier lifecycle already accepted. Files copied by
// this attempt always need the publish. Retained applied rows need it only
// while the batch has no durable acknowledgement; after that, a retry owes
// cleanup alone and must not recreate the container again.
func (c *NextRestartCommit) NeedsRuntimePublication() bool {
	if c == nil {
		return false
	}
	if c.publication != nil {
		switch c.publication.phase() {
		case storage.PublicationDispatched, storage.PublicationIdentityUnresolved, storage.PublicationVerified, storage.PublicationCleaned:
			// A dispatch is resolved by identity verification; a verified
			// publication owes bookkeeping only.
			return false
		}
	}
	if c.undo != nil {
		return true
	}
	return c.pendingContent && !c.published
}

// Retain seals the batch and leaves every recorded phase and row in place. It
// is the caller's fail-closed response to drift or an unverifiable identity.
func (c *NextRestartCommit) Retain(ctx context.Context) error {
	return c.sealRetained(ctx)
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
// finalization. The publication phase and the batch acknowledgement are written
// in one transaction. If persisting them fails, the retry conservatively
// retains the dispatch rather than closing over an unproven one.
func (c *NextRestartCommit) markPublished(ctx context.Context) error {
	if c == nil || c.acknowledge == nil || c.published {
		return nil
	}
	ackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if c.publication != nil {
		containerID := c.publication.record.ExpectedContainerID
		if containerID == "" {
			containerID = c.publication.record.BaselineContainerID
		}
		if err := c.store.MarkUpdatePublicationVerified(ackCtx, c.batchID, c.publication.attempt(), containerID); err != nil {
			return err
		}
		c.publication.record.Phase = storage.PublicationVerified
		c.published = true
		return nil
	}
	if err := c.acknowledge(ackCtx); err != nil {
		return err
	}
	c.published = true
	return nil
}

// unsealRetained releases a claim only after a full compensation. It never
// clears an acknowledgement, so an accepted publication stays sealed.
func (c *NextRestartCommit) unsealRetained(ctx context.Context) error {
	if c == nil || c.unseal == nil || c.published {
		return nil
	}
	unsealCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return c.unseal(unsealCtx)
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

// abandonPreparation releases the durable record only when no runtime dispatch
// was recorded. A dispatched or identity-unresolved attempt is unverified and
// must be settled by identity verification, never erased by compensation.
func (c *NextRestartCommit) abandonPreparation(ctx context.Context) error {
	if c == nil || c.publication == nil {
		return nil
	}
	switch c.publication.phase() {
	case storage.PublicationPreparing, storage.PublicationPrepared, storage.PublicationApplying:
		return c.store.AbandonUpdatePublication(ctx, c.batchID, c.publication.attempt())
	default:
		return nil
	}
}

// abandonPreflightDispatch releases a dispatched attempt whose typed Docker
// preflight proved no container mutation began. Callers must only use it with
// that proof; an uncertain dispatch is retained and resolved by identity.
func (c *NextRestartCommit) abandonPreflightDispatch(ctx context.Context) error {
	if c == nil || c.publication == nil {
		return nil
	}
	if c.publication.phase() != storage.PublicationDispatched {
		return nil
	}
	return c.store.AbandonPreflightDispatch(ctx, c.batchID, c.publication.attempt())
}

// commitNextRestartFilesHeld publishes the already-selected next-restart rows.
// The caller owns the exclusive data-write gate through the returned undo.
// publication binds recovery to the durable batch snapshot. A batch without a
// durable snapshot is never restored from the mutable game-data manifest.
func commitNextRestartFilesHeld(ctx context.Context, store *storage.Store, server *storage.Server, content []storage.StagedContentChange, recreateModule func(context.Context, string) error, publication *publicationAttempt, resolver *nodestorage.Resolver) (func() error, error) {
	if publication != nil && publication.phase() == storage.PublicationApplying && publication.record.ModuleIntent != "" {
		return nil, errModuleRecoveryAmbiguous
	}
	acc, err := openAccessor(ctx, store, server, resolver)
	if err != nil {
		return nil, err
	}
	snaps, err := commitFilesForPublication(ctx, acc, content, publication)
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
		// The durable snapshot is the recovery evidence. Leave the tree in
		// place so the retry can restore it instead of destroying evidence.
		return nil, err
	}
	// Module database rows and containers may change before the content row is
	// marked APPLIED. Record that intent durably first so a resume can tell that
	// a runtime mutation may already have started.
	if publication != nil {
		if err := publication.recordModuleIntent(ctx, moduleRowIDs(content)); err != nil {
			// No module row or container has changed yet, and the durable
			// snapshot still describes the live tree. Leave it intact for the
			// retry to restore; do not remove the recovery evidence.
			return nil, err
		}
	}
	moduleIDs, undoModules, err := applyStagedModules(ctx, store, server, content, resolver)
	if err != nil {
		return nil, err
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
			// Leave the durable snapshot for the retry; removing it here would
			// hide which files this attempt already wrote.
			return nil, markErr
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
	if err := store.MarkUpdatePublicationCleaned(ctx, batchID); err != nil {
		return err
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

// requireReviewedModRuntimeConsistency refuses a batch whose pending config
// rows would change the Docker runtime target while a reviewed mod candidate is
// also staged, and refuses a candidate whose durable admission target no longer
// matches the effective target. The insert path rejects the mixed state, so
// only pre-existing data reaches the first check. The target revalidation runs
// before any live-tree mutation and preserves the staged and live bytes.
func requireReviewedModRuntimeConsistency(ctx context.Context, store *storage.Store, server *storage.Server, rows []storage.StagedContentChange) error {
	var reviewed []storage.StagedContentChange
	for _, row := range rows {
		if row.Kind != storage.StagedKindMod || row.ExpectedLiveSha512 == "" {
			continue
		}
		if err := store.RequireNoPendingReviewedModRuntimeChanges(ctx, server.ID, row.BatchID); err != nil {
			return err
		}
		reviewed = append(reviewed, row)
	}
	if len(reviewed) == 0 {
		return nil
	}
	loader, game, err := store.ReviewedModRuntimeTarget(ctx, server)
	if err != nil {
		if errors.Is(err, storage.ErrReviewedModRuntimeUnknown) {
			return fmt.Errorf("%w: %v; review and stage the mod update again", storage.ErrStagedModRuntimeTargetChanged, err)
		}
		return err
	}
	for _, row := range reviewed {
		if row.ReviewedRuntimeLoader == "" || row.ReviewedRuntimeVersion == "" {
			return fmt.Errorf("%w: staged reviewed mod %s has no durable runtime target; review and stage the update again",
				storage.ErrStagedModRuntimeTargetChanged, row.FileName)
		}
		if row.ReviewedRuntimeLoader != string(loader) || row.ReviewedRuntimeVersion != game {
			return fmt.Errorf("%w: staged reviewed mod %s was reviewed for %s/%s but the effective target is %s/%s; review and stage the update again",
				storage.ErrStagedModRuntimeTargetChanged, row.FileName, row.ReviewedRuntimeLoader, row.ReviewedRuntimeVersion, loader, game)
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
	return commitStreamingFiles(ctx, acc, rows, nil)
}

// commitFilesForPublication is the next-restart variant. Its recovery tree is
// governed by the durable batch publication snapshot instead of the mutable
// manifest in the game data filesystem.
func commitFilesForPublication(ctx context.Context, acc nodestorage.Accessor, rows []storage.StagedContentChange, publication *publicationAttempt) ([]fileSnap, error) {
	return commitStreamingFiles(ctx, acc, rows, publication)
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
	// A retained recovery lease keeps the exclusive data path owned until the
	// next-restart retry resolves its snapshot. That is contention, not a failed
	// publication, so the sweep waits for the next tick instead of reporting an
	// error on every run.
	if errors.Is(err, datawrite.ErrExclusiveBusy) {
		return true
	}
	return strings.Contains(strings.ToUpper(err.Error()), "UNIQUE")
}
