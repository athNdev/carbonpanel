// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// INTEGRATION SEAM (OSS-376 / OSS-379 alignment)
// ------------------------------------------------
// The native-daemon lane owns the ONE canonical dockerd-options renderer. At
// the time of writing that renderer had not yet landed on main: the merged
// PR527 renderer still duplicated options between CLI flags and config.json,
// rendered rootless as a direct system unit, and rendered rootful as a non-root
// service with an empty capability bound. We therefore implement the same
// one-source-of-options rule locally and expose it behind DaemonRenderer.
//
// When the native lane publishes its fixed canonical renderer, wire an adapter
// here with SetDaemonRenderer and DELETE the local option definitions below.
// No installer caller, CLI verb or fixture should need to change: they all go
// through RenderDaemon. This file never writes to the host; it renders strings
// for disposable fixtures and for the caller-owned artifact output directory.

// DaemonMode is the qualified isolation mode of the private daemon.
type DaemonMode string

const (
	ModeRootless DaemonMode = "rootless"
	ModeRootful  DaemonMode = "rootful"
)

// DaemonSpec is a disposable, secret-free description of the private daemon.
// Every path is caller-owned; ConfigPath must be inside that owned state/data
// tree, never a global host configuration path.
type DaemonSpec struct {
	NodeID              string     `json:"node_id"`
	Mode                DaemonMode `json:"mode"`
	ConfigPath          string     `json:"config_path"`
	SocketPath          string     `json:"socket_path"`
	DataRoot            string     `json:"data_root"`
	ExecRoot            string     `json:"exec_root"`
	ContainerdNamespace string     `json:"containerd_namespace"`
	BridgeCIDR          string     `json:"bridge_cidr"`
	PortRange           string     `json:"port_range"`
	StorageDriver       string     `json:"storage_driver"`
	RuntimeUID          int        `json:"runtime_uid,omitempty"` // rootless XDG_RUNTIME_DIR owner
	// RuntimeRoot and StateRoot are the caller-owned containment boundaries.
	// SocketPath must stay under RuntimeRoot; ConfigPath, DataRoot and ExecRoot
	// must stay under StateRoot. They are never global host paths.
	RuntimeRoot string `json:"runtime_root"`
	StateRoot   string `json:"state_root"`
	// DaemonBinary and RootlessLauncher are optional read-only overrides for
	// disposable fixtures. Production leaves them empty (the canonical paths
	// are rendered). Fixtures point them at a temporary no-op binary.
	DaemonBinary     string `json:"daemon_binary,omitempty"`
	RootlessLauncher string `json:"rootless_launcher,omitempty"`
}

// DaemonRender is the in-memory result. Nothing here is installed or executed.
type DaemonRender struct {
	Mode        string `json:"mode"`
	Unit        string `json:"unit"`
	Config      string `json:"config"`
	InstallHint string `json:"install_hint"`
}

// DaemonRenderer is the seam. The local implementation is the current single
// source of daemon options; a shared implementation can replace it without
// changing any consumer.
type DaemonRenderer interface {
	Render(DaemonSpec) (DaemonRender, error)
}

var daemonRenderer DaemonRenderer = localDaemonRenderer{}

// SetDaemonRenderer replaces the local renderer with the shared canonical
// renderer once it is available. It is a build-time wiring hook, never driven
// by untrusted CLI input.
func SetDaemonRenderer(r DaemonRenderer) {
	if r != nil {
		daemonRenderer = r
	}
}

// RenderDaemon renders one spec through the active renderer.
func RenderDaemon(spec DaemonSpec) (DaemonRender, error) {
	return daemonRenderer.Render(spec)
}

// daemonOption is the ONLY definition of a dockerd option. Both the config
// document and the unit are derived from this list: the unit carries no option
// flags at all, only --config-file. This is the one-source-of-options rule the
// PR527 review found missing.
type daemonOption struct {
	key   string
	value any
}

// rootfulCapabilities is the explicit, non-empty capability bound a rootful
// dockerd needs. It deliberately rejects the "User=nonroot + empty bound"
// rendering from PR527, which cannot start a daemon.
var rootfulCapabilities = []string{
	"CAP_SYS_ADMIN", "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_SYS_RESOURCE",
	"CAP_SYS_CHROOT", "CAP_DAC_OVERRIDE", "CAP_SETUID", "CAP_SETGID",
	"CAP_AUDIT_WRITE", "CAP_MKNOD", "CAP_CHOWN", "CAP_FOWNER",
	"CAP_LINUX_IMMUTABLE",
}

