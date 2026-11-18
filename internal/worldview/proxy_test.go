// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/rbac"
)

// proxyFixture is a local httptest renderer plus in-memory fakes. It is NOT a
// live renderer, container or browser; the assertions cover proxy authz and
// transport behavior only.
type proxyFixture struct {
	handler  http.Handler
	sessions *SessionRegistry
	doer     *countingDoer
	store    *fakeStore
}

func newLocalProxyFixture(t *testing.T, body string, status int) *proxyFixture {
	t.Helper()
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Connection", "keep-alive") // hop-by-hop: must not be forwarded
		w.Header().Set("Set-Cookie", "renderer=secret")
		w.Header().Set("X-Renderer-Only", "secret")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(renderer.Close)

	store := newFakeStore()
	store.addServer("srv-a", "default")
	store.addServer("srv-b", "remote-1")
	store.addServer("srv-r", "remote-1")
	users := &fakeAuth{users: map[string]*auth.AuthenticatedUser{
		"tok-a":     {ID: "user-a", Username: "alice", Roles: []string{"user"}},
		"tok-b":     {ID: "user-b", Username: "bob", Roles: []string{"user"}},
		"tok-guest": {ID: "guest", Username: "guest", Roles: []string{"guest"}},
	}}
	enforcer := &fakeEnforcer{allow: func(roles []string, resource, action, objectID string) (bool, error) {
		if resource != rbac.ResourceWorldView || action != rbac.ActionRead {
			return false, nil
		}
		userRole := len(roles) > 0 && roles[0] == "user"
		return userRole && (objectID == "srv-a" || objectID == "srv-r"), nil
	}}
	ep := endpointForFixture(t, renderer.URL, "", RendererBlueMap)
	resolver := NewResolver(Config{Endpoints: []Endpoint{ep}}, &fakeLocator{local: map[string]bool{
		"default": true, "remote-1": false,
	}})
	doer := newCountingDoer(renderer.Client())
	sessions := NewSessionRegistry()
	handler := NewProxyHandler(ProxyDeps{
		Auth: users, Authorizer: enforcer, Subusers: store, Servers: store,
		Resolver: resolver, Sessions: sessions, Client: doer,
	})
	return &proxyFixture{handler: handler, sessions: sessions, doer: doer, store: store}
}

func callProxy(h http.Handler, key, token, method string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, ProxyPrefix+key+"/tiles/0.png?scale=1", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestProxyDeniesUnauthenticatedAndUnauthorizedBeforeUpstreamFixture checks
// 401/403 happens before the renderer is contacted at all.
func TestProxyDeniesUnauthenticatedAndUnauthorizedBeforeUpstreamFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "png-bytes", http.StatusOK)
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	cases := []struct {
		name       string
		key, token string
		want       int
	}{
		{"no token", key, "", http.StatusUnauthorized},
		{"invalid token", key, "bad-token", http.StatusUnauthorized},
		{"reader role denied", key, "tok-guest", http.StatusForbidden},
		{"unknown key", "no-such-key", "tok-a", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := f.doer.hits()
			rec := callProxy(f.handler, tc.key, tc.token, http.MethodGet)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if f.doer.hits() != before {
				t.Fatal("renderer was contacted for a denied request")
			}
		})
	}
}

// TestProxyKeyIsBoundToIssuingUserFixture proves a key issued to user A cannot
// be replayed by user B even when B also holds world_view/read.
func TestProxyKeyIsBoundToIssuingUserFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "png-bytes", http.StatusOK)
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
	before := f.doer.hits()
	rec := callProxy(f.handler, key, "tok-b", http.MethodGet)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if f.doer.hits() != before {
		t.Fatal("cross-user key replay contacted the renderer")
	}
}

// TestProxyExpiredKeyFixture rejects expired keys with no upstream request.
func TestProxyExpiredKeyFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "png-bytes", http.StatusOK)
	start := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	now := start
	f.sessions.now = func() time.Time { return now }
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
	now = start.Add(2 * time.Minute)
	before := f.doer.hits()
	rec := callProxy(f.handler, key, "tok-a", http.MethodGet)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if f.doer.hits() != before {
		t.Fatal("expired key contacted the renderer")
	}
}

