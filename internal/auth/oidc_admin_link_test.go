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
	idp        *httptest.Server
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
	env := &linkEnv{fixture: f, idp: idp, manager: m, store: store, server: ts, client: client, handler: h, adminID: admin.ID, adminName: admin.Username, adminToken: adminToken, password: password}
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
	token, err := e.handler.LinkStore().CreateIntent(e.adminID)
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	return token
}

func (e *linkEnv) exchange(t *testing.T, code string) (*http.Response, string) {
	t.Helper()
	resp, err := e.client.Post(e.server.URL+"/api/v1/auth/oidc/exchange", "application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatalf("POST exchange: %v", err)
	}
	return resp, readOIDCHTTPBody(t, resp)
}

func assertOnlyLocalAdminGrant(t *testing.T, store *db.Store, userID string) {
	t.Helper()
	assertOnlyLocalGrant(t, store, userID, "admin")
}

func assertOnlyLocalGrant(t *testing.T, store *db.Store, userID, role string) {
	t.Helper()
	var grants []db.UserRole
	if err := store.DB().Where("user_id = ? AND role_name = ?", userID, role).Find(&grants).Error; err != nil {
		t.Fatalf("%s grants: %v", role, err)
	}
	if len(grants) != 1 || grants[0].Source != "local" {
		t.Fatalf("%s grant must remain single and local-owned, got %+v", role, grants)
	}
}

func TestOIDCExplicitLocalAdminLinkAndSessionRotation(t *testing.T) {
	env := newLinkEnv(t, []string{"carbon-admins"})
	ctx := context.Background()

	if _, err := env.manager.AuthenticateFromHeader(ctx, "Bearer "+env.adminToken); err != nil {
		t.Fatalf("pre-link local session invalid: %v", err)
	}

	token := env.beginIntent(t)
	authorize := env.loginWithToken(t, token)
	if authorize.Query().Get("link_token") != "" || strings.Contains(authorize.String(), token) {
		t.Fatalf("link token must not be forwarded to the IdP: %s", authorize)
	}
	if authorize.Query().Get("code_challenge_method") != "S256" || authorize.Query().Get("state") == "" || authorize.Query().Get("nonce") == "" {
		t.Fatalf("explicit link flow lost PKCE/state/nonce: %s", authorize)
	}

	state := authorize.Query().Get("state")
	env.fixture.nonce = authorize.Query().Get("nonce")
	cbResp, err := env.client.Get(env.server.URL + "/api/v1/auth/oidc/callback?" + oidcCallbackQuery("fixture-code", state).Encode())
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	cbBody := readOIDCHTTPBody(t, cbResp)
	_ = cbResp.Body.Close()
	cbLocHeader := cbResp.Header.Get("Location")
	if cbLocHeader == "" {
		t.Fatalf("callback returned no redirect: %s", cbBody)
	}
	cbLoc, err := url.Parse(cbLocHeader)
	if err != nil {
		t.Fatalf("parse callback redirect: %v", err)
	}
	if cbLoc.Query().Get("error") != "" {
		t.Fatalf("callback rejected: %s", cbLoc)
	}
	code := cbLoc.Query().Get("code")
	if code == "" {
		t.Fatal("callback must issue a one-time exchange code")
	}

	resp, body := env.exchange(t, code)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", resp.StatusCode, body)
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.Token == "" {
		t.Fatalf("exchange payload err=%v body=%s", err, body)
	}
	_ = resp.Body.Close()

	user, err := env.store.GetUser(context.Background(), env.adminID)
	if err != nil {
		t.Fatalf("linked admin lookup: %v", err)
	}
	if user.AuthProvider != "local" || user.OIDCSubject != "fixture-subject" || user.OIDCIssuer != env.fixture.issuer {
		t.Fatalf("link changed identity or local provider: %+v", user)
	}
	assertOnlyLocalAdminGrant(t, env.store, env.adminID)

	authenticated, err := env.manager.AuthenticateFromHeader(ctx, "Bearer "+payload.Token)
	if err != nil || authenticated == nil || !hasRole(authenticated.Roles, "admin") {
		t.Fatalf("linked OIDC session not admin: user=%+v err=%v", authenticated, err)
	}
	if _, err := env.manager.AuthenticateFromHeader(ctx, "Bearer "+env.adminToken); err == nil {
		t.Fatal("stale local session survived OIDC sign-in rotation")
	}
	if _, _, _, _, err := env.manager.Login(ctx, env.adminName, env.password); err != nil {
		t.Fatalf("local break-glass login no longer works after link: %v", err)
	}

	replay := env.loginWithToken(t, token)
	if replay.Query().Get("error") != "link_invalid" {
		t.Fatalf("link token replay must be refused: %s", replay)
	}
}

