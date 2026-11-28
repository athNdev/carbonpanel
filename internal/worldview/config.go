// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package worldview implements the observation-only WorldViewService and the
// authenticated same-origin reverse proxy for renderer assets (ADR 0015).
//
// Nothing in this package exposes a renderer origin, port or token to the
// browser. Renderer endpoints are node-scoped configuration, not user input.
package worldview

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// ProxyPrefix is the same-origin path prefix mounted before the frontend
	// catch-all. The opaque session key follows it, then the renderer asset
	// path. It is a path, never a URL or a credential.
	ProxyPrefix = "/api/v1/worldview/map/"

	// MaxAssetBytes bounds a single proxied renderer asset.
	MaxAssetBytes = 8 << 20

	// DefaultSessionTTL is the lifetime of an issued map session key.
	DefaultSessionTTL = 5 * time.Minute
)

var (
	// ErrRendererUnavailable means no endpoint is configured for this
	// server/node. The caller must report ABSENT/unavailable, never fall back
	// to a controller-local daemon or another server's renderer.
	ErrRendererUnavailable = errors.New("worldview: no renderer endpoint configured")
	// ErrUnsupportedRenderer means the configured renderer kind has no
	// verified adapter. Capabilities must be reported as unsupported.
	ErrUnsupportedRenderer = errors.New("worldview: unsupported renderer type")
	// ErrInvalidEndpoint means an explicitly configured endpoint violated the
	// node-scoped host rules (loopback for local, allowlisted node-local host
	// for remote). It fails closed.
	ErrInvalidEndpoint = errors.New("worldview: invalid renderer endpoint")
	// ErrInvalidOverlay rejects malformed overlay input before any write or audit.
	ErrInvalidOverlay = errors.New("worldview: invalid overlay")
)

// RendererKind is the configured adapter kind. Unknown values are unsupported.
type RendererKind string

const (
	RendererBlueMap   RendererKind = "bluemap"
	RendererSquaremap RendererKind = "squaremap"
	RendererDynmap    RendererKind = "dynmap"
	RendererStatic    RendererKind = "static"
)

// Endpoint is an explicit, operator-owned renderer location.
//
// Local nodes may only use a loopback host (127.0.0.1 or ::1): a renderer is a
// sidecar and must not publish a public port. Remote nodes must name a specific
// node-local host that the operator has allowlisted; the controller never
// substitutes its own localhost for a remote node.
type Endpoint struct {
	NodeID   string       // canonical node id; empty matches any node
	ServerID string       // optional per-server override (more specific wins)
	Kind     RendererKind // bluemap | squaremap | dynmap | static
	Host     string
	Port     int
	BasePath string // renderer web root, e.g. /bluemap/
	// HealthPath is probed by the HTTP adapters. Empty means "/".
	HealthPath string
	// PlayerFeed is an explicit opt-in for a separate player-location bridge.
	// Without it no adapter advertises the "players" capability.
	PlayerFeed bool
}

// Config is the small, explicit renderer topology. An empty Config (the
// default) means every world is ABSENT: world viewing never enables a renderer
// by guessing an address.
type Config struct {
	Endpoints    []Endpoint
	AllowedHosts []string // explicit hosts for remote node-local endpoints
}

// NodeLocator is the node-awareness seam. *docker.Locator satisfies it; no
// controller-local fallback is permitted when IsLocal fails.
type NodeLocator interface {
	IsLocal(ctx context.Context, nodeID string) (bool, error)
}

// Resolver turns a server's node into a renderer adapter or a typed error.
type Resolver struct {
	cfg       Config
	locator   NodeLocator
	hc        HTTPDoer
	discovery ModuleDiscovery
}

