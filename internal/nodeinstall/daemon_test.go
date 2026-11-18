// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func rootlessDaemonSpec() DaemonSpec {
	return DaemonSpec{
		NodeID:              "node-daemon-fixture",
		Mode:                ModeRootless,
		ConfigPath:          "/srv/carbon-node/state/daemon.json",
		SocketPath:          "/run/user/1000/carbon-node/docker.sock",
		DataRoot:            "/srv/carbon-node/state/docker/data",
		ExecRoot:            "/srv/carbon-node/state/docker/exec",
		ContainerdNamespace: "carbon-node-fixture",
		BridgeCIDR:          "10.231.0.1/24",
		PortRange:           "30000-31000",
		StorageDriver:       "overlay2",
		RuntimeUID:          1000,
	}
}

func rootfulDaemonSpec() DaemonSpec {
	d := rootlessDaemonSpec()
	d.Mode = ModeRootful
	d.SocketPath = "/run/carbon-node/docker.sock"
	d.RuntimeUID = 0
	return d
}

// TestDaemonOptionsSingleSource is the direct regression for the PR527 finding
// "rendered dockerd options were duplicated between CLI flags and config.json".
func TestDaemonOptionsSingleSource(t *testing.T) {
	for _, spec := range []DaemonSpec{rootlessDaemonSpec(), rootfulDaemonSpec()} {
		t.Run(string(spec.Mode), func(t *testing.T) {
			rendered, err := RenderDaemon(spec)
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]json.RawMessage
			if err := json.Unmarshal([]byte(rendered.Config), &cfg); err != nil {
				t.Fatalf("config is not valid JSON: %v", err)
			}
			wantKeys := []string{
				"hosts", "data-root", "exec-root", "containerd-namespace", "bridge",
				"bip", "storage-driver", "no-new-privileges", "live-restore",
				"userland-proxy", "log-driver", "log-opts", "iptables",
				"ip6tables", "features",
			}
			for _, key := range wantKeys {
				q := `"` + key + `"`
				if got := strings.Count(rendered.Config, q); got != 1 {
					t.Fatalf("config key %q appears %d times, want exactly once", key, got)
				}
				if _, ok := cfg[key]; !ok {
					t.Fatalf("config is missing key %q", key)
				}
			}
			// Every option lives ONLY in config.json. The unit carries no
			// option flags, only --config-file.
			if !strings.Contains(rendered.Unit, "--config-file="+spec.ConfigPath) {
				t.Fatal("unit does not reference the config file")
			}
			for _, flag := range []string{
				"--host=", "--data-root=", "--exec-root=", "--containerd-namespace=",
				"--bip=", "--storage-driver=", "--no-new-privileges", "--live-restore",
			} {
				if strings.Contains(rendered.Unit, flag) {
					t.Fatalf("unit duplicates daemon option %q from config.json", flag)
				}
			}
			var count int
			for _, line := range strings.Split(rendered.Unit, "\n") {
				if strings.HasPrefix(line, "ExecStart=") {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("unit has %d ExecStart lines, want 1", count)
			}
		})
	}
}

// TestDaemonRootlessPerUserLauncher is the regression for "rootless was
// rendered as a direct dockerd SYSTEM unit".
func TestDaemonRootlessPerUserLauncher(t *testing.T) {
	spec := rootlessDaemonSpec()
	rendered, err := RenderDaemon(spec)
	if err != nil {
		t.Fatal(err)
	}
	unit := rendered.Unit
	for _, want := range []string{
		"rootless per-user service",
		"WantedBy=default.target",
		"Environment=XDG_RUNTIME_DIR=/run/user/1000",
		"ExecStart=/usr/bin/dockerd-rootless.sh --config-file=" + spec.ConfigPath,
		"Delegate=cpu cpuset io memory pids",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("rootless unit missing %q", want)
		}
	}
	for _, forbidden := range []string{"\nUser=", "WantedBy=multi-user.target", "sudo", "CAP_SYS_ADMIN"} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("rootless unit must not contain %q (system-unit/privilege leak)", forbidden)
		}
	}
	if !strings.Contains(rendered.InstallHint, "systemctl --user") {
		t.Fatal("rootless install hint is not a per-user launcher")
	}
	if strings.Contains(rendered.InstallHint, "sudo systemctl") {
		t.Fatal("rootless install hint must not install a system unit")
	}
}