func TestOIDCExplicitLinkFailClosed(t *testing.T) {
	t.Run("invalid token", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		loc := env.loginWithToken(t, "not-a-real-token")
		if loc.Query().Get("error") != "link_invalid" {
			t.Fatalf("invalid link token accepted: %s", loc)
		}
	})

	t.Run("non-admin local user", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		user, err := env.manager.CreateLocalUser(context.Background(), "plain-user", "", "PlainPass!2345")
		if err != nil {
			t.Fatalf("CreateLocalUser: %v", err)
		}
		token, err := env.handler.LinkStore().CreateIntent(user.ID)
		if err != nil {
			t.Fatalf("CreateIntent: %v", err)
		}
		loc := env.loginWithToken(t, token)
		if loc.Query().Get("error") != "link_invalid" {
			t.Fatalf("non-admin local intent accepted: %s", loc)
		}
	})

	t.Run("expired token", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		token := env.beginIntent(t)
		realNow := env.handler.LinkStore().now
		env.handler.LinkStore().now = func() time.Time { return realNow().Add(6 * time.Minute) }
		loc := env.loginWithToken(t, token)
		if loc.Query().Get("error") != "link_invalid" {
			t.Fatalf("expired link token accepted: %s", loc)
		}
	})

	t.Run("subject collision refuses without binding", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		other := &db.User{ID: "other-issuer-user", Username: "other", AuthProvider: "oidc", OIDCIssuer: "https://other.example", OIDCSubject: "fixture-subject", IsActive: true}
		if err := env.store.CreateUser(context.Background(), other); err != nil {
			t.Fatalf("seed other identity: %v", err)
		}
		token := env.beginIntent(t)
		authorize := env.loginWithToken(t, token)
		state := authorize.Query().Get("state")
		env.fixture.nonce = authorize.Query().Get("nonce")
		resp, err := env.client.Get(env.server.URL + "/api/v1/auth/oidc/callback?" + oidcCallbackQuery("fixture-code", state).Encode())
		if err != nil {
			t.Fatalf("GET callback: %v", err)
		}
		body := readOIDCHTTPBody(t, resp)
		_ = resp.Body.Close()
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatalf("parse callback: %v (%s)", err, body)
		}
		if loc.Query().Get("error") != "link_required" || loc.Query().Get("code") != "" {
			t.Fatalf("subject collision must fail closed: %s", loc)
		}
		admin, err := env.store.GetUser(context.Background(), env.adminID)
		if err != nil || admin.OIDCSubject != "" || admin.OIDCIssuer != "" {
			t.Fatalf("refused link must not bind identity: %+v err=%v", admin, err)
		}
	})

	t.Run("unlinked privileged conflict still refused", func(t *testing.T) {
		env := newLinkEnv(t, []string{"carbon-admins"})
		legacy := &db.User{ID: "legacy-oidc-admin", Username: "legacy-admin", AuthProvider: "oidc", OIDCIssuer: env.fixture.issuer, OIDCSubject: "fixture-subject", IsActive: true}
		if err := env.store.CreateUser(context.Background(), legacy); err != nil {
			t.Fatalf("seed legacy OIDC admin: %v", err)
		}
		if err := env.store.AssignRole(context.Background(), legacy.ID, "admin", "local"); err != nil {
			t.Fatalf("AssignRole: %v", err)
		}
		authorize := env.loginWithToken(t, "")
		state := authorize.Query().Get("state")
		env.fixture.nonce = authorize.Query().Get("nonce")
		resp, err := env.client.Get(env.server.URL + "/api/v1/auth/oidc/callback?" + oidcCallbackQuery("fixture-code", state).Encode())
		if err != nil {
			t.Fatalf("GET callback: %v", err)
		}
		body := readOIDCHTTPBody(t, resp)
		_ = resp.Body.Close()
		loc, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatalf("parse callback: %v (%s)", err, body)
		}
		if loc.Query().Get("error") != "role_conflict" || loc.Query().Get("code") != "" {
			t.Fatalf("unlinked privileged conflict must stay refused: %s", loc)
		}
	})
}

