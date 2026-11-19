// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
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
