// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// oidcHTTPEnv drives the three REAL OIDC REST routes over HTTP. The only mock
// is the external IdP (newFullOIDCFixture); the handlers, redirect flow, state
// cookie, one-time exchange code and session store are production code.
type oidcHTTPEnv struct {
	fixture *fullOIDCFixture
	idp     *httptest.Server
	manager *Manager
	store   *db.Store
	server  *httptest.Server
	client  *http.Client
}

func newOIDCHTTPEnv(t *testing.T, configure func(*testing.T, *fullOIDCFixture)) *oidcHTTPEnv {
	t.Helper()
	f, idp := newFullOIDCFixture(t)
	if configure != nil {
		configure(t, f)
	}
	m, store := newTestManager(t, testAuthConfig())

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
		RoleMapping:       map[string]string{"user": "user"},
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
	client := &http.Client{
		Jar: jar,
		// Keep every redirect visible so the test asserts the real 302 target.
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

// login performs GET /api/v1/auth/oidc/login and returns the IdP authorize URL.
func (e *oidcHTTPEnv) login(t *testing.T) *url.URL {
	t.Helper()
	resp, err := e.client.Get(e.server.URL + "/api/v1/auth/oidc/login")
	if err != nil {
		t.Fatalf("GET login: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("login status=%d body=%s", resp.StatusCode, readOIDCHTTPBody(t, resp))
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("login redirect: %v", err)
	}
	q := loc.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("login must use code+S256 PKCE, got %v", q)
	}
	for _, key := range []string{"code_challenge", "state", "nonce"} {
		if q.Get(key) == "" {
			t.Fatalf("login redirect missing %s: %s", key, loc)
		}
	}
	if q.Get("code_verifier") != "" {
		t.Fatal("code_verifier must never appear in the authorization URL")
	}
	var stateCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == oidcStateCookie {
			stateCookie = c
		}
	}
	if stateCookie == nil || stateCookie.Value != q.Get("state") {
		t.Fatalf("state cookie must bind to redirect state: %+v", stateCookie)
	}
	if !stateCookie.HttpOnly || stateCookie.SameSite != http.SameSiteLaxMode || stateCookie.Path != "/" || stateCookie.MaxAge != 300 {
		t.Fatalf("insecure state cookie flags: %+v", stateCookie)
	}
	return loc
}

// callback performs GET /api/v1/auth/oidc/callback with the browser's state
// cookie (unless noJarClient is set) and returns the redirect target.
func (e *oidcHTTPEnv) callback(t *testing.T, query url.Values, noJarClient bool) *url.URL {
	t.Helper()
	client := e.client
	if noJarClient {
		client = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, err := client.Get(e.server.URL + "/api/v1/auth/oidc/callback?" + query.Encode())
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", resp.StatusCode, readOIDCHTTPBody(t, resp))
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("callback redirect: %v", err)
	}
	if loc.Path != "/login" {
		t.Fatalf("callback must return to the SPA login route, got %s", loc)
	}
	return loc
}

func (e *oidcHTTPEnv) exchange(t *testing.T, code string) (*http.Response, string) {
	t.Helper()
	resp, err := e.client.Post(e.server.URL+"/api/v1/auth/oidc/exchange",
		"application/json", strings.NewReader(`{"code":"`+code+`"}`))
	if err != nil {
		t.Fatalf("POST exchange: %v", err)
	}
	body := readOIDCHTTPBody(t, resp)
	return resp, body
}

func readOIDCHTTPBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(body)
}

func oidcCallbackQuery(code, state string) url.Values {
	return url.Values{"code": {code}, "state": {state}}
}

// TestOIDCRestHandlersOverHTTPSuccessAndRotation walks the complete browser
// flow through the real REST routes: login redirect, IdP callback, one-time
// exchange, session validation and session rotation on a fresh sign-in.
func TestOIDCRestHandlersOverHTTPSuccessAndRotation(t *testing.T) {
	env := newOIDCHTTPEnv(t, nil)
	ctx := context.Background()

	authorize := env.login(t)
	state := authorize.Query().Get("state")
	env.fixture.nonce = authorize.Query().Get("nonce")

	cb := env.callback(t, oidcCallbackQuery("fixture-code", state), false)
	if cb.Query().Get("error") != "" {
		t.Fatalf("callback unexpectedly rejected: %s", cb)
	}
	code := cb.Query().Get("code")
	if code == "" {
		t.Fatal("callback must return a one-time exchange code, not a token")
	}

	resp, body := env.exchange(t, code)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", resp.StatusCode, body)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("exchange response must be no-store, got %q", resp.Header.Get("Cache-Control"))
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("exchange JSON: %v (%s)", err, body)
	}
	if payload.Token == "" {
		t.Fatal("exchange returned no session token")
	}
	_ = resp.Body.Close()

	user, err := env.manager.AuthenticateFromHeader(ctx, "Bearer "+payload.Token)
	if err != nil || user == nil {
		t.Fatalf("issued session token is not usable: user=%+v err=%v", user, err)
	}

	// The exchange code and the callback state are both single-use.
	if replay, replayBody := env.exchange(t, code); replay.StatusCode != http.StatusBadRequest {
		t.Fatalf("exchange replay status=%d body=%s", replay.StatusCode, replayBody)
	} else {
		_ = replay.Body.Close()
	}
	replayed := env.callback(t, oidcCallbackQuery("fixture-code", state), false)
	if replayed.Query().Get("error") != "invalid_state" || replayed.Query().Get("code") != "" {
		t.Fatalf("replayed callback must not issue a code: %s", replayed)
	}

	// A fresh sign-in rotates the session: the old credential stops working
	// before the new one is issued.
	authorize2 := env.login(t)
	state2 := authorize2.Query().Get("state")
	env.fixture.nonce = authorize2.Query().Get("nonce")
	cb2 := env.callback(t, oidcCallbackQuery("fixture-code", state2), false)
	code2 := cb2.Query().Get("code")
	if code2 == "" || code2 == code {
		t.Fatalf("second sign-in must produce a new exchange code: %s", cb2)
	}
	resp2, body2 := env.exchange(t, code2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second exchange status=%d body=%s", resp2.StatusCode, body2)
	}
	var payload2 struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body2), &payload2); err != nil || payload2.Token == "" {
		t.Fatalf("second exchange JSON: %v (%s)", err, body2)
	}
	_ = resp2.Body.Close()
	if payload2.Token == payload.Token {
		t.Fatal("session rotation must issue a distinct token")
	}
	if _, err := env.manager.AuthenticateFromHeader(ctx, "Bearer "+payload.Token); err == nil {
		t.Fatal("previous session was not invalidated by a fresh sign-in")
	}
	if _, err := env.manager.AuthenticateFromHeader(ctx, "Bearer "+payload2.Token); err != nil {
		t.Fatalf("new session is not usable after rotation: %v", err)
	}
}