// Resolve returns the adapter for the given node/server. A remote node without
// an explicit, allowlisted node-local endpoint yields ErrRendererUnavailable
// and no adapter at all.
func (r *Resolver) Resolve(ctx context.Context, nodeID, serverID string) (Renderer, error) {
	if r == nil {
		return nil, ErrRendererUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Explicit operator endpoints ALWAYS win. An operator who configured an
	// endpoint has made a deliberate trust decision (ADR 0015); discovery must
	// never override it.
	ep, ok := r.cfg.endpointFor(nodeID, serverID)
	if ok {
		local := false
		if r.locator != nil {
			var err error
			local, err = r.locator.IsLocal(ctx, nodeID)
			if err != nil {
				return nil, fmt.Errorf("%w: cannot classify node %q: %w", ErrRendererUnavailable, nodeID, err)
			}
		}
		if err := r.cfg.validateEndpoint(ep, local); err != nil {
			return nil, err
		}
		return newRenderer(ep, r.healthClient()), nil
	}

	return r.resolveDiscovered(ctx, nodeID, serverID)
}

// allowsNativeFallback preserves explicit endpoint precedence, even when the
// configured endpoint cannot currently be reached.
func (r *Resolver) allowsNativeFallback(nodeID, serverID string, resolveErr error) bool {
	if !errors.Is(resolveErr, ErrRendererUnavailable) {
		return false
	}
	if r == nil {
		return true
	}
	_, configured := r.cfg.endpointFor(nodeID, serverID)
	return !configured
}

// resolveDiscovered locates an installed renderer through the PRIVATE backend
// transport. It is node-local and fail-closed: no port probing, no host-port
// dependency, no cross-server fallback and no controller-local fallback.
func (r *Resolver) resolveDiscovered(ctx context.Context, nodeID, serverID string) (Renderer, error) {
	if r.discovery == nil || r.locator == nil {
		return nil, fmt.Errorf("%w: node %q server %q", ErrRendererUnavailable, nodeID, serverID)
	}
	local, err := r.locator.IsLocal(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot classify node %q: %w", ErrRendererUnavailable, nodeID, err)
	}
	if !local {
		// A remote node has no private owned-network path from the controller.
		// It needs an explicit, allowlisted node-local endpoint (rule 3 of ADR 0015).
		return nil, fmt.Errorf("%w: node %q is remote; no private renderer transport", ErrRendererUnavailable, nodeID)
	}
	facts, err := r.discovery.FindRenderer(ctx, canonicalNodeID(nodeID), serverID)
	if err != nil {
		return nil, fmt.Errorf("%w: module discovery for node %q server %q: %w", ErrRendererUnavailable, nodeID, serverID, err)
	}
	if !identityMatches(facts, nodeID, serverID) {
		// Exact server/node/module identity is mandatory; never serve a map
		// discovered for a different server or an untrusted template.
		return nil, fmt.Errorf("%w: node %q server %q", ErrRendererUnavailable, nodeID, serverID)
	}
	return newDiscoveredRenderer(facts, r.healthClient()), nil
}

// NewResolver builds a resolver. A nil locator fails closed: no endpoint can
// be classified as local, so remote-only endpoints stay unavailable. Without a
// discovery implementation it also preserves the explicit-config-only behavior.
func NewResolver(cfg Config, locator NodeLocator) *Resolver {
	return &Resolver{cfg: cfg, locator: locator, discovery: NoModuleDiscovery{}}
}

// NewResolverWithDiscovery builds a resolver that can also locate installed
// renderer modules through the PRIVATE backend transport. Explicit operator
// endpoints still take precedence over discovery.
func NewResolverWithDiscovery(cfg Config, locator NodeLocator, discovery ModuleDiscovery) *Resolver {
	if discovery == nil {
		discovery = NoModuleDiscovery{}
	}
	return &Resolver{cfg: cfg, locator: locator, discovery: discovery}
}

func (r *Resolver) healthClient() HTTPDoer {
	if r.hc != nil {
		return r.hc
	}
	return newHealthClient()
}

// endpointFor selects a per-server override before a node-wide endpoint.
func (c Config) endpointFor(nodeID, serverID string) (Endpoint, bool) {
	var nodeWide *Endpoint
	for i := range c.Endpoints {
		ep := &c.Endpoints[i]
		if ep.ServerID != "" && ep.ServerID == serverID && (ep.NodeID == "" || ep.NodeID == nodeID) {
			return *ep, true
		}
		if ep.ServerID == "" && ep.NodeID == nodeID {
			nodeWide = ep
		}
	}
	if nodeWide != nil {
		return *nodeWide, true
	}
	// A node-agnostic fallback is only used for the built-in local node.
	if nodeID == "" || nodeID == "local" || nodeID == "default" {
		for i := range c.Endpoints {
			ep := &c.Endpoints[i]
			if ep.ServerID == "" && ep.NodeID == "" {
				return *ep, true
			}
		}
	}
	return Endpoint{}, false
}

func (c Config) validateEndpoint(ep Endpoint, local bool) error {
	host := strings.TrimSpace(ep.Host)
	if host == "" || ep.Port < 1 || ep.Port > 65535 {
		return fmt.Errorf("%w: host/port missing for node %q server %q", ErrInvalidEndpoint, ep.NodeID, ep.ServerID)
	}
	loopback := isLoopbackHost(host)
	if local {
		if !loopback {
			return fmt.Errorf("%w: local node %q endpoint host %q is not loopback", ErrInvalidEndpoint, ep.NodeID, host)
		}
	} else {
		if loopback {
			return fmt.Errorf("%w: remote node %q cannot use controller loopback host %q", ErrInvalidEndpoint, ep.NodeID, host)
		}
		allowed := false
		for _, h := range c.AllowedHosts {
			if strings.EqualFold(strings.TrimSpace(h), host) {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("%w: remote node %q host %q is not allowlisted", ErrInvalidEndpoint, ep.NodeID, host)
		}
	}
	if ep.BasePath != "" && (!strings.HasPrefix(ep.BasePath, "/") || strings.Contains(ep.BasePath, "..") || strings.ContainsAny(ep.BasePath, "?#\\")) {
		return fmt.Errorf("%w: base path %q", ErrInvalidEndpoint, ep.BasePath)
	}
	if ep.HealthPath != "" && (!strings.HasPrefix(ep.HealthPath, "/") || strings.Contains(ep.HealthPath, "..")) {
		return fmt.Errorf("%w: health path %q", ErrInvalidEndpoint, ep.HealthPath)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	h := strings.Trim(host, "[]")
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() && (ip.To4() != nil && ip.String() == "127.0.0.1" || ip.To4() == nil && ip.String() == "::1"))
}

func (e Endpoint) baseURL() string {
	path := e.BasePath
	if path == "" {
		path = "/"
	}
	u := url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort(strings.Trim(e.Host, "[]"), strconv.Itoa(e.Port)),
		Path:   path,
	}
	return u.String()
}
