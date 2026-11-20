// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/events"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// WorldLibraryRoot is the controller-side root for immutable revision archives.
func WorldLibraryRoot(cfg *config.Config) string {
	if cfg == nil || cfg.Storage.DataDir == "" {
		return filepath.Join(filepath.Join("data"), ".world-library")
	}
	return filepath.Join(cfg.Storage.DataDir, ".world-library")
}

// NewWorldBinder assembles the journalled binder with explicit lifecycle,
// resolver and authorization dependencies at the composition root.
func NewWorldBinder(store *storage.Store, cfg *config.Config, log *logger.Logger,
	resolver *nodestorage.Resolver, sender worldlib.CommandSender, servers *ServerService,
	enforcer *rbac.Enforcer) *worldlib.Binder {
	return worldlib.NewBinder(store, log, resolver, sender,
		newWorldLifecycleAdapter(servers, store, enforcer), WorldLibraryRoot(cfg))
}

// RecoverWorldBindings observes retained node state at startup. It restores a
// level family only when the live family is missing and leaves ambiguous states
// for operator resolution; it never retries a mutation that may have succeeded.
func RecoverWorldBindings(ctx context.Context, store *storage.Store, binder *worldlib.Binder, log *logger.Logger) {
	if binder == nil {
		return
	}
	servers, err := store.ListServers(ctx)
	if err != nil {
		if log != nil {
			log.Warn("World Library recovery could not list servers: %v", err)
		}
		return
	}
	for _, server := range servers {
		generation := int64(1)
		if node, err := store.GetNode(ctx, server.NodeID); err == nil && node.EndpointGeneration > 0 {
			generation = node.EndpointGeneration
		}
		summary, err := binder.Recover(ctx, server.ID, server.DataPath, server.NodeID, generation)
		if err != nil {
			if log != nil {
				log.Warn("World Library recovery for %s is unresolved: %v", server.ID, err)
			}
			continue
		}
		if summary != "no world recovery state" && log != nil {
			log.Info("World Library recovery for %s: %s", server.ID, summary)
		}
	}
}

// worldLifecycleAdapter changes the existing container under the bind journal.
// The public power RPCs keep their normal journal and grant checks.
type worldLifecycleAdapter struct {
	servers  *ServerService
	store    *storage.Store
	enforcer *rbac.Enforcer
}

func newWorldLifecycleAdapter(servers *ServerService, store *storage.Store, enforcer *rbac.Enforcer) *worldLifecycleAdapter {
	return &worldLifecycleAdapter{servers: servers, store: store, enforcer: enforcer}
}

func (a *worldLifecycleAdapter) requireServerPermission(ctx context.Context, serverID, action string) error {
	if a.enforcer == nil {
		return errors.New("rbac enforcer unavailable")
	}
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return errors.New("authentication required to change server lifecycle")
	}
	allowed, err := a.enforcer.Enforce(user.Roles, rbac.ResourceServers, action, serverID)
	if err != nil {
		return err
	}
	if !allowed {
		return errors.New("missing servers:" + action + " permission for this server")
	}
	return nil
}

func (a *worldLifecycleAdapter) ownedContainer(ctx context.Context, serverID, action string) (*storage.Server, *docker.Client, error) {
	if err := a.requireServerPermission(ctx, serverID, action); err != nil {
		return nil, nil, err
	}
	if a.servers == nil {
		return nil, nil, errors.New("server service is unavailable")
	}
	server, err := a.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, nil, err
	}
	if server.ContainerID == "" {
		return nil, nil, errors.New("world lifecycle requires an existing container")
	}
	if err := operation.AssertWorldBindOwner(ctx, a.store, server); err != nil {
		return nil, nil, err
	}
	client, err := a.servers.getDockerClientStrict(server.NodeID)
	return server, client, err
}

func (a *worldLifecycleAdapter) BeginTransition(serverID string) (func(), error) {
	transition, err := beginServerLifecycleTransition(serverID)
	if err != nil {
		return nil, err
	}
	return transition.Release, nil
}

