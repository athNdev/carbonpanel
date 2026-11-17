// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodestorage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type isolationAccessor struct{ root string }

func (a *isolationAccessor) Stat(string) (os.FileInfo, error)          { return nil, os.ErrNotExist }
func (a *isolationAccessor) ReadFile(string) ([]byte, error)           { return nil, os.ErrNotExist }
func (a *isolationAccessor) WriteFile(string, []byte) error            { return nil }
func (a *isolationAccessor) ReadDir(string) ([]os.DirEntry, error)     { return nil, nil }
func (a *isolationAccessor) MkdirAll(string) error                     { return nil }
func (a *isolationAccessor) Remove(string) error                       { return nil }
func (a *isolationAccessor) RemoveAll(string) error                    { return nil }
func (a *isolationAccessor) Rename(string, string) error               { return nil }
func (a *isolationAccessor) Root() string                              { return a.root }
func (a *isolationAccessor) Close() error                              { return nil }

type isolationTransport struct {
	opens int
	root  string
}

func (t *isolationTransport) Open(_ context.Context, _ NodeInfo, _ string) (Accessor, error) {
	t.opens++
	return &isolationAccessor{root: t.root}, nil
}

// TestResolversAreIndependent pins W16's instance ownership: two resolvers in
// the same process select their own transports for identical remote nodes, and
// changing one cannot change the other. There is no process-global transport
// to install or clear (SetTransport no longer exists).
func TestResolversAreIndependent(t *testing.T) {
	rootA := filepath.Join(t.TempDir(), "worker-a")
	rootB := filepath.Join(t.TempDir(), "worker-b")
	transportA := &isolationTransport{root: rootA}
	transportB := &isolationTransport{root: rootB}
	resolverA := NewResolver(transportA)
	resolverB := NewResolver(transportB)

	// Identical node identity, no shared controller mount: each resolver must
	// open its own worker transport.
	node := NodeInfo{ID: "node-1", Name: "node-1", StorageRoot: filepath.Join(t.TempDir(), "only-on-node")}
	if !RequiresRemoteTransport(node) {
		t.Fatal("fixture must require a worker transport")
	}
	accA, err := resolverA.ForNode(context.Background(), node, filepath.Join("srv", "data", "one"))
	if err != nil {
		t.Fatalf("resolver A: %v", err)
	}
	defer func() { _ = accA.Close() }()
	accB, err := resolverB.ForNode(context.Background(), node, filepath.Join("srv", "data", "two"))
	if err != nil {
		t.Fatalf("resolver B: %v", err)
	}
	defer func() { _ = accB.Close() }()

	if transportA.opens != 1 || transportB.opens != 1 {
		t.Fatalf("transport opens = (%d, %d), want (1, 1)", transportA.opens, transportB.opens)
	}
	if accA.Root() != rootA || accB.Root() != rootB {
		t.Fatalf("roots = (%q, %q), want (%q, %q)", accA.Root(), accB.Root(), rootA, rootB)
	}

	// Mutating one resolver's transport does not change the other instance.
	resolverA.transport = nil
	if _, err := resolverA.ForNode(context.Background(), node, "srv"); !IsUnavailable(err) {
		t.Fatalf("resolver A without transport must fail closed: %v", err)
	}
	if _, err := resolverB.ForNode(context.Background(), node, "srv"); err != nil {
		t.Fatalf("resolver B must keep its own transport: %v", err)
	}
}