// TestDaemonRootfulExplicitCapabilities is the regression for "rootful was
// rendered as non-root with an empty capability bound".
func TestDaemonRootfulExplicitCapabilities(t *testing.T) {
	rendered, err := RenderDaemon(rootfulDaemonSpec())
	if err != nil {
		t.Fatal(err)
	}
	unit := rendered.Unit
	for _, want := range []string{"User=root", "Group=root", "CapabilityBoundingSet=CAP_", "AmbientCapabilities=CAP_"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("rootful unit missing %q", want)
		}
	}
	for _, cap := range []string{"CAP_SYS_ADMIN", "CAP_NET_ADMIN", "CAP_NET_RAW", "CAP_SYS_RESOURCE"} {
		if !strings.Contains(unit, cap) {
			t.Fatalf("rootful capability bound missing %q", cap)
		}
	}
	for _, forbidden := range []string{"CapabilityBoundingSet=\n", "User=carbon-node", "dockerd-rootless.sh"} {
		if strings.Contains(unit, forbidden) {
			t.Fatalf("rootful unit must not contain %q", forbidden)
		}
	}
	if strings.Contains(unit, "WantedBy=default.target") {
		t.Fatal("rootful unit must not be a per-user unit")
	}
}

func TestDaemonRenderRefusals(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*DaemonSpec)
	}{
		{"global config path", func(d *DaemonSpec) { d.ConfigPath = "/etc/docker/daemon.json" }},
		{"host socket", func(d *DaemonSpec) { d.SocketPath = "/var/run/docker.sock" }},
		{"public api", func(d *DaemonSpec) { d.PortRange = "0.0.0.0:2375" }},
		{"shared roots", func(d *DaemonSpec) { d.DataRoot = d.ExecRoot }},
		{"shared namespace", func(d *DaemonSpec) { d.ContainerdNamespace = "moby" }},
		{"rootless without uid", func(d *DaemonSpec) { d.RuntimeUID = 0 }},
		{"rootless socket outside user runtime", func(d *DaemonSpec) { d.SocketPath = "/run/carbon-node/docker.sock" }},
		{"unknown mode", func(d *DaemonSpec) { d.Mode = "sidecar" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := rootlessDaemonSpec()
			tc.mut(&spec)
			rendered, err := RenderDaemon(spec)
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if rendered.Unit != "" || rendered.Config != "" {
				t.Fatal("refused spec still produced rendered output")
			}
		})
	}
}

// fakeRenderer proves the integration seam is real: the shared canonical
// renderer can be injected without changing any caller.
type fakeRenderer struct{ called bool }

func (f *fakeRenderer) Render(DaemonSpec) (DaemonRender, error) {
	f.called = true
	return DaemonRender{Mode: "seam", Unit: "[Unit]\n"}, nil
}

func TestDaemonRendererSeam(t *testing.T) {
	previous := daemonRenderer
	defer func() { daemonRenderer = previous }()
	fake := &fakeRenderer{}
	SetDaemonRenderer(fake)
	out, err := RenderDaemon(rootlessDaemonSpec())
	if err != nil || !fake.called || out.Mode != "seam" {
		t.Fatalf("renderer seam not used: %v %+v", err, out)
	}
	SetDaemonRenderer(nil) // nil must not clear the active renderer
	out, err = RenderDaemon(rootlessDaemonSpec())
	if err != nil || out.Mode != "seam" {
		t.Fatalf("nil override changed renderer: %v %+v", err, out)
	}
}
