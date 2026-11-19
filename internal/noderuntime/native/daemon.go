// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
)

// DaemonOptions is the CANONICAL source of every dockerd option this mode
// emits. It is deliberately exported so the OSS-379 portable installer lane
// can consume the same renderer instead of inventing a second flag set.
//
// Every field maps to one daemon.json key. The rendered systemd unit references
// only --config-file; it must never repeat an option from this struct, because
// dockerd rejects duplicate/conflicting CLI and config options.
type DaemonOptions struct {
	Hosts               []string          `json:"hosts"`
	DataRoot            string            `json:"data-root"`
	ExecRoot            string            `json:"exec-root"`
	Pidfile             string            `json:"pidfile"`
	ContainerdNamespace string            `json:"containerd-namespace"`
	BIP                 string            `json:"bip,omitempty"`
	StorageDriver       string            `json:"storage-driver"`
	NoNewPrivileges     bool              `json:"no-new-privileges"`
	LiveRestore         bool              `json:"live-restore"`
	UserlandProxy       bool              `json:"userland-proxy"`
	LogDriver           string            `json:"log-driver"`
	LogOpts             map[string]string `json:"log-opts,omitempty"`
	Iptables            bool              `json:"iptables"`
	Ip6tables           bool              `json:"ip6tables"`
	Features            map[string]bool   `json:"features,omitempty"`
}

// RenderConfig serialises the canonical options to daemon.json. Encoding a Go
// struct guarantees one occurrence of each key; a duplicate-key config cannot
// be produced here.
func (o DaemonOptions) RenderConfig() ([]byte, error) {
	return json.MarshalIndent(o, "", "  ")
}

