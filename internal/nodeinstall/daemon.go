// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"encoding/json"
	"fmt"
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

func (d DaemonSpec) validate() error {
	if d.NodeID == "" {
		return fmt.Errorf("%w: daemon spec needs a node ID", ErrInvalidInput)
	}
	if d.Mode != ModeRootless && d.Mode != ModeRootful {
		return fmt.Errorf("%w: unknown daemon mode %q", ErrInvalidInput, d.Mode)
	}
	if d.ConfigPath == "" || strings.HasPrefix(d.ConfigPath, "/etc/") || d.ConfigPath == "/etc" {
		return fmt.Errorf("%w: config path must be inside a caller-owned directory, not global host config", ErrInvalidInput)
	}
	if d.SocketPath == "" || d.SocketPath == "/var/run/docker.sock" {
		return fmt.Errorf("%w: daemon must use a private socket", ErrInvalidInput)
	}
	if d.DataRoot == "" || d.ExecRoot == "" || d.DataRoot == d.ExecRoot ||
		d.DataRoot == "/var/lib/docker" || d.ExecRoot == "/var/run/docker" {
		return fmt.Errorf("%w: daemon needs dedicated, distinct data and exec roots", ErrInvalidInput)
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
ExecStart=/usr/bin/dockerd-rootless.sh --config-file=%s
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
`, spec.RuntimeUID, spec.ConfigPath, syslog)
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
ExecStart=/usr/bin/dockerd --config-file=%s
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
`, spec.ConfigPath, caps, caps, syslog)
		out.InstallHint = fmt.Sprintf("operator-reviewed, not executed: install the rendered unit, then systemctl daemon-reload && systemctl enable --now carbon-node-%s-rootful.service", spec.NodeID)
	}
	return out, nil
}
