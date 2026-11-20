// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/golang-jwt/jwt/v5"
)

func TestOIDCTransactionStoreAtomicConsume(t *testing.T) {
	store := newOIDCTxStore()
	tx := &oidcTransaction{State: "state-a", Nonce: "nonce-a", CodeVerifier: "verifier-a", CreatedAt: time.Now()}
	if err := store.create(tx); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, ok := store.consume("state-a")
	if !ok || got.Nonce != "nonce-a" || got.CodeVerifier != "verifier-a" {
		t.Fatalf("consume=%+v ok=%v", got, ok)
	}
	if _, ok := store.consume("state-a"); ok {
		t.Fatal("replayed state must not be consumable")
	}

	expired := &oidcTransaction{State: "state-b", CreatedAt: time.Now().Add(-time.Hour)}
	if err := store.create(expired); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if _, ok := store.consume("state-b"); ok {
		t.Fatal("expired transaction must not be consumable")
	}

	bounded := newOIDCTxStore()
	bounded.max = 1
	if err := bounded.create(&oidcTransaction{State: "one", CreatedAt: time.Now()}); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if err := bounded.create(&oidcTransaction{State: "two", CreatedAt: time.Now()}); err == nil {
		t.Fatal("bounded store must refuse overflow")
	}
}

func TestOIDCExchangeStoreOneTime(t *testing.T) {
	store := newOIDCExchangeStore()
	code, err := store.create("session-token-value", "192.0.2.10")
	if err != nil || code == "" {
		t.Fatalf("create code=%q err=%v", code, err)
	}
	token, ok := store.consume(code, "192.0.2.10")
	if !ok || token != "session-token-value" {
		t.Fatalf("consume token=%q ok=%v", token, ok)
	}
	if _, ok := store.consume(code, "192.0.2.10"); ok {
		t.Fatal("exchange code replay must be refused")
	}

	other, err := store.create("other-token", "192.0.2.20")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	if _, ok := store.consume(other, "192.0.2.99"); ok {
		t.Fatal("a different client must not redeem the code")
	}
}

func TestOIDCHandleLoginUsesPKCEStateNonceAndCookieFlags(t *testing.T) {
	var issuer string
	var live *httptest.Server
	live = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/.well-known/openid-configuration") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"id_token_signing_alg_values_supported":["RS256"]}`,
				live.URL, live.URL+"/authorize", live.URL+"/token", live.URL+"/jwks")
			return
		}
		http.NotFound(w, r)
	}))
	issuer = live.URL
	defer live.Close()

	m, store := newTestManager(t, testAuthConfig())
	h, err := NewOIDCHandler(m, store, &config.OIDCConfig{
		Enabled: true, IssuerURI: issuer, ClientID: "carbon-panel",
		RedirectURL: issuer + "/api/v1/auth/oidc/callback",
	}, logger.New())
	if err != nil {
		t.Fatalf("NewOIDCHandler: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/login", nil)
	h.HandleLogin(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse redirect: %v", err)
	}
	q := u.Query()
	if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		t.Fatalf("expected code+S256, got %v", q)
	}
	for _, key := range []string{"code_challenge", "state", "nonce"} {
		if q.Get(key) == "" {
			t.Fatalf("missing %s in %s", key, loc)
		}
	}
	if q.Get("code_verifier") != "" {
		t.Fatal("code_verifier must never appear in the authorization URL")
	}
	cookies := rec.Result().Cookies()
	var stateCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == oidcStateCookie {
			stateCookie = c
		}
	}
	if stateCookie == nil || !stateCookie.HttpOnly || stateCookie.SameSite != http.SameSiteLaxMode || stateCookie.MaxAge != 300 || stateCookie.Path != "/" {
		t.Fatalf("state cookie flags: %+v", stateCookie)
	}
	if stateCookie.Value != q.Get("state") {
		t.Fatal("state cookie must bind to the redirect state")
	}
}

