// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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
	defer func() { _ = resp.Body.Close() }()
	h := &Health{
		State: v1.RendererState_RENDERER_STATE_ERROR, Source: "renderer.http." + string(a.ep.Kind),
		Confidence: "stale", Detail: fmt.Sprintf("renderer probe returned HTTP %d", resp.StatusCode),
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return h, nil
	}

	// A listening web server is NOT a configured/rendered world (OSS-388). For
	// the BlueMap adapter, require the renderer's own settings.json map list to
	// contain at least one map. An HTTP 200 on "/" only proves the listener is
	// up; BlueMap starts that listener even when a target world is missing and
	// its map fails to load. No version/last-render fact is fabricated here.
	if a.ep.Kind == RendererBlueMap {
		ids, err := blueMapMapIDs(ctx, a.ep.baseURL(), a.hc)
		if err != nil || len(ids) == 0 {
			return blueMapNoMapHealth(err), nil
		}
		if !blueMapAssetsReady(ctx, a.ep.baseURL(), ids, a.hc) {
			h.State = v1.RendererState_RENDERER_STATE_ABSENT
			h.Confidence = "unknown"
			h.Detail = blueMapNoAssetsDetail
			return h, nil
		}
	}

	h.State = v1.RendererState_RENDERER_STATE_READY
	h.Confidence = "detected"
	h.Detail = "renderer endpoint answered the configured health probe"
	parseHealthBody(io.LimitReader(resp.Body, 64<<10), h)
	return h, nil
}

// blueMapMapIDs reads the renderer's own authoritative map list from
// /settings.json over the private endpoint. It is not a port probe and the
// response never reaches a browser. An empty list means the web server is up
// but no map is configured/rendered for any target world.
//
// BlueMap v5.29 serializes maps as a JSON array of map ID strings
// ({"maps":["world"]}); earlier fixtures and compatible renderers may use
// objects with an "id" field. Accept both without relaxing the configured
// map ID safeguard enforced by callers. Malformed or mixed/invalid entry
// shapes remain parse errors, preserving the previous fail-closed behavior.
func blueMapMapIDs(ctx context.Context, baseURL string, hc HTTPDoer) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/settings.json", nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("settings.json returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}

	// Legacy/fixture-compatible format: {"maps":[{"id":"world"}]}.
	// Try this first to preserve existing parse semantics exactly.
	var legacy struct {
		Maps []struct {
			ID string `json:"id"`
		} `json:"maps"`
	}
	if err := json.Unmarshal(body, &legacy); err == nil {
		ids := make([]string, 0, len(legacy.Maps))
		for _, m := range legacy.Maps {
			if id := strings.TrimSpace(m.ID); id != "" {
				ids = append(ids, id)
			}
		}
		return ids, nil
	}

	// Upstream BlueMap v5.29 format: {"maps":["world"]}.
	// Only a pure string array is accepted here; any other shape stays an error.
	var stringIDs struct {
		Maps []string `json:"maps"`
	}
	if err := json.Unmarshal(body, &stringIDs); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(stringIDs.Maps))
	for _, id := range stringIDs.Maps {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			return nil, fmt.Errorf("settings.json contains an empty map id")
		}
		ids = append(ids, trimmed)
	}
	return ids, nil
}

// discoveredHealthForExplicitNoMap reports the honest onboarding state for a
// configured BlueMap endpoint whose web server is up but whose map list is
// empty. No endpoint address or internal error is echoed to the client.
func blueMapNoMapHealth(err error) *Health {
	h := &Health{
		State: v1.RendererState_RENDERER_STATE_ABSENT, Source: "renderer.http.bluemap",
		Confidence: "detected", Detail: "BlueMap web server is reachable but has no configured map; configure the target world to finish setup",
	}
	if err != nil {
		h.Confidence = "unknown"
		h.Detail = "BlueMap web server is reachable but its configured map list could not be read; configure the target world to finish setup"
	}
	return h
}

const (
	blueMapNoAssetsDetail = "BlueMap has a configured map but its map textures could not be read; wait for rendering or fix the renderer web root before opening the map"

	// maxBlueMapProbedMaps bounds the number of network probes performed by a
	// single health check even if settings.json carries a very large map list.
	maxBlueMapProbedMaps = 32
	// BlueMap 5.29 textures.json can exceed 2 MiB decompressed for real worlds.
	maxBlueMapTexturesBytes = 8 << 20
)