func hasRole(roles []string, want string) bool {
	for _, role := range roles {
		if role == want {
			return true
		}
	}
	return false
}

func TestOIDCExplicitLinkPreservesAllLocalPrivilegedGrants(t *testing.T) {
	env := newLinkEnv(t, []string{"carbon-admins", "carbon-owners"})
	ctx := context.Background()
	if err := env.store.AssignRole(ctx, env.adminID, "owner", "local"); err != nil {
		t.Fatalf("AssignRole owner: %v", err)
	}

	token := env.beginIntent(t)
	authorize := env.loginWithToken(t, token)
	state := authorize.Query().Get("state")
	env.fixture.nonce = authorize.Query().Get("nonce")
	resp, err := env.client.Get(env.server.URL + "/api/v1/auth/oidc/callback?" + oidcCallbackQuery("fixture-code", state).Encode())
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	body := readOIDCHTTPBody(t, resp)
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("parse callback: %v (%s)", err, body)
	}
	if loc.Query().Get("error") != "" {
		t.Fatalf("callback rejected: %s", loc)
	}
	code := loc.Query().Get("code")
	if code == "" {
		t.Fatal("callback must issue one-time exchange code")
	}
	ex, exBody := env.exchange(t, code)
	if ex.StatusCode != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", ex.StatusCode, exBody)
	}
	_ = ex.Body.Close()

	// Both mapped privileged roles were held locally before the link. Removing
	// one while ranging over desired must not skip the next, or it would be
	// re-granted from OIDC and duplicate the break-glass grant.
	assertOnlyLocalGrant(t, env.store, env.adminID, "admin")
	assertOnlyLocalGrant(t, env.store, env.adminID, "owner")
	authenticated, err := env.manager.AuthenticateFromHeader(ctx, "Bearer "+mustTokenFromExchange(t, exBody))
	if err != nil {
		t.Fatalf("AuthenticateFromHeader: %v", err)
	}
	if !hasRole(authenticated.Roles, "admin") || !hasRole(authenticated.Roles, "owner") {
		t.Fatalf("linked session lost local privileged roles: %v", authenticated.Roles)
	}
}

func mustTokenFromExchange(t *testing.T, body string) string {
	t.Helper()
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.Token == "" {
		t.Fatalf("exchange payload err=%v body=%s", err, body)
	}
	return payload.Token
}

func TestWithoutRoleDoesNotMutateOriginal(t *testing.T) {
	original := []string{"admin", "owner", "user"}
	got := withoutRole(original, "admin")
	if len(got) != 2 || got[0] != "owner" || got[1] != "user" {
		t.Fatalf("withoutRole result = %v", got)
	}
	if original[0] != "admin" || original[1] != "owner" || original[2] != "user" {
		t.Fatalf("withoutRole mutated the ranged slice: %v", original)
	}
}
