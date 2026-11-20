// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// oidcProviderProfile models a tenant-independent IdP/profile shape using the
// local fixture discovery and signing endpoints. No live tenant or credential.
type oidcProviderProfile struct {
	name           string
	issuerPath     string
	roleClaim      string
	roleMapping    map[string]string
	claims         map[string]any
	absentPolicy   string
	defaultRoles   []string
	allowRawRoles  bool
	rejectUnmapped bool
	expectedRoles  []string
	forbiddenRoles []string
}

func (p oidcProviderProfile) handlerConfig(issuer, redirectURL string) *config.OIDCConfig {
	policy := p.absentPolicy
	if policy == "" {
		policy = "deny"
	}
	return &config.OIDCConfig{
		Enabled:            true,
		IssuerURI:          issuer,
		ClientID:           "carbon-panel",
		ClientSecret:       "fixture-secret",
		RedirectURL:        redirectURL,
		RoleClaim:          p.roleClaim,
		RoleMapping:        p.roleMapping,
		AbsentClaimPolicy:  policy,
		DefaultRoles:       append([]string(nil), p.defaultRoles...),
		AllowRawClaimRoles: p.allowRawRoles,
		RejectUnmapped:     p.rejectUnmapped,
	}
}

func newOIDCHTTPEnvForProfile(t *testing.T, profile oidcProviderProfile) *oidcHTTPEnv {
	t.Helper()
	f, idp := newOIDCProviderFixture(t, profile.issuerPath)
	if len(profile.claims) > 0 {
		f.claimsOverride = profile.claims
	}

	m, store := newTestManager(t, testAuthConfig())

	var h *OIDCHandler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/oidc/login", func(w http.ResponseWriter, r *http.Request) { h.HandleLogin(w, r) })
	mux.HandleFunc("/api/v1/auth/oidc/callback", func(w http.ResponseWriter, r *http.Request) { h.HandleCallback(w, r) })
	mux.HandleFunc("/api/v1/auth/oidc/exchange", func(w http.ResponseWriter, r *http.Request) { h.HandleExchange(w, r) })

	ts := httptest.NewServer(mux)
	handler, err := NewOIDCHandler(m, store, profile.handlerConfig(f.issuer, ts.URL+"/api/v1/auth/oidc/callback"), logger.New())
	if err != nil {
		ts.Close()
		idp.Close()
		t.Fatalf("NewOIDCHandler(%s): %v", profile.name, err)
	}
	h = handler

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	env := &oidcHTTPEnv{fixture: f, idp: idp, manager: m, store: store, server: ts, client: client}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		ts.Close()
		idp.Close()
	})
	return env
}

func TestOIDCProviderProfilesStrictInteropSeam(t *testing.T) {
	profiles := []oidcProviderProfile{
		{
			name:        "okta-custom-auth-server-groups-array",
			issuerPath:  "/oauth2/default",
			roleClaim:   "groups",
			roleMapping: map[string]string{"carbon-users": "user"},
			claims: map[string]any{
				"groups":            []string{"carbon-users"},
				"preferred_username": "okta.user",
				"email":             "okta.user@example.test",
			},
			expectedRoles:  []string{"user"},
			forbiddenRoles: []string{"admin", "owner", "root", "superadmin", "system"},
		},
		{
			name:        "authentik-application-issuer-groups-array",
			issuerPath:  "/application/o/carbon-panel/",
			roleClaim:   "groups",
			roleMapping: map[string]string{"carbon-admins": "admin", "carbon-users": "user"},
			claims: map[string]any{
				"groups":             []string{"carbon-users"},
				"preferred_username": "authentik.user",
				"email":              "authentik.user@example.test",
			},
			expectedRoles:  []string{"user"},
			forbiddenRoles: []string{"owner", "root", "superadmin", "system"},
		},
	}

	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			env := newOIDCHTTPEnvForProfile(t, profile)
			authorize := env.login(t)
			state := authorize.Query().Get("state")
			if state == "" {
				t.Fatal("login must issue a state value")
			}
			env.fixture.nonce = authorize.Query().Get("nonce")

			cb := env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			if cb.Query().Get("error") != "" {
				t.Fatalf("callback rejected: %s", cb)
			}
			code := cb.Query().Get("code")
			if code == "" {
				t.Fatal("callback must return one-time exchange code")
			}
			resp, body := env.exchange(t, code)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("exchange status=%d body=%s", resp.StatusCode, body)
			}
			var payload struct {
				Token string `json:"token"`
			}
			if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.Token == "" {
				t.Fatalf("exchange payload decode err=%v body=%s", err, body)
			}
			_ = resp.Body.Close()

			user, err := env.manager.AuthenticateFromHeader(context.Background(), "Bearer "+payload.Token)
			if err != nil || user == nil {
				t.Fatalf("session not usable: user=%+v err=%v", user, err)
			}
			for _, want := range profile.expectedRoles {
				if !containsRole(user.Roles, want) {
					t.Fatalf("roles=%v missing expected %q", user.Roles, want)
				}
			}
			for _, banned := range profile.forbiddenRoles {
				if containsRole(user.Roles, banned) {
					t.Fatalf("roles=%v must not contain privileged role %q", user.Roles, banned)
				}
			}

			replay := env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			if replay.Query().Get("error") != "invalid_state" || replay.Query().Get("code") != "" {
				t.Fatalf("replayed callback must be refused without code: %s", replay)
			}
		})
	}
}