// sharedDaemonPaths are never allowed as private runtime/state paths. The list
// covers the host daemon socket, data/exec roots, containerd roots and global
// config, including their /run aliases.
var sharedDaemonPaths = []string{
	"/var/run/docker.sock", "/run/docker.sock",
	"/var/lib/docker", "/var/run/docker", "/run/docker",
	"/var/lib/containerd", "/run/containerd", "/var/run/containerd",
	"/etc/docker", "/etc/containerd",
}

func hasDotDot(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func isSharedDaemonPath(candidate string) bool {
	clean := path.Clean(filepath.ToSlash(candidate))
	for _, shared := range sharedDaemonPaths {
		if clean == shared || strings.HasPrefix(clean, shared+"/") {
			return true
		}
	}
	return false
}

// containedIn reports whether candidate is strictly inside root (never equal
// to it). It uses the slash-based path package so validation is identical on
// every host OS; only the filesystem-aware helpers use filepath.
func containedIn(root, candidate string) bool {
	r := path.Clean(filepath.ToSlash(root))
	p := path.Clean(filepath.ToSlash(candidate))
	return p != r && strings.HasPrefix(p, r+"/")
}

func (d DaemonSpec) validate() error {
	if d.NodeID == "" {
		return fmt.Errorf("%w: daemon spec needs a node ID", ErrInvalidInput)
	}
	if d.RuntimeRoot == "" || d.StateRoot == "" {
		return fmt.Errorf("%w: runtime and state containment roots are required", ErrInvalidInput)
	}
	for _, root := range []string{d.RuntimeRoot, d.StateRoot} {
		if !path.IsAbs(filepath.ToSlash(root)) || hasDotDot(root) || isSharedDaemonPath(root) || path.Clean(filepath.ToSlash(root)) == "/" {
			return fmt.Errorf("%w: unsafe containment root %q", ErrInvalidInput, root)
		}
	}
	if d.RuntimeRoot == d.StateRoot || containedIn(d.RuntimeRoot, d.StateRoot) || containedIn(d.StateRoot, d.RuntimeRoot) {
		return fmt.Errorf("%w: runtime and state roots must be distinct and disjoint", ErrInvalidInput)
	}
	if d.Mode != ModeRootless && d.Mode != ModeRootful {
		return fmt.Errorf("%w: unknown daemon mode %q", ErrInvalidInput, d.Mode)
	}
	if d.ConfigPath == "" || hasDotDot(d.ConfigPath) || isSharedDaemonPath(d.ConfigPath) ||
		!containedIn(d.StateRoot, d.ConfigPath) {
		return fmt.Errorf("%w: config path must be inside the caller-owned state root", ErrInvalidInput)
	}
	if d.SocketPath == "" || hasDotDot(d.SocketPath) || isSharedDaemonPath(d.SocketPath) ||
		!containedIn(d.RuntimeRoot, d.SocketPath) {
		return fmt.Errorf("%w: daemon must use a private socket inside the runtime root", ErrInvalidInput)
	}
	if d.DataRoot == "" || d.ExecRoot == "" || d.DataRoot == d.ExecRoot ||
		hasDotDot(d.DataRoot) || hasDotDot(d.ExecRoot) ||
		isSharedDaemonPath(d.DataRoot) || isSharedDaemonPath(d.ExecRoot) ||
		!containedIn(d.StateRoot, d.DataRoot) || !containedIn(d.StateRoot, d.ExecRoot) {
		return fmt.Errorf("%w: daemon needs dedicated, distinct roots inside the state root", ErrInvalidInput)
	}
	if d.ContainerdNamespace == "" || d.ContainerdNamespace == "moby" || d.ContainerdNamespace == "default" {
		return fmt.Errorf("%w: daemon needs a dedicated containerd namespace", ErrInvalidInput)
	}
	if d.BridgeCIDR == "" || d.PortRange == "" ||
		strings.Contains(d.PortRange, "2375") || strings.Contains(d.PortRange, "2376") ||
		strings.HasPrefix(d.PortRange, "0.0.0.0") {
		return fmt.Errorf("%w: no public raw API or shared port range", ErrInvalidInput)
	}
	if d.Mode == ModeRootless {
		if d.RuntimeUID <= 0 {
			return fmt.Errorf("%w: rootless mode needs a numeric runtime UID", ErrInvalidInput)
		}
		want := "/run/user/" + strconv.Itoa(d.RuntimeUID) + "/"
		if !strings.HasPrefix(d.SocketPath, want) {
			return fmt.Errorf("%w: rootless socket must live under %s", ErrInvalidInput, want)
		}
	}
	return nil
}

// options is the single source of daemon options. Adding an option here adds
// it to config.json exactly once; the unit never repeats any of these keys.
func (d DaemonSpec) options() []daemonOption {
	return []daemonOption{
		{"hosts", []string{"unix://" + d.SocketPath}},
		{"data-root", d.DataRoot},
		{"exec-root", d.ExecRoot},
		{"containerd-namespace", d.ContainerdNamespace},
		{"bridge", "none"},
		{"bip", d.BridgeCIDR},
		{"storage-driver", d.StorageDriver},
		{"no-new-privileges", true},
		{"live-restore", true},
		{"userland-proxy", false},
		{"log-driver", "journald"},
		{"log-opts", map[string]string{"tag": "carbon-node-" + d.NodeID}},
		{"iptables", true},
		{"ip6tables", false},
		{"features", map[string]bool{"containerd-snapshotter": false}},
	}
}

func renderConfigFromOptions(opts []daemonOption) (string, error) {
	var b strings.Builder
	b.WriteString("{\n")
	for i, o := range opts {
		v, err := json.Marshal(o.value)
		if err != nil {
			return "", err
		}
		k, err := json.Marshal(o.key)
		if err != nil {
			return "", err
		}
		b.WriteString("  ")
		b.Write(k)
		b.WriteString(": ")
		b.Write(v)
		if i < len(opts)-1 {
			b.WriteByte(',')
		}
		b.WriteByte('\n')
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// ValidateDaemonPaths is the filesystem-aware companion to the pure spec
// validation. It refuses symlink escapes, traversal already caught above,
// world-writable ancestors and collisions with an existing daemon socket or
// root. It is called by the read-only render-daemon CLI verb before rendering,
// so an unsafe spec never even reaches the renderer. It never creates, removes
// or writes anything, and it works with temporary fixture paths only.
func ValidateDaemonPaths(spec DaemonSpec) error {
	if err := spec.validate(); err != nil {
		return err
	}
	// Resolve both roots through any existing symlinks and require containment
	// after resolution. This catches a root that is itself a link to /var/lib.
	resolvedRuntime, err := resolveOwnedRoot(spec.RuntimeRoot)
	if err != nil {
		return err
	}
	resolvedState, err := resolveOwnedRoot(spec.StateRoot)
	if err != nil {
		return err
	}
	if resolvedRuntime == resolvedState || containedIn(resolvedRuntime, resolvedState) || containedIn(resolvedState, resolvedRuntime) {
		return fmt.Errorf("%w: resolved runtime and state roots overlap", ErrInvalidInput)
	}
	checks := []struct {
		root string
		path string
		kind string
	}{
		{resolvedRuntime, spec.SocketPath, "socket"},
		{resolvedState, spec.ConfigPath, "config"},
		{resolvedState, spec.DataRoot, "data root"},
		{resolvedState, spec.ExecRoot, "exec root"},
	}
	for _, c := range checks {
		if err := checkPathComponent(c.root, c.path, c.kind); err != nil {
			return err
		}
	}
	// Collision: an existing socket, config file or daemon data directory at
	// the chosen path means we would take over another daemon's resources.
	for _, p := range []string{spec.SocketPath, spec.ConfigPath, spec.DataRoot, spec.ExecRoot} {
		if info, err := os.Lstat(p); err == nil {
			if p == spec.SocketPath || p == spec.ConfigPath || info.IsDir() {
				return fmt.Errorf("%w: path already exists and may belong to another daemon: %s", ErrAlreadyExists, p)
			}
		}
	}
	return nil
}

// resolveOwnedRoot EvalSymlinks an existing root (or the nearest existing
// ancestor for a path that does not exist yet) and rejects a symlink that
// resolves outside the declared root or onto a shared daemon path.
func resolveOwnedRoot(root string) (string, error) {
	clean := filepath.Clean(root)
	var resolved string
	if r, err := filepath.EvalSymlinks(clean); err == nil {
		resolved = r
	} else {
		// Walk up to the nearest existing ancestor, resolve it, then append
		// the remaining components. Fixtures use non-existent temp paths.
		cur := clean
		var tail []string
		for {
			if _, err := os.Lstat(cur); err == nil {
				break
			}
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
			tail = append([]string{filepath.Base(cur)}, tail...)
			cur = parent
		}
		r, err := filepath.EvalSymlinks(cur)
		if err != nil {
			return "", fmt.Errorf("%w: cannot resolve ancestor of %s: %v", ErrInvalidInput, root, err)
		}
		resolved = filepath.Join(append([]string{r}, tail...)...)
	}
	resolved = filepath.Clean(resolved)
	if hasDotDot(resolved) || isSharedDaemonPath(resolved) || resolved == "/" {
		return "", fmt.Errorf("%w: containment root resolves to an unsafe path: %s", ErrInvalidInput, resolved)
	}
	return filepath.ToSlash(resolved), nil
}

// checkPathComponent walks each existing component from root to path. A
// symlink whose target escapes the resolved root is refused; a world-writable
// ancestor (mode 0002) is refused because another user could redirect the path.
func checkPathComponent(root, path, kind string) error {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s escapes its containment root", ErrInvalidInput, kind)
	}
	cur := filepath.Clean(root)
	parts := strings.Split(filepath.ToSlash(rel), "/")
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			if os.IsNotExist(err) {
				continue // remaining components do not exist yet
			}
			return fmt.Errorf("%w: inspect %s: %v", ErrInvalidInput, kind, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return fmt.Errorf("%w: dangling symlink in %s: %v", ErrInvalidInput, kind, err)
			}
			target = filepath.Clean(target)
			if !containedIn(root, target) && target != root {
				return fmt.Errorf("%w: %s symlink escapes its root: %s", ErrInvalidInput, kind, target)
			}
		}
		parentInfo, err := os.Stat(filepath.Dir(cur))
		if err == nil && parentInfo.Mode().Perm()&0o002 != 0 {
			return fmt.Errorf("%w: %s parent is world-writable: %s", ErrInvalidInput, kind, filepath.Dir(cur))
		}
	}
	return nil
}