func (a *worldLifecycleAdapter) StopServer(ctx context.Context, serverID string) error {
	server, client, err := a.ownedContainer(ctx, serverID, rbac.ActionStop)
	if err != nil {
		return err
	}
	window := docker.DefaultStopWindow
	if cfg, err := a.store.GetServerConfig(ctx, serverID); err == nil {
		window = docker.StopWindow(cfg)
	}
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), window+15*time.Second)
	defer cancel()
	server.Status = storage.StatusStopping
	if err := a.store.UpdateServer(stopCtx, server); err != nil {
		return err
	}
	if a.servers.proxy != nil {
		if server.ProxyHostname != "" {
			if err := a.servers.proxy.RemoveServerRoute(server.ID); err != nil {
				a.servers.log.Warn("world bind: cannot remove proxy route for %s: %v", server.ID, err)
			}
		} else {
			a.servers.proxy.StopDirectKnock(server.ID)
		}
	}
	found, err := client.StopContainerWithin(stopCtx, server.ContainerID, window)
	if err != nil {
		return err
	}
	if !found {
		return errors.New("container disappeared during the world stop")
	}
	observed, err := client.ObserveContainer(stopCtx, server.ContainerID)
	if err != nil {
		return err
	}
	if observed.Running || observed.Status != storage.StatusStopped {
		return fmt.Errorf("container is %s after the world stop", observed.Status)
	}

	server.Status = storage.StatusStopped
	if err := a.store.UpdateServer(stopCtx, server); err != nil {
		return err
	}
	if a.servers.bus != nil {
		a.servers.bus.Emit(stopCtx, events.Event{
			Type: v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP, ServerID: server.ID,
		})
	}
	a.servers.auditPower(stopCtx, "", activity.EventServerStop, server.ID, "")
	return nil
}

func (a *worldLifecycleAdapter) StartServer(ctx context.Context, serverID string) error {
	server, client, err := a.ownedContainer(ctx, serverID, rbac.ActionStart)
	if err != nil {
		return err
	}
	release, err := a.servers.acquireNodeAdmission(ctx, server)
	if err != nil {
		return err
	}
	defer release()
	if err := a.servers.checkServerStartCapacity(ctx, server); err != nil {
		return err
	}
	if err := operation.AssertWorldBindOwner(ctx, a.store, server); err != nil {
		return err
	}
	if err := serverruntime.Start(ctx, a.store, client, server); err != nil {
		return err
	}
	observed, err := client.ObserveContainer(ctx, server.ContainerID)
	if err != nil {
		return err
	}
	if !observed.Running || observed.OOMKilled ||
		(observed.Status != storage.StatusRunning && observed.Status != storage.StatusStarting) {
		return fmt.Errorf("container is %s after world restart", observed.Status)
	}
	now := time.Now()
	server.Status = storage.StatusStarting
	server.LastStarted = &now
	if err := a.store.UpdateServer(ctx, server); err != nil {
		return err
	}
	if a.servers.proxy != nil && server.ProxyHostname != "" {
		if err := a.servers.proxy.UpdateServerRoute(server); err != nil {
			a.servers.log.Warn("world bind: cannot restore proxy route for %s: %v", server.ID, err)
		}
	}
	a.servers.auditPower(ctx, "", activity.EventServerStart, server.ID, "")
	return nil
}

func (a *worldLifecycleAdapter) IsRunning(ctx context.Context, serverID string) (bool, error) {
	server, err := a.store.GetServer(ctx, serverID)
	if err != nil {
		return false, err
	}
	if a.servers == nil {
		return false, errors.New("server service is unavailable")
	}
	client, err := a.servers.getDockerClientStrict(server.NodeID)
	if err != nil {
		return false, err
	}
	if server.ContainerID == "" {
		resolved, err := client.ResolveContainer(ctx, server.ID)
		if errors.Is(err, docker.ErrContainerNotResolved) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if resolved != nil {
			return false, errors.New("untracked container must be reconciled before binding")
		}
		return false, nil
	}
	observed, err := client.ObserveContainer(ctx, server.ContainerID)
	if err != nil {
		return false, err
	}
	switch observed.Status {
	case storage.StatusStopped:
		if !observed.Running {
			return false, nil
		}
	case storage.StatusRunning, storage.StatusUnhealthy:
		if !observed.Running {
			break
		}
		if err := a.requireServerPermission(ctx, serverID, rbac.ActionStop); err != nil {
			return false, err
		}
		if err := a.requireServerPermission(ctx, serverID, rbac.ActionStart); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, fmt.Errorf("world bind requires a stable container state; found %s", observed.Status)
}
