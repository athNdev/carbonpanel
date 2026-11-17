// SPDX-License-Identifier: MPL-2.0
package serverruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
)

type recreationStore interface {
	UpdateServer(context.Context, *db.Server) error
}

// ContainerRecreator preserves Docker's canonical identity, including rollback
// to the original container and partial replacement failures.
type ContainerRecreator interface {
	RecreateContainer(context.Context, string, *db.Server, *db.ServerConfig) (*docker.RecreateContainerResult, error)
}

type containerStarter interface {
	StartContainer(context.Context, string) error
}

// Start preserves a failed start outcome even when cancellation races the
// daemon call. Success status, LastStarted, journal kinds and events are owned
// by the caller after the daemon has accepted the start.
func Start(ctx context.Context, store recreationStore, client containerStarter, server *db.Server) error {
	if err := client.StartContainer(ctx, server.ContainerID); err != nil {
		server.Status = db.StatusError
		return errors.Join(err, persistRecovery(ctx, store, server))
	}
	return nil
}

func persistRecovery(ctx context.Context, store recreationStore, server *db.Server) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return store.UpdateServer(recoveryCtx, server)
}

// RecreationError distinguishes a failed daemon outcome from a successful
// replacement whose identity could not be saved. Staged callers must retain
// pending recovery markers in the latter case, rather than undo the live spec.
type RecreationError struct {
	Operation   error
	Persistence error
}

func (e *RecreationError) Error() string { return errors.Join(e.Operation, e.Persistence).Error() }
func (e *RecreationError) Unwrap() error { return errors.Join(e.Operation, e.Persistence) }

// Recreate records the canonical identity before callers can start a stopped
// replacement or finish their staged transaction. Failed recreation records an
// error status without advancing LastStarted. Callers retain success status,
// start intent, staged rollback, journals and events.
func Recreate(ctx context.Context, store recreationStore, client ContainerRecreator, server *db.Server, config *db.ServerConfig) (*docker.RecreateContainerResult, error) {
	result, recreateErr := client.RecreateContainer(ctx, server.ContainerID, server, config)
	if result != nil {
		server.ContainerID = result.NewContainerID
	} else {
		server.ContainerID = ""
	}
	if recreateErr == nil && server.ContainerID == "" {
		recreateErr = errors.New("container recreation returned no container ID")
	}
	if recreateErr != nil {
		server.Status = db.StatusError
	}
	// Docker may already have changed identity before cancellation arrived.
	// Recording that observed outcome must survive the initiating request, but
	// it must not hold the lifecycle owner indefinitely.
	persistErr := persistRecovery(ctx, store, server)
	if persistErr != nil {
		persistErr = fmt.Errorf("failed to persist container recreation recovery state: %w", persistErr)
	}
	if recreateErr != nil || persistErr != nil {
		return result, &RecreationError{Operation: recreateErr, Persistence: persistErr}
	}
	return result, nil
}