// ConfigKeys returns the sorted daemon.json keys this renderer emits. It is
// the parity contract: no other option source may introduce these keys again.
func (o DaemonOptions) ConfigKeys() []string {
	raw, err := json.Marshal(o)
	if err != nil {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Validate enforces isolation on the canonical option set. It is independent
// of DaemonSpec so the installer lane can call it on its own derived options.
func (o DaemonOptions) Validate() []Check {
	var checks []Check
	add := func(code string, ok bool, msg, remedy string) {
		checks = append(checks, Check{Code: code, Passed: ok, Message: msg, Remedy: remedy})
	}
	privateSocket := len(o.Hosts) == 1 && strings.HasPrefix(o.Hosts[0], "unix://") &&
		!strings.Contains(o.Hosts[0], "/var/run/docker.sock") && !strings.HasPrefix(o.Hosts[0], "tcp://")
	add("OPTIONS_PRIVATE_SOCKET", privateSocket,
		"exactly one private unix socket, no TCP/raw API listener",
		"Use a single unix:// host under the node runtime directory.")
	add("OPTIONS_DEDICATED_ROOTS",
		o.DataRoot != "" && o.ExecRoot != "" && o.DataRoot != "/var/lib/docker" &&
			o.ExecRoot != "/var/run/docker" && o.DataRoot != o.ExecRoot,
		"data and exec roots are dedicated", "Use distinct private data-root and exec-root paths.")
	add("OPTIONS_NAMESPACE",
		o.ContainerdNamespace != "" && o.ContainerdNamespace != "moby" && o.ContainerdNamespace != "default",
		"containerd namespace is dedicated", "Use a node-specific containerd namespace.")
	// A private pidfile prevents collision with a preinstalled engine's
	// /var/run/docker.pid. It must live under the owned runtime root and must
	// not clobber another owned path.
	pidOK := o.Pidfile != "" && o.Pidfile != "/var/run/docker.pid" && o.Pidfile != "/run/docker.pid"
	add("OPTIONS_PRIVATE_PIDFILE", pidOK,
		"private pidfile, never the shared default", "Use a dedicated *.pid path inside the node runtime directory; never /var/run/docker.pid.")
	add("OPTIONS_NO_PUBLIC_API", !strings.Contains(strings.Join(o.Hosts, " "), "0.0.0.0"),
		"no public raw Docker API listener", "Never publish 2375/2376; use the private unix socket only.")
	add("OPTIONS_LOG_DRIVER", o.LogDriver == "journald" || o.LogDriver == "json-file",
		"log driver is a supported named driver", "Use journald (production) or json-file (disposable fixtures only).")
	return checks
}

// ValidateOrRefuse returns the first failing canonical-option code.
func (o DaemonOptions) ValidateOrRefuse() string {
	for _, c := range o.Validate() {
		if !c.Passed {
			return c.Code
		}
	}
	return ""
}

// DaemonSpec is the disposable description of the dedicated daemon process.
// It contains paths, identity and the selected mode; the OPTIONS are the only
// source of daemon flags. No unit is written to a host and no process starts.
type DaemonSpec struct {
	NodeID              string
	Mode                DaemonMode
	SocketPath          string
	ConfigPath          string
	XDG_RUNTIME_DIR     string
	DataRoot            string
	ExecRoot            string
	Pidfile             string // private pidfile under the owned runtime root
	ContainerdNamespace string
	BridgeCIDR          string
	PortRange           string
	StorageDriver       string
	ServiceUID          string // rootless service user, informational; user unit has no User=
	LogIdentifier       string
	LogDriver           string // defaults to journald; json-file for disposable fixtures without journald
}

// Options derives the canonical daemon.json option set. This is the ONLY
// place dockerd options are declared; both the config and the unit consume it.
func (d DaemonSpec) Options() DaemonOptions {
	logDriver := d.LogDriver
	if logDriver == "" {
		logDriver = "journald"
	}
	return DaemonOptions{
		Hosts:               []string{"unix://" + d.SocketPath},
		DataRoot:            d.DataRoot,
		ExecRoot:            d.ExecRoot,
		Pidfile:             d.Pidfile,
		ContainerdNamespace: d.ContainerdNamespace,
		BIP:                 d.BridgeCIDR,
		StorageDriver:       d.StorageDriver,
		NoNewPrivileges:     true,
		LiveRestore:         true,
		UserlandProxy:       false,
		LogDriver:           logDriver,
		LogOpts:             map[string]string{"tag": d.LogIdentifier},
		Iptables:            true,
		Ip6tables:           false,
		Features:            map[string]bool{"containerd-snapshotter": false},
	}
}

// Validate enforces mode-specific isolation invariants on the spec.
func (d DaemonSpec) Validate() []Check {
	checks := d.Options().Validate()
	add := func(code string, ok bool, msg, remedy string) {
		checks = append(checks, Check{Code: code, Passed: ok, Message: msg, Remedy: remedy})
	}
	add("DAEMON_PRIVATE_SOCKET",
		d.SocketPath != "" && d.SocketPath != "/var/run/docker.sock",
		"socket is not the shared host Docker socket", "Use a private socket under the node runtime directory.")
	add("DAEMON_PRIVATE_NETWORK",
		d.BridgeCIDR != "" && d.PortRange != "" && d.PortRange != "2375" && d.PortRange != "2376",
		"private bridge and node-local port range are configured", "Choose non-overlapping private ranges excluding raw API ports.")
	add("DAEMON_CONFIG_PATH", d.ConfigPath != "",
		"config path is set", "Every mode must reference a canonical daemon.json path.")
	// The pidfile must be private (under the socket's owned runtime root) and
	// must not clobber any other owned path. This is the no-clobber gate that
	// keeps a second dockerd from colliding on /var/run/docker.pid.
	runtimeDir := filepath.Dir(d.SocketPath)
	pidPrivate := d.Pidfile != "" && sameOrUnder(d.Pidfile, runtimeDir) &&
		d.Pidfile != d.SocketPath && d.Pidfile != d.ConfigPath &&
		d.Pidfile != d.DataRoot && d.Pidfile != d.ExecRoot &&
		filepath.Ext(d.Pidfile) == ".pid"
	add("DAEMON_PIDFILE_NO_CLOBBER", pidPrivate,
		"pidfile is under the owned runtime root and distinct from other paths", "Choose a dedicated *.pid path beside the private socket.")
	if d.Mode == ModeRootful {
		// An explicit rootful service runs as root. A non-root User= with an
		// empty capability set cannot bind/operate dockerd and is refused.
		add("DAEMON_ROOTFUL_NO_USER", d.ServiceUID == "",
			"explicit rootful service has no User= override", "Run the dedicated unit as root with the documented capability set.")
	}
	if d.Mode == ModeRootless {
		add("DAEMON_ROOTLESS_RUNTIME_DIR", d.XDG_RUNTIME_DIR != "" && !strings.HasPrefix(d.XDG_RUNTIME_DIR, "/tmp"),
			"rootless per-user launcher has a private runtime dir", "Set XDG_RUNTIME_DIR to a private 0700 directory outside /tmp.")
		launcher := d.Launcher()
		add("DAEMON_ROOTLESS_LAUNCHER", launcher == "dockerd-rootless.sh",
			"rootless uses the supported per-user launcher", "Use dockerd-rootless.sh in a systemd --user unit; never a direct system dockerd unit.")
	}
	return checks
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

// UnitKind is "user" for the rootless per-user systemd unit and "system" for
// the explicit rootful service. It is exported for the installer lane.
func (d DaemonSpec) UnitKind() string {
	if d.Mode == ModeRootless {
		return "user"
	}
	return "system"
}

// Launcher is the supported dockerd entrypoint for the selected mode.
func (d DaemonSpec) Launcher() string {
	if d.Mode == ModeRootless {
		return "dockerd-rootless.sh"
	}
	return "dockerd"
}

// Manifest bundles everything the OSS-379 installer needs to render a daemon
// in any deployment mode without re-declaring options. It is exported and
// mode-agnostic; the installer consumes ConfigJSON, Unit and UnitKind.
type Manifest struct {
	NodeID     string
	Mode       DaemonMode
	UnitKind   string
	Launcher   string
	ConfigPath string
	SocketPath string
	ConfigJSON string
	Unit       string
	Options    DaemonOptions
}

// BuildManifest renders config and unit from one option source. It refuses
// invalid specs, so an installer can never emit a broken or duplicated unit.
func (d DaemonSpec) BuildManifest() (Manifest, error) {
	if code := d.ValidateOrRefuse(); code != "" {
		return Manifest{}, fmt.Errorf("refused %s: daemon spec violates private-isolation invariants", code)
	}
	cfg, err := d.Options().RenderConfig()
	if err != nil {
		return Manifest{}, err
	}
	unit, err := d.RenderUnit()
	if err != nil {
		return Manifest{}, err
	}
	return Manifest{
		NodeID: d.NodeID, Mode: d.Mode, UnitKind: d.UnitKind(),
		Launcher: d.Launcher(), ConfigPath: d.ConfigPath, SocketPath: d.SocketPath,
		ConfigJSON: string(cfg), Unit: unit, Options: d.Options(),
	}, nil
}

// Rootless: a SUPPORTED per-user systemd unit. There is no User=, no system
// multi-user.target and no direct dockerd invocation. dockerd-rootless.sh sets
// up rootlesskit, XDG_RUNTIME_DIR and the user namespace, then forwards the
// single --config-file option to dockerd.
var rootlessUnitTemplate = template.Must(template.New("rootlessUnit").Parse(`[Unit]
Description=Carbon Panel managed node daemon (rootless, per-user)
Documentation=https://docs.carbonpanel.example/managed-node
After=network-online.target

[Service]
Type=notify
NotifyAccess=all
Environment=XDG_RUNTIME_DIR={{.XDG_RUNTIME_DIR}}
ExecStart=/usr/bin/dockerd-rootless.sh --config-file={{.ConfigPath}}
Delegate=cpu cpuset io memory pids
Restart=on-failure
RestartSec=2
StandardOutput=journal
StandardError=journal
SyslogIdentifier={{.LogIdentifier}}

[Install]
WantedBy=default.target
`))

// rootfulCapabilitySet is the capability set required for a real dockerd to
// (a) start and (b) launch DEFAULT-capability containers. It is the union of
// Docker's default container caps (CHOWN, DAC_OVERRIDE, FSETID, FOWNER, MKNOD,
// NET_RAW, SETGID, SETUID, SETFCAP, SETPCAP, NET_BIND_SERVICE, SYS_CHROOT,
// KILL, AUDIT_WRITE) plus the daemon caps SYS_ADMIN, NET_ADMIN and
// SYS_RESOURCE. Omitting FSETID, KILL, NET_BIND_SERVICE or SETFCAP makes
// default-cap containers fail with "unable to apply caps: operation not
// permitted" — observed on a real engine qualification, not assumed.
const rootfulCapabilitySet = "CAP_CHOWN CAP_DAC_OVERRIDE CAP_FSETID CAP_FOWNER CAP_KILL CAP_MKNOD " +
	"CAP_NET_ADMIN CAP_NET_BIND_SERVICE CAP_NET_RAW CAP_SETPCAP CAP_SETFCAP CAP_SETGID CAP_SETUID " +
	"CAP_SYS_ADMIN CAP_SYS_CHROOT CAP_SYS_RESOURCE CAP_AUDIT_WRITE"

// Rootful: an EXPLICIT dedicated service running as root. dockerd needs
// CAP_SYS_ADMIN (mounts/overlay/network namespaces), CAP_NET_ADMIN/RAW,
// CAP_SYS_RESOURCE and the full default container capability set so ordinary
// game containers start. An empty CapabilityBoundingSet or a non-root User=
// is refused. ProtectSystem=full keeps /usr, /boot and /etc read-only without
// making the private data root unwritable, and NoNewPrivileges stays off so
// the runtime can still apply container capability sets.
var rootfulUnitTemplate = template.Must(template.New("rootfulUnit").Parse(`[Unit]
Description=Carbon Panel managed node daemon (dedicated rootful)
Documentation=https://docs.carbonpanel.example/managed-node
After=network-online.target

[Service]
Type=notify
PIDFile={{.Pidfile}}
ExecStart=/usr/bin/dockerd --config-file={{.ConfigPath}}
CapabilityBoundingSet={{.Capabilities}}
AmbientCapabilities={{.Capabilities}}
NoNewPrivileges=no
ProtectSystem=full
ProtectHome=yes
PrivateTmp=yes
RestrictSUIDSGID=no
Delegate=cpu cpuset io memory pids
LimitNOFILE=infinity
LimitNPROC=infinity
TasksMax=infinity
KillMode=process
Restart=on-failure
RestartSec=2
StandardOutput=journal
StandardError=journal
SyslogIdentifier={{.LogIdentifier}}

[Install]
WantedBy=multi-user.target
`))

// RenderUnit renders the mode-correct unit into memory. Nothing is written to
// a host. The config path comes from d.ConfigPath and the ExecStart line
// references only --config-file: one source of daemon options.
func (d DaemonSpec) RenderUnit() (string, error) {
	if code := d.ValidateOrRefuse(); code != "" {
		return "", fmt.Errorf("refused %s: daemon spec violates private-isolation invariants", code)
	}
	tmpl := rootfulUnitTemplate
	if d.Mode == ModeRootless {
		tmpl = rootlessUnitTemplate
	}
	data := struct {
		DaemonSpec
		Capabilities string
	}{DaemonSpec: d, Capabilities: rootfulCapabilitySet}
	var b bytes.Buffer
	if err := tmpl.Execute(&b, data); err != nil {
		return "", err
	}
	return b.String(), nil
}

// RenderConfig renders the canonical daemon.json into memory.
func (d DaemonSpec) RenderConfig() (string, error) {
	if code := d.ValidateOrRefuse(); code != "" {
		return "", fmt.Errorf("refused %s: daemon spec violates private-isolation invariants", code)
	}
	b, err := d.Options().RenderConfig()
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckOptionParity asserts that the rendered unit and config share exactly
// one option source. It fails when a config key is repeated as an ExecStart
// flag, when a duplicate JSON key is present, or when a flag other than
// --config-file is passed to the daemon.
func CheckOptionParity(configJSON, unit string) []Check {
	var checks []Check
	add := func(code string, ok bool, msg, remedy string) {
		checks = append(checks, Check{Code: code, Passed: ok, Message: msg, Remedy: remedy})
	}
	keys, duplicate, err := jsonConfigKeys(configJSON)
	add("PARITY_NO_DUPLICATE_KEYS", err == nil && !duplicate,
		"daemon.json has no duplicate option keys", "Emit daemon options from one Go struct.")
	hasPidfile := false
	for _, k := range keys {
		if k == "pidfile" {
			hasPidfile = true
		}
	}
	add("PARITY_PRIVATE_PIDFILE", hasPidfile,
		"daemon.json declares a private pidfile", "Never leave dockerd on the shared /var/run/docker.pid default.")
	execStart := execStartLine(unit)
	add("PARITY_EXEC_START_PRESENT", execStart != "",
		"unit has an ExecStart line", "Render a mode-correct systemd unit.")
	flags := cliFlags(execStart)
	extra := make([]string, 0)
	for _, f := range flags {
		name := strings.TrimPrefix(strings.SplitN(f, "=", 2)[0], "--")
		if name == "config-file" {
			continue
		}
		extra = append(extra, f)
	}
	add("PARITY_SINGLE_OPTION_SOURCE", len(extra) == 0,
		"ExecStart contains only --config-file", "Move every other daemon option into daemon.json.")
	conflicts := make([]string, 0)
	for _, k := range keys {
		for _, f := range extra {
			if strings.HasPrefix(f, "--"+k+"=") || f == "--"+k {
				conflicts = append(conflicts, k)
			}
		}
	}
	add("PARITY_NO_KEY_CONFLICT", len(conflicts) == 0,
		"no config key is repeated as a CLI flag", "Remove conflicting CLI flags from ExecStart.")
	return checks
}

// CheckUnitContract verifies the qualification-critical properties of a
// rendered unit. It is exported so the installer lane and fixtures can assert
// the same contract the real systemd qualification depends on.
func CheckUnitContract(unit string, mode DaemonMode) []Check {
	var checks []Check
	add := func(code string, ok bool, msg, remedy string) {
		checks = append(checks, Check{Code: code, Passed: ok, Message: msg, Remedy: remedy})
	}
	if mode == ModeRootless {
		add("UNIT_ROOTLESS_NOTIFY_ACCESS", strings.Contains(unit, "NotifyAccess=all"),
			"rootless Type=notify accepts READY from the child dockerd under rootlesskit",
			"Add NotifyAccess=all; without it the unit stays activating/start although the socket serves.")
		add("UNIT_ROOTLESS_USER_SESSION", strings.Contains(unit, "WantedBy=default.target") && !strings.Contains(unit, "User="),
			"rootless is a per-user unit", "Use a systemd --user unit with no User= override.")
		return checks
	}
	add("UNIT_ROOTFUL_PIDFILE", strings.Contains(unit, "PIDFile="),
		"rootful unit tracks the private pidfile", "Add PIDFile=<private pidfile> matching daemon.json.")
	for _, cap := range []string{"CAP_FSETID", "CAP_KILL", "CAP_NET_BIND_SERVICE", "CAP_SETFCAP"} {
		add("UNIT_ROOTFUL_CAP_"+cap, strings.Contains(unit, cap),
			"default-container capability "+cap+" is in the unit bound set",
			"Add the full Docker default container capability set to CapabilityBoundingSet and AmbientCapabilities.")
	}
	add("UNIT_ROOTFUL_NO_USER", !strings.Contains(unit, "User="),
		"rootful service has no User= override", "Run the dedicated unit as root with the documented capability set.")
	return checks
}

func jsonConfigKeys(raw string) ([]string, bool, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	tok, err := dec.Token()
	if err != nil {
		return nil, false, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, false, fmt.Errorf("config root is not an object")
	}
	seen := make(map[string]bool)
	duplicate := false
	keys := make([]string, 0)
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false, err
		}
		key, ok := t.(string)
		if !ok {
			return nil, false, fmt.Errorf("config key is not a string")
		}
		if seen[key] {
			duplicate = true
		}
		seen[key] = true
		keys = append(keys, key)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false, err
		}
	}
	sort.Strings(keys)
	return keys, duplicate, nil
}

func execStartLine(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "ExecStart=") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "ExecStart="))
		}
	}
	return ""
}

