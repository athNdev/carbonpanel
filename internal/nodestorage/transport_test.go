// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodestorage

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
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
	bin := carbonStorageBin(t)
	provider := &creatingProcessTransport{ProcessTransport: ProcessTransport{Bin: bin}}
	SetTransport(provider)
	defer SetTransport(nil)

	base := t.TempDir()
	nodeRoot := filepath.Join(base, "remote-root")
	dataRoot := filepath.Join(base, "controller", "servers", "srv-a")
	node := NodeInfo{ID: "node-1", Name: "node-1", StorageRoot: nodeRoot}
	if !RequiresRemoteTransport(node) {
		t.Fatal("fixture must require worker transport before the node-side root is created")
	}
	acc, err := ForNode(node, dataRoot)
	if err != nil {
		t.Fatalf("ForNode transport: %v", err)
	}
	defer func() { _ = acc.Close() }()
	if provider.opens != 1 {
		t.Fatalf("worker transport opened %d times, want 1", provider.opens)
	}

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
	controllerSibling := dataRoot
	if _, err := os.Stat(controllerSibling); !os.IsNotExist(err) {
		t.Fatalf("controller must not create %q, stat err=%v", controllerSibling, err)
	}
}

// The node-side directory appears only when the transport is selected. A
// pre-created controller-visible root would silently test shared storage.
type creatingProcessTransport struct {
	ProcessTransport
	opens int
}

func (p *creatingProcessTransport) Open(ctx context.Context, node NodeInfo, root string) (Accessor, error) {
	p.opens++
	if err := os.MkdirAll(nodeDataRoot(node, root), 0o700); err != nil {
		return nil, err
	}
	return p.ProcessTransport.Open(ctx, node, root)
}

func TestProcessTransportCancellationAndClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	node := NodeInfo{Name: "worker", StorageRoot: t.TempDir()}
	if err := os.MkdirAll(nodeDataRoot(node, "srv"), 0o700); err != nil {
		t.Fatal(err)
	}
	p := ProcessTransport{Bin: carbonStorageBin(t)}
	acc, err := p.Open(ctx, node, "srv")
	if err != nil {
		t.Fatal(err)
	}
	if err := acc.WriteFile("server.properties", []byte("motd=worker")); err != nil {
		t.Fatal(err)
	}
	cancel()
	done := make(chan error, 1)
	go func() {
		for {
			_, err := acc.ReadFile("server.properties")
			if err != nil {
				_ = acc.Close()
				done <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled worker accepted a read")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled worker did not release its pipes")
	}
	if err := acc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestMain(m *testing.M) {
	if role := os.Getenv("CARBONPANEL_TEST_STORAGE_WORKER"); role != "" {
		if role == "refuse" {
			fmt.Println(`{"ok":false,"error":"worker refused root"}`)
		} else {
			fmt.Println("invalid-json")
		}
		// Deliberately ignore stdin EOF. Failed handshakes must reap this child.
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestProcessTransportReapsFailedHandshake(t *testing.T) {
	for _, role := range []string{"refuse", "malformed"} {
		t.Run(role, func(t *testing.T) {
			t.Setenv("CARBONPANEL_TEST_STORAGE_WORKER", role)
			bin, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err = (&ProcessTransport{Bin: bin}).Open(ctx, NodeInfo{StorageRoot: t.TempDir()}, "srv")
			if !IsUnavailable(err) {
				t.Fatalf("handshake error=%v, want unavailable", err)
			}
			if ctx.Err() != nil {
				t.Fatal("failed handshake waited for caller cancellation instead of reaping worker")
			}
		})
	}
}

func TestProcessTransportEarlyExit(t *testing.T) {
	// The test executable rejects accessor arguments before running any tests.
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	p := ProcessTransport{Bin: bin}
	if _, err := p.Open(ctx, NodeInfo{StorageRoot: t.TempDir()}, "srv"); !IsUnavailable(err) {
		t.Fatalf("early worker exit: %v, want unavailable", err)
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
