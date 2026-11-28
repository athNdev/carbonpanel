// SPDX-License-Identifier: MPL-2.0
package serverruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/moby/moby/api/types/container"
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
		return errors.Join(err, persistRecovery(ctx, store, server, map[string]any{"status": server.Status}))
	}
	return nil
}

// PersistStarted records only the fields advanced by a successful start.
func PersistStarted(ctx context.Context, store recreationStore, server *db.Server) error {
	return persistFields(ctx, store, server, map[string]any{"status": server.Status, "last_started": server.LastStarted})
}

func persistFields(ctx context.Context, store recreationStore, server *db.Server, fields map[string]any) error {
	if writer, ok := store.(interface {
		UpdateServerFields(context.Context, string, map[string]any) error
	}); ok {
		return writer.UpdateServerFields(ctx, server.ID, fields)
	}
	return store.UpdateServer(ctx, server)
}

func persistRecovery(ctx context.Context, store recreationStore, server *db.Server, fields map[string]any) error {
	recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return persistFields(recoveryCtx, store, server, fields)
}

// containerResolver re-finds a server's canonical container by label or name.
type containerResolver interface {
	ResolveContainer(context.Context, string) (*container.Summary, error)
}

// ReconcileContainerIdentity makes the tracked container match the canonical
// live container before a retry may start or recreate it. It returns true when
// a different canonical container was adopted and persisted. Adoption repairs
// the tracked id only: it is never evidence that the batch was published. A
// caller must separately verify the identity captured at creation on the
// durable publication record. A missing or unresolvable container is an error,
// never permission to act on the stale tracked ID.
func ReconcileContainerIdentity(ctx context.Context, store recreationStore, client containerResolver, server *db.Server) (bool, error) {
	resolved, err := client.ResolveContainer(ctx, server.ID)
	if err != nil {
		if errors.Is(err, docker.ErrContainerNotResolved) {
			return false, fmt.Errorf("no canonical container for server %s; refusing to act on the stale tracked ID %q: %w", server.ID, server.ContainerID, err)
		}
		return false, fmt.Errorf("could not reconcile container identity for server %s: %w", server.ID, err)
	}
	if resolved == nil || resolved.ID == "" {
		return false, fmt.Errorf("container resolution for server %s returned no identity; refusing to act on the stale tracked ID %q", server.ID, server.ContainerID)
	}
	if resolved.ID == server.ContainerID {
		return false, nil
	}
	// Persist through the resolved identity, including the UpdateServer
	// fallback used by stores without a field-only writer. Restore the prior
	// value on failure so the caller never keeps a phantom id.
	previous := server.ContainerID
	server.ContainerID = resolved.ID
	if err := persistRecovery(ctx, store, server, map[string]any{"container_id": resolved.ID}); err != nil {
		server.ContainerID = previous
		return false, fmt.Errorf("resolved container %s for server %s but could not save it: %w", resolved.ID, server.ID, err)
	}
	return true, nil
}

// RecreationError distinguishes a failed daemon outcome from a successful
// replacement whose identity could not be saved. Staged callers must retain
// pending recovery markers in the latter case, rather than undo the live spec.
type RecreationError struct {
	Operation   error
	Persistence error
	// Preflight reports a typed Docker failure that ended before any container
	// mutation. Only then is it safe to compensate staged changes while the old
	// container (possibly still running) is untouched.
	Preflight bool
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
	fields := map[string]any{"container_id": server.ContainerID}
	if recreateErr != nil {
		fields["status"] = server.Status
	}
	persistErr := persistRecovery(ctx, store, server, fields)
	if persistErr != nil {
		persistErr = fmt.Errorf("failed to persist container recreation recovery state: %w", persistErr)
	}
	if recreateErr != nil || persistErr != nil {
		return result, &RecreationError{
			Operation:   recreateErr,
			Persistence: persistErr,
			Preflight:   recreateErr != nil && errors.Is(recreateErr, docker.ErrRecreatePreflight),
		}
	}
	return result, nil
}
