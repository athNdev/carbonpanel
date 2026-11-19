// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/auth"
)

// newAssetProxyFixture is a local httptest renderer with a caller-owned asset
// handler. It is NOT a live renderer or a browser: the assertions cover exact
// BlueMap gzip routing and the bytes delivered through the same authz gates.
func newAssetProxyFixture(t *testing.T, upstream http.Handler) *proxyFixture {
	t.Helper()
	return newAssetProxyFixtureWithKind(t, upstream, RendererBlueMap)
}

func newAssetProxyFixtureWithKind(t *testing.T, upstream http.Handler, kind RendererKind) *proxyFixture {
	t.Helper()
	renderer := httptest.NewServer(upstream)
	t.Cleanup(renderer.Close)

	store := newFakeStore()
	store.addServer("srv-a", "default")
	users := &fakeAuth{users: map[string]*auth.AuthenticatedUser{
		"tok-a": {ID: "user-a", Username: "alice", Roles: []string{"user"}},
	}}
	enforcer := &fakeEnforcer{allow: func([]string, string, string, string) (bool, error) { return true, nil }}
	ep := endpointForFixture(t, renderer.URL, "", kind)
	resolver := NewResolver(Config{Endpoints: []Endpoint{ep}}, &fakeLocator{local: map[string]bool{"default": true}})
	doer := newCountingDoer(renderer.Client())
	sessions := NewSessionRegistry()
	handler := NewProxyHandler(ProxyDeps{
		Auth: users, Authorizer: enforcer, Subusers: store, Servers: store,
		Resolver: resolver, Sessions: sessions, Client: doer,
	})
	return &proxyFixture{handler: handler, sessions: sessions, doer: doer, store: store}
}

func callAssetProxy(t *testing.T, f *proxyFixture, key, token, method, asset, acceptEncoding string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, ProxyPrefix+key+"/"+asset, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if acceptEncoding != "" {
		req.Header.Set("Accept-Encoding", acceptEncoding)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func gzipBytes(t *testing.T, plain []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func ungzip(t *testing.T, compressed []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gzip read: %v", err)
	}
	return out
}

// TestProxyGzipTextureVariantServesRealBytesFixture reproduces the exact
// showcase defect: a web root that stores ONLY textures.json.gz returns 404
// for the plain path the webapp requests. The proxy must retry the exact
// ".gz" sibling under the same gates and deliver the real gzipped bytes with
// the media type and Content-Encoding BlueMap's own webserver would use.
func TestProxyGzipTextureVariantServesRealBytesFixture(t *testing.T) {
	plain := []byte(`{"textures":["block/atlas.png"],"version":5}`)
	compressed := gzipBytes(t, plain)
	f := newAssetProxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maps/world/textures.json.gz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/gzip") // plain static server label
		_, _ = w.Write(compressed)
	}))
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	rec := callAssetProxy(t, f, key, "tok-a", http.MethodGet, "maps/world/textures.json", "gzip, deflate")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json (BlueMap strips .gz for the media type)", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), compressed) {
		t.Fatal("proxy did not serve the real gzipped texture bytes")
	}
	if got := ungzip(t, rec.Body.Bytes()); !bytes.Equal(got, plain) {
		t.Fatalf("decompressed texture = %q, want %q", got, plain)
	}
	if f.doer.hits() != 2 {
		t.Fatalf("upstream hits = %d, want plain 404 then exact .gz retry", f.doer.hits())
	}
}

// TestProxyGzipTileVariantServesRealBytesFixture covers the hi-res .prbm tile
// path: same gzip fallback, but the uncompressed media type is octet-stream.
func TestProxyGzipTileVariantServesRealBytesFixture(t *testing.T) {
	tile := []byte{0x42, 0x4d, 0x41, 0x50, 0x00, 0x01, 0x02, 0x03}
	compressed := gzipBytes(t, tile)
	f := newAssetProxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maps/world/tiles/0/x-1z2.prbm.gz" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(compressed)
	}))
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	rec := callAssetProxy(t, f, key, "tok-a", http.MethodGet, "maps/world/tiles/0/x-1z2.prbm", "gzip")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := ungzip(t, rec.Body.Bytes()); !bytes.Equal(got, tile) {
		t.Fatal("proxy did not serve the exact tile bytes")
	}
}

