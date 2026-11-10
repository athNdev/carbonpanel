// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodestorage

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func carbonStorageBin(t *testing.T) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "carbon-storage")
	if os.PathSeparator == '\\' {
		out += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/carbon-storage")
	cmd.Dir = moduleRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build carbon-storage: %v\n%s", err, out)
	}
	return out
}

func TestRequiresRemoteTransport(t *testing.T) {
	base := t.TempDir()
	visible := filepath.Join(base, "visible")
	if err := os.MkdirAll(visible, 0o755); err != nil {
		t.Fatal(err)
	}
	hidden := filepath.Join(base, "hidden")
	node := NodeInfo{Name: "r1", StorageRoot: hidden}
	if !RequiresRemoteTransport(node) {
		t.Fatal("expected transport when node root is not visible on controller")
	}
	node.StorageRoot = visible
	if RequiresRemoteTransport(node) {
		t.Fatal("same-path shared root should not use transport")
	}
	node.ControllerStorageRoot = filepath.Join(base, "mount")
	if RequiresRemoteTransport(node) {
		t.Fatal("explicit controller mount must not use transport")
	}
}

func TestForNodeRemoteTransportRoundTrip(t *testing.T) {
	t.Setenv("TEST_WORKER_BIN", "1")
	bin := carbonStorageBin(t)
	SetTransport(&ProcessTransport{Bin: bin})
	defer SetTransport(nil)

	base := t.TempDir()
	nodeRoot := filepath.Join(base, "remote-root")
	dataRoot := filepath.Join("var", "data", "servers", "srv-a")
	if err := os.MkdirAll(filepath.Join(nodeRoot, "srv-a"), 0o755); err != nil {
		t.Fatal(err)
	}
	node := NodeInfo{ID: "node-1", Name: "node-1", StorageRoot: nodeRoot}

	if _, err := MapDataPath(node, dataRoot); err != nil {
		t.Fatalf("MapDataPath: %v", err)
	}
	acc, err := ForNode(node, dataRoot)
	if err != nil {
		t.Fatalf("ForNode transport: %v", err)
	}
	defer func() { _ = acc.Close() }()

	if err := acc.WriteFile("server.properties", []byte("motd=remote")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := acc.ReadFile("server.properties")
	if err != nil || string(got) != "motd=remote" {
		t.Fatalf("ReadFile = %q, %v", got, err)
	}
	onDisk := filepath.Join(nodeRoot, "srv-a", "server.properties")
	raw, err := os.ReadFile(onDisk)
	if err != nil || string(raw) != "motd=remote" {
		t.Fatalf("data must land on node-side root only: %q, %v", raw, err)
	}
	controllerSibling := filepath.Join(base, "srv-a")
	if _, err := os.Stat(controllerSibling); !os.IsNotExist(err) {
		t.Fatalf("controller must not create %q, stat err=%v", controllerSibling, err)
	}
}

func TestForNodeRemoteTransportRefusedWithoutProvider(t *testing.T) {
	SetTransport(nil)
	node := NodeInfo{ID: "node-2", Name: "node-2", StorageRoot: filepath.Join(t.TempDir(), "only-on-node")}
	if _, err := ForNode(node, filepath.Join("var", "data", "servers", "srv-b")); err == nil || !IsUnavailable(err) {
		t.Fatalf("want ErrUnavailable without transport, got %v", err)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func TestProcessTransportRequiresBin(t *testing.T) {
	var p ProcessTransport
	if _, err := p.Open(context.Background(), NodeInfo{Name: "r"}, "x"); err == nil || !IsUnavailable(err) {
		t.Fatalf("want ErrUnavailable, got %v", err)
	}
}
