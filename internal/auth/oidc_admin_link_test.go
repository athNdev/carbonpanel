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
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// linkEnv drives the real OIDC REST handlers over HTTP with only the IdP
// mocked at the local httptest boundary. The local admin and all sessions are
// real manager/store state.
type linkEnv struct {
	fixture    *fullOIDCFixture
	manager    *Manager
	store      *db.Store
	server     *httptest.Server
	client     *http.Client
	handler    *OIDCHandler
	adminID    string
	adminName  string
	adminToken string
	password   string
}

func newLinkEnv(t *testing.T, groups []string) *linkEnv {
	t.Helper()
	f, idp := newFullOIDCFixture(t)
	f.claimsOverride = map[string]any{"groups": groups}
	m, store := newTestManager(t, testAuthConfig())

	const password = "ExplicitLink!2345"
	admin, err := m.CreateLocalUser(context.Background(), "link-admin", "admin@example.test", password)
	if err != nil {
		t.Fatalf("CreateLocalUser: %v", err)
	}
	if err := store.AssignRole(context.Background(), admin.ID, "admin", "local"); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}
	_, _, adminToken, _, err := m.Login(context.Background(), "link-admin", password)
	if err != nil {
		t.Fatalf("admin Login: %v", err)
	}

	var h *OIDCHandler
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/auth/oidc/login", func(w http.ResponseWriter, r *http.Request) { h.HandleLogin(w, r) })
	mux.HandleFunc("/api/v1/auth/oidc/callback", func(w http.ResponseWriter, r *http.Request) { h.HandleCallback(w, r) })
	mux.HandleFunc("/api/v1/auth/oidc/exchange", func(w http.ResponseWriter, r *http.Request) { h.HandleExchange(w, r) })
	mux.HandleFunc("POST /test/begin-link", func(w http.ResponseWriter, r *http.Request) {
		caller, err := m.AuthenticateFromHeader(r.Context(), r.Header.Get("Authorization"))
		if err != nil {
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		token, cookie, err := h.BeginLink(r.Context(), caller)
		if err != nil {
			http.Error(w, "link refused", http.StatusForbidden)
			return
		}
		http.SetCookie(w, cookie)
		_, _ = w.Write([]byte(token))
	})
	ts := httptest.NewServer(mux)

	handler, err := NewOIDCHandler(m, store, &config.OIDCConfig{
		Enabled:           true,
		IssuerURI:         f.issuer,
		ClientID:          "carbon-panel",
		ClientSecret:      "fixture-secret",
		RedirectURL:       ts.URL + "/api/v1/auth/oidc/callback",
		RoleClaim:         "groups",
		RoleMapping:       map[string]string{"carbon-admins": "admin", "carbon-owners": "owner", "carbon-users": "user"},
		AbsentClaimPolicy: "deny",
	}, logger.New())
	if err != nil {
		ts.Close()
		idp.Close()
		t.Fatalf("NewOIDCHandler: %v", err)
	}
	h = handler

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	env := &linkEnv{fixture: f, manager: m, store: store, server: ts, client: client, handler: h, adminID: admin.ID, adminName: admin.Username, adminToken: adminToken, password: password}
	t.Cleanup(func() {
		client.CloseIdleConnections()
		ts.Close()
		idp.Close()
	})
	return env
}

func (e *linkEnv) loginWithToken(t *testing.T, token string) *url.URL {
	t.Helper()
	target := e.server.URL + "/api/v1/auth/oidc/login"
	if token != "" {
		target += "?link_token=" + url.QueryEscape(token)
	}
	resp, err := e.client.Get(target)
	if err != nil {
		t.Fatalf("GET login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login status=%d body=%s", resp.StatusCode, readOIDCHTTPBody(t, resp))
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse login redirect: %v", err)
	}
	return loc
}

func (e *linkEnv) beginIntent(t *testing.T) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.server.URL+"/test/begin-link", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+e.adminToken)
	resp, err := e.client.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, resp.Cookies(), 1)
	require.True(t, resp.Cookies()[0].HttpOnly)
	require.Equal(t, http.SameSiteLaxMode, resp.Cookies()[0].SameSite)
	return readOIDCHTTPBody(t, resp)
}

func (e *linkEnv) callback(t *testing.T, authorize *url.URL, client *http.Client) *url.URL {
	t.Helper()
	e.fixture.nonce = authorize.Query().Get("nonce")
	state := authorize.Query().Get("state")
	resp, err := client.Get(e.server.URL + "/api/v1/auth/oidc/callback?" + oidcCallbackQuery("fixture-code", state).Encode())
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	return loc
}

