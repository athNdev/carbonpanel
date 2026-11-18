// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package bluemapdiscovery implements worldview.ModuleDiscovery for installed
// builtin-bluemap modules. It binds a renderer to the exact server/node/module
// identity using NODE-SCOPED container metadata (labels, image, network,
// effective mounts) and reaches it only through the private owned network. It
// never probes ports, trusts host-port bindings, exposes a public listener or
// touches another server's module. Adoption is read-only: no config write, no
// restart, and all config/asset reads go through the confined node data
// accessor with bounded reads.
package bluemapdiscovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldview"
)

const (
	bluemapImageTag = "ghcr.io/bluemap-minecraft/bluemap:v5.29"
	bluemapWebPort  = 8100
)

// Store is the narrow persisted-data seam used by discovery. *storage.Store
// satisfies it; no new global state is introduced.
type Store interface {
	ListModulesByTemplate(ctx context.Context, templateID string) ([]*storage.Module, error)
	GetServer(ctx context.Context, id string) (*storage.Server, error)
	GetServerConfig(ctx context.Context, id string) (*storage.ServerConfig, error)
}

// Logger is the subset used here; nil is allowed and fails silent.
type Logger interface {
	Warn(format string, args ...any)
	Error(format string, args ...any)
}

// configAccessor is the confined, node-owned data seam. *nodestorage.Accessor
// satisfies it. All paths are relative to one server data root; absolute paths
// and traversal are refused by the implementation.
type configAccessor interface {
	ReadFile(rel string) ([]byte, error)
	ReadDir(rel string) ([]os.DirEntry, error)
	Close() error
}

// Discovery locates an installed builtin-bluemap module through panel-owned
// state, node-scoped container metadata and the owned network.
type Discovery struct {
	store       Store
	pool        *docker.ClientPool
	resolver    *nodestorage.Resolver
	networkName string
	log         Logger
}

func New(store Store, pool *docker.ClientPool, resolver *nodestorage.Resolver, networkName string, log Logger) *Discovery {
	return &Discovery{store: store, pool: pool, resolver: resolver, networkName: networkName, log: log}
}

var _ worldview.ModuleDiscovery = (*Discovery)(nil)

