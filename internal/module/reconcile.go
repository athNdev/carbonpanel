// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package module

import (
	"context"
	"errors"
	"fmt"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
)

// ReconcileOutcome says what a module convergence pass did. It is returned
// rather than only logged so the reconciler and its tests can assert on it.
type ReconcileOutcome string

const (
	// ReconcileInSync means the recorded module state already matched the
	// container.
	ReconcileInSync ReconcileOutcome = "in_sync"
	// ReconcileAdopted means the container id or the status was corrected from
	// what the daemon reported.
	ReconcileAdopted ReconcileOutcome = "adopted"
	// ReconcileRecreated means a missing container was recreated for a running
	// module whose parent server is running.
	ReconcileRecreated ReconcileOutcome = "recreated"
	// ReconcileStopped means the container is gone and the module is recorded
	// as stopped instead of claiming it runs.
	ReconcileStopped ReconcileOutcome = "stopped"
	// ReconcileSkipped means there was nothing safe to do: the module is gone,
	// its node cannot be reached, or its parent is not running.
	ReconcileSkipped ReconcileOutcome = "skipped"
)

// moduleAction is the decision a pass makes before it touches Docker.
type moduleAction string

const (
	actionNone          moduleAction = "none"
	actionAdopt         moduleAction = "adopt"
	actionRecreate      moduleAction = "recreate"
	actionMarkGone      moduleAction = "mark_gone"
	actionWaitForParent moduleAction = "wait_for_parent"
)

// ModuleObservation is what the daemon reported about a module's container.
type ModuleObservation struct {
	// Exists is false when no container could be resolved for the module.
	Exists bool
	// ContainerID is the daemon's id for the container, which may differ from
	// the recorded one after an external recreate.
	ContainerID string
	// Running is true when the container's state is running.
	Running bool
}

// decideModule is the pure part of a convergence pass: given the record, the
// parent's stored status and the observed container, it says what to do.
// Keeping this separate is what makes every branch testable without a daemon.
func decideModule(mod *storage.Module, parent *storage.Server, obs ModuleObservation) moduleAction {
	if mod == nil {
		return actionNone
	}
	if !obs.Exists {
		// A module that is not supposed to be running is simply recorded as
		// stopped. There is nothing to resurrect.
		if mod.Status != storage.ModuleStatusRunning {
			if mod.ContainerID != "" {
				return actionMarkGone
			}
			return actionNone
		}
		// A sidecar is never resurrected on its own: the parent server must be
		// up, otherwise the module would start against a world nobody is
		// serving.
		if parent == nil || parent.Status != storage.StatusRunning {
			return actionMarkGone
		}
		return actionRecreate
	}

	if obs.ContainerID != "" && obs.ContainerID != mod.ContainerID {
		return actionAdopt
	}
	if obs.Running && mod.Status != storage.ModuleStatusRunning {
		return actionAdopt
	}
	if !obs.Running && mod.Status == storage.ModuleStatusRunning {
		return actionAdopt
	}
	return actionNone
}

// ReconcileModule converges one module record with the container the daemon
// reports. It adopts a drifted container id or status, records a container that
// disappeared as stopped, and recreates a missing container only when the
// module and its parent server are both meant to be running.
//
// A node that cannot be reached leaves the record untouched: an unreachable
// daemon is not evidence that the container is gone.
func (m *Manager) ReconcileModule(ctx context.Context, moduleID string) (ReconcileOutcome, error) {
	if strings.TrimSpace(moduleID) == "" {
		return ReconcileSkipped, nil
	}

	mod, err := m.store.GetModule(ctx, moduleID)
	if err != nil || mod == nil {
		// Deleted between the event and this pass; nothing to converge.
		return ReconcileSkipped, nil
	}

	cli, err := m.resolveDocker(mod.NodeID)
	if err != nil {
		return ReconcileSkipped, fmt.Errorf("resolve docker for module %s: %w", mod.Name, err)
	}

	obs, err := observeModule(ctx, cli, mod.ID)
	if err != nil {
		return ReconcileSkipped, err
	}

	parent, _ := m.store.GetServer(ctx, mod.ServerID)
	action := decideModule(mod, parent, obs)

	switch action {
	case actionNone:
		return ReconcileInSync, nil

	case actionMarkGone:
		if err := m.store.UpdateModuleStatus(ctx, mod.ID, storage.ModuleStatusStopped); err != nil {
			return ReconcileSkipped, fmt.Errorf("record module %s as stopped: %w", mod.Name, err)
		}
		if mod.ContainerID != "" {
			if err := m.store.UpdateModuleContainerID(ctx, mod.ID, ""); err != nil {
				return ReconcileSkipped, fmt.Errorf("clear container id for module %s: %w", mod.Name, err)
			}
		}
		m.logger.Info("Module %s has no container; recorded as stopped", mod.Name)
		return ReconcileStopped, nil

	case actionAdopt:
		if obs.ContainerID != "" && obs.ContainerID != mod.ContainerID {
			if err := m.store.UpdateModuleContainerID(ctx, mod.ID, obs.ContainerID); err != nil {
				return ReconcileSkipped, fmt.Errorf("adopt container for module %s: %w", mod.Name, err)
			}
			m.logger.Info("Adopted container %s for module %s", shortID(obs.ContainerID), mod.Name)
		}
		status := storage.ModuleStatusStopped
		if obs.Running {
			status = storage.ModuleStatusRunning
		}
		if status != mod.Status {
			if err := m.store.UpdateModuleStatus(ctx, mod.ID, status); err != nil {
				return ReconcileSkipped, fmt.Errorf("record module %s status: %w", mod.Name, err)
			}
		}
		return ReconcileAdopted, nil

	case actionRecreate:
		if err := m.RecreateModule(ctx, mod.ID); err != nil {
			return ReconcileSkipped, fmt.Errorf("recreate module %s: %w", mod.Name, err)
		}
		m.logger.Info("Recreated missing container for module %s", mod.Name)
		return ReconcileRecreated, nil

	default:
		return ReconcileSkipped, nil
	}
}

// observeModule resolves and inspects a module's container. A container that
// cannot be found yields Exists=false and no error; a daemon failure is an
// error so the caller leaves the record alone.
func observeModule(ctx context.Context, cli *docker.Client, moduleID string) (ModuleObservation, error) {
	summary, err := cli.ResolveModuleContainer(ctx, moduleID)
	if err != nil {
		if errors.Is(err, docker.ErrContainerNotResolved) {
			return ModuleObservation{Exists: false}, nil
		}
		return ModuleObservation{}, fmt.Errorf("resolve module container: %w", err)
	}
	if summary == nil || summary.ID == "" {
		return ModuleObservation{Exists: false}, nil
	}

	status, err := cli.GetContainerStatus(ctx, summary.ID)
	if err != nil {
		return ModuleObservation{}, fmt.Errorf("inspect module container: %w", err)
	}
	return ModuleObservation{
		Exists:      true,
		ContainerID: summary.ID,
		Running:     status == storage.StatusRunning,
	}, nil
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