// BlueMap's webapp loads textures for every map at startup. A healthy first
// map cannot compensate for a broken secondary map.
func blueMapAssetsReady(ctx context.Context, baseURL string, ids []string, hc HTTPDoer) bool {
	if len(ids) > maxBlueMapProbedMaps {
		return false
	}
	for _, id := range ids {
		if err := blueMapTexturesReady(ctx, baseURL, id, hc); err != nil {
			return false
		}
	}
	return true
}

// blueMapTexturesReady verifies that the map data the webapp actually loads on
// startup is reachable over the private renderer endpoint. BlueMap v5.29 stores
// map data gzipped on disk: its own webserver answers the plain path by
// negotiating Accept-Encoding: gzip, while a plain static web root only has
// textures.json.gz. Both routes are accepted, with exactly one sibling retry,
// and the decompressed body must be a plausible textures document. Without the
// sibling probe a gzip-only web root would be ABSENT and no session would ever
// be issued, so the proxy's matching retry could not be exercised.
func blueMapTexturesReady(ctx context.Context, baseURL, mapID string, hc HTTPDoer) error {
	mapID = strings.TrimSpace(mapID)
	if mapID == "" || strings.ContainsAny(mapID, "/\\?&#%") || strings.Contains(mapID, "..") {
		return errors.New("invalid BlueMap map id")
	}
	plain := strings.TrimRight(baseURL, "/") + "/maps/" + mapID + "/textures.json"
	resp, err := doBlueMapTexturesGet(ctx, plain, hc)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		defer func() { _ = resp.Body.Close() }()
		return verifyBlueMapTextures(resp, false)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("textures.json returned HTTP %d", resp.StatusCode)
	}
	variant, err := doBlueMapTexturesGet(ctx, plain+".gz", hc)
	if err != nil {
		return err
	}
	defer func() { _ = variant.Body.Close() }()
	if variant.StatusCode < 200 || variant.StatusCode >= 300 {
		return fmt.Errorf("textures.json and textures.json.gz returned HTTP %d/%d", resp.StatusCode, variant.StatusCode)
	}
	return verifyBlueMapTextures(variant, true)
}

func doBlueMapTexturesGet(ctx context.Context, url string, hc HTTPDoer) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept-Encoding", "gzip")
	return hc.Do(req)
}

// verifyBlueMapTextures decodes a bounded body and requires a plausible
// BlueMap textures document. A SPA/HTML fallback or an unrelated JSON document
// with a 200 is not a rendered map and must not be called READY.
//
// BlueMap v5.29 serves textures as a top-level JSON array of texture objects.
// Older fixtures and compatible renderers may wrap the array in
// {"textures":[...]}.
func verifyBlueMapTextures(resp *http.Response, literalGzip bool) error {
	reader := io.LimitReader(resp.Body, maxBlueMapTexturesBytes)
	if literalGzip || strings.Contains(strings.ToLower(resp.Header.Get("Content-Encoding")), "gzip") {
		zr, err := gzip.NewReader(reader)
		if err != nil {
			return fmt.Errorf("textures gzip stream: %w", err)
		}
		defer func() { _ = zr.Close() }()
		reader = zr
	}
	body, err := io.ReadAll(io.LimitReader(reader, maxBlueMapTexturesBytes))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return errors.New("textures.json returned an empty body")
	}
	// Accept the object-wrapped format: {"textures":[...]}.
	var doc struct {
		Textures json.RawMessage `json:"textures"`
	}
	if err := json.Unmarshal(body, &doc); err == nil {
		if len(doc.Textures) == 0 {
			return errors.New("textures.json has no textures field")
		}
		var textures []json.RawMessage
		if err := json.Unmarshal(doc.Textures, &textures); err != nil {
			return errors.New("textures.json textures field is not an array")
		}
		return nil
	}
	// Accept BlueMap v5.29's top-level textures array.
	var textures []json.RawMessage
	if err := json.Unmarshal(body, &textures); err != nil {
		return errors.New("textures.json is not a plausible textures document")
	}
	if len(textures) == 0 {
		return errors.New("textures.json returned an empty textures array")
	}
	return nil
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
