// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// HTTPDoer is the narrow seam used by adapters and the proxy. *http.Client
// satisfies it; fixtures inject an httptest-backed client.
type HTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// refuseRedirects keeps the node allowlist and same-origin guarantee intact: a
// renderer 3xx must never turn an allowlisted node-local request into a request
// to an external or controller-local address (SSRF), nor inject foreign content
// under the panel origin. Do returns an error, which callers report as
// unavailable/bad gateway without following the redirect.
func refuseRedirects(_ *http.Request, _ []*http.Request) error {
	return fmt.Errorf("worldview: renderer redirects are not followed")
}

func newHealthClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, CheckRedirect: refuseRedirects}
}
func newProxyClient() *http.Client {
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: refuseRedirects}
}

// Health is the adapter's honesty envelope. A zero/unknown value is kept as
// unknown; nothing here is back-filled with a plausible-looking timestamp.
type Health struct {
	State      v1.RendererState
	LastRender *timestamppb.Timestamp
	Version    string
	Source     string
	Confidence string // detected | inferred | unknown | stale | conflict
	Detail     string
}

// Renderer is an adapter over one configured renderer endpoint. ProxyBaseURL
// is internal only: it is never returned to a browser or placed in a response.
type Renderer interface {
	Type() v1.RendererType
	Descriptor() *v1.RendererDescriptor
	Health(ctx context.Context) (*Health, error)
	Capabilities() []*v1.Capability
	ProxyBaseURL() string
}

// Capability names are the stable vocabulary shared with the console.
const (
	CapabilityMap2D       = "map.2d"
	CapabilityMap3D       = "map.3d"
	CapabilityDimensions  = "dimensions"
	CapabilityMarkers     = "markers"
	CapabilityPlayers     = "players"
	CapabilityTimeWeather = "time_weather"
	CapabilityTelemetry   = "telemetry"
	CapabilityStatic      = "static"
)

// AllCapabilityNames is the full matrix ListCapabilities reports. Unsupported
// entries are returned with supported=false and an explicit detail string.
var AllCapabilityNames = []string{
	CapabilityMap2D, CapabilityMap3D, CapabilityDimensions, CapabilityMarkers,
	CapabilityPlayers, CapabilityTimeWeather, CapabilityTelemetry, CapabilityStatic,
}

func capability(name string, supported bool, detail string) *v1.Capability {
	return &v1.Capability{Name: name, Supported: supported, Detail: detail}
}

func rendererType(kind RendererKind) v1.RendererType {
	switch kind {
	case RendererBlueMap:
		return v1.RendererType_RENDERER_TYPE_BLUEMAP
	case RendererSquaremap:
		return v1.RendererType_RENDERER_TYPE_SQUAREMAP
	case RendererDynmap:
		return v1.RendererType_RENDERER_TYPE_DYNMAP
	case RendererStatic:
		return v1.RendererType_RENDERER_TYPE_STATIC
	default:
		return v1.RendererType_RENDERER_TYPE_UNSPECIFIED
	}
}

func newRenderer(ep Endpoint, hc HTTPDoer) Renderer {
	switch ep.Kind {
	case RendererBlueMap, RendererSquaremap, RendererDynmap:
		return &httpRenderer{ep: ep, hc: hc}
	case RendererStatic:
		return &staticRenderer{}
	default:
		return &unsupportedRenderer{}
	}
}

// httpRenderer probes the configured loopback/node-local base URL with real
// HTTP GETs. Its capability profile comes from the verified adapter matrix in
// ADR 0015; it never invents version, last-render or live-player facts.
type httpRenderer struct {
	ep Endpoint
	hc HTTPDoer
}

func (a *httpRenderer) Type() v1.RendererType { return rendererType(a.ep.Kind) }

func (a *httpRenderer) ProxyBaseURL() string { return a.ep.baseURL() }

func (a *httpRenderer) Descriptor() *v1.RendererDescriptor {
	var names []string
	for _, c := range a.Capabilities() {
		if c.Supported {
			names = append(names, c.Name)
		}
	}
	return &v1.RendererDescriptor{
		Type:         a.Type(),
		Capabilities: names,
		// Same-origin prefix only. The per-user key is supplied by
		// GetWorldViewSession, never a renderer host or port.
		WebBasePath: ProxyPrefix,
	}
}