// TestProxyScopedKeyCannotReachAnotherServerFixture confirms the server scope
// comes from the session record, never from a query string or header the
// browser can manipulate.
func TestProxyScopedKeyCannotReachAnotherServerFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "png-bytes", http.StatusOK)
	key, _ := f.sessions.Issue("srv-b", "world-b", "user-a", time.Minute)
	before := f.doer.hits()
	req := httptest.NewRequest(http.MethodGet, ProxyPrefix+key+"/tiles/0.png?server_id=srv-a", nil)
	req.Header.Set("Authorization", "Bearer tok-a")
	req.Header.Set("X-Server-ID", "srv-a")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if f.doer.hits() != before {
		t.Fatal("cross-server key request contacted the renderer")
	}
}

// TestProxyRemoteNodeWithoutEndpointFixture verifies a remote node with no
// node-local renderer endpoint yields 503 and never serves controller-local
// data. The only configured fixture renderer belongs to the local node.
func TestProxyRemoteNodeWithoutEndpointFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "png-bytes", http.StatusOK)
	key, _ := f.sessions.Issue("srv-r", "world-r", "user-a", time.Minute)
	before := f.doer.hits()
	rec := callProxy(f.handler, key, "tok-a", http.MethodGet)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", rec.Code, rec.Body.String())
	}
	if f.doer.hits() != before {
		t.Fatal("remote-node-without-endpoint contacted the controller-local renderer")
	}
}

// TestProxyBoundsResponseBodyFixture checks the 8 MiB cap is enforced before a
// response body is written and that only safe headers reach the browser.
func TestProxyBoundsResponseBodyFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "png-bytes", http.StatusOK)
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
	rec := callProxy(f.handler, key, "tok-a", http.MethodGet)
	if rec.Code != http.StatusOK || rec.Body.String() != "png-bytes" {
		t.Fatalf("status/body = %d/%q", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff header")
	}
	if rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	for _, name := range []string{"Connection", "Set-Cookie", "X-Renderer-Only"} {
		if rec.Header().Get(name) != "" {
			t.Errorf("hop-by-hop/renderer header %s leaked: %q", name, rec.Header().Get(name))
		}
	}

	// A canned oversized fixture avoids allocating 8 MiB in the test.
	huge := &funcDoer{fn: func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header: http.Header{
				"Content-Length": []string{strconv.Itoa(MaxAssetBytes + 1)},
				"Content-Type":   []string{"image/png"},
			},
			Body: io.NopCloser(strings.NewReader("small")),
		}, nil
	}}
	// A dedicated handler isolates the canned oversized response.
	registry := NewSessionRegistry()
	localKey, _ := registry.Issue("srv-a", "world-a", "user-a", time.Minute)
	oversizedHandler := NewProxyHandler(ProxyDeps{
		Auth: &fakeAuth{users: map[string]*auth.AuthenticatedUser{
			"tok-a": {ID: "user-a", Username: "alice", Roles: []string{"user"}},
		}},
		Authorizer: &fakeEnforcer{allow: func([]string, string, string, string) (bool, error) { return true, nil }},
		Resolver: NewResolver(Config{Endpoints: []Endpoint{endpointForFixture(t, "http://127.0.0.1:1", "", RendererBlueMap)}},
			&fakeLocator{local: map[string]bool{"default": true}}),
		Servers: f.store, Sessions: registry, Client: huge,
	})
	bigRec := callProxy(oversizedHandler, localKey, "tok-a", http.MethodGet)
	if bigRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized asset status = %d, want 413", bigRec.Code)
	}
	if huge.hits() != 1 {
		t.Fatalf("upstream hits = %d, want 1", huge.hits())
	}
}

// TestProxyRejectsUpgradeAndNonSafeMethodsFixture keeps the panel /ws as the
// only WebSocket route and prevents arbitrary methods reaching the renderer.
func TestProxyRejectsUpgradeAndNonSafeMethodsFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "png-bytes", http.StatusOK)
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	t.Run("POST", func(t *testing.T) {
		before := f.doer.hits()
		if rec := callProxy(f.handler, key, "tok-a", http.MethodPost); rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want 405", rec.Code)
		}
		if f.doer.hits() != before {
			t.Fatal("POST contacted the renderer")
		}
	})
	t.Run("websocket upgrade", func(t *testing.T) {
		before := f.doer.hits()
		req := httptest.NewRequest(http.MethodGet, ProxyPrefix+key+"/tiles/0.png", nil)
		req.Header.Set("Authorization", "Bearer tok-a")
		req.Header.Set("Upgrade", "websocket")
		req.Header.Set("Connection", "Upgrade")
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rec.Code)
		}
		if f.doer.hits() != before {
			t.Fatal("upgrade request contacted the renderer")
		}
	})
}

