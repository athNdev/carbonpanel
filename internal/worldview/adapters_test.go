// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestBlueMapAdapterHealthFromHTTPFixture verifies the adapter performs a real
// GET against its configured loopback base URL and takes version/last-render
// only from that response. No live renderer is involved.
func TestBlueMapAdapterHealthFromHTTPFixture(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(serveBlueMapFixture))
	defer ts.Close()

	ep := endpointForFixture(t, ts.URL, "", RendererBlueMap)
	adapter := newRenderer(ep, ts.Client())
	h, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != v1.RendererState_RENDERER_STATE_READY || h.Confidence != "detected" {
		t.Fatalf("health state/confidence = %s/%s, want READY/detected", h.State, h.Confidence)
	}
	if h.Version != "5.29" {
		t.Fatalf("version = %q, want 5.29 from fixture", h.Version)
	}
	if h.LastRender == nil || h.LastRender.AsTime().Format(time.RFC3339) != "2026-11-18T10:00:00Z" {
		t.Fatalf("last_render = %v, want fixture timestamp", h.LastRender)
	}
	desc := adapter.Descriptor()
	if desc.GetWebBasePath() != ProxyPrefix || desc.GetType() != v1.RendererType_RENDERER_TYPE_BLUEMAP {
		t.Fatalf("descriptor = %+v", desc)
	}
	if !strings.Contains(adapter.ProxyBaseURL(), "http://") {
		t.Fatalf("ProxyBaseURL = %q, want internal http URL (never sent to browser)", adapter.ProxyBaseURL())
	}
}

// TestBlueMapWebServerWithoutMapIsNotReadyFixture locks down OSS-388's exact
// readiness gap: a 2xx from "/" is only a listening web server, not a
// configured or rendered target world.
func TestBlueMapWebServerWithoutMapIsNotReadyFixture(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/settings.json" {
			_, _ = w.Write([]byte(`{"maps":[]}`))
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	defer ts.Close()

	adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
	h, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != v1.RendererState_RENDERER_STATE_ABSENT {
		t.Fatalf("state = %s, want ABSENT (setup needed) for a web server with no map", h.State)
	}
	if !strings.Contains(h.Detail, "no configured map") {
		t.Fatalf("detail = %q, want an actionable next step", h.Detail)
	}
	if strings.Contains(h.Detail, ts.URL) {
		t.Fatalf("detail leaked the renderer address: %q", h.Detail)
	}
}

// TestBlueMapWebServerWithStringMapIDsIsReadyFixture covers the upstream
// BlueMap v5.29 settings.json format, which serializes map IDs as a string
// array ({"maps":["world"]}) rather than objects with an "id" field.
func TestBlueMapWebServerWithStringMapIDsIsReadyFixture(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/settings.json":
			_, _ = w.Write([]byte(`{"maps":["world"]}`))
		case strings.HasSuffix(r.URL.Path, "/textures.json"):
			_, _ = w.Write([]byte(`{"textures":["block/stone.png"]}`))
		default:
			_, _ = w.Write([]byte(`{"version":"5.29"}`))
		}
	}))
	defer ts.Close()

	adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
	h, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("state = %s, want READY for a configured BlueMap v5.29 string map list; detail=%q", h.State, h.Detail)
	}
}

// TestBlueMapMissingTexturesIsNotReadyFixture pins the fail-closed readiness
// rule behind the black-canvas defect: a renderer with a valid map list whose
// textures.json network response is 404 must not be READY and must not yield
// a map session. A visible iframe/canvas is not evidence of a viewable map.
func TestBlueMapMissingTexturesIsNotReadyFixture(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/settings.json" {
			_, _ = w.Write([]byte(`{"maps":["world"]}`))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/maps/world/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"version":"5.29"}`))
	}))
	defer ts.Close()

	adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
	h, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State == v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("state = READY although textures.json returned 404; black canvas would be captured")
	}
	if h.State != v1.RendererState_RENDERER_STATE_ABSENT {
		t.Fatalf("state = %s, want ABSENT for unreachable map textures", h.State)
	}
	if !strings.Contains(h.Detail, "textures") || strings.Contains(h.Detail, ts.URL) {
		t.Fatalf("detail = %q, want actionable textures diagnostic without renderer address", h.Detail)
	}
}

// TestBlueMapEmptyTexturesIsNotReadyFixture rejects an empty 200 body as well:
// an empty texture asset is the same black-canvas failure as a 404.
func TestBlueMapEmptyTexturesIsNotReadyFixture(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/settings.json" {
			_, _ = w.Write([]byte(`{"maps":["world"]}`))
			return
		}
		_, _ = w.Write(nil)
	}))
	defer ts.Close()

	adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
	h, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State == v1.RendererState_RENDERER_STATE_READY {
		t.Fatal("state = READY although textures.json was empty")
	}
}

// TestBlueMapRealTexturesIsReadyFixture confirms the positive path still
// reports READY when the map data answers with non-empty bytes.
func TestBlueMapRealTexturesIsReadyFixture(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/settings.json" {
			_, _ = w.Write([]byte(`{"maps":["world"]}`))
			return
		}
		if r.URL.Path == "/maps/world/textures.json" {
			_, _ = w.Write([]byte(`{"textures":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"version":"5.29"}`))
	}))
	defer ts.Close()

	adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
	h, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("state = %s, want READY when real texture bytes are served", h.State)
	}
}

// TestBlueMapGzipOnlyTexturesIsReadyFixture proves the readiness probe uses the
// same exact gzip sibling route as the proxy. A gzip-only static web root must
// be READY (so a session is issued) rather than ABSENT, otherwise the fixed
// proxy route could never be exercised.
func TestBlueMapGzipOnlyTexturesIsReadyFixture(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(`{"textures":["block/stone.png"]}`))
	_ = zw.Close()
	compressed := buf.Bytes()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/settings.json":
			_, _ = w.Write([]byte(`{"maps":["world"]}`))
		case "/maps/world/textures.json.gz":
			w.Header().Set("Content-Type", "application/gzip")
			_, _ = w.Write(compressed)
		case "/":
			_, _ = w.Write([]byte(`{"version":"5.29"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
	h, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State != v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("state = %s, want READY when only textures.json.gz exists; detail=%q", h.State, h.Detail)
	}
}

