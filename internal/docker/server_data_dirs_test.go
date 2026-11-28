// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

func assertWorkTemp(t *testing.T, dataDir string) {
	t.Helper()
	workTemp := serverWorkTempHostPath(dataDir)
	info, err := os.Stat(workTemp)
	if err != nil {
		t.Fatalf("work temp %q: %v", workTemp, err)
	}
	if !info.IsDir() {
		t.Fatalf("work temp %q is not a directory", workTemp)
	}
	if runtime.GOOS == "linux" {
		if info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
			t.Fatalf("work temp mode = %o, want 1777", info.Mode())
		}
	}
}

func TestPrepareServerDataDirsKeepsLocalAndNoStorageBehavior(t *testing.T) {
	cases := map[string]nodestorage.NodeInfo{
		"local":             {Name: "local", IsLocal: true},
		"no storage layout": {Name: "r1"},
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			// serverDataPath is the panel-side path in these modes.
			serverDataPath := filepath.Join(t.TempDir(), "servers", "one")
			client := &Client{nodeStorage: node}

			if err := client.prepareServerDataDirs(serverDataPath); err != nil {
				t.Fatalf("prepareServerDataDirs: %v", err)
			}
			if _, err := os.Stat(serverDataPath); err != nil {
				t.Fatalf("server data dir %q: %v", serverDataPath, err)
			}
			assertWorkTemp(t, serverDataPath)
		})
	}
}

func TestPrepareServerDataDirsCreatesWorkTempThroughControllerMount(t *testing.T) {
	// A remote node whose storage the controller mounts: the node-side path is
	// not usable in this process, so the work temp must be created under the
	// controller mount, not at the panel-side path.
	base := t.TempDir()
	nodeRoot := filepath.Join(base, "node-storage")
	controllerRoot := filepath.Join(base, "controller-mount")
	if err := os.MkdirAll(controllerRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	serverDataPath := filepath.Join(base, "panel-side", "servers", "one")
	client := &Client{nodeStorage: nodestorage.NodeInfo{
		Name:                  "remote-1",
		StorageRoot:           nodeRoot,
		ControllerStorageRoot: controllerRoot,
	}}

	if err := client.prepareServerDataDirs(serverDataPath); err != nil {
		t.Fatalf("prepareServerDataDirs: %v", err)
	}

	assertWorkTemp(t, filepath.Join(controllerRoot, "one"))
	if _, err := os.Stat(filepath.Join(nodeRoot, "one")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node-side path must not be created locally, stat err = %v", err)
	}
	if _, err := os.Stat(serverDataPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("panel-side path must not be created, stat err = %v", err)
	}
}

func TestPrepareServerDataDirsLeavesNodeOnlyStorageToTheMount(t *testing.T) {
	// Transport-only storage (node root not visible, no controller mount) has
	// no controller-visible path: Docker's CreateMountpoint owns creation.
	nodeRoot := filepath.Join(t.TempDir(), "only-on-node")
	serverDataPath := filepath.Join(t.TempDir(), "servers", "one")
	client := &Client{nodeStorage: nodestorage.NodeInfo{Name: "remote-2", StorageRoot: nodeRoot}}

	if err := client.prepareServerDataDirs(serverDataPath); err != nil {
		t.Fatalf("prepareServerDataDirs: %v", err)
	}
	if _, err := os.Stat(serverDataPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("panel-side path must not be created, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(nodeRoot, "one")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("node-side path must not be created, stat err = %v", err)
	}
}

func TestPrepareServerDataDirsSkipsAMissingControllerMount(t *testing.T) {
	// A configured controller mount that is not actually mounted must not be
	// fabricated on the panel host: EnsureDataRoot refuses the missing root.
	base := t.TempDir()
	controllerRoot := filepath.Join(base, "not-mounted")
	serverDataPath := filepath.Join(base, "servers", "one")
	client := &Client{nodeStorage: nodestorage.NodeInfo{
		Name:                  "remote-3",
		StorageRoot:           filepath.Join(base, "node-storage"),
		ControllerStorageRoot: controllerRoot,
	}}

	if err := client.prepareServerDataDirs(serverDataPath); err != nil {
		t.Fatalf("prepareServerDataDirs: %v", err)
	}
	if _, err := os.Stat(controllerRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing controller mount must not be created, stat err = %v", err)
	}
	if _, err := os.Stat(serverDataPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("panel-side path must not be created, stat err = %v", err)
	}
}
