// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"fmt"
	"path/filepath"
	"strings"
)

// HostFacts is the bounded, redacted input to host-native preflight. It is a
// plain value so fixtures can supply synthetic facts without touching a live
// host, global configuration or a real daemon. Facts are observed, never
// inferred from a successful Docker ping: a preinstalled engine is not proof
// that this mode is qualified.
type HostFacts struct {
	OS            string
	Architecture  string
	KernelMajor   int
	KernelMinor   int
	Distro        string
	KernelVersion string

	Systemd            bool
	SystemdUserSession bool
	LingerEnabled      bool
	DedicatedUIDAvailable bool

	UserNamespaces      bool
	SubUIDAvailable     bool
	SubGIDAvailable     bool
	CgroupVersion       string // "v2", "v1" or "unknown"
	DelegatedControllers []string

	StorageDriver   string
	StorageFSType   string
	FTypeEnabled    bool
	LogDriver       string // empty selects journald; json-file for disposable fixtures
	XDG_RUNTIME_DIR string
	RuntimeDirMode  uint32

	// PrivilegedPortStart mirrors net.ipv4.ip_unprivileged_port_start. A value
	// above 1024 means game ports below 1024 need an explicit capability gate.
	PrivilegedPortStart uint32

	// HostDockerPresent records a preinstalled/shared engine. It is a warning
	// signal only; it never authorises using that engine as a fallback.
	HostDockerPresent bool

	// Network facts for private bridge / namespace isolation.
	BridgeCIDR          string
	BridgeOverlapsHost  bool
	ChosenPortRange     string
	PortRangeOverlaps2375 bool

	// Chosen private paths and identity for the dedicated daemon.
	ChosenSocket        string
	ChosenDataRoot      string
	ChosenExecRoot      string
	ContainerdNamespace string
	HostSocketPath      string // defaults to /var/run/docker.sock when empty
}

// Refusal codes are stable, precise and safe to surface in a doctor report.
// They are strings (not proto enum values) because they are mode-local
// diagnostics; the shared wire contract is not extended by this package.
const (
	CodeOSUnsupported              = "HOST_OS_UNSUPPORTED"
	CodeSystemdRequired            = "SYSTEMD_REQUIRED"
	CodeUserNamespacesDisabled     = "ROOTLESS_USERNAMES_DISABLED"
	CodeSubIDsMissing              = "ROOTLESS_SUBUID_SUBGID_MISSING"
	CodeCgroupV2NotDelegated       = "ROOTLESS_CGROUP_V2_NOT_DELEGATED"
	CodeStorageDriverUnsupported   = "STORAGE_DRIVER_UNSUPPORTED"
	CodeRuntimeDirUnsafe           = "ROOTLESS_RUNTIME_DIR_UNSAFE"
	CodePrivilegedPortUnsupported  = "PRIVILEGED_PORT_UNSUPPORTED"
	CodePrivateSocketCollision     = "PRIVATE_SOCKET_COLLISION"
	CodeDedicatedRootCollision     = "DEDICATED_ROOT_COLLISION"
	CodeNamespaceCollision         = "CONTAINERD_NAMESPACE_COLLISION"
	CodePrivateNetworkCollision    = "PRIVATE_NETWORK_COLLISION"
	CodeSharedDaemonFallback       = "SHARED_DAEMON_FALLBACK_FORBIDDEN"
	CodePublicDockerAPI            = "PUBLIC_DOCKER_API_FORBIDDEN"
	CodeRootfulPrereqUnmet         = "ROOTFUL_PREREQ_UNMET"
	CodeUnsupportedTLS              = "UNSUPPORTED_TLS_TOPOLOGY"
)

// DaemonMode is the selected process mode after gates are applied.
type DaemonMode string

const (
	ModeNone    DaemonMode = ""
	ModeRootless DaemonMode = "rootless"
	ModeRootful  DaemonMode = "dedicated-rootful"
)

// Check is one named preflight result with an actionable remedy.
type Check struct {
	Code    string
	Passed  bool
	Unknown bool
	Message string
	Remedy  string
}

