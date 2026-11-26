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
// Before runtime mutation a failure can be compensated immediately. Once a
// runtime request has been sent, compensation needs a confirmed stopped process.
type RestartPublication struct {
	Commit           *NextRestartCommit
	Configs          []*storage.StagedConfigChange
	RuntimeAttempted bool
	RuntimeAccepted  bool
}

func (p *RestartPublication) HasChanges() bool { return p.Commit.HasContent() || len(p.Configs) != 0 }

type restartConfigStore interface {
	RevertStagedConfigApplyToStaged(context.Context, string) (*storage.StagedConfigChange, error)
}

type RestartObserver interface {
	GetContainerStatus(context.Context, string) (storage.ServerStatus, error)
}

// Resolve returns additional finalization or recovery errors to the lifecycle
// caller. It releases exclusive ownership after all observations and repairs.
func (p *RestartPublication) Resolve(ctx context.Context, store restartConfigStore, observer RestartObserver, containerID string, cause error) error {
	defer p.Commit.Finish()
	if cause == nil {
		if p.Commit != nil {
			return p.Commit.complete()
		}
		return nil
	}
	if !p.HasChanges() || p.RuntimeAccepted {
		return nil
	}
	if p.RuntimeAttempted {
		// A daemon can still be processing a timed-out request even if one
		// immediate inspect reports a stopped container.
		var transportErr net.Error
		if ctx.Err() != nil || errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || errors.As(cause, &transportErr) || errors.Is(cause, io.EOF) || errors.Is(cause, io.ErrUnexpectedEOF) {
			return errors.New("staged changes retained because the runtime request outcome is uncertain")
		}
		if observer == nil || containerID == "" {
			return errors.New("staged changes retained because the container state is unknown")
		}
		observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		status, err := observer.GetContainerStatus(observeCtx, containerID)
		cancel()
		if err != nil {
			return fmt.Errorf("staged changes retained because container inspection failed: %w", err)
		}
		if status != storage.StatusStopped {
			return fmt.Errorf("staged changes retained because container status is %s", status)
		}
	}
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	var failed error
	for i := len(p.Configs) - 1; i >= 0; i-- {
		id := p.Configs[i].ID
		if _, err := store.RevertStagedConfigApplyToStaged(recoveryCtx, id); err != nil {
			failed = errors.Join(failed, fmt.Errorf("restage config %s: %w", id, err))
		}
	}
	return errors.Join(failed, p.Commit.Undo())
}
