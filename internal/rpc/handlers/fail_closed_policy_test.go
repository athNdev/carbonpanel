// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/require"
)

// testScopedPolicy mints an anonymous identity whose only grant is the given
// resource/action on one object. It proves object scoping rather than relying
// on the no-auth administrator shortcut.
func testScopedPolicy(t *testing.T, resource, action, objectID string) (*auth.Manager, *rbac.Enforcer) {
	t.Helper()
	store, err := storage.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{
			Path:           filepath.Join(t.TempDir(), "scoped.db"),
			MaxConnections: 5,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())
	enforcer, err := rbac.NewEnforcer(store.DB())
	require.NoError(t, err)
	require.NoError(t, enforcer.SeedDefaultPolicies(false))
	require.NoError(t, enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{
		{Resource: resource, Action: action, ObjectID: objectID},
	}))
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local:           config.LocalConfig{Enabled: true},
		AnonymousAccess: true,
	})
	require.NoError(t, err)
	return manager, enforcer
}

func TestLegacyAdaptersFailClosedWithoutEnforcer(t *testing.T) {
	manager, _ := testNoAuthPolicy(t) // no-auth admin: authentication succeeds

	t.Run("metrics host", func(t *testing.T) {
		h := NewMetricsHandler(nil, nil, manager, nil, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/metrics/host", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("stream upload", func(t *testing.T) {
		h := NewUploadStreamHandler(nil, manager, nil, logger.New())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/upload/session-1", bytes.NewReader(nil)))
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("staged config list", func(t *testing.T) {
		store := setupTestStore(t)
		h := NewStagedConfigHandler(store, logger.New(), manager, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/staged-config/srv-a/staged", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("packwiz management", func(t *testing.T) {
		h := NewPackwizHandler(nil, nil, nil, logger.New(), manager, nil, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/packwiz/packs", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)
	})
}

func TestLegacyAdaptersEnforceObjectScope(t *testing.T) {
	t.Run("metrics server metrics are server scoped", func(t *testing.T) {
		manager, enforcer := testScopedPolicy(t, rbac.ResourceServers, rbac.ActionRead, "srv-owned")
		h := NewMetricsHandler(nil, nil, manager, enforcer, nil)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/metrics/servers/srv-other", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)

		// The authorized object passes policy and reaches the handler; with no
		// collector/store it ends in 404 rather than a policy refusal.
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/metrics/servers/srv-owned", nil))
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("mod search uses the server in the path", func(t *testing.T) {
		manager, enforcer := testScopedPolicy(t, rbac.ResourceMods, rbac.ActionRead, "srv-owned")
		h := NewModOnlineManager(nil, logger.New(), manager, enforcer)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/servers/srv-other/mods/search", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("staged config mutations do not reveal record existence to unauthorized callers", func(t *testing.T) {
		store := setupTestStore(t)
		manager, enforcer := testScopedPolicy(t, rbac.ResourceServerConfig, rbac.ActionCreate, "srv-owned")
		h := NewStagedConfigHandler(store, logger.New(), manager, enforcer)

		// The caller has no grant for srv-other, so an unknown staged ID under
		// that server must be refused before any existence lookup.
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/staged-config/srv-other/staged/does-not-exist/apply", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)
	})

	t.Run("staged config list uses the server in the path", func(t *testing.T) {
		store := setupTestStore(t)
		manager, enforcer := testScopedPolicy(t, rbac.ResourceServerConfig, rbac.ActionRead, "srv-owned")
		h := NewStagedConfigHandler(store, logger.New(), manager, enforcer)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/staged-config/srv-other/staged", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)

		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/staged-config/srv-owned/staged", nil))
		require.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("staged config mutations resolve the real owner", func(t *testing.T) {
		ctx := context.Background()
		store := setupTestStore(t)
		for _, id := range []string{"srv-owned", "srv-other"} {
			require.NoError(t, store.CreateServer(ctx, &storage.Server{
				ID: id, Name: id, NodeID: "local", Port: 25565,
				Memory: 2048, MaxPlayers: 20, Status: storage.StatusStopped,
			}))
		}
		change, err := store.StageConfigChange(ctx, "srv-other", map[string]any{"motd": "x"}, storage.StagedApplyOnRestart, "")
		require.NoError(t, err)

		// Grant create only on srv-owned. The URL names srv-owned but the staged
		// record belongs to srv-other, so the mutation must be refused.
		manager, enforcer := testScopedPolicy(t, rbac.ResourceServerConfig, rbac.ActionCreate, "srv-owned")
		h := NewStagedConfigHandler(store, logger.New(), manager, enforcer)

		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/staged-config/srv-owned/staged/"+change.ID+"/apply", nil))
		require.Equal(t, http.StatusForbidden, rec.Code)

		// The victim's change is still staged: no mutation happened.
		got, err := store.GetStagedConfigChange(ctx, change.ID)
		require.NoError(t, err)
		require.Equal(t, storage.StagedStatusStaged, got.Status)
	})
}

func TestLegacyAdaptersAcceptExplicitNoAuthAdmin(t *testing.T) {
	manager, enforcer := testNoAuthPolicy(t)

	rec := httptest.NewRecorder()
	NewMetricsHandler(nil, nil, manager, enforcer, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v2/metrics/host", nil))
	require.NotEqual(t, http.StatusUnauthorized, rec.Code)
	require.NotEqual(t, http.StatusForbidden, rec.Code)

	store := setupTestStore(t)
	rec = httptest.NewRecorder()
	NewStagedConfigHandler(store, logger.New(), manager, enforcer).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/staged-config/srv-a/staged", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}