// Selection is the result of applying the compatibility gates. Mode is empty
// when no qualified path exists; Reasons then carry the exact refusals.
type Selection struct {
	Mode    DaemonMode
	Checks  []Check
	Reasons []Check
}

// ApplyGates evaluates every compatibility gate against facts and selects the
// least-privileged qualified mode. Rootless is chosen when all rootless gates
// pass. If a rootless gate fails, a hardened dedicated rootful service is
// selected only when its own prerequisites pass and no universal gate fails.
// Otherwise Mode is empty and the node must fail closed.
//
// A preinstalled/shared Docker daemon never contributes to qualification and
// is always refused as a fallback (CodeSharedDaemonFallback).
func ApplyGates(f HostFacts) Selection {
	s := Selection{}
	add := func(c Check) { s.Checks = append(s.Checks, c) }
	// universalOK tracks only gates that apply to BOTH modes. Rootless-specific
	// failures must not silently disqualify the rootful compatibility path.
	universalOK := true
	universalFail := func(code, msg, remedy string) {
		add(Check{Code: code, Message: msg, Remedy: remedy})
		universalOK = false
	}

	// Universal gates: apply to both modes.
	if !strings.EqualFold(f.OS, "linux") {
		universalFail(CodeOSUnsupported, "host-native private daemon requires Linux", "Use a Linux host or select a different deployment mode.")
	}
	hostSocket := f.HostSocketPath
	if hostSocket == "" {
		hostSocket = "/var/run/docker.sock"
	}
	if f.ChosenSocket == "" || samePath(f.ChosenSocket, hostSocket) || !isPrivateRuntimePath(f.ChosenSocket, f.XDG_RUNTIME_DIR) {
		universalFail(CodePrivateSocketCollision, "daemon socket is not private to this installation", "Use a dedicated socket under the node runtime directory; never /var/run/docker.sock.")
	}
	if f.ChosenDataRoot == "" || f.ChosenExecRoot == "" ||
		sameOrUnder(f.ChosenDataRoot, "/var/lib/docker") || sameOrUnder(f.ChosenExecRoot, "/var/run/docker") {
		universalFail(CodeDedicatedRootCollision, "data/exec root overlaps a shared Docker root", "Allocate dedicated data-root and exec-root paths under the node's private subtree.")
	}
	if f.ContainerdNamespace == "" || f.ContainerdNamespace == "moby" || f.ContainerdNamespace == "default" {
		universalFail(CodeNamespaceCollision, "containerd namespace is shared with a host engine", "Use a dedicated containerd namespace for this installation.")
	}
	if f.BridgeOverlapsHost || f.PortRangeOverlaps2375 || f.BridgeCIDR == "" {
		universalFail(CodePrivateNetworkCollision, "private network range overlaps the host or raw API ports", "Choose a non-overlapping bridge CIDR and a port range that excludes 2375/2376.")
	}
	if f.HostDockerPresent {
		add(Check{Code: "HOST_DOCKER_PRESENT", Message: "a preinstalled Docker engine is present; it is not used as a fallback", Remedy: "Keep the managed daemon on its private socket and roots."})
	}
	add(Check{Code: "NO_SHARED_DAEMON_FALLBACK", Passed: true, Message: "shared/preinstalled daemon fallback is disabled", Remedy: ""})

	// Rootless gates. A failure here selects the rootful path; it never
	// authorises using a preinstalled engine.
	rootlessOK := true
	markRootless := func(code, msg, remedy string) {
		add(Check{Code: code, Message: msg, Remedy: remedy})
		rootlessOK = false
	}
	if !f.UserNamespaces {
		markRootless(CodeUserNamespacesDisabled, "unprivileged user namespaces are disabled", "Enable user namespaces on the host or choose the dedicated rootful path.")
	}
	if !f.SubUIDAvailable || !f.SubGIDAvailable {
		markRootless(CodeSubIDsMissing, "subuid/subgid ranges are unavailable", "Configure /etc/subuid and /etc/subgid for the service user or choose the dedicated rootful path.")
	}
	if strings.TrimSpace(f.CgroupVersion) != "v2" || !hasAll(f.DelegatedControllers, "cpu", "cpuset", "io", "memory", "pids") {
		markRootless(CodeCgroupV2NotDelegated, "cgroup v2 controllers are not delegated", "Delegate cpu, cpuset, io, memory and pids to the service user; memory/pids limits are refused otherwise.")
	}
	if !storageDriverQualified(f) {
		markRootless(CodeStorageDriverUnsupported, "no supported rootless storage driver is available", "Use overlay2 on kernel >=5.11 with ftype=1/ext4, or fuse-overlayfs/btrfs/vfs.")
	}
	if f.XDG_RUNTIME_DIR == "" || strings.HasPrefix(filepath.Clean(f.XDG_RUNTIME_DIR), "/tmp") || f.RuntimeDirMode == 0 || f.RuntimeDirMode&0o077 != 0 {
		markRootless(CodeRuntimeDirUnsafe, "XDG_RUNTIME_DIR is missing, world-accessible or under /tmp", "Use a private 0700 runtime directory outside /tmp (loginctl enable-linger).")
	}
	if f.PrivilegedPortStart > 1024 {
		add(Check{Code: CodePrivilegedPortUnsupported, Passed: true, Message: "privileged ports require an explicit capability grant", Remedy: "Use ports >=1024 or grant CAP_NET_BIND_SERVICE explicitly; no silent grant is performed."})
	}

	if universalOK && rootlessOK {
		s.Mode = ModeRootless
		return s
	}
	if !universalOK {
		s.Reasons = failedChecks(s.Checks)
		return s
	}

	// Dedicated rootful compatibility path: explicit, hardened, never silent.
	if !f.Systemd || !f.DedicatedUIDAvailable {
		add(Check{Code: CodeRootfulPrereqUnmet, Message: "dedicated rootful prerequisites are unmet", Remedy: "Requires systemd and a dedicated non-login service UID; do not fall back to a preinstalled engine."})
		s.Reasons = failedChecks(s.Checks)
		return s
	}
	s.Mode = ModeRootful
	add(Check{Code: "ROOTFUL_COMPAT_PATH", Passed: true, Message: "hardened dedicated rootful service selected because rootless prerequisites are unmet", Remedy: "Apply the hardened unit; no blanket privileges and no shared socket."})
	return s
}