// TestProxyMissingTextureFailsClosedFixture proves a missing texture is never
// turned into an empty or black 200: only one exact retry is attempted and the
// 404 reaches the browser so capture readiness can fail.
func TestProxyMissingTextureFailsClosedFixture(t *testing.T) {
	f := newAssetProxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	rec := callAssetProxy(t, f, key, "tok-a", http.MethodGet, "maps/world/textures.json", "gzip")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for a missing texture (no black canvas accepted)", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want none on a failed asset", got)
	}
	if f.doer.hits() != 2 {
		t.Fatalf("upstream hits = %d, want 1 plain + 1 exact .gz retry only", f.doer.hits())
	}
}

// TestProxyLiteralCompressedRouteFixture covers clientDecompression mode: the
// browser requests the literal ".gz" asset and decompresses it itself, so the
// proxy must NOT claim Content-Encoding and must correct the static server's
// application/gzip label to the uncompressed media type.
func TestProxyLiteralCompressedRouteFixture(t *testing.T) {
	plain := []byte(`{"textures":[]}`)
	compressed := gzipBytes(t, plain)
	f := newAssetProxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maps/world/textures.json.gz" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(compressed)
	}))
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	rec := callAssetProxy(t, f, key, "tok-a", http.MethodGet, "maps/world/textures.json.gz", "gzip")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding = %q, want none for client-side decompression", got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if got := ungzip(t, rec.Body.Bytes()); !bytes.Equal(got, plain) {
		t.Fatal("literal .gz route did not serve the exact compressed bytes")
	}
	if f.doer.hits() != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1 (no fallback on a literal .gz path)", f.doer.hits())
	}
}

// TestProxyForwardsNativeContentEncodingFixture proves Accept-Encoding reaches
// BlueMap's own webserver: the native 200 + Content-Encoding response is
// relayed byte-for-byte without a redundant fallback request.
func TestProxyForwardsNativeContentEncodingFixture(t *testing.T) {
	plain := []byte(`{"maps":["world"]}`)
	compressed := gzipBytes(t, plain)
	var upstreamSaw string
	f := newAssetProxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/maps/world/settings.json" {
			http.NotFound(w, r)
			return
		}
		upstreamSaw = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		_, _ = w.Write(compressed)
	}))
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	rec := callAssetProxy(t, f, key, "tok-a", http.MethodGet, "maps/world/settings.json", "gzip, deflate, br")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if upstreamSaw != "gzip, deflate, br" {
		t.Fatalf("upstream Accept-Encoding = %q, want the browser value forwarded", upstreamSaw)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip relayed from native webserver", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), compressed) {
		t.Fatal("native compressed bytes were altered")
	}
	if f.doer.hits() != 1 {
		t.Fatalf("upstream hits = %d, want exactly 1 when the native webserver answers", f.doer.hits())
	}
}

// TestProxyDoesNotWidenCSPFixture pins that the proxy never sets a CSP of its
// own. The panel's securityHeaders middleware remains the only CSP source; no
// unsafe-eval or screenshot-only header bypass is introduced on this path.
func TestProxyDoesNotWidenCSPFixture(t *testing.T) {
	f := newAssetProxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("asset"))
	}))
	key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)

	rec := callAssetProxy(t, f, key, "tok-a", http.MethodGet, "index.html", "gzip")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp != "" {
		t.Fatalf("proxy set its own CSP %q; CSP must remain panel-owned and unchanged", csp)
	}
	for name, values := range rec.Header() {
		for _, v := range values {
			if strings.Contains(strings.ToLower(v), "unsafe-eval") {
				t.Errorf("header %s widened CSP with unsafe-eval: %q", name, v)
			}
		}
	}
}