// FindRenderer returns the private, identity-bound facts for the exact
// (node, server) builtin-bluemap module, or nil. It is read-only.
func (d *Discovery) FindRenderer(ctx context.Context, nodeID, serverID string) (*worldview.InstalledRenderer, error) {
	if d == nil || d.store == nil || d.pool == nil || d.resolver == nil || serverID == "" {
		return nil, nil
	}
	nodeID = canonicalNode(nodeID)
	modules, err := d.store.ListModulesByTemplate(ctx, worldview.BuiltinBlueMapTemplateID)
	if err != nil {
		return nil, fmt.Errorf("list bluemap modules: %w", err)
	}
	var chosen *storage.Module
	for _, m := range modules {
		if m == nil || m.ServerID != serverID || canonicalNode(m.NodeID) != nodeID {
			continue
		}
		// Idempotent installs mean one module per server; if an operator has
		// more than one, choose deterministically rather than guessing by port.
		if chosen == nil || m.ID < chosen.ID {
			chosen = m
		}
	}
	if chosen == nil {
		return nil, nil
	}

	server, err := d.store.GetServer(ctx, serverID)
	if err != nil || server == nil || server.DataPath == "" {
		return nil, fmt.Errorf("renderer server has no data path: %w", err)
	}
	levelName := ""
	if cfg, cfgErr := d.store.GetServerConfig(ctx, serverID); cfgErr == nil {
		levelName = worldview.ActualLevelName(cfg)
	}

	facts := &worldview.InstalledRenderer{
		ModuleID: chosen.ID, ServerID: serverID, NodeID: nodeID,
		TemplateID:    chosen.TemplateID,
		ContainerPort: bluemapWebPort,
		Version:       chosen.Metadata["bluemap_version"],
	}

	// Runtime truth first: labels, image, state, owned-network membership and
	// the EFFECTIVE world bind. No HTTP is issued unless every gate passes.
	runtime, inspectErr := d.pool.GetClientStrictContext(ctx, nodeID)
	if inspectErr != nil {
		if d.log != nil {
			d.log.Warn("bluemap discovery: node %s client unavailable: %v", nodeID, inspectErr)
		}
		facts.Lifecycle = worldview.ModuleFailed
		return facts, nil
	}
	identity, inspectErr := runtime.InspectModuleRuntime(ctx, chosen.ID, chosen.ContainerID, d.networkName)
	if inspectErr != nil || identity == nil {
		facts.Lifecycle = worldview.ModuleFailed
		return facts, nil
	}
	facts.Lifecycle = lifecycleFromState(identity.State)
	facts.IdentityVerified = identityMatchesRuntime(chosen, serverID, nodeID, identity)
	facts.NetworkVerified = identity.OnOwnedNetwork
	// Dial the inspected owned-network IP. Docker DNS names are NOT resolvable
	// from the controller in host-network or native-binary deployment modes.
	facts.BaseURL = rendererBaseURL(identity)

	// The requested canonical world is the server's actual level folder. A
	// non-default folder is mounted at /app/world; compare the real bind source.
	expectedWorldSource := filepath.Join(server.DataPath, levelName)
	mount, hasWorldMount := identity.ModuleWorldMount()
	if hasWorldMount && levelName != "" {
		facts.ReadOnlyWorldMount = mount.ReadOnly
		facts.MountVerified = mountMatches(identity, expectedWorldSource)
	}

	if !facts.IdentityVerified || !facts.NetworkVerified || !facts.MountVerified || !hasWorldMount || facts.Lifecycle != worldview.ModuleRunning {
		// Deliberately no webUp() and no config reads here: a failed/stopped or
		// wrongly mounted module is not a renderer, even if a listener is up.
		return facts, nil
	}

	accessor, err := d.resolver.ForNode(ctx, nodestorage.LocalNode(), server.DataPath)
	if err != nil {
		if d.log != nil {
			d.log.Warn("bluemap discovery: confined accessor for %s failed: %v", serverID, err)
		}
		return facts, nil
	}
	defer func() { _ = accessor.Close() }()

	facts.AcceptDownload = readAcceptDownload(accessor)
	mapID, worldOK := readMapConfig(accessor)
	if worldOK && levelName != "" {
		facts.MapID = mapID
		facts.WorldName = levelName
		facts.WorldMatchesServer = true
	}
	facts.Rendered = tilesExist(accessor, mapID)
	facts.WebUp = webUp(ctx, facts.BaseURL)
	return facts, nil
}

// rendererBaseURL uses the owned-network IP observed on the container. An
// empty/invalid IP fails closed with no BaseURL: no DNS guessing, no host port.
func rendererBaseURL(identity *docker.ModuleRuntimeIdentity) string {
	if identity == nil {
		return ""
	}
	ip := net.ParseIP(strings.TrimSpace(identity.RendererIP))
	if ip == nil {
		return ""
	}
	return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(bluemapWebPort))
}

func canonicalNode(nodeID string) string {
	if strings.TrimSpace(nodeID) == "" {
		return "default"
	}
	return nodeID
}

func lifecycleFromState(state string) worldview.ModuleLifecycle {
	switch strings.ToLower(state) {
	case "running", "restarting":
		return worldview.ModuleRunning
	case "created", "paused":
		return worldview.ModulePending
	case "":
		return worldview.ModuleFailed
	default:
		return worldview.ModuleStopped
	}
}