func (a *httpRenderer) Capabilities() []*v1.Capability {
	return capabilitiesFor(a.ep.Kind, a.ep.PlayerFeed)
}

func (a *httpRenderer) Health(ctx context.Context) (*Health, error) {
	path := a.ep.HealthPath
	if path == "" {
		path = "/"
	}
	probe := strings.TrimRight(a.ep.baseURL(), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe, nil)
	if err != nil {
		// Never echo endpoint addresses into a client-visible detail string.
		return &Health{State: v1.RendererState_RENDERER_STATE_ERROR, Source: "renderer.http." + string(a.ep.Kind), Confidence: "unknown", Detail: "renderer probe URL is invalid"}, err
	}
	resp, err := a.hc.Do(req)
	if err != nil {
		return &Health{
			State: v1.RendererState_RENDERER_STATE_ERROR, Source: "renderer.http." + string(a.ep.Kind),
			Confidence: "unknown", Detail: "renderer endpoint did not answer the health probe",
		}, err
	}
	defer resp.Body.Close()
	h := &Health{
		State: v1.RendererState_RENDERER_STATE_ERROR, Source: "renderer.http." + string(a.ep.Kind),
		Confidence: "stale", Detail: fmt.Sprintf("renderer probe returned HTTP %d", resp.StatusCode),
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return h, nil
	}
	h.State = v1.RendererState_RENDERER_STATE_READY
	h.Confidence = "detected"
	h.Detail = "renderer endpoint answered the configured health probe"
	parseHealthBody(io.LimitReader(resp.Body, 64<<10), h)
	return h, nil
}

// parseHealthBody reads optional real fields from the renderer response. A
// missing or unparsable field stays empty/unknown; it is never defaulted to a
// current timestamp or a guessed version.
func parseHealthBody(r io.Reader, h *Health) {
	raw, err := io.ReadAll(r)
	if err != nil || len(raw) == 0 {
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return
	}
	if v, ok := fields["version"]; ok {
		var s string
		if json.Unmarshal(v, &s) == nil && strings.TrimSpace(s) != "" {
			h.Version = s
		}
	}
	var last json.RawMessage
	if v, ok := fields["last_render"]; ok {
		last = v
	} else if v, ok := fields["lastRender"]; ok {
		last = v
	}
	if len(last) == 0 {
		return
	}
	var s string
	if json.Unmarshal(last, &s) == nil {
		if ts, err := time.Parse(time.RFC3339, s); err == nil {
			t := timestamppb.New(ts)
			if t.CheckValid() == nil {
				h.LastRender = t
			}
		}
		return
	}
	var seconds float64
	if json.Unmarshal(last, &seconds) == nil && seconds > 0 {
		t := timestamppb.New(time.Unix(0, int64(seconds*float64(time.Second))))
		if t.CheckValid() == nil {
			h.LastRender = t
		}
	}
}

