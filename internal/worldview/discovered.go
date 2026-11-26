// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// discoveredRenderer is an adapter over a module located through the PRIVATE
// backend transport. It shares the same-origin proxy and capability matrix as
// an explicitly configured BlueMap renderer, but its location is never exposed
// to the browser and its state is derived from the module's own facts rather
// than from a listening port.
type discoveredRenderer struct {
	facts *InstalledRenderer
	hc    HTTPDoer
}

func newDiscoveredRenderer(facts *InstalledRenderer, hc HTTPDoer) Renderer {
	return &discoveredRenderer{facts: facts, hc: hc}
}

func (d *discoveredRenderer) Type() v1.RendererType { return rendererType(RendererBlueMap) }

func discoveredReady(h *Health, err error) bool {
	return err == nil && h != nil && h.State == v1.RendererState_RENDERER_STATE_READY
}

func (d *discoveredRenderer) matchesWorldRef(ref *v1.WorldRef) bool {
	return (ref.GetKind() == v1.WorldRef_KIND_UNSPECIFIED || ref.GetKind() == v1.WorldRef_KIND_SERVER_LOCAL) &&
		ref.GetBindingId() == "" && ref.GetSnapshotId() == "" &&
		(ref.GetLevelName() == "" || ref.GetLevelName() == d.facts.WorldName)
}

func (d *discoveredRenderer) matchesSessionWorld(key string) bool {
	f := d.facts
	return f.WorldMatchesServer && f.AcceptDownload && f.Rendered &&
		key == worldKey(&v1.WorldRef{
			Kind: v1.WorldRef_KIND_SERVER_LOCAL, ServerId: f.ServerID, LevelName: f.WorldName,
		})
}

// ProxyBaseURL rechecks runtime provenance for every asset, including assets
// requested with a session issued before the renderer or its mount changed.
func (d *discoveredRenderer) ProxyBaseURL() string {
	f := d.facts
	if f.Lifecycle != ModuleRunning || !f.IdentityVerified || !f.NetworkVerified ||
		!f.MountVerified || !f.ReadOnlyWorldMount || (f.LegacyTag && !f.ProvenanceVerified) {
		return ""
	}
	return f.BaseURL
}

func (d *discoveredRenderer) Capabilities() []*v1.Capability {
	// A discovered module has no explicitly configured player bridge feed, so
	// live player positions stay unsupported (ADR 0015).
	return capabilitiesFor(RendererBlueMap, false)
}

func (d *discoveredRenderer) Descriptor() *v1.RendererDescriptor {
	var names []string
	for _, c := range d.Capabilities() {
		if c.Supported {
			names = append(names, c.Name)
		}
	}
	return &v1.RendererDescriptor{
		Type:         d.Type(),
		Version:      d.facts.Version,
		Capabilities: names,
		WebBasePath:  ProxyPrefix,
	}
}

const discoveredSource = "worldview.discovered.bluemap"

func discoveredHealth(state v1.RendererState, confidence, detail string) *Health {
	return &Health{State: state, Source: discoveredSource, Confidence: confidence, Detail: detail}
}