// TestOIDCRestHandlersOverHTTPRefusals proves the real REST boundary rejects
// bad state/nonce, IdP errors and rejected callbacks without issuing a session.
func TestOIDCRestHandlersOverHTTPRefusals(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, *fullOIDCFixture)
		callback  func(t *testing.T, env *oidcHTTPEnv, state string) *url.URL
		wantError string
	}{
		{
			name: "invalid state",
			callback: func(t *testing.T, env *oidcHTTPEnv, _ string) *url.URL {
				return env.callback(t, oidcCallbackQuery("fixture-code", "attacker-state"), false)
			},
			wantError: "invalid_state",
		},
		{
			name: "missing state cookie",
			callback: func(t *testing.T, env *oidcHTTPEnv, state string) *url.URL {
				return env.callback(t, oidcCallbackQuery("fixture-code", state), true)
			},
			wantError: "invalid_state",
		},
		{
			name: "missing authorization code",
			callback: func(t *testing.T, env *oidcHTTPEnv, state string) *url.URL {
				return env.callback(t, url.Values{"state": {state}}, false)
			},
			wantError: "invalid_state",
		},
		{
			name: "provider error",
			callback: func(t *testing.T, env *oidcHTTPEnv, state string) *url.URL {
				q := oidcCallbackQuery("", state)
				q.Set("error", "access_denied")
				q.Set("error_description", "user refused consent")
				return env.callback(t, q, false)
			},
			wantError: "access_denied",
		},
		{
			name: "provider temporarily unavailable",
			callback: func(t *testing.T, env *oidcHTTPEnv, state string) *url.URL {
				q := oidcCallbackQuery("", state)
				q.Set("error", "temporarily_unavailable")
				return env.callback(t, q, false)
			},
			wantError: "oidc_degraded",
		},
		{
			name: "invalid nonce",
			configure: func(t *testing.T, f *fullOIDCFixture) {
				f.claimsOverride = map[string]any{"nonce": "attacker-nonce"}
			},
			callback: func(t *testing.T, env *oidcHTTPEnv, state string) *url.URL {
				return env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			},
			wantError: "access_denied",
		},
		{
			name: "callback rejection wrong audience",
			configure: func(t *testing.T, f *fullOIDCFixture) {
				f.claimsOverride = map[string]any{"aud": "other-client"}
			},
			callback: func(t *testing.T, env *oidcHTTPEnv, state string) *url.URL {
				return env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			},
			wantError: "access_denied",
		},
		{
			name: "callback rejection bad signature",
			configure: func(t *testing.T, f *fullOIDCFixture) {
				untrusted, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatalf("untrusted key: %v", err)
				}
				f.signingKey = untrusted
				f.signingKID = "fixture-key" // JWKS still advertises the trusted key
			},
			callback: func(t *testing.T, env *oidcHTTPEnv, state string) *url.URL {
				return env.callback(t, oidcCallbackQuery("fixture-code", state), false)
			},
			wantError: "access_denied",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newOIDCHTTPEnv(t, tc.configure)
			authorize := env.login(t)
			state := authorize.Query().Get("state")
			env.fixture.nonce = authorize.Query().Get("nonce")

			got := tc.callback(t, env, state)
			if got.Query().Get("error") != tc.wantError {
				t.Fatalf("error=%q want %q (location %s)", got.Query().Get("error"), tc.wantError, got)
			}
			if got.Query().Get("code") != "" {
				t.Fatalf("refused callback issued an exchange code: %s", got)
			}
			// No callback in this table may create an account or a session.
			if user, err := env.store.GetUserByOIDCIssuerAndSubject(context.Background(), env.fixture.issuer, "fixture-subject"); err == nil {
				t.Fatalf("refused callback created user %+v", user)
			}
		})
	}
}
