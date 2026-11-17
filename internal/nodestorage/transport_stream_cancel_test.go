// SPDX-License-Identifier: MPL-2.0
package nodestorage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Exercise the production CommandContext adapter, rather than a test process
// whose cancellation cannot kill it before EOF cleanup runs.
func TestProcessTransportShutdownAbortsOwnedStreams(t *testing.T) {
	bin := carbonStorageBin(t)
	for _, cancelOperation := range []bool{false, true} {
		name := "close"
		if cancelOperation {
			name = "cancel"
		}
		t.Run(name, func(t *testing.T) {
			for attempt := 0; attempt < 5; attempt++ {
				node := NodeInfo{Name: "fixture", StorageRoot: t.TempDir()}
				root := nodeDataRoot(node, "srv")
				if err := os.MkdirAll(root, 0700); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				acc, err := (&ProcessTransport{Bin: bin}).Open(ctx, node, "srv")
				if err != nil {
					t.Fatal(err)
				}
				if err := acc.WriteFile("target", []byte("old")); err != nil {
					t.Fatal(err)
				}
				writer, err := acc.BeginWrite(ctx, "target", 1024)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Write([]byte("new")); err != nil {
					t.Fatal(err)
				}
				if cancelOperation {
					cancel()
				}
				if err := acc.Close(); err != nil {
					t.Fatal(err)
				}
				_ = writer.Close()
				body, err := os.ReadFile(filepath.Join(root, "target"))
				if err != nil || string(body) != "old" {
					t.Fatalf("old target lost: %q %v", body, err)
				}
				partials, err := filepath.Glob(filepath.Join(root, ".carbon-write-*.partial"))
				if err != nil {
					t.Fatal(err)
				}
				if len(partials) != 0 {
					t.Fatalf("owned uncommitted temporary leaked: %v", partials)
				}
			}
		})
	}
}
