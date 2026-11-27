// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
)

// RestartPublication tracks the staged changes made by one lifecycle call.
// Before a runtime mutation a failure can be compensated immediately. A known
// Docker preflight failure also proves no replacement was mutated. Any other
// request may have been accepted, so its staged files/config are retained for
// an explicit retry instead of being rolled back under a replacement that may
// already contain them.
type RestartPublication struct {
	Commit           *NextRestartCommit
	Configs          []*storage.StagedConfigChange
	RuntimeAttempted bool
	// RuntimeAccepted is positive evidence that the tracked runtime contains
	// this batch. It is not a synonym for "do not compensate": a replacement
	// whose ID could not be persisted must not be acknowledged here.
	RuntimeAccepted bool
	// RuntimeIdentityUnresolved records a replacement Docker created whose
	// canonical identity could not be saved. The runtime may contain the batch,
	// so nothing may be compensated, acknowledged or cleaned up until a retry
	// reconciles the tracked container identity. The durable identity-unresolved
	// marker authorizes that retry to adopt the canonical container this
	// dispatch created, then verify it like any other dispatch.
	RuntimeIdentityUnresolved bool
	// RuntimePreflight records a typed Docker failure that ended before any
	// container mutation. The old container may still be running; that alone is
	// not evidence about a failed replacement.
	RuntimePreflight bool
	// RuntimeInPlaceMayHaveMutated records an in-place runtime request (a
	// restart or unpause of the existing container) that may have exposed the
	// staged bytes. A later fallback's typed preflight cannot erase it: that
	// preflight proves only that the replacement was not mutated, not that the
	// in-place request was never accepted.
	RuntimeInPlaceMayHaveMutated bool
}

func (p *RestartPublication) HasChanges() bool {
	return p != nil && p.Commit != nil && (p.Commit.HasContent() || len(p.Configs) != 0)
}

// BeginDispatch durably records the exact applied identities and dispatch
// intent before the first runtime mutation of this lifecycle.
func (p *RestartPublication) BeginDispatch(ctx context.Context) error {
	if p == nil || p.Commit == nil || !p.HasChanges() {
		return nil
	}
	return p.Commit.BeginDispatch(ctx, p.Configs)
}

// replacementIdentityAttempts bounds the idempotent write of a created
// replacement's identity. Repeating the same identity cannot duplicate a
// mutation, so a transient database failure is retried; a permanent phase or
// attempt refusal is not.
const (
	replacementIdentityAttempts   = 3
	replacementIdentityRetryDelay = 50 * time.Millisecond
)

// RecordReplacement stores the identity Docker returned at creation. A retry
// may verify exactly this identity; it is never inferred from discovery. The
// write uses a bounded context detached from caller cancellation: once Docker
// has created the replacement, losing that identity to an abandoned request
// would leave the retry unable to verify the container that may already hold
// this batch. A transient failure is retried idempotently; if the identity
// still cannot be persisted, the record is explicitly marked
// identity-unresolved so a retry can adopt and verify the canonical container
// instead of refusing every attempt with no path.
func (p *RestartPublication) RecordReplacement(ctx context.Context, containerID string) error {
	if p == nil || p.Commit == nil {
		return nil
	}
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var recordErr error
	for attempt := 0; attempt < replacementIdentityAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-recordCtx.Done():
				recordErr = recordCtx.Err()
			case <-time.After(replacementIdentityRetryDelay):
				recordErr = p.Commit.RecordReplacement(recordCtx, containerID)
			}
		} else {
			recordErr = p.Commit.RecordReplacement(recordCtx, containerID)
		}
		if recordErr == nil {
			return nil
		}
		if errors.Is(recordErr, storage.ErrPublicationTransition) || errors.Is(recordErr, storage.ErrPublicationStaleAttempt) {
			// A permanent refusal means the record already holds different
			// evidence. Do not overwrite it with an unresolved marker: retain
			// the batch and let the recorded phase decide the retry.
			p.RuntimeIdentityUnresolved = true
			return fmt.Errorf("failed to record replacement identity %q: %w", containerID, recordErr)
		}
		if recordCtx.Err() != nil {
			break
		}
	}
	// Docker created a replacement and the exact identity is not durable. The
	// runtime may contain this batch, so nothing may be compensated or
	// acknowledged. Persist the explicit unresolved marker that lets a retry
	// adopt the created container; if even that write fails, escalate with the
	// batch id instead of leaving an unstated dead end.
	p.RuntimeIdentityUnresolved = true
	identityErr := fmt.Errorf("failed to record replacement identity %q: %w", containerID, recordErr)
	if markerErr := p.Commit.markIdentityUnresolved(ctx); markerErr != nil {
		return errors.Join(identityErr, markerErr)
	}
	return identityErr
}

type restartConfigStore interface {
	RevertStagedConfigApplyToStaged(context.Context, string) (*storage.StagedConfigChange, error)
}