func (e *linkEnv) signInOIDC(t *testing.T) (*AuthenticatedUser, string) {
	t.Helper()
	result := e.callback(t, e.loginWithToken(t, ""), e.client)
	require.Equal(t, "/login", result.Path)
	require.Empty(t, result.Query().Get("error"))
	require.NotEmpty(t, result.Query().Get("code"))
	resp, body := e.exchange(t, result.Query().Get("code"))
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &payload))
	user, err := e.manager.AuthenticateFromHeader(context.Background(), "Bearer "+payload.Token)
	require.NoError(t, err)
	return user, payload.Token
}

func (e *linkEnv) assertUnlinked(t *testing.T) {
	t.Helper()
	user, err := e.store.GetUser(context.Background(), e.adminID)
	require.NoError(t, err)
	require.Empty(t, user.OIDCIssuer)
	require.Empty(t, user.OIDCSubject)
}

func (e *linkEnv) exchange(t *testing.T, code string) (*http.Response, string) {
	t.Helper()
	resp, err := e.client.Post(e.server.URL+"/api/v1/auth/oidc/exchange", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatalf("POST exchange: %v", err)
	}
	return resp, readOIDCHTTPBody(t, resp)
}

func TestOIDCExplicitLocalAdminLinkAndCredentialRoles(t *testing.T) {
	env := newLinkEnv(t, []string{"carbon-admins", "carbon-users"})
	ctx := context.Background()
	require.NoError(t, env.store.AssignRole(ctx, env.adminID, "owner", "local"))
	token := env.beginIntent(t)
	authorize := env.loginWithToken(t, token)
	require.NotContains(t, authorize.String(), token)
	require.Equal(t, "S256", authorize.Query().Get("code_challenge_method"))
	require.Equal(t, "login", authorize.Query().Get("prompt"))
	require.NotEmpty(t, authorize.Query().Get("state"))
	require.NotEmpty(t, authorize.Query().Get("nonce"))

	result := env.callback(t, authorize, env.client)
	require.Equal(t, "/settings", result.Path)
	require.Equal(t, "success", result.Query().Get("oidc_link"))
	require.Empty(t, result.Query().Get("code"))
	local, err := env.manager.AuthenticateFromHeader(ctx, "Bearer "+env.adminToken)
	require.NoError(t, err)
	require.Equal(t, "local", local.SessionProvider)
	require.ElementsMatch(t, []string{"admin", "owner"}, local.Roles)

	user, err := env.store.GetUser(ctx, env.adminID)
	require.NoError(t, err)
	require.Equal(t, "local", user.AuthProvider)
	require.Equal(t, "fixture-subject", user.OIDCSubject)
	require.Equal(t, env.fixture.issuer, user.OIDCIssuer)
	require.Equal(t, "admin@example.test", *user.Email)
	for source, expected := range map[string][]string{
		"local": {"admin", "owner"}, "oidc": {"admin", "user"},
	} {
		roles, err := env.store.GetUserRoleNamesBySource(ctx, env.adminID, source)
		require.NoError(t, err)
		require.ElementsMatch(t, expected, roles)
	}

	oidcUser, _ := env.signInOIDC(t)
	require.Equal(t, env.adminID, oidcUser.ID)
	require.Equal(t, "oidc", oidcUser.SessionProvider)
	require.ElementsMatch(t, []string{"admin", "user"}, oidcUser.Roles)
	_, _, err = env.manager.GenerateAPIToken(WithUser(ctx, oidcUser), env.adminID, "fixture-token", nil)
	require.ErrorIs(t, err, ErrLocalAPITokenSession)
	_, err = env.manager.AuthenticateFromHeader(ctx, "Bearer "+env.adminToken)
	require.Error(t, err)

	env.fixture.claimsOverride = map[string]any{"groups": []string{"carbon-users"}}
	oidcUser, _ = env.signInOIDC(t)
	require.Equal(t, env.adminID, oidcUser.ID)
	require.Equal(t, []string{"user"}, oidcUser.Roles)
	_, roles, localToken, _, err := env.manager.Login(ctx, env.adminName, env.password)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"admin", "owner"}, roles)
	local, err = env.manager.ValidateSession(ctx, localToken)
	require.NoError(t, err)
	require.False(t, env.handler.CanLinkOIDC(ctx, local))
}

