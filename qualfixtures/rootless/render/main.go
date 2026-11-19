// SPDX-License-Identifier: MPL-2.0
// Command render emits the EXACT shipped rootless per-user systemd unit and
// canonical daemon.json from the production renderer. It is a qualification
// fixture tool: it writes only the two requested files into a directory and
// never installs a unit, starts a daemon or touches a host path.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/athNdev/carbon-panel/internal/noderuntime/native"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: render <output-dir>")
		os.Exit(2)
	}
	out := os.Args[1]

	spec := native.DaemonSpec{
		NodeID:              "node-rootless-qual",
		Mode:                native.ModeRootless,
		SocketPath:          "/run/user/1000/carbon-node/docker.sock",
		ConfigPath:          "/run/user/1000/carbon-node/daemon.json",
		XDG_RUNTIME_DIR:     "/run/user/1000",
		DataRoot:            "/home/ubuntu/carbon-qual/docker",
		ExecRoot:            "/run/user/1000/carbon-node/exec",
		Pidfile:             "/run/user/1000/carbon-node/docker.pid",
		ContainerdNamespace: "carbon-qual-rootless",
		BridgeCIDR:          "10.231.0.1/24",
		PortRange:           "30000-31000",
		StorageDriver:       "vfs",
		ServiceUID:          "ubuntu",
		LogIdentifier:       "carbon-node-rootless-qual",
		LogDriver:           "journald",
	}

	manifest, err := spec.BuildManifest()
	if err != nil {
		fmt.Fprintf(os.Stderr, "render refused: %v\n", err)
		os.Exit(1)
	}
	for _, c := range native.CheckUnitContract(manifest.Unit, native.ModeRootless) {
		if !c.Passed {
			fmt.Fprintln(os.Stderr, "unit contract failed:", c.Code)
			os.Exit(1)
		}
	}
	for _, c := range native.CheckOptionParity(manifest.ConfigJSON, manifest.Unit) {
		if !c.Passed {
			fmt.Fprintln(os.Stderr, "option parity failed:", c.Code)
			os.Exit(1)
		}
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	files := map[string]string{
		"carbon-node-rootless.service": manifest.Unit,
		"daemon.json":                  manifest.ConfigJSON,
	}
	for name, body := range files {
		p := filepath.Join(out, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("wrote", p)
	}
	fmt.Print(manifest.Unit)
}
