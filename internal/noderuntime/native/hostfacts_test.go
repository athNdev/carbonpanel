// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"strings"
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// qualifiedFacts is a disposable, synthetic host description. No field is read
// from the real machine: tests never touch a socket, daemon or host config.
func qualifiedFacts() HostFacts {
	return HostFacts{
		OS: "linux", Architecture: "amd64", Distro: "fixture-linux",
		KernelVersion: "6.8.0-fixture", KernelMajor: 6, KernelMinor: 8,
		Systemd: true, SystemdUserSession: true, LingerEnabled: true,
		RootfulServiceQualified: true,
		UserNamespaces:          true, SubUIDAvailable: true, SubGIDAvailable: true,
		CgroupVersion: "v2", DelegatedControllers: []string{"cpu", "cpuset", "io", "memory", "pids"},
		StorageDriver: "overlay2", StorageFSType: "ext4", FTypeEnabled: true,
		XDG_RUNTIME_DIR: "/run/user/1000/carbon-node", RuntimeDirMode: 0o700,
		PrivilegedPortStart: 1024,
		BridgeCIDR:          "10.231.0.1/24", ChosenPortRange: "30000-31000",
		ChosenSocket:   "/run/user/1000/carbon-node/docker.sock",
		ChosenDataRoot: "/var/lib/carbon-node/docker", ChosenExecRoot: "/run/carbon-node/docker",
		ContainerdNamespace: "carbon-node-1",
	}
}

func TestApplyGatesRootlessQualified(t *testing.T) {
	sel := ApplyGates(qualifiedFacts())
	if sel.Mode != ModeRootless {
		t.Fatalf("expected rootless, got %q (%s)", sel.Mode, Describe(sel))
	}
	for _, c := range sel.Reasons {
		t.Fatalf("unexpected refusal on qualified host: %s", c.Code)
	}
	caps := Capabilities(qualifiedFacts(), sel)
	found := map[v1CapabilityAlias]bool{}
	for _, c := range caps.GetCapabilities() {
		found[v1CapabilityAlias(c.String())] = true
	}
	if !found["NODE_CAPABILITY_ROOTLESS"] || !found["NODE_CAPABILITY_CGROUP_V2"] || !found["NODE_CAPABILITY_PRIVATE_SOCKET"] {
		t.Fatalf("capability snapshot missing rootless/cgroup/private flags: %v", caps.GetCapabilities())
	}
}

// v1CapabilityAlias keeps the assertion readable without importing the shared
// proto enum into a pure gate test.
type v1CapabilityAlias string

func TestApplyGatesRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*HostFacts)
		want string
	}{
		{"non-linux", func(f *HostFacts) { f.OS = "windows" }, CodeOSUnsupported},
		{"userns disabled", func(f *HostFacts) { f.UserNamespaces = false }, CodeRootlessRefusalFor("ROOTLESS_USERNAMES_DISABLED")},
		{"no subids", func(f *HostFacts) { f.SubGIDAvailable = false }, CodeRootlessRefusalFor("ROOTLESS_SUBUID_SUBGID_MISSING")},
		{"no cgroup delegation", func(f *HostFacts) { f.DelegatedControllers = []string{"cpu"} }, CodeRootlessRefusalFor("ROOTLESS_CGROUP_V2_NOT_DELEGATED")},
		{"bad storage driver", func(f *HostFacts) { f.StorageDriver = "nfs" }, CodeRootlessRefusalFor("STORAGE_DRIVER_UNSUPPORTED")},
		{"unsafe runtime dir", func(f *HostFacts) { f.XDG_RUNTIME_DIR = "/tmp/carbon"; f.RuntimeDirMode = 0o777 }, CodeRootlessRefusalFor("ROOTLESS_RUNTIME_DIR_UNSAFE")},
		{"host socket chosen", func(f *HostFacts) { f.ChosenSocket = "/var/run/docker.sock" }, CodePrivateSocketCollision},
		{"shared data root", func(f *HostFacts) { f.ChosenDataRoot = "/var/lib/docker" }, CodeDedicatedRootCollision},
		{"shared namespace", func(f *HostFacts) { f.ContainerdNamespace = "moby" }, CodeNamespaceCollision},
		{"overlapping network", func(f *HostFacts) { f.BridgeOverlapsHost = true }, CodePrivateNetworkCollision},
		{"raw api port", func(f *HostFacts) { f.PortRangeOverlaps2375 = true }, CodePrivateNetworkCollision},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := qualifiedFacts()
			tc.mut(&f)
			sel := ApplyGates(f)
			if !hasCode(sel.Checks, tc.want) && !hasCode(sel.Reasons, tc.want) {
				t.Fatalf("expected refusal %s, got %s", tc.want, Describe(sel))
			}
		})
	}
}