func oidcTestHandler(t *testing.T) (*Manager, *OIDCHandler, *db.Store) {
	t.Helper()
	m, store := newTestManager(t, testAuthConfig())
	h := &OIDCHandler{
		manager: m,
		store:   store,
		config: &config.OIDCConfig{
			IssuerURI: "https://issuer-a.test", RoleClaim: "groups",
			DefaultRoles: []string{"user"}, AbsentClaimPolicy: "deny",
			AllowRawClaimRoles: true,
		},
		log: logger.New(),
	}
	return m, h, store
}

func TestOIDCIdentityNoImplicitAdminNoAutoLink(t *testing.T) {
	ctx := context.Background()
	m, h, store := oidcTestHandler(t)

	u1, err := h.findOrCreateOIDCUser(ctx, "https://issuer-a.test", "same-sub", "alice", "alice@example.test")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	u1again, err := h.findOrCreateOIDCUser(ctx, "https://issuer-a.test", "same-sub", "alice", "alice@example.test")
	if err != nil || u1again.ID != u1.ID {
		t.Fatalf("issuer+subject must resolve to same user: %+v err=%v", u1again, err)
	}
	roles, _ := store.GetUserRoleNames(ctx, u1.ID)
	for _, role := range roles {
		if isPrivilegedRole(role) {
			t.Fatalf("OIDC sign-in implicitly granted privileged role %q", role)
		}
	}

	// The same subject from another issuer must not associate with, or inherit
	// anything from, the first account. It requires explicit admin linking.
	if _, err := h.findOrCreateOIDCUser(ctx, "https://issuer-b.test", "same-sub", "alice-b", ""); err != ErrLinkRequired {
		t.Fatalf("same subject from a different issuer must require linking, got %v", err)
	}
	_ = u1

	legacy := &db.User{ID: "legacy-1", Username: "legacy", AuthProvider: "oidc", OIDCSubject: "legacy-sub", IsActive: true}
	if err := store.CreateUser(ctx, legacy); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	if _, err := h.findOrCreateOIDCUser(ctx, "https://issuer-a.test", "legacy-sub", "legacy", ""); err != ErrLinkRequired {
		t.Fatalf("issuer-less legacy row must require explicit linking, got %v", err)
	}
	_ = m
}

func TestOIDCRawClaimCannotGrantPrivilegedRole(t *testing.T) {
	_, h, _ := oidcTestHandler(t)
	got := h.resolveClaimRoles(map[string]any{"groups": []any{"admin", "user", "root"}})
	for _, role := range got {
		if isPrivilegedRole(role) {
			t.Fatalf("raw claim granted privileged role %q", role)
		}
	}
	if len(got) != 1 || got[0] != "user" {
		t.Fatalf("roles=%v", got)
	}

	h.config.RoleMapping = map[string]string{"admins": "admin"}
	got = h.resolveClaimRoles(map[string]any{"groups": []any{"admins"}})
	if len(got) != 1 || got[0] != "admin" {
		t.Fatalf("explicit reviewed mapping must allow admin: %v", got)
	}
}

type fullOIDCFixture struct {
	issuer           string
	serverURL        string
	issuerPath       string
	key              *rsa.PrivateKey
	keyID            string
	nonce            string
	tokenCalls       int
	lastCodeVerifier string
	lastClientID     string
	lastClientSecret string

	// Mutable fixture controls for harmless failure/rollover scenarios.
	claimsOverride map[string]any
	signingKey     *rsa.PrivateKey
	signingKID     string
	jwksKey        *rsa.PrivateKey
	jwksKID        string
	extraBody      string
}

func newFullOIDCFixture(t *testing.T) (*fullOIDCFixture, *httptest.Server) {
	return newOIDCProviderFixture(t, "")
}

