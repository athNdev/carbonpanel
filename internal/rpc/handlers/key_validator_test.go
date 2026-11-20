// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyValidatorFixture builds the legacy adapter with the same policy shapes
// the generated RPC tests use: no credentials, anonymous, global/scoped grants,
// explicit no-auth mode, and a missing enforcer.
func keyValidatorFixture(t *testing.T, noAuth, anonymous, global, scoped, nilEnforcer bool) http.Handler {
	t.Helper()
	store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "panel.db"), MaxConnections: 5}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())
	enforcer, err := rbac.NewEnforcer(store.DB())
	require.NoError(t, err)
	require.NoError(t, enforcer.SeedDefaultPolicies(false))
	if global || scoped {
		object := "*"
		if scoped {
			object = "settings-one"
		}
		require.NoError(t, enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{{Resource: rbac.ResourceSettings, Action: rbac.ActionRead, ObjectID: object}}))
	}
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{Local: config.LocalConfig{Enabled: !noAuth}, AnonymousAccess: anonymous, AllowNoAuth: noAuth})
	require.NoError(t, err)
	if nilEnforcer {
		enforcer = nil
	}
	return NewKeyValidatorHandler(manager, enforcer, logger.New())
}

func postKeyValidation(handler http.Handler, body ValidateKeyRequest) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/validate-key", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestKeyValidatorGlobalSettingsPolicy(t *testing.T) {
	for _, fixture := range []struct {
		name                                           string
		noAuth, anonymous, global, scoped, nilEnforcer bool
		status                                         int
	}{
		{name: "missing credentials", status: http.StatusUnauthorized},
		{name: "missing permission", anonymous: true, status: http.StatusForbidden},
		{name: "resource-scoped grant cannot validate globally", anonymous: true, scoped: true, status: http.StatusForbidden},
		{name: "global settings read", anonymous: true, global: true, status: http.StatusOK},
		{name: "explicit no-auth admin", noAuth: true, status: http.StatusOK},
		{name: "missing enforcer", noAuth: true, nilEnforcer: true, status: http.StatusForbidden},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			handler := keyValidatorFixture(t, fixture.noAuth, fixture.anonymous, fixture.global, fixture.scoped, fixture.nilEnforcer)
			rec := postKeyValidation(handler, ValidateKeyRequest{Provider: "unsupported-provider", APIKey: "x"})
			if rec.Code != fixture.status {
				t.Fatalf("status=%d want %d: %s", rec.Code, fixture.status, rec.Body.String())
			}
			if fixture.status != http.StatusOK {
				return
			}
			var resp ValidateKeyResponse
			require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
			assert.False(t, resp.Valid)
			assert.Contains(t, resp.Message, "Unsupported provider")
		})
	}
}

func TestKeyValidator_CurseForge(t *testing.T) {
	handler := keyValidatorFixture(t, true, false, false, false, false)

	// Empty key is a soft result, not an error: no upstream traffic is needed.
	rec := postKeyValidation(handler, ValidateKeyRequest{Provider: "curseforge", APIKey: ""})
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ValidateKeyResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.Valid)
	assert.Contains(t, resp.Message, "empty")
}

func TestKeyValidator_InvalidMethod(t *testing.T) {
	handler := keyValidatorFixture(t, true, false, false, false, false)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/validate-key", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestKeyValidator_UnsupportedProvider(t *testing.T) {
	handler := keyValidatorFixture(t, true, false, false, false, false)

	rec := postKeyValidation(handler, ValidateKeyRequest{Provider: "nope", APIKey: "x"})
	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ValidateKeyResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	assert.False(t, resp.Valid)
	assert.Contains(t, resp.Message, "Unsupported provider")
}

func TestKeyValidator_CookieSessionUsesGlobalSettingsPolicy(t *testing.T) {
	store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "panel.db"), MaxConnections: 5}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())

	enforcer, err := rbac.NewEnforcer(store.DB())
	require.NoError(t, err)
	require.NoError(t, enforcer.SeedDefaultPolicies(false))
	require.NoError(t, enforcer.SetPermissionsForRole("user", []rbac.Permission{{Resource: rbac.ResourceSettings, Action: rbac.ActionRead, ObjectID: "*"}}))

	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{Local: config.LocalConfig{Enabled: true}, SessionTimeout: 3600})
	require.NoError(t, err)

	user, err := manager.CreateLocalUser(t.Context(), "alice", "alice@example.com", "ValidPass123!")
	require.NoError(t, err)
	require.NoError(t, store.AssignRole(t.Context(), user.ID, "user", "local"))

	_, _, token, _, err := manager.Login(t.Context(), "alice", "ValidPass123!")
	require.NoError(t, err)

	handler := NewKeyValidatorHandler(manager, enforcer, logger.New())

	t.Run("authorized cookie session", func(t *testing.T) {
		rec := postKeyValidationWithCookie(handler, ValidateKeyRequest{Provider: "unsupported-provider", APIKey: "x"}, token)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var resp ValidateKeyResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
		assert.False(t, resp.Valid)
		assert.Contains(t, resp.Message, "Unsupported provider")
	})

	require.NoError(t, enforcer.SetPermissionsForRole("user", nil))
	t.Run("cookie session denied without global permission", func(t *testing.T) {
		rec := postKeyValidationWithCookie(handler, ValidateKeyRequest{Provider: "unsupported-provider", APIKey: "x"}, token)
		require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
	})
}

func postKeyValidationWithCookie(handler http.Handler, body ValidateKeyRequest, token string) *httptest.ResponseRecorder {
	encoded, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/validate-key", bytes.NewReader(encoded))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}
