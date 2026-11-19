// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/rbac"
)

// measuredProxyFixture is a loopback renderer plus the real production proxy
// handler. sourceAt is stamped by the renderer the instant it starts answering,
// so a client measurement is source -> authenticated proxy -> full body read.
// It is a fixture only: no live renderer, container, browser or public network.
type measuredProxyFixture struct {
	gateway *httptest.Server
	key     string
	source  chan time.Time
}

func newMeasuredProxyFixture(t *testing.T, payload []byte) *measuredProxyFixture {
	t.Helper()
	source := make(chan time.Time, 128)
	renderer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		source <- time.Now()
		w.Header().Set("Content-Type", "image/png")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(renderer.Close)

	store := newFakeStore()
	store.addServer("srv-a", "default")
	users := &fakeAuth{users: map[string]*auth.AuthenticatedUser{
		"tok-a": {ID: "user-a", Username: "alice", Roles: []string{"user"}},
	}}
	enforcer := &fakeEnforcer{allow: func(roles []string, resource, action, objectID string) (bool, error) {
		return resource == rbac.ResourceWorldView && action == rbac.ActionRead && objectID == "srv-a", nil
	}}
	ep := endpointForFixture(t, renderer.URL, "", RendererBlueMap)
	resolver := NewResolver(Config{Endpoints: []Endpoint{ep}}, &fakeLocator{local: map[string]bool{"default": true}})
	sessions := NewSessionRegistry()
	key, _ := sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
	handler := NewProxyHandler(ProxyDeps{
		Auth: users, Authorizer: enforcer, Subusers: store, Servers: store,
		Resolver: resolver, Sessions: sessions,
	})
	gateway := httptest.NewServer(handler)
	t.Cleanup(gateway.Close)
	return &measuredProxyFixture{gateway: gateway, key: key, source: source}
}

// TestProxySourceToClientLatencyMeasuredFixture records an actual bounded
// source-to-client latency distribution on the production proxy path. This is
// the backend leg of the acceptance "source -> UI latency" evidence; the browser
// leg is measured separately on an owned fixture and reported with the same
// limits. No renderer or live "map" timing is claimed.
func TestProxySourceToClientLatencyMeasuredFixture(t *testing.T) {
	payload := bytes.Repeat([]byte("t"), 64<<10)
	f := newMeasuredProxyFixture(t, payload)

	const iterations = 30
	latencies := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		req, err := http.NewRequest(http.MethodGet, f.gateway.URL+ProxyPrefix+f.key+"/tiles/0.png", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer tok-a")
		resp, err := f.gateway.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		sourceAt := <-f.source
		if resp.StatusCode != http.StatusOK || !bytes.Equal(body, payload) {
			t.Fatalf("sample %d: status=%d bytes=%d", i, resp.StatusCode, len(body))
		}
		latencies = append(latencies, time.Since(sourceAt))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[len(latencies)/2]
	p95 := latencies[(len(latencies)*95)/100]
	max := latencies[len(latencies)-1]
	t.Logf("MEASURED source-to-client latency fixture: n=%d payload=%dKiB p50=%s p95=%s max=%s",
		iterations, len(payload)/1024, p50, p95, max)
	// Bounded loopback budget: a shared Windows runner can be slow but a 250ms
	// p95 for a 64 KiB in-process asset is a generous, honest ceiling.
	if p95 > 250*time.Millisecond {
		t.Fatalf("source-to-client p95 %s exceeds 250ms budget (max %s)", p95, max)
	}
}

// TestProxyStreamingResourceBudgetMeasuredFixture measures the panel-side
// memory budget of the production streaming seam with a full 8 MiB asset. It
// proves the controller never allocates an asset-sized buffer. Renderer process
// RAM and on-disk tile storage are outside this fixture (no renderer exists).
func TestProxyStreamingResourceBudgetMeasuredFixture(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, MaxAssetBytes)
	// One warm-up so the 32 KiB copy buffer and io.CopyBuffer internals are not
	// counted as first-call initialization.
	if n := copyAsset(io.Discard, bytes.NewReader(payload), nil); n != MaxAssetBytes {
		t.Fatalf("warm-up copied %d, want %d", n, MaxAssetBytes)
	}
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	n := copyAsset(io.Discard, bytes.NewReader(payload), nil)
	runtime.ReadMemStats(&after)
	if n != MaxAssetBytes {
		t.Fatalf("copied %d bytes, want %d", n, MaxAssetBytes)
	}
	var delta uint64
	if after.HeapAlloc > before.HeapAlloc {
		delta = after.HeapAlloc - before.HeapAlloc
	}
	t.Logf("MEASURED proxy streaming budget: asset=%dKiB buffer=%dKiB heap delta=%dKiB",
		len(payload)/1024, proxyCopyBufferSize/1024, delta/1024)
	if delta > 2<<20 {
		t.Fatalf("proxy streaming heap delta %d KiB exceeds 2 MiB budget", delta/1024)
	}

	// A truncated oversize response must also stay bounded: the LimitedReader
	// stops at MaxAssetBytes even when the source is larger.
	oversize := io.MultiReader(bytes.NewReader(payload), bytes.NewReader(payload))
	if n := copyAsset(io.Discard, oversize, nil); n != MaxAssetBytes {
		t.Fatalf("oversize copy = %d, want capped %d", n, MaxAssetBytes)
	}
}