// capabilitiesFor is the honest, per-adapter capability matrix. Only BlueMap's
// 2D/3D/dimensions/markers are advertised from the verified ADR 0015 matrix;
// player positions require an explicitly configured bridge feed. Time/weather
// and telemetry are never promised by any adapter.
func capabilitiesFor(kind RendererKind, playerFeed bool) []*v1.Capability {
	unsupported := func(name, detail string) *v1.Capability { return capability(name, false, detail) }
	switch kind {
	case RendererBlueMap:
		caps := []*v1.Capability{
			capability(CapabilityMap2D, true, "verified BlueMap adapter"),
			capability(CapabilityMap3D, true, "verified BlueMap adapter"),
			capability(CapabilityDimensions, true, "verified BlueMap adapter"),
			capability(CapabilityMarkers, true, "verified BlueMap adapter"),
		}
		if playerFeed {
			caps = append(caps, capability(CapabilityPlayers, true, "explicit player bridge feed configured"))
		} else {
			caps = append(caps, unsupported(CapabilityPlayers, "no player bridge feed configured; live positions are not verified"))
		}
		return append(caps,
			unsupported(CapabilityTimeWeather, "not verified by any world-view adapter"),
			unsupported(CapabilityTelemetry, "not verified by any world-view adapter"),
			unsupported(CapabilityStatic, "BlueMap is a live renderer, not a static snapshot"),
		)
	case RendererSquaremap, RendererDynmap:
		name := string(kind) + " adapter"
		return []*v1.Capability{
			capability(CapabilityMap2D, true, "verified "+name),
			unsupported(CapabilityMap3D, "2D-only renderer"),
			unsupported(CapabilityDimensions, "not verified by the "+name),
			unsupported(CapabilityMarkers, "not verified by the "+name),
			unsupported(CapabilityPlayers, "no player bridge feed configured"),
			unsupported(CapabilityTimeWeather, "not verified by any world-view adapter"),
			unsupported(CapabilityTelemetry, "not verified by any world-view adapter"),
			unsupported(CapabilityStatic, "live renderer, not a static snapshot"),
		}
	case RendererStatic:
		return []*v1.Capability{
			capability(CapabilityMap2D, true, "static tile image"),
			unsupported(CapabilityMap3D, "offline tile snapshot"),
			unsupported(CapabilityDimensions, "not verified for a static snapshot"),
			unsupported(CapabilityMarkers, "not verified for a static snapshot"),
			unsupported(CapabilityPlayers, "offline snapshot has no live players"),
			unsupported(CapabilityTimeWeather, "not verified by any world-view adapter"),
			unsupported(CapabilityTelemetry, "not verified by any world-view adapter"),
			capability(CapabilityStatic, true, "read-only imported tile snapshot"),
		}
	default:
		out := make([]*v1.Capability, 0, len(AllCapabilityNames))
		for _, name := range AllCapabilityNames {
			out = append(out, unsupported(name, "no verified adapter for the configured renderer"))
		}
		return out
	}
}

// staticRenderer is an offline, read-only tile snapshot. It performs no HTTP
// probe and reports stale with no timestamp rather than pretending liveness.
type staticRenderer struct{}

func (*staticRenderer) Type() v1.RendererType { return v1.RendererType_RENDERER_TYPE_STATIC }
func (*staticRenderer) ProxyBaseURL() string  { return "" }
func (*staticRenderer) Capabilities() []*v1.Capability {
	return capabilitiesFor(RendererStatic, false)
}
func (*staticRenderer) Descriptor() *v1.RendererDescriptor {
	var names []string
	for _, c := range (&staticRenderer{}).Capabilities() {
		if c.Supported {
			names = append(names, c.Name)
		}
	}
	return &v1.RendererDescriptor{Type: v1.RendererType_RENDERER_TYPE_STATIC, Capabilities: names, WebBasePath: ProxyPrefix}
}
func (*staticRenderer) Health(context.Context) (*Health, error) {
	return &Health{
		State: v1.RendererState_RENDERER_STATE_STALE, Source: "static.tile-snapshot",
		Confidence: "stale", Detail: "offline tile snapshot; no live renderer",
	}, nil
}

// unsupportedRenderer represents a configured kind with no verified adapter.
type unsupportedRenderer struct{}

func (*unsupportedRenderer) Type() v1.RendererType { return v1.RendererType_RENDERER_TYPE_UNSPECIFIED }
func (*unsupportedRenderer) ProxyBaseURL() string  { return "" }
func (*unsupportedRenderer) Capabilities() []*v1.Capability {
	return capabilitiesFor(RendererKind(""), false)
}
func (*unsupportedRenderer) Descriptor() *v1.RendererDescriptor {
	return &v1.RendererDescriptor{Type: v1.RendererType_RENDERER_TYPE_UNSPECIFIED, WebBasePath: ProxyPrefix}
}
func (*unsupportedRenderer) Health(context.Context) (*Health, error) {
	return &Health{
		State: v1.RendererState_RENDERER_STATE_UNSUPPORTED, Source: "worldview.adapter",
		Confidence: "unknown", Detail: "no verified adapter for the configured renderer type",
	}, nil
}