// newOIDCProviderFixture starts a tenant-independent OIDC discovery/provider
// fixture. issuerPath models provider-specific issuer shapes (for example
// "/oauth2/default" or "/application/o/carbon-panel/") without live tenants.
func newOIDCProviderFixture(t *testing.T, issuerPath string) (*fullOIDCFixture, *httptest.Server) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	f := &fullOIDCFixture{
		issuerPath: issuerPath,
		key:        key, keyID: "fixture-key",
		signingKey: key, signingKID: "fixture-key",
		jwksKey: key, jwksKID: "fixture-key",
	}
	prefix := strings.TrimSuffix(issuerPath, "/")
	discoveryPath := "/.well-known/openid-configuration"
	if prefix != "" {
		discoveryPath = prefix + "/.well-known/openid-configuration"
	}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc(discoveryPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"issuer":%q,"authorization_endpoint":%q,"token_endpoint":%q,"jwks_uri":%q,"userinfo_endpoint":%q,"id_token_signing_alg_values_supported":["RS256"]}`,
			f.issuer, f.issuer+"/authorize", f.issuer+"/token", f.issuer+"/jwks", f.issuer+"/userinfo")
	})
	mux.HandleFunc(prefix+"/token", func(w http.ResponseWriter, r *http.Request) {
		f.tokenCalls++
		_ = r.ParseForm()
		f.lastCodeVerifier = r.Form.Get("code_verifier")
		f.lastClientID, f.lastClientSecret, _ = r.BasicAuth()
		if f.lastClientID == "" {
			f.lastClientID = r.Form.Get("client_id")
			f.lastClientSecret = r.Form.Get("client_secret")
		}
		claims := jwt.MapClaims{
			"iss": f.issuer, "aud": "carbon-panel", "sub": "fixture-subject",
			"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
			"nonce": f.nonce, "groups": []string{"user"},
		}
		for key, value := range f.claimsOverride {
			if value == nil {
				delete(claims, key)
			} else {
				claims[key] = value
			}
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		tok.Header["kid"] = f.signingKID
		raw, err := tok.SignedString(f.signingKey)
		if err != nil {
			http.Error(w, "sign failed", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "fixture-access-token", "token_type": "Bearer",
			"expires_in": 3600, "id_token": raw,
		})
	})
	mux.HandleFunc(prefix+"/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": f.jwksKID, "use": "sig", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(f.jwksKey.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(f.jwksKey.PublicKey.E)).Bytes()),
		}}})
	})
	mux.HandleFunc(prefix+"/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sub":"fixture-subject"}`))
	})
	mux.HandleFunc(prefix+"/extra", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(f.extraBody))
	})
	srv = httptest.NewServer(mux)
	f.serverURL = srv.URL
	f.issuer = srv.URL + issuerPath
	return f, srv
}

func TestOIDCFullCallbackPKCENonceReplayAndExchange(t *testing.T) {
	f, srv := newFullOIDCFixture(t)
	defer srv.Close()

	m, store := newTestManager(t, testAuthConfig())
	h, err := NewOIDCHandler(m, store, &config.OIDCConfig{
		Enabled: true, IssuerURI: f.issuer, ClientID: "carbon-panel", ClientSecret: "fixture-secret",
		RedirectURL: f.issuer + "/api/v1/auth/oidc/callback",
		RoleClaim:   "groups", RoleMapping: map[string]string{"user": "user"},
		AbsentClaimPolicy: "deny",
	}, logger.New())
	if err != nil {
		t.Fatalf("NewOIDCHandler: %v", err)
	}

	const remote = "192.0.2.55:41000"
	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/login", nil)
	loginReq.RemoteAddr = remote
	h.HandleLogin(loginRec, loginReq)
	if loginRec.Code != http.StatusFound {
		t.Fatalf("login status=%d", loginRec.Code)
	}
	authURL, err := url.Parse(loginRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("auth URL: %v", err)
	}
	state := authURL.Query().Get("state")
	f.nonce = authURL.Query().Get("nonce")
	tx := h.txStore.entries[state]
	if tx == nil || tx.CodeVerifier == "" || tx.Nonce != f.nonce {
		t.Fatalf("transaction not bound: %+v", tx)
	}

	callbackReq := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=fixture-code&state="+url.QueryEscape(state), nil)
		req.RemoteAddr = remote
		req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: state})
		return req
	}
	cbRec := httptest.NewRecorder()
	h.HandleCallback(cbRec, callbackReq())
	if cbRec.Code != http.StatusFound {
		t.Fatalf("callback status=%d body=%s", cbRec.Code, cbRec.Body.String())
	}
	returnURL, err := url.Parse(cbRec.Header().Get("Location"))
	if err != nil || !strings.HasPrefix(returnURL.Path, "/login") || returnURL.Query().Get("error") != "" {
		t.Fatalf("unexpected callback return: %v err=%v", returnURL, err)
	}
	exchangeCode := returnURL.Query().Get("code")
	if exchangeCode == "" {
		t.Fatal("callback must return a one-time exchange code, not a token")
	}
	if f.tokenCalls != 1 || f.lastCodeVerifier != tx.CodeVerifier {
		t.Fatalf("token calls=%d verifier_match=%v", f.tokenCalls, f.lastCodeVerifier == tx.CodeVerifier)
	}
	if f.lastClientID != "carbon-panel" || f.lastClientSecret != "fixture-secret" {
		t.Fatal("confidential client credentials were not sent to the token endpoint")
	}

	// Replayed callback must be refused and must not contact the IdP again.
	replayRec := httptest.NewRecorder()
	h.HandleCallback(replayRec, callbackReq())
	replayURL, _ := url.Parse(replayRec.Header().Get("Location"))
	if replayURL.Query().Get("error") != "invalid_state" || f.tokenCalls != 1 {
		t.Fatalf("replay error=%q tokenCalls=%d", replayURL.Query().Get("error"), f.tokenCalls)
	}

	exchangeReq := func(code, addr string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oidc/exchange", strings.NewReader(`{"code":"`+code+`"}`))
		req.RemoteAddr = addr
		return req
	}
	exRec := httptest.NewRecorder()
	h.HandleExchange(exRec, exchangeReq(exchangeCode, remote))
	if exRec.Code != http.StatusOK || !strings.Contains(exRec.Body.String(), `"token"`) {
		t.Fatalf("exchange status=%d body=%s", exRec.Code, exRec.Body.String())
	}
	replayEx := httptest.NewRecorder()
	h.HandleExchange(replayEx, exchangeReq(exchangeCode, remote))
	if replayEx.Code != http.StatusBadRequest {
		t.Fatalf("exchange replay status=%d", replayEx.Code)
	}
}

