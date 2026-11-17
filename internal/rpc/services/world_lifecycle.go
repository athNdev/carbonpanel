// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"path/filepath"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
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

// worldLifecycleAdapter gives the World Binder the narrow stop/start seam it
// needs without reaching through the HTTP mux. A world binding grant is NOT a
// servers:stop/start grant, so each call re-checks the server-scoped permission
// explicitly before delegating to ServerService.
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

func (a *worldLifecycleAdapter) StopServer(ctx context.Context, serverID string) error {
	if err := a.requireServerPermission(ctx, serverID, rbac.ActionStop); err != nil {
		return err
	}
	if a.servers == nil {
		return errors.New("server service is unavailable")
	}
	_, err := a.servers.StopServer(ctx, connect.NewRequest(&v1.StopServerRequest{Id: serverID}))
	return err
}

func (a *worldLifecycleAdapter) StartServer(ctx context.Context, serverID string) error {
	if err := a.requireServerPermission(ctx, serverID, rbac.ActionStart); err != nil {
		return err
	}
	if a.servers == nil {
		return errors.New("server service is unavailable")
	}
	_, err := a.servers.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: serverID}))
	return err
}

func (a *worldLifecycleAdapter) IsRunning(ctx context.Context, serverID string) (bool, error) {
	server, err := a.store.GetServer(ctx, serverID)
	if err != nil {
		return false, err
	}
	return server.Status == storage.StatusRunning, nil
}