func TestOIDCProviderProfilesRejectStrictIssuerAudienceState(t *testing.T) {
	profiles := []oidcProviderProfile{
		{name: "okta", issuerPath: "/oauth2/default", roleClaim: "groups", roleMapping: map[string]string{"carbon-users": "user"}, claims: map[string]any{"groups": []string{"carbon-users"}}},
		{name: "authentik", issuerPath: "/application/o/carbon-panel/", roleClaim: "groups", roleMapping: map[string]string{"carbon-users": "user"}, claims: map[string]any{"groups": []string{"carbon-users"}}},
	}

	for _, profile := range profiles {
		t.Run(profile.name+"/wrong-audience", func(t *testing.T) {
			env := newOIDCHTTPEnvForProfile(t, profile)
			authorize := env.login(t)
			state := authorize.Query().Get("state")
			env.fixture.nonce = authorize.Query().Get("nonce")
			if env.fixture.claimsOverride == nil {
				env.fixture.claimsOverride = map[string]any{}
			}
			env.fixture.claimsOverride["aud"] = "other-client"
			cb := env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			if cb.Query().Get("error") != "access_denied" || cb.Query().Get("code") != "" {
				t.Fatalf("wrong audience must be refused: %s", cb)
			}
		})

		t.Run(profile.name+"/issuer-mismatch", func(t *testing.T) {
			env := newOIDCHTTPEnvForProfile(t, profile)
			authorize := env.login(t)
			state := authorize.Query().Get("state")
			env.fixture.nonce = authorize.Query().Get("nonce")
			if env.fixture.claimsOverride == nil {
				env.fixture.claimsOverride = map[string]any{}
			}
			env.fixture.claimsOverride["iss"] = env.fixture.issuer + "-other"
			cb := env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			if cb.Query().Get("error") == "" || cb.Query().Get("code") != "" {
				t.Fatalf("issuer mismatch must be refused: %s", cb)
			}
		})

		t.Run(profile.name+"/invalid-state", func(t *testing.T) {
			env := newOIDCHTTPEnvForProfile(t, profile)
			authorize := env.login(t)
			_ = authorize.Query().Get("state")
			env.fixture.nonce = authorize.Query().Get("nonce")
			cb := env.callback(t, oidcCallbackQuery("fixture-code", "attacker-state"), false)
			if cb.Query().Get("error") != "invalid_state" || cb.Query().Get("code") != "" {
				t.Fatalf("invalid state must be refused: %s", cb)
			}
		})
	}
}