// identityMatchesRuntime binds panel state to the exact container: module,
// server, node and template labels plus the pinned, unprivileged image.
func identityMatchesRuntime(module *storage.Module, serverID, nodeID string, runtime *docker.ModuleRuntimeIdentity) bool {
	return runtime.ModuleID == module.ID &&
		runtime.ServerID == serverID &&
		canonicalNode(runtime.NodeID) == nodeID &&
		runtime.TemplateID == worldview.BuiltinBlueMapTemplateID &&
		runtime.Image == bluemapImageTag &&
		!runtime.Privileged
}

// mountMatches is the pure gate for the effective world bind: the node-side
// source must be the requested canonical level folder, the container target
// /app/world, the bind read-only, and the private web port exposed internally.
func mountMatches(identity *docker.ModuleRuntimeIdentity, expectedWorldSource string) bool {
	if identity == nil {
		return false
	}
	mount, ok := identity.ModuleWorldMount()
	if !ok || !mount.ReadOnly || !containsPort(identity.ContainerPorts, bluemapWebPort) {
		return false
	}
	return samePath(mount.Source, expectedWorldSource)
}

func containsPort(ports []int, want int) bool {
	for _, p := range ports {
		if p == want {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	// Windows path comparison is case-insensitive for the same volume.
	return strings.EqualFold(ca, cb)
}

// readAcceptDownload observes the explicit acknowledgement in core.conf via
// the confined accessor. Missing/unreadable config is false.
func readAcceptDownload(accessor configAccessor) bool {
	data, err := accessor.ReadFile("modules/bluemap/config/core.conf")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			key, value, ok = strings.Cut(line, "=")
		}
		if ok && strings.EqualFold(strings.Trim(key, `"' `), "accept-download") &&
			strings.EqualFold(strings.Trim(strings.TrimSpace(value), `"' `), "true") {
			return true
		}
	}
	return false
}

// readMapConfig finds a map whose `world` points at the container's /app/world
// bind. It returns the map id and whether such a config exists. Non-default
// dimensions need their own explicit config, which is observed not guessed.
func readMapConfig(accessor configAccessor) (string, bool) {
	entries, err := accessor.ReadDir("modules/bluemap/config/maps")
	if err != nil {
		return "", false
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(name), ".conf") || !plainComponent(name) {
			continue
		}
		raw, err := accessor.ReadFile("modules/bluemap/config/maps/" + name)
		if err != nil {
			continue
		}
		id, world, ok := parseMapConfig(string(raw))
		if !ok || !isAppWorld(world) || !plainComponent(id) {
			continue
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return "", false
	}
	sort.Strings(ids)
	return ids[0], true
}

func parseMapConfig(raw string) (id, world string, ok bool) {
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, ":")
		if !found {
			key, value, found = strings.Cut(line, "=")
		}
		if !found {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"' `)
		value = strings.Trim(strings.TrimSpace(value), `"' `)
		switch strings.ToLower(key) {
		case "id":
			id = value
		case "world":
			world = value
		}
	}
	return id, world, id != ""
}

func plainComponent(name string) bool {
	return name != "" && name != "." && name != ".." &&
		!strings.ContainsAny(name, "/\\:\x00\r\n") && strings.TrimSpace(name) == name
}

func isAppWorld(world string) bool {
	w := strings.TrimSpace(strings.Trim(world, `"' `))
	switch w {
	case "/app/world", "world", "./world", "/app/world/":
		return true
	}
	return false
}

// tilesExist observes whether the renderer produced any scoped tile for the
// configured map. A directory with no tiles is a render in progress.
func tilesExist(accessor configAccessor, mapID string) bool {
	if !plainComponent(mapID) {
		return false
	}
	entries, err := accessor.ReadDir("modules/bluemap/web/maps/" + mapID + "/tiles")
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// webUp only answers "is the private listener up". It is called only after all
// identity/network/mount gates pass; readiness is composed from configured map
// and rendered-tile facts, never from this HTTP 200 alone.
func webUp(ctx context.Context, baseURL string) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("bluemap discovery: redirects are not followed")
	}}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}