// A rootless-only failure with a qualified rootful fallback selects the
// hardened dedicated rootful path, never a preinstalled engine.
func TestApplyGatesRootfulFallback(t *testing.T) {
	f := qualifiedFacts()
	f.UserNamespaces = false
	sel := ApplyGates(f)
	if sel.Mode != ModeRootful {
		t.Fatalf("expected dedicated rootful fallback, got %q (%s)", sel.Mode, Describe(sel))
	}
	if !hasCode(sel.Checks, CodeUserNamespacesDisabled) {
		t.Fatal("rootless refusal not recorded in report")
	}
	if !hasCode(sel.Checks, "ROOTFUL_COMPAT_PATH") {
		t.Fatal("rootful compatibility path not recorded")
	}
}

// Effective resource enforcement must not be advertised when cgroup v2
// delegation is incomplete. Missing cpuset refuses rootless qualification and,
// on a rootful fallback, the captive daemon must not claim CGROUP_V2.
func TestCapabilitiesRequireFullCgroupV2Delegation(t *testing.T) {
	f := qualifiedFacts()
	f.UserNamespaces = false
	f.DelegatedControllers = []string{"cpu", "io", "memory", "pids"}
	sel := ApplyGates(f)
	if sel.Mode != ModeRootful {
		t.Fatalf("expected dedicated rootful fallback, got %q (%s)", sel.Mode, Describe(sel))
	}
	if !hasCode(sel.Checks, "ROOTLESS_CGROUP_V2_NOT_DELEGATED") {
		t.Fatalf("missing cgroup delegation refusal: %s", Describe(sel))
	}
	caps := Capabilities(f, sel)
	for _, c := range caps.GetCapabilities() {
		if c.String() == "NODE_CAPABILITY_CGROUP_V2" {
			t.Fatalf("over-advertised cgroup v2 capability without full delegation: %v", caps.GetCapabilities())
		}
		if c.String() == "NODE_CAPABILITY_ROOTLESS" {
			t.Fatalf("rootful fallback must not advertise rootless: %v", caps.GetCapabilities())
		}
	}
}

// Cgroup v1 hosts cannot claim cgroup v2 effective resource enforcement.
func TestCapabilitiesDoNotAdvertiseCgroupV2OnCgroupV1(t *testing.T) {
	f := qualifiedFacts()
	f.CgroupVersion = "v1"
	sel := ApplyGates(f)
	if sel.Mode == ModeRootless {
		t.Fatal("cgroup v1 host must not qualify as rootless")
	}
	caps := Capabilities(f, sel)
	for _, c := range caps.GetCapabilities() {
		if c.String() == "NODE_CAPABILITY_CGROUP_V2" {
			t.Fatalf("advertised cgroup v2 capability on cgroup v1 host: %v", caps.GetCapabilities())
		}
	}
}

func TestApplyGatesFailsClosedWithoutQualifiedPath(t *testing.T) {
	f := qualifiedFacts()
	f.UserNamespaces = false
	f.RootfulServiceQualified = false
	sel := ApplyGates(f)
	if sel.Mode != ModeNone {
		t.Fatalf("expected fail-closed, got %q", sel.Mode)
	}
	if !hasCode(sel.Reasons, CodeRootfulPrereqUnmet) {
		t.Fatalf("missing precise rootful refusal: %s", Describe(sel))
	}
}

func TestSharedDaemonIsNeverAFallback(t *testing.T) {
	f := qualifiedFacts()
	f.HostDockerPresent = true
	sel := ApplyGates(f)
	if sel.Mode != ModeRootless {
		t.Fatalf("preinstalled daemon must not change qualification, got %q", sel.Mode)
	}
	if !hasCode(sel.Checks, "NO_SHARED_DAEMON_FALLBACK") {
		t.Fatal("no-shared-fallback invariant not recorded")
	}
	// A request to use the shared daemon as the chosen endpoint is refused.
	f.ChosenSocket = "/var/run/docker.sock"
	if hasCode(ApplyGates(f).Checks, CodePrivateSocketCollision) == false {
		t.Fatal("shared host socket was not refused")
	}
}