func cliFlags(line string) []string {
	fields := strings.Fields(line)
	var out []string
	for _, f := range fields {
		if strings.HasPrefix(f, "--") {
			out = append(out, f)
		}
	}
	return out
}

// SpecFromFacts derives a private daemon spec from qualified facts. Rootless
// selections use the per-user runtime directory; rootful selections run as
// root with an explicit capability set (no User= override).
func SpecFromFacts(nodeID string, f HostFacts, sel Selection) DaemonSpec {
	xdg := f.XDG_RUNTIME_DIR
	configPath := "/etc/carbon-panel/daemon.json"
	if sel.Mode == ModeRootless {
		configPath = xdg + "/carbon-node/daemon.json"
	}
	// Private pidfile beside the private socket, never the shared default.
	pidfile := path.Join(path.Dir(f.ChosenSocket), "docker.pid")
	return DaemonSpec{
		NodeID:              nodeID,
		Mode:                sel.Mode,
		SocketPath:          f.ChosenSocket,
		ConfigPath:          configPath,
		XDG_RUNTIME_DIR:     xdg,
		DataRoot:            f.ChosenDataRoot,
		ExecRoot:            f.ChosenExecRoot,
		Pidfile:             pidfile,
		ContainerdNamespace: f.ContainerdNamespace,
		BridgeCIDR:          f.BridgeCIDR,
		PortRange:           f.ChosenPortRange,
		StorageDriver:       f.StorageDriver,
		LogIdentifier:       "carbon-node-" + nodeID,
		LogDriver:           f.LogDriver,
	}
}
