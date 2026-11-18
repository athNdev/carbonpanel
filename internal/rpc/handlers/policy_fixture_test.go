// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/stretchr/testify/require"
)

// testNoAuthPolicy returns the same dependencies production uses in explicit
// allow_no_auth mode: a real manager that mints the synthetic admin and a real
// enforcer. Legacy handler tests must not rely on nil dependencies, because
// those now fail closed (B-01).
func testNoAuthPolicy(t *testing.T) (*auth.Manager, *rbac.Enforcer) {
	t.Helper()
	store, err := db.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{
			Path:           filepath.Join(t.TempDir(), "policy.db"),
			MaxConnections: 5,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())
	enforcer, err := rbac.NewEnforcer(store.DB())
	require.NoError(t, err)
	require.NoError(t, enforcer.SeedDefaultPolicies(false))
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{AllowNoAuth: true})
	require.NoError(t, err)
	return manager, enforcer
}