// A capability is a usable feature grant, not a raw host fact. No mode means
// no captive daemon can use any daemon-dependent feature, so every such grant
// is withheld at one boundary regardless of which universal gate refused.
// Host diagnostics and daemon-independent agent facilities stay available.
func TestCapabilitiesWithholdRuntimeGrantsWithoutQualifiedMode(t *testing.T) {
	runtimeGrants := []string{
		"NODE_CAPABILITY_PRIVATE_SOCKET",
		"NODE_CAPABILITY_DEDICATED_ROOTS",
		"NODE_CAPABILITY_CONTAINERD_NAMESPACE",
		"NODE_CAPABILITY_ROOTLESS",
		"NODE_CAPABILITY_CGROUP_V2",
		"NODE_CAPABILITY_PRIVILEGED_PORT",
	}
	for _, tc := range []struct {
		name string
		mut  func(*HostFacts)
		code string
	}{
		{"socket collision", func(f *HostFacts) { f.ChosenSocket = "/var/run/docker.sock" }, CodePrivateSocketCollision},
		{"dedicated root collision", func(f *HostFacts) { f.ChosenDataRoot = "/var/lib/docker" }, CodeDedicatedRootCollision},
		{"namespace collision", func(f *HostFacts) { f.ContainerdNamespace = "moby" }, CodeNamespaceCollision},
		{"private network collision", func(f *HostFacts) { f.BridgeOverlapsHost = true }, CodePrivateNetworkCollision},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := qualifiedFacts()
			tc.mut(&f)
			sel := ApplyGates(f)
			if sel.Mode != ModeNone || !hasCode(sel.Reasons, tc.code) {
				t.Fatalf("expected fail-closed with %s, got %s", tc.code, Describe(sel))
			}
			snapshot := Capabilities(f, sel)
			caps := capabilitySet(snapshot)
			for _, grant := range runtimeGrants {
				if caps[grant] {
					t.Errorf("unusable runtime grant %s advertised without a qualified mode: %v", grant, caps)
				}
			}
			// Independently usable facilities and host diagnostics are preserved.
			if !caps["NODE_CAPABILITY_STORAGE_WORKER"] || !caps["NODE_CAPABILITY_DRAIN"] {
				t.Fatalf("daemon-independent facilities must remain granted: %v", caps)
			}
			if snapshot.GetCgroupVersion() != "v2" || snapshot.GetStorageDriver() != "overlay2" || !snapshot.GetSystemd() {
				t.Fatalf("host diagnostics lost on fail-closed snapshot: %+v", snapshot)
			}
		})
	}
}

// An authorized qualified mode still advertises its grants. Rootful with full
// cgroup delegation is a qualifying compatibility path and must advertise
// cgroup v2 resource enforcement (but never the rootless-only grant).
func TestCapabilitiesQualifiedRootfulModeAdvertisesGrants(t *testing.T) {
	f := qualifiedFacts()
	f.UserNamespaces = false // rootless disqualified, rootful explicitly qualified
	sel := ApplyGates(f)
	if sel.Mode != ModeRootful {
		t.Fatalf("expected qualified rootful mode, got %q (%s)", sel.Mode, Describe(sel))
	}
	caps := capabilitySet(Capabilities(f, sel))
	for _, grant := range []string{
		"NODE_CAPABILITY_PRIVATE_SOCKET",
		"NODE_CAPABILITY_DEDICATED_ROOTS",
		"NODE_CAPABILITY_CONTAINERD_NAMESPACE",
		"NODE_CAPABILITY_CGROUP_V2",
		"NODE_CAPABILITY_PRIVILEGED_PORT",
	} {
		if !caps[grant] {
			t.Errorf("qualified rootful mode missing grant %s: %v", grant, caps)
		}
	}
	if caps["NODE_CAPABILITY_ROOTLESS"] {
		t.Fatalf("rootful mode must not advertise rootless: %v", caps)
	}
}

func TestPreflightReportIsRedacted(t *testing.T) {
	sel := ApplyGates(qualifiedFacts())
	report := PreflightReport("node-fixture", sel, fixtureTime())
	if report.GetNodeId() != "node-fixture" || len(report.GetChecks()) == 0 {
		t.Fatal("preflight report missing identity or checks")
	}
	// Host paths and secrets must not leak into the wire report messages.
	for _, c := range report.GetChecks() {
		if strings.Contains(c.GetMessage(), "1000/carbon-node") || strings.Contains(c.GetRemedy(), "/var/lib/carbon-node") {
			t.Fatalf("preflight check leaked a private path: %+v", c)
		}
	}
}

func capabilitySet(caps interface{ GetCapabilities() []v1.NodeCapability }) map[string]bool {
	out := make(map[string]bool)
	for _, c := range caps.GetCapabilities() {
		out[c.String()] = true
	}
	return out
}

func hasCode(checks []Check, code string) bool {
	for _, c := range checks {
		if c.Code == code {
			return true
		}
	}
	return false
}

// CodeRootlessRefusalFor documents that the precise code is the constant in
// hostfacts.go; the indirection keeps the table above readable.
func CodeRootlessRefusalFor(code string) string { return code }
