// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/noderuntime/native"
)

func rootlessDaemonSpec() DaemonSpec {
	return DaemonSpec{
		NodeID:              "node-daemon-fixture",
		Mode:                ModeRootless,
		ConfigPath:          "/srv/carbon-node/state/daemon.json",
		SocketPath:          "/run/user/1000/carbon-node/docker.sock",
		Pidfile:             "/run/user/1000/carbon-node/docker.pid",
		DataRoot:            "/srv/carbon-node/state/docker/data",
		ExecRoot:            "/srv/carbon-node/state/docker/exec",
		ContainerdNamespace: "carbon-node-fixture",
		BridgeCIDR:          "10.231.0.1/24",
		PortRange:           "30000-31000",
		StorageDriver:       "overlay2",
		RuntimeUID:          1000,
		RuntimeRoot:         "/run/user/1000/carbon-node",
		StateRoot:           "/srv/carbon-node/state",
	}
}

func rootfulDaemonSpec() DaemonSpec {
	d := rootlessDaemonSpec()
	d.Mode = ModeRootful
	d.SocketPath = "/run/carbon-node/docker.sock"
	d.Pidfile = "/run/carbon-node/docker.pid"
	d.RuntimeRoot = "/run/carbon-node"
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
			// The canonical source emits bip only. A "bridge": "none" plus
			// "bip" combination is invalid dockerd config and must never appear.
			if _, ok := cfg["bridge"]; ok {
				t.Fatal("canonical config still contains the invalid bridge key")
			}
			if strings.Contains(rendered.Config, `"bridge"`) {
				t.Fatal("canonical config still contains a bridge key")
			}
			wantKeys := []string{
				"hosts", "data-root", "exec-root", "containerd-namespace",
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
			for _, c := range native.CheckOptionParity(rendered.Config, rendered.Unit) {
				if !c.Passed {
					t.Fatalf("canonical option parity failed: %s", c.Code)
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

// TestDaemonCanonicalPrivatePidfile proves the installer caller passes a
// caller-owned private pidfile through the shared renderer into both the
// config and (for rootful) the unit, without weakening the invariant.
func TestDaemonCanonicalPrivatePidfile(t *testing.T) {
	for _, spec := range []DaemonSpec{rootlessDaemonSpec(), rootfulDaemonSpec()} {
		t.Run(string(spec.Mode), func(t *testing.T) {
			rendered, err := RenderDaemon(spec)
			if err != nil {
				t.Fatal(err)
			}
			var cfg map[string]json.RawMessage
			if err := json.Unmarshal([]byte(rendered.Config), &cfg); err != nil {
				t.Fatal(err)
			}
			var got string
			if err := json.Unmarshal(cfg["pidfile"], &got); err != nil {
				t.Fatal(err)
			}
			if got != spec.Pidfile {
				t.Fatalf("config pidfile = %q, want caller-owned %q", got, spec.Pidfile)
			}
			if strings.Contains(rendered.Config, "/var/run/docker.pid") {
				t.Fatal("shared default pidfile leaked into config")
			}
			if spec.Mode == ModeRootful && !strings.Contains(rendered.Unit, "PIDFile="+spec.Pidfile) {
				t.Fatal("rootful unit missing caller-owned PIDFile directive")
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
		"rootless, per-user",
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
	for _, want := range []string{"CapabilityBoundingSet=CAP_", "AmbientCapabilities=CAP_", "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("rootful unit missing %q", want)
		}
	}
	for _, line := range strings.Split(unit, "\n") {
		if strings.HasPrefix(line, "User=") {
			t.Fatalf("explicit rootful unit must not override User=: %q", line)
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
		{"missing pidfile", func(d *DaemonSpec) { d.Pidfile = "" }},
		{"shared default pidfile", func(d *DaemonSpec) { d.Pidfile = "/var/run/docker.pid" }},
		{"pidfile clobbers socket", func(d *DaemonSpec) { d.Pidfile = d.SocketPath }},
		{"pidfile outside runtime root", func(d *DaemonSpec) { d.Pidfile = "/srv/other/docker.pid" }},
		{"public api", func(d *DaemonSpec) { d.PortRange = "0.0.0.0:2375" }},
		{"shared roots", func(d *DaemonSpec) { d.DataRoot = d.ExecRoot }},
		{"shared namespace", func(d *DaemonSpec) { d.ContainerdNamespace = "moby" }},
		{"rootless without uid", func(d *DaemonSpec) { d.RuntimeUID = 0 }},
		{"rootless socket outside user runtime", func(d *DaemonSpec) { d.SocketPath = "/run/carbon-node/docker.sock" }},
		{"unknown mode", func(d *DaemonSpec) { d.Mode = "sidecar" }},
		{"shared host socket", func(d *DaemonSpec) { d.SocketPath = "/var/run/docker.sock" }},
		{"shared containerd root", func(d *DaemonSpec) { d.DataRoot = "/var/lib/containerd/data" }},
		{"traversal in state root", func(d *DaemonSpec) { d.StateRoot = "/srv/carbon-node/../docker" }},
		{"overlapping roots", func(d *DaemonSpec) { d.RuntimeRoot = d.StateRoot }},
		{"socket outside runtime root", func(d *DaemonSpec) { d.SocketPath = "/run/carbon-node/docker.sock" }},
		{"config outside state root", func(d *DaemonSpec) { d.ConfigPath = "/srv/other/daemon.json" }},
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

// rootlessFixtureRuntime derives a caller-owned temporary runtime root from
// the ACTUAL environment instead of hardcoding UID 1000. Production requires a
// rootless socket under /run/user/<uid>/; the uid must come from the running
// caller, which differs between developer hosts and CI runners. The temporary
// subdirectory is created inside that user runtime dir and removed by cleanup.
// If the platform has no usable user runtime dir, ok is false and the caller
// uses rootful mode so the defensive cases still run entirely inside t.TempDir.
func rootlessFixtureRuntime(t *testing.T) (runtimeRoot string, uid int, ok bool) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "", -1, false
	}
	uid = os.Getuid()
	if uid <= 0 {
		// Rootless requires a non-root caller; a root runner cannot provide a
		// valid per-user runtime identity. Fall back to rootful temp paths.
		return "", uid, false
	}
	base := "/run/user/" + strconv.Itoa(uid)
	info, err := os.Stat(base)
	if err != nil || !info.IsDir() {
		return "", uid, false
	}
	dir, err := os.MkdirTemp(base, "carbon-installer-path-fixture-*")
	if err != nil {
		return "", uid, false
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir, uid, true
}

// tempSpec builds a spec whose state paths are inside a disposable temp
// directory and whose rootless runtime root is derived from the caller's real
// environment. The production containment/refusal rules are never relaxed.
func tempSpec(t *testing.T, root string) DaemonSpec {
	t.Helper()
	runtimeRoot, uid, ok := rootlessFixtureRuntime(t)
	mode := ModeRootless
	if !ok {
		// No user runtime dir available: use rootful mode so every path stays
		// inside the caller-owned temp tree and no system path is touched.
		mode = ModeRootful
		runtimeRoot = filepath.Join(root, "run")
		uid = 0
	}
	return DaemonSpec{
		NodeID: "node-path-fixture", Mode: mode,
		RuntimeRoot: runtimeRoot, StateRoot: filepath.Join(root, "state"),
		ConfigPath:          filepath.Join(root, "state", "daemon.json"),
		SocketPath:          filepath.Join(runtimeRoot, "docker.sock"),
		Pidfile:             filepath.Join(runtimeRoot, "docker.pid"),
		DataRoot:            filepath.Join(root, "state", "data"),
		ExecRoot:            filepath.Join(root, "state", "exec"),
		ContainerdNamespace: "carbon-node-fixture", BridgeCIDR: "10.231.0.1/24",
		PortRange: "30000-31000", StorageDriver: "vfs", RuntimeUID: uid,
	}
}

// TestDaemonDefensivePathRefusals proves fail-closed behaviour against unsafe
// filesystem layouts using TEMPORARY paths only: no privileged write, no real
// daemon path, no host config mutation.
func TestDaemonDefensivePathRefusals(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX containment roots are required; this fixture runs on the owned Linux systemd fixture")
	}
	base := t.TempDir()
	good := tempSpec(t, base)
	for _, dir := range []string{good.RuntimeRoot, good.StateRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := ValidateDaemonPaths(good); err != nil {
		t.Fatalf("clean temp spec refused: %v", err)
	}

	t.Run("symlink escape outside owned root", func(t *testing.T) {
		root := t.TempDir()
		spec := tempSpec(t, root)
		for _, dir := range []string{spec.RuntimeRoot, spec.StateRoot} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		outside := t.TempDir()
		link := filepath.Join(spec.StateRoot, "data-link")
		if err := os.Symlink(outside, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		spec.DataRoot = filepath.Join(link, "data")
		if err := ValidateDaemonPaths(spec); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("symlink escape accepted: %v", err)
		}
	})

	t.Run("existing socket collision", func(t *testing.T) {
		root := t.TempDir()
		spec := tempSpec(t, root)
		for _, dir := range []string{spec.RuntimeRoot, spec.StateRoot} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		ln, err := net.Listen("unix", spec.SocketPath)
		if err != nil {
			t.Skipf("unix sockets unavailable: %v", err)
		}
		defer func() { _ = ln.Close() }()
		if err := ValidateDaemonPaths(spec); !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("socket collision accepted: %v", err)
		}
	})

	t.Run("world-writable parent", func(t *testing.T) {
		root := t.TempDir()
		spec := tempSpec(t, root)
		for _, dir := range []string{spec.RuntimeRoot, spec.StateRoot} {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		// The guard inspects the parents of components that exist. Create the
		// data root so the world-writable state root is actually on the path.
		if err := os.MkdirAll(spec.DataRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(spec.StateRoot, 0o777); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = os.Chmod(spec.StateRoot, 0o755) }()
		if err := ValidateDaemonPaths(spec); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("world-writable parent accepted: %v", err)
		}
	})
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
