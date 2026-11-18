// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"strings"
	"testing"
)

func privateSpec() DaemonSpec {
	return DaemonSpec{
		NodeID: "node-fixture-native", Mode: ModeRootless,
		SocketPath:          "/run/user/1000/carbon-node/docker.sock",
		DataRoot:            "/var/lib/carbon-node/docker",
		ExecRoot:            "/run/carbon-node/docker",
		ContainerdNamespace: "carbon-node-1",
		BridgeCIDR:          "10.231.0.1/24",
		PortRange:           "30000-31000",
		StorageDriver:       "overlay2",
		LogIdentifier:       "carbon-node-node-fixture-native",
	}
}

func TestDaemonSpecPrivateIsolation(t *testing.T) {
	d := privateSpec()
	if code := d.ValidateOrRefuse(); code != "" {
		t.Fatalf("private spec refused: %s", code)
	}
}

func TestDaemonSpecRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*DaemonSpec)
		want string
	}{
		{"host socket", func(d *DaemonSpec) { d.SocketPath = "/var/run/docker.sock" }, "DAEMON_PRIVATE_SOCKET"},
		{"shared data root", func(d *DaemonSpec) { d.DataRoot = "/var/lib/docker" }, "DAEMON_DEDICATED_ROOTS"},
		{"shared namespace", func(d *DaemonSpec) { d.ContainerdNamespace = "moby" }, "DAEMON_CONTAINERD_NAMESPACE"},
		{"public api", func(d *DaemonSpec) { d.PortRange = "0.0.0.0:2375" }, "DAEMON_NO_PUBLIC_API"},
		{"raw api port", func(d *DaemonSpec) { d.PortRange = "2376" }, "DAEMON_NO_PUBLIC_API"},
		{"rootful without uid", func(d *DaemonSpec) { d.Mode = ModeRootful }, "DAEMON_ROOTFUL_DEDICATED_UID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := privateSpec()
			tc.mut(&d)
			if code := d.ValidateOrRefuse(); code != tc.want {
				t.Fatalf("expected %s, got %q", tc.want, code)
			}
			if _, err := d.RenderUnit("/run/carbon-node/daemon.json"); err == nil {
				t.Fatal("unsafe spec produced a systemd unit")
			}
		})
	}
}

func TestDaemonRenderIsInMemoryAndHardened(t *testing.T) {
	d := privateSpec()
	unit, err := d.RenderUnit("/run/carbon-node/daemon.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"NoNewPrivileges=yes", "ProtectSystem=strict", "Delegate=cpu cpuset io memory pids", "--host=unix://" + d.SocketPath, "--containerd-namespace=" + d.ContainerdNamespace} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing hardening directive %q", want)
		}
	}
	if strings.Contains(unit, "--privileged") || strings.Contains(unit, "0.0.0.0:2375") {
		t.Fatal("unit contains forbidden privilege or public API setting")
	}
	config, err := d.RenderConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, `"no-new-privileges": true`) || !strings.Contains(config, `"userland-proxy": false`) {
		t.Fatal("config missing least-privilege defaults")
	}
	if strings.Contains(config, "2375") || strings.Contains(config, "/var/run/docker.sock") {
		t.Fatal("config contains a public endpoint or shared socket")
	}
}

func TestSpecFromFactsRootfulUsesDedicatedUID(t *testing.T) {
	f := qualifiedFacts()
	f.UserNamespaces = false
	sel := ApplyGates(f)
	if sel.Mode != ModeRootful {
		t.Fatalf("expected rootful fallback, got %q", sel.Mode)
	}
	d := SpecFromFacts("node-fixture", f, sel)
	if d.ServiceUID != "carbon-node" {
		t.Fatalf("rootful spec missing dedicated UID: %+v", d)
	}
	if code := d.ValidateOrRefuse(); code != "" {
		t.Fatalf("rootful spec refused: %s", code)
	}
}
