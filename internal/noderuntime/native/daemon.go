// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"bytes"
	"fmt"
	"text/template"
)

// DaemonSpec is the complete, disposable description of the dedicated daemon
// process. It contains only paths, identity and policy: no binaries are
// installed, no unit is written to the host and no process is started here.
// Rendering returns strings for an owned fixture or a future packaging lane.
type DaemonSpec struct {
	NodeID              string
	Mode                DaemonMode
	SocketPath          string
	DataRoot            string
	ExecRoot            string
	ContainerdNamespace string
	BridgeCIDR          string
	PortRange           string
	StorageDriver       string
	ServiceUID          string // empty for rootless (user service)
	LogIdentifier       string
}

// Validate enforces the mode's private-isolation invariants. It is a pure
// function so a fixture can prove every refusal without a live host.
func (d DaemonSpec) Validate() []Check {
	var checks []Check
	hostSocket := "/var/run/docker.sock"
	add := func(code string, ok bool, msg, remedy string) {
		checks = append(checks, Check{Code: code, Passed: ok, Message: msg, Remedy: remedy})
	}
	add("DAEMON_PRIVATE_SOCKET",
		d.SocketPath != "" && d.SocketPath != hostSocket && d.SocketPath != "/var/run/docker.sock",
		"socket is not the shared host Docker socket", "Use a private socket under the node runtime directory.")
	add("DAEMON_DEDICATED_ROOTS",
		d.DataRoot != "" && d.ExecRoot != "" && d.DataRoot != "/var/lib/docker" && d.ExecRoot != "/var/run/docker" && d.DataRoot != d.ExecRoot,
		"data and exec roots are dedicated", "Use distinct private data-root and exec-root paths.")
	add("DAEMON_CONTAINERD_NAMESPACE",
		d.ContainerdNamespace != "" && d.ContainerdNamespace != "moby" && d.ContainerdNamespace != "default",
		"containerd namespace is dedicated", "Use a node-specific containerd namespace.")
	add("DAEMON_NO_PUBLIC_API",
		!publicAPIRequested(d),
		"no public raw Docker API listener is configured", "Never publish 0.0.0.0:2375/2376; use the private Unix socket only.")
	add("DAEMON_PRIVATE_NETWORK",
		d.BridgeCIDR != "" && d.PortRange != "" && d.PortRange != "2375" && d.PortRange != "2376",
		"private bridge and node-local port range are configured", "Choose non-overlapping private ranges excluding raw API ports.")
	if d.Mode == ModeRootful {
		add("DAEMON_ROOTFUL_DEDICATED_UID",
			d.ServiceUID != "" && d.ServiceUID != "root",
			"dedicated rootful service uses a non-root service UID", "Create a non-login dedicated UID and a hardened unit.")
	}
	return checks
}

func publicAPIRequested(d DaemonSpec) bool {
	return d.PortRange == "0.0.0.0:2375" || d.PortRange == "0.0.0.0:2376" || d.PortRange == "2375" || d.PortRange == "2376"
}

// ValidateOrRefuse returns a single precise failure code or empty string when
// every daemon invariant holds.
func (d DaemonSpec) ValidateOrRefuse() string {
	for _, c := range d.Validate() {
		if !c.Passed {
			return c.Code
		}
	}
	return ""
}

var unitTemplate = template.Must(template.New("unit").Parse(`[Unit]
Description=Carbon Panel managed node daemon ({{.Mode}})
Documentation=https://docs.carbonpanel.example/managed-node
After=network-online.target

[Service]
Type=notify
{{- if .ServiceUID}}
User={{.ServiceUID}}
{{- end}}
ExecStart=/usr/bin/dockerd --config-file={{.ConfigPath}} --host=unix://{{.SocketPath}} --data-root={{.DataRoot}} --exec-root={{.ExecRoot}} --containerd-namespace={{.ContainerdNamespace}} --bip={{.BridgeCIDR}} --storage-driver={{.StorageDriver}} --no-new-privileges=true --live-restore=true
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
RestrictSUIDSGID=yes
CapabilityBoundingSet=
Delegate=cpu cpuset io memory pids
StandardOutput=journal
StandardError=journal
SyslogIdentifier={{.LogIdentifier}}
Restart=on-failure

[Install]
WantedBy=multi-user.target
`))

var configTemplate = template.Must(template.New("config").Parse(`{
  "hosts": ["unix://{{.SocketPath}}"],
  "data-root": "{{.DataRoot}}",
  "exec-root": "{{.ExecRoot}}",
  "containerd-namespace": "{{.ContainerdNamespace}}",
  "bridge": "none",
  "bip": "{{.BridgeCIDR}}",
  "storage-driver": "{{.StorageDriver}}",
  "no-new-privileges": true,
  "live-restore": true,
  "userland-proxy": false,
  "log-driver": "journald",
  "log-opts": {"tag": "{{.LogIdentifier}}"},
  "iptables": true,
  "ip6tables": false,
  "features": {"containerd-snapshotter": false}
}
`))

type renderData struct {
	DaemonSpec
	ConfigPath string
}

// RenderUnit renders a hardened systemd unit into memory. Nothing is written
// to the host. Refuses invalid specs so an unsafe unit cannot be produced.
func (d DaemonSpec) RenderUnit(configPath string) (string, error) {
	if code := d.ValidateOrRefuse(); code != "" {
		return "", fmt.Errorf("refused %s: %s", code, "daemon spec violates private-isolation invariants")
	}
	var b bytes.Buffer
	if err := unitTemplate.Execute(&b, renderData{DaemonSpec: d, ConfigPath: configPath}); err != nil {
		return "", err
	}
	return b.String(), nil
}

// RenderConfig renders the daemon config into memory. JSON is avoided here so
// the fixture output stays deterministic and free of machine-specific paths.
func (d DaemonSpec) RenderConfig() (string, error) {
	if code := d.ValidateOrRefuse(); code != "" {
		return "", fmt.Errorf("refused %s: %s", code, "daemon spec violates private-isolation invariants")
	}
	var b bytes.Buffer
	if err := configTemplate.Execute(&b, d); err != nil {
		return "", err
	}
	return b.String(), nil
}

// SpecFromFacts derives a private daemon spec from qualified facts. It is
// called only after ApplyGates selects a mode; a rootless selection uses the
// per-user runtime socket and no rootful UID.
func SpecFromFacts(nodeID string, f HostFacts, sel Selection) DaemonSpec {
	uid := ""
	if sel.Mode == ModeRootful {
		uid = "carbon-node"
	}
	return DaemonSpec{
		NodeID:              nodeID,
		Mode:                sel.Mode,
		SocketPath:          f.ChosenSocket,
		DataRoot:            f.ChosenDataRoot,
		ExecRoot:            f.ChosenExecRoot,
		ContainerdNamespace: f.ContainerdNamespace,
		BridgeCIDR:          f.BridgeCIDR,
		PortRange:           f.ChosenPortRange,
		StorageDriver:       f.StorageDriver,
		ServiceUID:          uid,
		LogIdentifier:       "carbon-node-" + nodeID,
	}
}
