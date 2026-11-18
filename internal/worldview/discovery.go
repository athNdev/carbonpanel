// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"errors"
)

// BuiltinBlueMapTemplateID is the only module template that discovery may
// bind to a renderer. Compatibility adapters stay explicitly configured.
const BuiltinBlueMapTemplateID = "builtin-bluemap"

var (
	// ErrDiscoveredRendererUnavailable means a module was found but its private
	// transport facts are not a usable renderer for this exact server/node.
	ErrDiscoveredRendererUnavailable = errors.New("worldview: discovered renderer unavailable")
)

// ModuleLifecycle is the panel-owned lifecycle of an installed module. It is
// the module's own status, never inferred from a port being open.
type ModuleLifecycle string

const (
	ModulePending ModuleLifecycle = "pending" // create/start in flight
	ModuleRunning ModuleLifecycle = "running"
	ModuleStopped ModuleLifecycle = "stopped"
	ModuleFailed  ModuleLifecycle = "failed"
)

// InstalledRenderer is the identity-bound, private fact set for one installed
// renderer module. It is produced exclusively by ModuleDiscovery from
// panel-owned state and the PRIVATE backend transport.
//
// No field may come from user input or a port probe. BaseURL is an owned
// node-local network address and is internal only: it is never serialized to a
// browser, never a public listener and never a controller-local fallback.
type InstalledRenderer struct {
	ModuleID   string
	ServerID   string
	NodeID     string
	TemplateID string
	Lifecycle  ModuleLifecycle

	// Runtime gates are observed from node-scoped container metadata before
	// any HTTP request. All three must be true before a renderer is contacted.
	IdentityVerified bool // exact module/server/node/template labels, pinned image, unprivileged
	NetworkVerified  bool // effective membership on the owned Docker network
	MountVerified    bool // effective /app/world bind is the actual level folder and read-only

	// BaseURL is the private renderer location on the owned node-local network,
	// e.g. http://carbon-panel-module-<module-id>:8100. It is constructed from
	// panel-owned module identity and the template-declared internal port, never
	// from a host-port binding or an operator URL.
	BaseURL string
	// ContainerPort is the template-declared internal port (not a host port).
	ContainerPort int
	// MapID is the BlueMap map id whose `world` points at this server's actual
	// level folder. Readiness requires the renderer's own /settings.json to
	// contain this exact map, not merely a listening web server.
	MapID string
	// WorldName is the ACTUAL target-world identity configured in the module's
	// own standalone BlueMap config. Empty means setup is not complete.
	WorldName string
	// WorldMatchesServer reports that WorldName equals the server's actual
	// configured level name, including non-default world folders.
	WorldMatchesServer bool
	// AcceptDownload records the explicit resource-download / license
	// acknowledgement in the module config. New installs require it; adoption
	// never rewrites it.
	AcceptDownload bool
	// WebUp reports that the private renderer web endpoint answered a probe.
	WebUp bool
	// Rendered reports that scoped tiles exist for the configured target world.
	// A listening web server alone never implies this.
	Rendered bool
	// ReadOnlyWorldMount records the effective, verified world bind mode.
	ReadOnlyWorldMount bool
	// Version, when reported by the renderer, is the actual running version.
	Version string
}

// ModuleDiscovery is the PRIVATE backend transport seam. It locates the exact
// installed renderer module for (nodeID, serverID) through panel-owned state
// and the node-local owned network.
//
// It MUST NOT probe ports or host bindings, MUST NOT fall back to another
// server's module, MUST NOT return a public URL, and MUST NOT broaden container
// privileges. Discovery is node-local: remote nodes are only reachable through
// an explicit operator endpoint and are reported unavailable otherwise.
type ModuleDiscovery interface {
	FindRenderer(ctx context.Context, nodeID, serverID string) (*InstalledRenderer, error)
}

// NoModuleDiscovery preserves the legacy explicit-config-only behavior. It is
// the default so existing callers keep failing closed until a discovery
// implementation is injected at the composition root.
type NoModuleDiscovery struct{}

func (NoModuleDiscovery) FindRenderer(context.Context, string, string) (*InstalledRenderer, error) {
	return nil, nil
}

// identityMatches enforces the exact server/node/module binding. It is the
// fail-closed check that prevents discovery from serving another server's map.
func identityMatches(facts *InstalledRenderer, nodeID, serverID string) bool {
	return facts != nil &&
		facts.ServerID == serverID &&
		canonicalNodeID(facts.NodeID) == canonicalNodeID(nodeID) &&
		facts.TemplateID == BuiltinBlueMapTemplateID &&
		facts.ModuleID != "" &&
		facts.BaseURL != ""
}
