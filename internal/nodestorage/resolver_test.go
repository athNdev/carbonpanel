// SPDX-License-Identifier: MPL-2.0
package nodestorage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

type resolverTransport struct {
	root    string
	started chan struct{}
	block   bool
}

func (p *resolverTransport) Open(ctx context.Context, _ NodeInfo, _ string) (Accessor, error) {
	if p.started != nil {
		close(p.started)
	}
	if p.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return NewLocal(p.root)
}

func TestIndependentResolversAndCancellation(t *testing.T) {
	node := NodeInfo{Name: "remote", StorageRoot: filepath.Join(t.TempDir(), "remote-only")}
	firstRoot, secondRoot := t.TempDir(), t.TempDir()
	first := NewResolver(&resolverTransport{root: firstRoot})
	second := NewResolver(&resolverTransport{root: secondRoot})
	for _, fixture := range []struct {
		resolver *Resolver
		root     string
	}{{first, firstRoot}, {second, secondRoot}, {first, firstRoot}} {
		accessor, err := fixture.resolver.ForNode(context.Background(), node, "server")
		if err != nil {
			t.Fatal(err)
		}
		if accessor.Root() != fixture.root {
			t.Fatalf("root %q, want %q", accessor.Root(), fixture.root)
		}
		if err := accessor.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := NewResolver(nil).ForNode(context.Background(), node, "server"); !IsUnavailable(err) {
		t.Fatalf("missing transport should fail closed: %v", err)
	}
	started := make(chan struct{})
	blocked := NewResolver(&resolverTransport{started: started, block: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := blocked.ForNode(ctx, node, "server"); done <- err }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("transport did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resolver did not propagate cancellation")
	}
}