// TestProxyGzipFallbackIsBlueMapMapDataOnlyFixture pins the review constraint:
// the fallback is not a generic 404 handler. It applies only to the BlueMap
// adapter and only to its textures/tile map-data paths; other renderers and
// browser assets keep the previous single-request pass-through behavior.
func TestProxyGzipFallbackIsBlueMapMapDataOnlyFixture(t *testing.T) {
	compressed := gzipBytes(t, []byte("real map data"))
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".gz") {
			_, _ = w.Write(compressed)
			return
		}
		http.NotFound(w, r)
	})
	cases := []struct {
		name    string
		kind    RendererKind
		asset   string
		want    int
		wantHit int
	}{
		{"bluemap textures", RendererBlueMap, "maps/world/textures.json", http.StatusOK, 2},
		{"bluemap tile", RendererBlueMap, "maps/world/tiles/0/x0z0.prbm", http.StatusOK, 2},
		{"bluemap browser asset", RendererBlueMap, "assets/index.js", http.StatusNotFound, 1},
		{"other renderer map data", RendererSquaremap, "maps/world/textures.json", http.StatusNotFound, 1},
		{"other renderer tile", RendererDynmap, "maps/world/tiles/0/x0z0.prbm", http.StatusNotFound, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newAssetProxyFixtureWithKind(t, upstream, tc.kind)
			key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
			rec := callAssetProxy(t, f, key, "tok-a", http.MethodGet, tc.asset, "gzip")
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if f.doer.hits() != tc.wantHit {
				t.Fatalf("upstream hits = %d, want %d", f.doer.hits(), tc.wantHit)
			}
		})
	}
}

// TestProxyRangeRequestKeepsRepresentationFixture verifies a Range request is
// never switched from the plain to the gzip representation, and that a native
// 206 with Content-Range is still relayed unchanged.
func TestProxyRangeRequestKeepsRepresentationFixture(t *testing.T) {
	compressed := gzipBytes(t, []byte("real map data"))
	t.Run("missing plain path is not retried for a range", func(t *testing.T) {
		f := newAssetProxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, ".gz") {
				_, _ = w.Write(compressed)
				return
			}
			http.NotFound(w, r)
		}))
		key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
		req := httptest.NewRequest(http.MethodGet, ProxyPrefix+key+"/maps/world/textures.json", nil)
		req.Header.Set("Authorization", "Bearer tok-a")
		req.Header.Set("Accept-Encoding", "gzip")
		req.Header.Set("Range", "bytes=0-3")
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (no representation switch for Range)", rec.Code)
		}
		if f.doer.hits() != 1 {
			t.Fatalf("upstream hits = %d, want 1 for a ranged request", f.doer.hits())
		}
	})
	t.Run("native partial content is relayed", func(t *testing.T) {
		f := newAssetProxyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Range", "bytes 0-3/13")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("real"))
		}))
		key, _ := f.sessions.Issue("srv-a", "world-a", "user-a", time.Minute)
		req := httptest.NewRequest(http.MethodGet, ProxyPrefix+key+"/maps/world/textures.json", nil)
		req.Header.Set("Authorization", "Bearer tok-a")
		req.Header.Set("Range", "bytes=0-3")
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusPartialContent {
			t.Fatalf("status = %d, want 206", rec.Code)
		}
		if got := rec.Header().Get("Content-Range"); got != "bytes 0-3/13" {
			t.Fatalf("Content-Range = %q, want relayed upstream value", got)
		}
		if got := rec.Header().Get("Accept-Ranges"); got != "bytes" {
			t.Fatalf("Accept-Ranges = %q, want bytes", got)
		}
		if rec.Body.String() != "real" {
			t.Fatalf("body = %q, want the exact partial bytes", rec.Body.String())
		}
		if f.doer.hits() != 1 {
			t.Fatalf("upstream hits = %d, want 1", f.doer.hits())
		}
	})
}

func TestAcceptsEncodingFixture(t *testing.T) {
	cases := []struct {
		header, coding string
		want           bool
	}{
		{"gzip, deflate", "gzip", true},
		{"GZIP", "gzip", true},
		{"gzip;q=0", "gzip", false},
		{"gzip;q=0, *", "gzip", false},
		{"gzip;q=0.001", "gzip", true},
		{"deflate", "gzip", false},
		{"*", "gzip", true},
		{"deflate, *;q=0", "gzip", false},
		{"", "gzip", false},
	}
	for _, tc := range cases {
		if got := acceptsEncoding(tc.header, tc.coding); got != tc.want {
			t.Errorf("acceptsEncoding(%q, %q) = %t, want %t", tc.header, tc.coding, got, tc.want)
		}
	}
}