// Health maps the module's own facts to an honest lifecycle state. A running
// web server alone is never READY: the exact target world must be configured,
// the resource-download acknowledgement must be present, and scoped tiles must
// exist. Each non-ready state carries a concrete next action in Detail, which
// the service surfaces through Error / Freshness.Note.
func (d *discoveredRenderer) Health(ctx context.Context) (*Health, error) {
	f := d.facts
	switch f.Lifecycle {
	case ModulePending:
		return discoveredHealth(v1.RendererState_RENDERER_STATE_INSTALLING, "inferred",
			"BlueMap is being created/started; wait for setup to finish before opening the map"), nil
	case ModuleStopped:
		if f.IdentityVerified && !f.MountVerified {
			return discoveredHealth(v1.RendererState_RENDERER_STATE_STOPPED, "detected",
				"BlueMap is stopped and its world mount needs repair. In the server’s Modules tab, edit BlueMap’s Volumes so /app/world points to the active level folder read-only. Review the BlueMap logs before restarting."), nil
		}
		return discoveredHealth(v1.RendererState_RENDERER_STATE_STOPPED, "detected",
			"The BlueMap module is stopped. Open the server’s Modules tab to check its logs and start it."), nil
	case ModuleFailed:
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "detected",
			"BlueMap module is missing or its container could not be verified on this node; reinstall through ModuleService"), nil
	}

	// Runtime gates are checked BEFORE any HTTP request. A running web server
	// on a wrong image/network/mount never counts as this server's renderer.
	if !f.IdentityVerified {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "detected",
			"BlueMap container identity, pinned version or privilege does not match this module; reinstall through ModuleService"), nil
	}
	if !f.NetworkVerified {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "detected",
			"BlueMap module is not on the owned network; there is no private renderer path to this world"), nil
	}
	if !f.MountVerified || !f.ReadOnlyWorldMount {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ABSENT, "detected",
			"BlueMap’s world mount does not match the active level or is not read-only. In the server’s Modules tab, edit BlueMap’s Volumes so /app/world points to the active level folder read-only."), nil
	}
	if f.LegacyTag && !f.ProvenanceVerified {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "detected",
			"an existing BlueMap module uses the legacy renderer tag but its identity, mount or world-map provenance could not be established; no config was changed"), nil
	}

	// Explicit operator consent is a precondition for a NEW owned install. For
	// an adopted module it was already recorded; we never rewrite it.
	if !f.AcceptDownload {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ABSENT, "detected",
			"BlueMap requires acknowledgement of the Mojang resource download. If you accept the Minecraft EULA and download, set accept-download: true in modules/bluemap/config/core.conf from the server’s Files tab, then restart BlueMap."), nil
	}

	// A configured world identity is required, and it must be the ACTUAL level
	// name of this server (including non-default world folders), not "world".
	if strings.TrimSpace(f.WorldName) == "" || !f.WorldMatchesServer {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ABSENT, "detected",
			"BlueMap has no configured map for this server’s actual world. Check modules/bluemap/config/maps in the server’s Files tab and point the Overworld map at /app/world."), nil
	}

	if !f.WebUp || strings.TrimSpace(f.BaseURL) == "" {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "detected",
			"BlueMap is installed but its private renderer endpoint is unreachable; start or restart the module"), nil
	}

	// A listening web server with a configured world but no scoped tiles is a
	// first render in progress, not a ready map.
	if !f.Rendered {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_RENDERING, "detected",
			"BlueMap is configured and reachable; the first render of this world is still in progress"), nil
	}

	// Ready facts still require the private endpoint to actually answer and to
	// serve the configured map. A listening web server alone never makes a
	// world READY (OSS-388). Probe the owned network, never a host port.
	return d.probe(ctx)
}

func (d *discoveredRenderer) probe(ctx context.Context) (*Health, error) {
	probe := strings.TrimRight(d.facts.BaseURL, "/") + "/"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe, nil)
	if err != nil {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "unknown",
			"BlueMap renderer probe URL is invalid; see panel logs"), err
	}
	resp, err := d.hc.Do(req)
	if err != nil {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "unknown",
			"BlueMap private renderer endpoint did not answer the health probe"), err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "stale",
			fmt.Sprintf("BlueMap renderer probe returned HTTP %d", resp.StatusCode)), nil
	}

	// The renderer's own settings.json is authoritative for which maps it
	// actually serves. Do not infer a map from an HTTP 200 on "/".
	ids, mapErr := blueMapMapIDs(ctx, d.facts.BaseURL, d.hc)
	if mapErr != nil || len(ids) == 0 || !containsString(ids, d.facts.MapID) {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ABSENT, "detected",
			"BlueMap is installed but does not serve a configured map for this server's world; configure the target world to finish setup"), nil
	}
	if !blueMapAssetsReady(ctx, d.facts.BaseURL, ids, d.hc) {
		return discoveredHealth(v1.RendererState_RENDERER_STATE_ERROR, "unknown", blueMapNoAssetsDetail), nil
	}

	h := discoveredHealth(v1.RendererState_RENDERER_STATE_READY, "detected",
		"BlueMap renderer answered the configured private health probe for this world")
	parseHealthBody(io.LimitReader(resp.Body, 64<<10), h)
	if f := d.facts.Version; f != "" && h.Version == "" {
		h.Version = f
	}
	return h, nil
}

func containsString(values []string, want string) bool {
	if want == "" {
		return false
	}
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}