// Resolve returns additional finalization or recovery errors to the lifecycle
// caller. It releases exclusive ownership after all observations and repairs.
// A runtime attempt that may have been accepted retains every row. A failure
// before this attempt mutated the runtime, or a typed preflight, compensates
// only the work this attempt did; rows that were already applied before the
// snapshot (and may be embedded in a prior replacement) stay applied. That
// evidence comes from the commit's durable rows, not the mutable server status.
func (p *RestartPublication) Resolve(ctx context.Context, store restartConfigStore, cause error) error {
	if p == nil || p.Commit == nil {
		return nil
	}
	defer p.Commit.Finish()
	if cause == nil {
		if p.Commit.NeedsIdentityVerification() && !p.RuntimeAccepted {
			// A durable dispatch exists but this caller did not verify its
			// exact replacement identity and did not observe a positive
			// in-process outcome. Success alone is not publication evidence.
			return errors.Join(p.Commit.sealRetained(ctx),
				errors.New("recorded runtime dispatch was not verified; retry the lifecycle operation to observe its replacement identity"))
		}
		// A successful lifecycle is the positive acknowledgement that the
		// runtime contains this batch's files and config. Persist that before
		// finalization so a later retry can finish cleanup without recreating
		// the container. The batch is sealed at the same time because new
		// staged work must not join a recovery tree that is being finalized.
		if p.HasChanges() || p.Commit.NeedsIdentityVerification() {
			if ackErr := p.Commit.markPublished(ctx); ackErr != nil {
				// Do not run cleanup or close the batch over an acknowledgement
				// that did not persist: the retry must resolve it first.
				return errors.Join(ackErr, p.Commit.sealRetained(ctx))
			}
		}
		if p.Commit.complete == nil {
			return nil
		}
		return p.Commit.complete()
	}
	if !p.HasChanges() {
		return nil
	}
	if p.RuntimeIdentityUnresolved {
		// Docker created a replacement but its canonical ID could not be saved.
		// The runtime may contain this batch, so retain and seal it; the
		// identity-unresolved record lets a retry adopt the created canonical
		// container and verify it before finalizing.
		return errors.Join(p.Commit.sealRetained(ctx),
			errors.New("container replacement succeeded but its canonical identity could not be saved; retry the lifecycle operation to adopt and verify the replacement before finalizing"))
	}
	if p.RuntimeAccepted {
		// The tracked runtime accepted the publication even though a later
		// lifecycle step failed. Retain it and record the acknowledgement so the
		// retry is finalize-only rather than another container mutation.
		if ackErr := p.Commit.markPublished(ctx); ackErr != nil {
			return errors.Join(ackErr, p.Commit.sealRetained(ctx))
		}
		return nil
	}
	if p.RuntimeAttempted && (!p.RuntimePreflight || p.RuntimeInPlaceMayHaveMutated) {
		// Seal admission before returning: the applied rows and their recovery
		// tree belong to this attempt, and a new staged row must not be appended
		// to the same batch before a retry resolves it.
		sealErr := p.Commit.sealRetained(ctx)
		if runtimeOutcomeUncertain(ctx, cause) {
			return errors.Join(sealErr, errors.New("staged changes retained because the runtime request outcome is uncertain; retry the lifecycle operation"))
		}
		// The replacement may have been created and may already contain the
		// staged files/config. A stopped inspect is not proof it is the old
		// container, so rollback would let that replacement outlive the rows.
		return errors.Join(sealErr, errors.New("staged changes retained because the replacement may contain them; retry the lifecycle operation"))
	}
	if p.Commit.NeedsIdentityVerification() && (!p.RuntimePreflight || p.RuntimeInPlaceMayHaveMutated) {
		// A durable dispatch intent exists but this attempt did not positively
		// observe its outcome. Never compensate or clean up over it; a retry
		// must verify the exact recorded replacement identity first.
		return errors.Join(p.Commit.sealRetained(ctx),
			errors.New("staged changes retained because a recorded runtime dispatch is unverified; retry the lifecycle operation to verify its outcome"))
	}
	// Seal before restoring anything. The caller still owns the data-write
	// lease; DB config staging does not take it, so the durable seal is what
	// prevents a new row from joining these rows mid-compensation.
	sealErr := p.Commit.sealRetained(ctx)
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	var failed error
	for i := len(p.Configs) - 1; i >= 0; i-- {
		id := p.Configs[i].ID
		if p.Commit.IsPriorConfig(id) {
			continue
		}
		if _, err := store.RevertStagedConfigApplyToStaged(recoveryCtx, id); err != nil {
			failed = errors.Join(failed, fmt.Errorf("restage config %s: %w", id, err))
		}
	}
	failed = errors.Join(failed, p.Commit.Undo())
	if p.Commit.HasPriorRuntimeState() {
		// Compensation restored only this attempt's rows. Durable applied rows
		// from an earlier attempt and their recovery tree remain; keep the batch
		// sealed so admission cannot add a new file against that tree. A
		// recorded dispatch is never abandoned: only identity verification may
		// settle its runtime outcome.
		if failed == nil && sealErr == nil {
			if err := p.Commit.abandonPreparation(recoveryCtx); err != nil {
				return errors.Join(sealErr, failed, err)
			}
		}
		return errors.Join(sealErr, failed)
	}
	if failed == nil && sealErr == nil {
		if p.Commit.NeedsIdentityVerification() {
			if !p.RuntimePreflight || p.RuntimeInPlaceMayHaveMutated {
				// The dispatch is unverified. Keep the batch sealed so
				// admission cannot append work to it.
				return errors.Join(sealErr, errors.New("recorded runtime dispatch retained; the batch stays sealed until its replacement identity is verified"))
			}
			// Every attempt ended in a typed preflight, which proves no
			// container mutation began. That is the only case in which a
			// dispatched record may be released without identity verification.
			if err := p.Commit.abandonPreflightDispatch(recoveryCtx); err != nil {
				return errors.Join(sealErr, err)
			}
		} else if err := p.Commit.abandonPreparation(recoveryCtx); err != nil {
			return errors.Join(sealErr, err)
		}
		failed = p.Commit.unsealRetained(ctx)
	}
	return errors.Join(sealErr, failed)
}

func runtimeOutcomeUncertain(ctx context.Context, cause error) bool {
	if ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) {
		return true
	}
	var transportErr net.Error
	return errors.As(cause, &transportErr)
}