// oidcCallbackWithFixture drives one harmless login+callback against a local
// OIDC fixture and returns the callback redirect Location for assertions.
func oidcCallbackWithFixture(t *testing.T, configure func(*fullOIDCFixture), after func(*testing.T, *db.Store, string)) string {
	t.Helper()
	f, srv := newFullOIDCFixture(t)
	defer srv.Close()
	if configure != nil {
		configure(f)
	}
	m, store := newTestManager(t, testAuthConfig())
	cfg := &config.OIDCConfig{
		Enabled: true, IssuerURI: f.issuer, ClientID: "carbon-panel", ClientSecret: "fixture-secret",
		RedirectURL: f.issuer + "/api/v1/auth/oidc/callback",
		RoleClaim:   "groups", RoleMapping: map[string]string{"user": "user", "admin": "admin"},
		AbsentClaimPolicy: "deny",
	}
	if f.extraBody != "" {
		cfg.ExtraClaimsURL = f.issuer + "/extra"
		cfg.ExtraClaimsName = "groups"
	}
	h, err := NewOIDCHandler(m, store, cfg, logger.New())
	if err != nil {
		t.Fatalf("NewOIDCHandler: %v", err)
	}
	const remote = "192.0.2.77:12345"
	loginRec := httptest.NewRecorder()
	loginReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/login", nil)
	loginReq.RemoteAddr = remote
	h.HandleLogin(loginRec, loginReq)
	authURL, err := url.Parse(loginRec.Header().Get("Location"))
	if err != nil {
		t.Fatalf("auth URL: %v", err)
	}
	state := authURL.Query().Get("state")
	f.nonce = authURL.Query().Get("nonce")

	cbRec := httptest.NewRecorder()
	cbReq := httptest.NewRequest(http.MethodGet, "/api/v1/auth/oidc/callback?code=fixture-code&state="+url.QueryEscape(state), nil)
	cbReq.RemoteAddr = remote
	cbReq.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: state})
	h.HandleCallback(cbRec, cbReq)
	if after != nil {
		after(t, store, f.issuer)
	}
	return cbRec.Header().Get("Location")
}