// TestBlueMapNonTexturesBodyIsNotReadyFixture rejects a 200 SPA/HTML fallback
// or an unrelated JSON document: a non-empty body alone is not map data.
func TestBlueMapNonTexturesBodyIsNotReadyFixture(t *testing.T) {
	for name, body := range map[string]string{
		"html":    `<html><body>panel shell</body></html>`,
		"json":    `{"version":"5.29"}`,
		"no array": `{"textures":"not-an-array"}`,
	} {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/settings.json" {
					_, _ = w.Write([]byte(`{"maps":["world"]}`))
					return
				}
				_, _ = w.Write([]byte(body))
			}))
			defer ts.Close()

			adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
			h, err := adapter.Health(context.Background())
			if err != nil {
				t.Fatalf("Health: %v", err)
			}
			if h.State == v1.RendererState_RENDERER_STATE_READY {
				t.Fatalf("body %q must not be accepted as real BlueMap textures", body)
			}
		})
	}
}

// TestBlueMapEveryConfiguredMapIsProbedFixture pins that a broken second map
// cannot be hidden behind a healthy first one: the webapp loads every map in
// settings.json, so all of them must answer with real textures.
func TestBlueMapEveryConfiguredMapIsProbedFixture(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/settings.json" {
			_, _ = w.Write([]byte(`{"maps":["world","nether"]}`))
			return
		}
		if r.URL.Path == "/maps/world/textures.json" {
			_, _ = w.Write([]byte(`{"textures":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
	h, err := adapter.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.State == v1.RendererState_RENDERER_STATE_READY {
		t.Fatal("state = READY although the second configured map has no textures")
	}
}

// TestBlueMapMalformedMixedMapListFailsClosedFixture preserves the previous
// malformed-settings failure behavior for mixed/invalid entry shapes. A numeric
// or otherwise invalid entry must not be ignored in a way that turns a malformed
// document into READY.
func TestBlueMapMalformedMixedMapListFailsClosedFixture(t *testing.T) {
	cases := []string{
		`{"maps":[123,"world"]}`,
		`{"maps":["world",123]}`,
		`{"maps":[true]}`,
		`{"maps":[{"id":123}]}`,
		`{"maps":["world",{"id":"world"}]}`,
		`{"maps":[""]}`,
	}
	for _, body := range cases {
		t.Run(body, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/settings.json" {
					_, _ = w.Write([]byte(body))
					return
				}
				_, _ = w.Write([]byte(`{"version":"5.29"}`))
			}))
			defer ts.Close()

			adapter := newRenderer(endpointForFixture(t, ts.URL, "", RendererBlueMap), ts.Client())
			h, err := adapter.Health(context.Background())
			if err != nil || h.State == v1.RendererState_RENDERER_STATE_READY {
				t.Fatalf("malformed/mixed settings %s must fail closed, got state=%v err=%v detail=%q", body, h.State, err, h.Detail)
			}
			if h.State != v1.RendererState_RENDERER_STATE_ABSENT {
				t.Fatalf("malformed/mixed settings %s state=%v, want ABSENT; detail=%q", body, h.State, h.Detail)
			}
			if !strings.Contains(h.Detail, "could not be read") {
				t.Fatalf("malformed/mixed settings %s detail=%q, want unreadable map-list diagnostic", body, h.Detail)
			}
		})
	}
}

// TestBlueMapCapabilitiesRequireConfiguredBridgeFixture shows live players are
// advertised only with an explicit feed, and time/weather/telemetry are always
// reported unsupported because no adapter verifies them.
func TestBlueMapCapabilitiesRequireConfiguredBridgeFixture(t *testing.T) {
	without := capabilitiesFor(RendererBlueMap, false)
	if got := lookupCapability(without, CapabilityPlayers); got == nil || got.GetSupported() {
		t.Fatalf("players capability without feed = %+v, want unsupported", got)
	}
	with := capabilitiesFor(RendererBlueMap, true)
	if got := lookupCapability(with, CapabilityPlayers); got == nil || !got.GetSupported() {
		t.Fatalf("players capability with feed = %+v, want supported", got)
	}
	for _, caps := range [][]*v1.Capability{without, with} {
		for _, name := range []string{CapabilityTimeWeather, CapabilityTelemetry} {
			got := lookupCapability(caps, name)
			if got == nil || got.GetSupported() || got.GetDetail() == "" {
				t.Errorf("%s = %+v, want supported=false with detail", name, got)
			}
		}
	}
	for _, name := range []string{CapabilityMap2D, CapabilityMap3D, CapabilityDimensions, CapabilityMarkers} {
		if got := lookupCapability(without, name); got == nil || !got.GetSupported() {
			t.Errorf("BlueMap %s = %+v, want supported", name, got)
		}
	}
}

// TestCompatibilityAndStaticCapabilityProfilesFixture pins the honest 2D-only
// and offline profiles: no adapter silently advertises a 3D or live-player
// lookalike for a renderer that does not support it.
func TestCompatibilityAndStaticCapabilityProfilesFixture(t *testing.T) {
	for _, kind := range []RendererKind{RendererSquaremap, RendererDynmap} {
		caps := capabilitiesFor(kind, true)
		for _, name := range []string{CapabilityMap3D, CapabilityPlayers, CapabilityTimeWeather, CapabilityTelemetry} {
			if got := lookupCapability(caps, name); got == nil || got.GetSupported() {
				t.Errorf("%s %s = %+v, want unsupported even with a feed flag", kind, name, got)
			}
		}
		if got := lookupCapability(caps, CapabilityMap2D); got == nil || !got.GetSupported() {
			t.Errorf("%s map.2d = %+v, want supported", kind, got)
		}
	}
	static := (&staticRenderer{}).Health
	h, err := static(context.Background())
	if err != nil || h.State != v1.RendererState_RENDERER_STATE_STALE || h.Confidence != "stale" || h.LastRender != nil {
		t.Fatalf("static health = %+v, %v; want stale without a timestamp", h, err)
	}
	if got := lookupCapability((&staticRenderer{}).Capabilities(), CapabilityStatic); got == nil || !got.GetSupported() {
		t.Fatalf("static capability = %+v, want supported", got)
	}
}

// TestUnsupportedAdapterIsHonestFixture checks an unknown renderer kind reports
// UNSUPPORTED/unknown and never claims any capability.
func TestUnsupportedAdapterIsHonestFixture(t *testing.T) {
	a := newRenderer(Endpoint{Kind: RendererKind("mystery")}, nil)
	h, err := a.Health(context.Background())
	if err != nil || h.State != v1.RendererState_RENDERER_STATE_UNSUPPORTED || h.Confidence != "unknown" {
		t.Fatalf("unsupported health = %+v, %v", h, err)
	}
	for _, c := range a.Capabilities() {
		if c.GetSupported() {
			t.Errorf("unsupported renderer advertised %s", c.GetName())
		}
	}
}

// TestAdapterAbsentAndFailedStatesFixture checks a non-2xx health response and
// an unreachable loopback endpoint are reported as errors, not fake READY.
func TestAdapterAbsentAndFailedStatesFixture(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	adapter := newRenderer(endpointForFixture(t, bad.URL, "", RendererBlueMap), bad.Client())
	h, err := adapter.Health(context.Background())
	if err != nil || h.State != v1.RendererState_RENDERER_STATE_ERROR || h.LastRender != nil {
		t.Fatalf("non-2xx health = %+v, %v", h, err)
	}

	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ep := endpointForFixture(t, dead.URL, "", RendererBlueMap)
	client := dead.Client()
	dead.Close()
	h2, err2 := newRenderer(ep, client).Health(context.Background())
	if err2 == nil || h2 == nil || h2.State != v1.RendererState_RENDERER_STATE_ERROR {
		t.Fatalf("unreachable health = %+v, %v", h2, err2)
	}
}

func lookupCapability(caps []*v1.Capability, name string) *v1.Capability {
	for _, c := range caps {
		if c.GetName() == name {
			return c
		}
	}
	return nil
}
