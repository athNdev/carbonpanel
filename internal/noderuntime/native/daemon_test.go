// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"encoding/json"
	"strings"
	"testing"
)

func privateSpec() DaemonSpec {
	return DaemonSpec{
		NodeID: "node-fixture-native", Mode: ModeRootless,
		SocketPath:          "/run/user/1000/carbon-node/docker.sock",
		ConfigPath:          "/run/user/1000/carbon-node/daemon.json",
		XDG_RUNTIME_DIR:     "/run/user/1000/carbon-node",
		DataRoot:            "/var/lib/carbon-node/docker",
		ExecRoot:            "/run/carbon-node/docker",
		ContainerdNamespace: "carbon-node-1",
		BridgeCIDR:          "10.231.0.1/24",
		PortRange:           "30000-31000",
		StorageDriver:       "vfs",
		LogIdentifier:       "carbon-node-node-fixture-native",
	}
}

func rootfulSpec() DaemonSpec {
	d := privateSpec()
	d.Mode = ModeRootful
	d.SocketPath = "/run/carbon-node/docker.sock"
	d.ConfigPath = "/etc/carbon-panel/daemon.json"
	d.ServiceUID = ""
	return d
}

func TestDaemonSpecPrivateIsolation(t *testing.T) {
	for _, d := range []DaemonSpec{privateSpec(), rootfulSpec()} {
		if code := d.ValidateOrRefuse(); code != "" {
			t.Fatalf("private spec refused: %s", code)
		}
	}
}

func TestDaemonSpecRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*DaemonSpec)
		want string
	}{
		{"host socket", func(d *DaemonSpec) { d.SocketPath = "/var/run/docker.sock" }, "OPTIONS_PRIVATE_SOCKET"},
		{"shared data root", func(d *DaemonSpec) { d.DataRoot = "/var/lib/docker" }, "OPTIONS_DEDICATED_ROOTS"},
		{"shared namespace", func(d *DaemonSpec) { d.ContainerdNamespace = "moby" }, "OPTIONS_NAMESPACE"},
		{"rootful user override", func(d *DaemonSpec) { d.Mode = ModeRootful; d.ServiceUID = "carbon-node" }, "DAEMON_ROOTFUL_NO_USER"},
		{"rootless unsafe runtime dir", func(d *DaemonSpec) { d.XDG_RUNTIME_DIR = "/tmp/carbon" }, "DAEMON_ROOTLESS_RUNTIME_DIR"},
		{"missing config path", func(d *DaemonSpec) { d.ConfigPath = "" }, "DAEMON_CONFIG_PATH"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := privateSpec()
			tc.mut(&d)
			if code := d.ValidateOrRefuse(); code != tc.want {
				t.Fatalf("expected %s, got %q", tc.want, code)
			}
			if _, err := d.RenderUnit(); err == nil {
				t.Fatal("unsafe spec produced a systemd unit")
			}
		})
	}
}

// Finding 1: one source of daemon options. The unit must reference only
// --config-file and the config must not carry a conflicting bridge option.
func TestSingleSourceOfDaemonOptions(t *testing.T) {
	for _, d := range []DaemonSpec{privateSpec(), rootfulSpec()} {
		unit, err := d.RenderUnit()
		if err != nil {
			t.Fatal(err)
		}
		config, err := d.RenderConfig()
		if err != nil {
			t.Fatal(err)
		}
		line := execStartLine(unit)
		flags := cliFlags(line)
		if len(flags) != 1 || flags[0] != "--config-file="+d.ConfigPath {
			t.Fatalf("ExecStart must contain only --config-file, got %q", line)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(config), &m); err != nil {
			t.Fatal(err)
		}
		if _, ok := m["bridge"]; ok {
			t.Fatal("config still contains bridge=none conflicting with bip")
		}
		if !strings.Contains(config, `"bip": "10.231.0.1/24"`) {
			t.Fatal("config lost the private bridge CIDR")
		}
		for _, code := range CheckOptionParity(config, unit) {
			if !code.Passed {
				t.Fatalf("parity check failed: %s: %s", code.Code, code.Message)
			}
		}
	}
}

// Finding 2: rootless is a per-user systemd --user unit using the supported
// dockerd-rootless.sh launcher, not a direct system dockerd unit.
func TestRootlessUnitIsPerUserLauncher(t *testing.T) {
	d := privateSpec()
	unit, err := d.RenderUnit()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ExecStart=/usr/bin/dockerd-rootless.sh --config-file=" + d.ConfigPath,
		"Environment=XDG_RUNTIME_DIR=" + d.XDG_RUNTIME_DIR,
		"WantedBy=default.target",
	}
	for _, s := range want {
		if !strings.Contains(unit, s) {
			t.Fatalf("rootless unit missing %q", s)
		}
	}
	for _, forbidden := range []string{"User=", "multi-user.target", "ExecStart=/usr/bin/dockerd ", "CapabilityBoundingSet="} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("rootless unit contains forbidden %q", forbidden)
		}
	}
	if d.UnitKind() != "user" || d.Launcher() != "dockerd-rootless.sh" {
		t.Fatalf("wrong rootless kind/launcher: %s/%s", d.UnitKind(), d.Launcher())
	}
}