func TestOIDCBadTokenRefusals(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*fullOIDCFixture)
		wantError string
	}{
		{"invalid issuer", func(f *fullOIDCFixture) { f.claimsOverride = map[string]any{"iss": "https://evil.example"} }, "access_denied"},
		{"wrong audience", func(f *fullOIDCFixture) { f.claimsOverride = map[string]any{"aud": "other-client"} }, "access_denied"},
		{"wrong nonce", func(f *fullOIDCFixture) { f.claimsOverride = map[string]any{"nonce": "wrong-nonce"} }, "access_denied"},
		{"missing nonce", func(f *fullOIDCFixture) { f.claimsOverride = map[string]any{"nonce": nil} }, "access_denied"},
		{"expired token", func(f *fullOIDCFixture) {
			f.claimsOverride = map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}
		}, "access_denied"},
		{"signature failure", func(f *fullOIDCFixture) {
			other, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatalf("other key: %v", err)
			}
			f.signingKey = other
		}, "access_denied"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loc := oidcCallbackWithFixture(t, tc.configure, nil)
			u, err := url.Parse(loc)
			if err != nil {
				t.Fatalf("callback redirect: %v", err)
			}
			if got := u.Query().Get("error"); got != tc.wantError {
				t.Fatalf("error=%q want %q (location %s)", got, tc.wantError, loc)
			}
		})
	}
}

func TestOIDCJWKSRolloverSucceeds(t *testing.T) {
	loc := oidcCallbackWithFixture(t, func(f *fullOIDCFixture) {
		rotated, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("rotated key: %v", err)
		}
		f.signingKey, f.signingKID = rotated, "rotated-key"
		f.jwksKey, f.jwksKID = rotated, "rotated-key"
	}, nil)
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("callback redirect: %v", err)
	}
	if u.Query().Get("error") != "" || u.Query().Get("code") == "" {
		t.Fatalf("rollover should still sign in: %s", loc)
	}
}

func TestOIDCExtraClaimsCannotElevateRoles(t *testing.T) {
	loc := oidcCallbackWithFixture(t, func(f *fullOIDCFixture) {
		// The extra-claims endpoint tries to replace the role claim with admin.
		f.extraBody = `["admin"]`
	}, func(t *testing.T, store *db.Store, issuer string) {
		user, err := store.GetUserByOIDCIssuerAndSubject(context.Background(), issuer, "fixture-subject")
		if err != nil {
			t.Fatalf("user lookup: %v", err)
		}
		roles, err := store.GetUserRoleNames(context.Background(), user.ID)
		if err != nil {
			t.Fatalf("roles: %v", err)
		}
		for _, role := range roles {
			if isPrivilegedRole(role) {
				t.Fatalf("extra claims elevated privileges: roles=%v", roles)
			}
		}
		if len(roles) != 1 || roles[0] != "user" {
			t.Fatalf("roles=%v", roles)
		}
	})
	u, err := url.Parse(loc)
	if err != nil || u.Query().Get("error") != "" {
		t.Fatalf("callback should succeed: %s err=%v", loc, err)
	}
}

func TestFetchExtraClaimsBoundsResponseBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", maxExtraClaimsBody+1)))
	}))
	defer srv.Close()
	h := testOIDCHandler("", nil, false)
	h.config.ExtraClaimsURL = srv.URL
	h.httpClient = srv.Client()
	if _, err := h.fetchExtraClaims(context.Background(), "tok"); err == nil {
		t.Fatal("oversized extra-claims response must be refused")
	}
}

func TestSessionValidatorContract(t *testing.T) {
	ctx := context.Background()
	m, _ := newTestManager(t, testAuthConfig())
	id := mustLocalUser(t, m, "validator-user")
	token := mustLogin(t, m, "validator-user", testPassword)

	v := NewSessionValidator(m)
	snap, err := v.Validate(ctx, token)
	if err != nil || !snap.Valid || snap.UserID != id {
		t.Fatalf("snapshot=%+v err=%v", snap, err)
	}
	if _, err := v.Validate(ctx, ""); err == nil {
		t.Fatal("empty token must be refused")
	}
	if snap, err := v.Validate(ctx, "not-a-token"); err == nil || snap.Valid {
		t.Fatalf("invalid token snapshot=%+v err=%v", snap, err)
	}
}