func TestOIDCRejectUnmappedRevokesLinkedSSOSession(t *testing.T) {
	env := newLinkEnv(t, []string{"carbon-admins"})
	ctx := context.Background()
	env.handler.config.RejectUnmapped = true
	result := env.callback(t, env.loginWithToken(t, env.beginIntent(t)), env.client)
	require.Equal(t, "success", result.Query().Get("oidc_link"))
	_, oidcToken := env.signInOIDC(t)
	_, _, localToken, _, err := env.manager.Login(ctx, env.adminName, env.password)
	require.NoError(t, err)

	env.fixture.claimsOverride = map[string]any{"groups": []string{}}
	result = env.callback(t, env.loginWithToken(t, ""), env.client)
	require.Equal(t, "no_mapped_roles", result.Query().Get("error"))
	_, err = env.manager.ValidateSession(ctx, oidcToken)
	require.ErrorIs(t, err, ErrSessionExpired)
	local, err := env.manager.ValidateSession(ctx, localToken)
	require.NoError(t, err)
	require.Equal(t, []string{"admin"}, local.Roles)
	roles, err := env.store.GetUserRoleNamesBySource(ctx, env.adminID, "oidc")
	require.NoError(t, err)
	require.Empty(t, roles)
}

func TestOIDCExplicitLinkFailClosed(t *testing.T) {
	t.Run("start browser differs", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		token := env.beginIntent(t)
		sameBrowser := env.client
		env.client = &http.Client{CheckRedirect: sameBrowser.CheckRedirect}
		result := env.loginWithToken(t, token)
		require.Equal(t, "link_invalid", result.Query().Get("error"))
		env.client = sameBrowser
		require.NotEmpty(t, env.loginWithToken(t, token).Query().Get("state"))
		require.Equal(t, "link_invalid", env.loginWithToken(t, token).Query().Get("error"))
		env.assertUnlinked(t)
	})

	t.Run("callback browser binding missing", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		authorize := env.loginWithToken(t, env.beginIntent(t))
		jar, err := cookiejar.New(nil)
		require.NoError(t, err)
		base, err := url.Parse(env.server.URL)
		require.NoError(t, err)
		jar.SetCookies(base, []*http.Cookie{{Name: oidcStateCookie, Value: authorize.Query().Get("state"), Path: "/"}})
		other := &http.Client{Jar: jar, CheckRedirect: env.client.CheckRedirect}
		require.Equal(t, "link_invalid", env.callback(t, authorize, other).Query().Get("error"))
		require.Equal(t, "invalid_state", env.callback(t, authorize, env.client).Query().Get("error"))
		env.assertUnlinked(t)
	})

	t.Run("revoked session even after fresh password login", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		ctx := context.Background()
		authorize := env.loginWithToken(t, env.beginIntent(t))
		require.NoError(t, env.manager.Logout(ctx, env.adminToken))
		_, _, replacement, _, err := env.manager.Login(ctx, env.adminName, env.password)
		require.NoError(t, err)
		require.NotEqual(t, env.adminToken, replacement)
		require.Equal(t, "link_invalid", env.callback(t, authorize, env.client).Query().Get("error"))
		env.assertUnlinked(t)
	})

	t.Run("expired intent between start and callback", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		authorize := env.loginWithToken(t, env.beginIntent(t))
		now := env.handler.linkStore.now()
		env.handler.linkStore.now = func() time.Time { return now.Add(oidcLinkTTL) }
		require.Equal(t, "link_invalid", env.callback(t, authorize, env.client).Query().Get("error"))
		env.assertUnlinked(t)
	})

	t.Run("subject collision refuses without binding", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		other := &db.User{ID: "other-issuer-user", Username: "other", AuthProvider: "oidc", OIDCIssuer: "https://other.example", OIDCSubject: "fixture-subject", IsActive: true}
		require.NoError(t, env.store.CreateUser(context.Background(), other))
		authorize := env.loginWithToken(t, env.beginIntent(t))
		result := env.callback(t, authorize, env.client)
		require.Equal(t, "link_required", result.Query().Get("oidc_link"))
		require.Empty(t, result.Query().Get("code"))
		env.assertUnlinked(t)
	})

	t.Run("unlinked privileged conflict still refused", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		ctx := context.Background()
		legacy := &db.User{ID: "legacy-oidc-admin", Username: "legacy-admin", AuthProvider: "oidc", OIDCIssuer: env.fixture.issuer, OIDCSubject: "fixture-subject", IsActive: true}
		require.NoError(t, env.store.CreateUser(ctx, legacy))
		require.NoError(t, env.store.AssignRole(ctx, legacy.ID, "admin", "local"))
		result := env.callback(t, env.loginWithToken(t, ""), env.client)
		require.Equal(t, "role_conflict", result.Query().Get("error"))
		require.Empty(t, result.Query().Get("code"))
	})
}