// Finding 3: rootful runs as root with the capability set dockerd needs and
// hardening that does not break it; no empty CapabilityBoundingSet.
func TestRootfulUnitIsExplicitCompatibleService(t *testing.T) {
	d := rootfulSpec()
	unit, err := d.RenderUnit()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ExecStart=/usr/bin/dockerd --config-file=" + d.ConfigPath,
		"CapabilityBoundingSet=CAP_CHOWN",
		"CAP_SYS_ADMIN",
		"CAP_NET_ADMIN",
		"Delegate=cpu cpuset io memory pids",
		"WantedBy=multi-user.target",
		"NoNewPrivileges=no",
	}
	for _, s := range want {
		if !strings.Contains(unit, s) {
			t.Fatalf("rootful unit missing %q", s)
		}
	}
	if strings.Contains(unit, "User=") {
		t.Fatal("rootful unit must not set User= (runs as root)")
	}
	if strings.Contains(unit, "CapabilityBoundingSet=\n") || strings.Contains(unit, "CapabilityBoundingSet= ") {
		t.Fatal("rootful unit has an empty capability set")
	}
	if d.ServiceUID != "" || d.UnitKind() != "system" || d.Launcher() != "dockerd" {
		t.Fatalf("wrong rootful spec: uid=%q kind=%s launcher=%s", d.ServiceUID, d.UnitKind(), d.Launcher())
	}
}

// Parity fixture: a duplicate config key, or an extra CLI flag, is refused.
func TestOptionParityDetectsDuplicatesAndExtraFlags(t *testing.T) {
	d := privateSpec()
	unit, err := d.RenderUnit()
	if err != nil {
		t.Fatal(err)
	}
	dupConfig := `{"hosts":["unix:///a"],"hosts":["unix:///b"]}`
	for _, c := range CheckOptionParity(dupConfig, unit) {
		if c.Code == "PARITY_NO_DUPLICATE_KEYS" && c.Passed {
			t.Fatal("duplicate JSON key was not detected")
		}
	}
	extraUnit := strings.Replace(unit, "--config-file="+d.ConfigPath,
		"--config-file="+d.ConfigPath+" --bip=10.231.0.1/24", 1)
	for _, c := range CheckOptionParity(`{"hosts":["unix:///a"],"bip":"10.231.0.1/24"}`, extraUnit) {
		if c.Code == "PARITY_SINGLE_OPTION_SOURCE" && c.Passed {
			t.Fatal("extra CLI daemon option was not detected")
		}
		if c.Code == "PARITY_NO_KEY_CONFLICT" && c.Passed {
			t.Fatal("CLI/config key conflict was not detected")
		}
	}
}

func TestCanonicalOptionsExportForInstaller(t *testing.T) {
	d := rootfulSpec()
	keys := d.Options().ConfigKeys()
	joined := strings.Join(keys, ",")
	for _, want := range []string{"hosts", "data-root", "exec-root", "containerd-namespace", "bip", "storage-driver", "no-new-privileges", "live-restore"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("canonical options missing %q: %s", want, joined)
		}
	}
	if strings.Contains(joined, "bridge") {
		t.Fatalf("canonical options must not carry conflicting bridge key: %s", joined)
	}
	if code := d.Options().ValidateOrRefuse(); code != "" {
		t.Fatalf("canonical options refused: %s", code)
	}
}

func TestBuildManifestIsConsumable(t *testing.T) {
	for _, d := range []DaemonSpec{privateSpec(), rootfulSpec()} {
		m, err := d.BuildManifest()
		if err != nil {
			t.Fatal(err)
		}
		if m.UnitKind != d.UnitKind() || m.ConfigJSON == "" || m.Unit == "" || len(m.Options.ConfigKeys()) == 0 {
			t.Fatalf("incomplete manifest: %+v", m)
		}
		var v map[string]interface{}
		if err := json.Unmarshal([]byte(m.ConfigJSON), &v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSpecFromFactsRootfulRunsAsRoot(t *testing.T) {
	f := qualifiedFacts()
	f.UserNamespaces = false
	sel := ApplyGates(f)
	if sel.Mode != ModeRootful {
		t.Fatalf("expected rootful fallback, got %q", sel.Mode)
	}
	d := SpecFromFacts("node-fixture", f, sel)
	if d.ServiceUID != "" || d.UnitKind() != "system" {
		t.Fatalf("rootful spec must run as root with a system unit: %+v", d)
	}
	if code := d.ValidateOrRefuse(); code != "" {
		t.Fatalf("rootful spec refused: %s", code)
	}
}