func TestOIDCProviderProfilesNonEscalation(t *testing.T) {
	profiles := []oidcProviderProfile{
		{name: "okta", issuerPath: "/oauth2/default", roleClaim: "groups", roleMapping: map[string]string{"carbon-users": "user"}, claims: map[string]any{"groups": []any{"admin", "carbon-users", "root"}}},
		{name: "authentik", issuerPath: "/application/o/carbon-panel/", roleClaim: "groups", roleMapping: map[string]string{"carbon-users": "user"}, claims: map[string]any{"groups": []any{"admin", "carbon-users", "system"}}},
	}

	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			env := newOIDCHTTPEnvForProfile(t, profile)
			authorize := env.login(t)
			state := authorize.Query().Get("state")
			env.fixture.nonce = authorize.Query().Get("nonce")
			cb := env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			if cb.Query().Get("error") != "" {
				t.Fatalf("callback rejected: %s", cb)
			}
			resp, body := env.exchange(t, cb.Query().Get("code"))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("exchange status=%d body=%s", resp.StatusCode, body)
			}
			var payload struct{ Token string `json:"token"` }
			if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.Token == "" {
				t.Fatalf("exchange payload err=%v body=%s", err, body)
			}
			_ = resp.Body.Close()

			dbUser, err := env.store.GetUserByOIDCIssuerAndSubject(context.Background(), env.fixture.issuer, "fixture-subject")
			if err != nil {
				t.Fatalf("user lookup: %v", err)
			}
			roles, err := env.store.GetUserRoleNames(context.Background(), dbUser.ID)
			if err != nil {
				t.Fatalf("roles: %v", err)
			}
			if !containsRole(roles, "user") {
				t.Fatalf("mapped user role missing, roles=%v", roles)
			}
			for _, banned := range []string{"admin", "owner", "root", "superadmin", "system"} {
				if containsRole(roles, banned) {
					t.Fatalf("privileged role %q escalated via unmapped/raw claim values: %v", banned, roles)
				}
			}
		})
	}
}

func containsRole(roles []string, want string) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

// TestOIDCProviderProfilesClaimNormalization verifies tenant-independent claim
// normalization and non-escalation semantics documented for provider profiles:
// role claim values may be arrays or JSON-encoded string arrays, mapping keys are
// matched case-insensitively, and unmapped privileged raw values are not granted.
func TestOIDCProviderProfilesClaimNormalization(t *testing.T) {
	cases := []struct {
		name           string
		issuerPath     string
		groupsClaim    any
		mapping        map[string]string
		expectRoles    []string
		forbiddenRoles []string
	}{
		{
			name:        "okta-json-string-array-case-insensitive-map",
			issuerPath:  "/oauth2/default",
			groupsClaim: `["Carbon-Users"]`,
			mapping:     map[string]string{"carbon-users": "user"},
			expectRoles: []string{"user"},
		},
		{
			name:           "authentik-any-array-unmapped-privileged-values",
			issuerPath:     "/application/o/carbon-panel/",
			groupsClaim:    []any{"carbon-users", "admin", "owner"},
			mapping:        map[string]string{"carbon-users": "user"},
			expectRoles:    []string{"user"},
			forbiddenRoles: []string{"admin", "owner", "root", "superadmin", "system"},
		},
		{
			name:        "okta-string-array-multiple-mapped",
			issuerPath:  "/oauth2/default",
			groupsClaim: []string{"carbon-users", "carbon-viewers"},
			mapping:     map[string]string{"carbon-users": "user", "carbon-viewers": "viewer"},
			expectRoles: []string{"user", "viewer"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			profile := oidcProviderProfile{
				name:        tc.name,
				issuerPath:  tc.issuerPath,
				roleClaim:   "groups",
				roleMapping: tc.mapping,
				claims:      map[string]any{"groups": tc.groupsClaim},
			}
			env := newOIDCHTTPEnvForProfile(t, profile)
			authorize := env.login(t)
			state := authorize.Query().Get("state")
			env.fixture.nonce = authorize.Query().Get("nonce")
			cb := env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			if cb.Query().Get("error") != "" {
				t.Fatalf("callback rejected: %s", cb)
			}
			resp, body := env.exchange(t, cb.Query().Get("code"))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("exchange status=%d body=%s", resp.StatusCode, body)
			}
			var payload struct{ Token string `json:"token"` }
			if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.Token == "" {
				t.Fatalf("exchange payload err=%v body=%s", err, body)
			}
			_ = resp.Body.Close()

			dbUser, err := env.store.GetUserByOIDCIssuerAndSubject(context.Background(), env.fixture.issuer, "fixture-subject")
			if err != nil {
				t.Fatalf("user lookup: %v", err)
			}
			roles, err := env.store.GetUserRoleNames(context.Background(), dbUser.ID)
			if err != nil {
				t.Fatalf("roles: %v", err)
			}
			for _, want := range tc.expectRoles {
				if !containsRole(roles, want) {
					t.Fatalf("roles=%v missing expected %q", roles, want)
				}
			}
			for _, banned := range tc.forbiddenRoles {
				if containsRole(roles, banned) {
					t.Fatalf("roles=%v must not contain privileged role %q", roles, banned)
				}
			}
		})
	}
}