type localDaemonRenderer struct{}

func (localDaemonRenderer) Render(spec DaemonSpec) (DaemonRender, error) {
	if err := spec.validate(); err != nil {
		return DaemonRender{}, err
	}
	config, err := renderConfigFromOptions(spec.options())
	if err != nil {
		return DaemonRender{}, err
	}
	out := DaemonRender{Mode: string(spec.Mode), Config: config}
	syslog := "carbon-node-" + spec.NodeID
	launcher := spec.DaemonBinary
	if launcher == "" {
		launcher = "/usr/bin/dockerd"
	}
	rootlessLauncher := spec.RootlessLauncher
	if rootlessLauncher == "" {
		rootlessLauncher = "/usr/bin/dockerd-rootless.sh"
	}
	switch spec.Mode {
	case ModeRootless:
		// Per-user systemd service: no User=, user target, rootless launcher,
		// socket under XDG_RUNTIME_DIR. This replaces the PR527 system unit.
		out.Unit = fmt.Sprintf(`[Unit]
Description=Carbon Panel managed node daemon (rootless per-user service)
After=network-online.target

[Service]
Type=notify
Environment=XDG_RUNTIME_DIR=/run/user/%d
ExecStart=%s --config-file=%s
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
RestrictSUIDSGID=yes
Delegate=cpu cpuset io memory pids
StandardOutput=journal
StandardError=journal
SyslogIdentifier=%s
Restart=on-failure

[Install]
WantedBy=default.target
`, spec.RuntimeUID, rootlessLauncher, spec.ConfigPath, syslog)
		out.InstallHint = fmt.Sprintf("operator-reviewed, not executed: systemctl --user daemon-reload && systemctl --user enable --now carbon-node-%s.service", spec.NodeID)
	case ModeRootful:
		// Explicit root service with a non-empty capability bound. PR527
		// rendered User=nonroot with CapabilityBoundingSet= (empty), which
		// cannot run dockerd; this is the compatible explicit-root form.
		caps := strings.Join(rootfulCapabilities, " ")
		out.Unit = fmt.Sprintf(`[Unit]
Description=Carbon Panel managed node daemon (rootful, explicit capabilities)
After=network-online.target

[Service]
Type=notify
User=root
Group=root
ExecStart=%s --config-file=%s
NoNewPrivileges=yes
CapabilityBoundingSet=%s
AmbientCapabilities=%s
SecureBits=keep-caps
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
Delegate=cpu cpuset io memory pids
StandardOutput=journal
StandardError=journal
SyslogIdentifier=%s
Restart=on-failure

[Install]
WantedBy=multi-user.target
`, launcher, spec.ConfigPath, caps, caps, syslog)
		out.InstallHint = fmt.Sprintf("operator-reviewed, not executed: install the rendered unit, then systemctl daemon-reload && systemctl enable --now carbon-node-%s-rootful.service", spec.NodeID)
	}
	return out, nil
}