func failedChecks(checks []Check) []Check {
	var out []Check
	for _, c := range checks {
		if !c.Passed && !c.Unknown {
			out = append(out, c)
		}
	}
	return out
}

func hasAll(list []string, want ...string) bool {
	set := make(map[string]struct{}, len(list))
	for _, v := range list {
		set[strings.TrimSpace(v)] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[w]; !ok {
			return false
		}
	}
	return len(set) > 0
}

func storageDriverQualified(f HostFacts) bool {
	switch strings.ToLower(strings.TrimSpace(f.StorageDriver)) {
	case "overlay2":
		return f.KernelMajor > 5 || (f.KernelMajor == 5 && f.KernelMinor >= 11)
	case "fuse-overlayfs":
		return f.KernelMajor > 4 || (f.KernelMajor == 4 && f.KernelMinor >= 18)
	case "btrfs", "vfs":
		return f.KernelMajor >= 4
	default:
		return false
	}
}

func samePath(a, b string) bool {
	return filepath.Clean(a) == filepath.Clean(b)
}

func sameOrUnder(path, parent string) bool {
	p, q := filepath.Clean(path), filepath.Clean(parent)
	return p == q || strings.HasPrefix(p, q+string(filepath.Separator))
}

func isPrivateRuntimePath(socket, runtimeDir string) bool {
	if runtimeDir == "" {
		return false
	}
	return sameOrUnder(socket, filepath.Clean(runtimeDir))
}

// Describe is a short, redacted human summary of a selection.
func Describe(s Selection) string {
	if s.Mode == ModeNone {
		if len(s.Reasons) == 0 {
			return "no qualified host-native mode (no reasons recorded)"
		}
		parts := make([]string, 0, len(s.Reasons))
		for _, r := range s.Reasons {
			parts = append(parts, r.Code)
		}
		return "no qualified host-native mode: " + strings.Join(parts, ", ")
	}
	return fmt.Sprintf("qualified host-native mode: %s", s.Mode)
}