// TestProxyHeadAndCancellationFixture verifies HEAD is supported and that a
// cancelled client context does not complete an upstream asset fetch.
func TestProxyHeadAndCancellationFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "png-bytes", http.StatusOK)
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
	if rec := callProxy(f.handler, key, "tok-a", http.MethodHead); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("HEAD status/body = %d/%q", rec.Code, rec.Body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, ProxyPrefix+key+"/tiles/0.png", nil).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer tok-a")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Fatal("cancelled request completed with 200")
	}
}

// TestProxyRendererRedirectIsNotFollowedFixture proves a renderer 3xx cannot
// turn an allowlisted node-local address into an external/controller-local
// request under the panel origin. Fixture only: no real renderer or container.
func TestProxyRendererRedirectIsNotFollowedFixture(t *testing.T) {
	externalHits := 0
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		externalHits++
		_, _ = io.WriteString(w, "external-secret")
	}))
	defer external.Close()

	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, external.URL+"/secret", http.StatusFound)
	}))
	defer renderer.Close()

	store := newFakeStore()
	store.addServer("srv-a", "default")
	users := &fakeAuth{users: map[string]*auth.AuthenticatedUser{
		"tok-a": {ID: "user-a", Username: "alice", Roles: []string{"user"}},
	}}
	enforcer := &fakeEnforcer{allow: func(roles []string, resource, action, objectID string) (bool, error) {
		return resource == rbac.ResourceWorldView && action == rbac.ActionRead && objectID == "srv-a", nil
	}}
	resolver := NewResolver(
		Config{Endpoints: []Endpoint{endpointForFixture(t, renderer.URL, "", RendererBlueMap)}},
		&fakeLocator{local: map[string]bool{"default": true}},
	)
	sessions := NewSessionRegistry()
	key, _ := sessions.Issue("srv-a", "world=srv-a%2Flevel", "user-a", time.Minute)
	handler := NewProxyHandler(ProxyDeps{
		Auth: users, Authorizer: enforcer, Subusers: store, Servers: store,
		Resolver: resolver, Sessions: sessions,
		Client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: refuseRedirects},
	})

	rec := callProxy(handler, key, "tok-a", http.MethodGet)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("redirecting renderer: got %d, want 502", rec.Code)
	}
	if externalHits != 0 {
		t.Fatalf("redirect target was contacted %d times", externalHits)
	}
	if strings.Contains(rec.Body.String(), "external-secret") {
		t.Fatal("external renderer body was relayed under the panel origin")
	}
}

// TestProxySubuserGrantFixture checks the per-server grant fallback: a role
// without world_view/read can still use a granted server's same-origin map.
// Fixture only; no live renderer or panel auth is involved.
func TestProxySubuserGrantFixture(t *testing.T) {
	f := newLocalProxyFixture(t, "tile-bytes", http.StatusOK)
	f.store.grant("guest", "srv-a", "world_view.read")
	key, _ := f.sessions.Issue("srv-a", "world=srv-a%2Flevel", "guest", time.Minute)
	rec := callProxy(f.handler, key, "tok-guest", http.MethodGet)
	if rec.Code != http.StatusOK {
		t.Fatalf("subuser-granted proxy: got %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if f.doer.hits() == 0 {
		t.Fatal("granted proxy did not contact the renderer")
	}
}

func TestProxyClientRefusesAllRedirectsUnit(t *testing.T) {
	if err := newProxyClient().CheckRedirect(&http.Request{}, nil); err == nil {
		t.Fatal("production proxy client follows redirects")
	}
	if err := newHealthClient().CheckRedirect(&http.Request{}, nil); err == nil {
		t.Fatal("renderer health client follows redirects")
	}
}
