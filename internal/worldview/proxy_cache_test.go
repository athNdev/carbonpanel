// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/rbac"
)

// cacheProxyFixture is a local httptest renderer that deliberately sends
// public/shared caching directives. Fixture only: no live renderer is involved.
type cacheProxyFixture struct {
	handler    http.Handler
	sessions   *SessionRegistry
	doer       *countingDoer
	upstreamMu sync.Mutex
	validators []string
}

func newCacheProxyFixture(t *testing.T, status int) *cacheProxyFixture {
	t.Helper()
	f := &cacheProxyFixture{}
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.upstreamMu.Lock()
		f.validators = []string{
			r.Header.Get("If-None-Match"), r.Header.Get("If-Modified-Since"),
		}
		f.upstreamMu.Unlock()
		// Hostile/shared caching directives that must never reach the browser.
		w.Header().Set("Cache-Control", "public, max-age=86400, s-maxage=604800")
		w.Header().Set("Expires", "Wed, 01 Jan 2031 00:00:00 GMT")
		w.Header().Set("Pragma", "public")
		w.Header().Set("Surrogate-Control", "max-age=86400")
		w.Header().Set("CDN-Cache-Control", "public, max-age=86400")
		w.Header().Set("ETag", `"renderer-etag"`)
		w.Header().Set("Last-Modified", "Wed, 01 Jan 2025 00:00:00 GMT")
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "protected-tile-bytes")
	}))
	t.Cleanup(renderer.Close)

	store := newFakeStore()
	store.addServer("srv-a", "default")
	users := &fakeAuth{users: map[string]*auth.AuthenticatedUser{
		"tok-a":     {ID: "user-a", Username: "alice", Roles: []string{"user"}},
		"tok-guest": {ID: "guest", Username: "guest", Roles: []string{"guest"}},
	}}
	enforcer := &fakeEnforcer{allow: func(roles []string, resource, action, objectID string) (bool, error) {
		return resource == rbac.ResourceWorldView && action == rbac.ActionRead &&
			len(roles) > 0 && roles[0] == "user" && objectID == "srv-a", nil
	}}
	resolver := NewResolver(
		Config{Endpoints: []Endpoint{endpointForFixture(t, renderer.URL, "", RendererBlueMap)}},
		&fakeLocator{local: map[string]bool{"default": true}},
	)
	f.sessions = NewSessionRegistry()
	f.doer = newCountingDoer(renderer.Client())
	f.handler = NewProxyHandler(ProxyDeps{
		Auth: users, Authorizer: enforcer, Subusers: store, Servers: store,
		Resolver: resolver, Sessions: f.sessions, Client: f.doer,
	})
	return f
}

func assertPrivateNoStoreFixture(t *testing.T, h http.Header) {
	t.Helper()
	if got := h.Get("Cache-Control"); got != proxyCachePolicy {
		t.Fatalf("Cache-Control = %q, want panel policy %q", got, proxyCachePolicy)
	}
	if got := h.Get("Pragma"); got != "no-cache" {
		t.Fatalf("Pragma = %q, want no-cache", got)
	}
	if got := h.Get("Expires"); got != "0" {
		t.Fatalf("Expires = %q, want 0", got)
	}
	for _, name := range []string{"ETag", "Last-Modified", "Surrogate-Control", "CDN-Cache-Control", "Vary", "Age"} {
		if got := h.Get(name); got != "" {
			t.Fatalf("%s leaked from renderer: %q", name, got)
		}
	}
}

// TestProxyCachePolicySuccessDeniedAndErrorFixture proves the panel-owned
// private/no-store policy is present on success, on authz failures and on error
// paths even when the renderer answers with public/shared cache directives.
func TestProxyCachePolicySuccessDeniedAndErrorFixture(t *testing.T) {
	f := newCacheProxyFixture(t, http.StatusOK)
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	success := callProxy(f.handler, key, "tok-a", http.MethodGet)
	if success.Code != http.StatusOK {
		t.Fatalf("success status = %d", success.Code)
	}
	assertPrivateNoStoreFixture(t, success.Header())
	if !strings.Contains(success.Body.String(), "protected-tile-bytes") {
		t.Fatalf("success body = %q", success.Body.String())
	}

	denied := callProxy(f.handler, key, "tok-guest", http.MethodGet)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("denied status = %d", denied.Code)
	}
	assertPrivateNoStoreFixture(t, denied.Header())

	unauthenticated := callProxy(f.handler, key, "", http.MethodGet)
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticated.Code)
	}
	assertPrivateNoStoreFixture(t, unauthenticated.Header())

	// Denied paths must not contact the renderer, so the only upstream hit is
	// the successful request.
	if f.doer.hits() != 1 {
		t.Fatalf("upstream hits = %d, want 1", f.doer.hits())
	}
}

// TestProxyConditionalValidatorsAreNotForwardedFixture proves client-side
// revalidation headers cannot produce an upstream 304 in this private path.
func TestProxyConditionalValidatorsAreNotForwardedFixture(t *testing.T) {
	f := newCacheProxyFixture(t, http.StatusOK)
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
	req := httptest.NewRequest(http.MethodGet, ProxyPrefix+key+"/tiles/0.png", nil)
	req.Header.Set("Authorization", "Bearer tok-a")
	req.Header.Set("If-None-Match", `"renderer-etag"`)
	req.Header.Set("If-Modified-Since", "Wed, 01 Jan 2025 00:00:00 GMT")
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertPrivateNoStoreFixture(t, rec.Header())
	f.upstreamMu.Lock()
	defer f.upstreamMu.Unlock()
	for i, name := range []string{"If-None-Match", "If-Modified-Since"} {
		if f.validators[i] != "" {
			t.Fatalf("upstream received %s = %q; validators must be stripped", name, f.validators[i])
		}
	}
}

// TestProxyUnsolicited304IsRefusedFixture covers the anomalous renderer 304
// explicitly: it must never be relayed as a blank protected asset or a redirect.
func TestProxyUnsolicited304IsRefusedFixture(t *testing.T) {
	f := newCacheProxyFixture(t, http.StatusNotModified)
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
	rec := callProxy(f.handler, key, "tok-a", http.MethodGet)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	assertPrivateNoStoreFixture(t, rec.Header())
}
