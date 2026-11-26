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
	RuntimeAccepted  bool
	// RuntimePreflight records a typed Docker failure that ended before any
	// container mutation. The old container may still be running; that alone is
	// not evidence about a failed replacement.
	RuntimePreflight bool
}

func (p *RestartPublication) HasChanges() bool { return p.Commit.HasContent() || len(p.Configs) != 0 }

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
	defer p.Commit.Finish()
	if cause == nil {
		if p.Commit != nil && p.Commit.complete != nil {
			return p.Commit.complete()
		}
		return nil
	}
	if !p.HasChanges() || p.RuntimeAccepted {
		return nil
	}
	if p.RuntimeAttempted && !p.RuntimePreflight {
		if runtimeOutcomeUncertain(ctx, cause) {
			return errors.New("staged changes retained because the runtime request outcome is uncertain; retry the lifecycle operation")
		}
		// The replacement may have been created and may already contain the
		// staged files/config. A stopped inspect is not proof it is the old
		// container, so rollback would let that replacement outlive the rows.
		return errors.New("staged changes retained because the replacement may contain them; retry the lifecycle operation")
	}
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
	return errors.Join(failed, p.Commit.Undo())
}

func runtimeOutcomeUncertain(ctx context.Context, cause error) bool {
	if ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) {
		return true
	}
	var transportErr net.Error
	return errors.As(cause, &transportErr)
}
