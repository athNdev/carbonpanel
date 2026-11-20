// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"

	storage "github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type nativeScopeFixture struct {
	*fakeStore
	node    *storage.Node
	binding *storage.WorldBinding
	busy    bool
}

func (f *nativeScopeFixture) GetNode(context.Context, string) (*storage.Node, error) {
	if f.node == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return f.node, nil
}

func (f *nativeScopeFixture) GetWorldBinding(context.Context, string, string) (*storage.WorldBinding, error) {
	if f.binding == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return f.binding, nil
}

func (f *nativeScopeFixture) AssertNoServerOperation(context.Context, string) error {
	if f.busy {
		return errors.New("fixture operation in progress")
	}
	return nil
}

func TestNativeScopeUsesActualLevelAndCurrentNode(t *testing.T) {
	ctx := context.Background()
	server := &storage.Server{ID: "fixture-server", DataPath: t.TempDir(), NodeID: "registered-controller"}
	f := &nativeScopeFixture{
		fakeStore: newFakeStore(),
		node:      &storage.Node{ID: server.NodeID, IsLocal: true, Enabled: true, EndpointGeneration: 2},
		binding:   &storage.WorldBinding{ID: "active-binding", RevisionID: "current-revision", NodeID: server.NodeID, Generation: 2},
	}
	f.addServer(server.ID, server.NodeID)
	f.setLevelName(server.ID, "a-custom-level")

	ref := &v1.WorldRef{
		Kind: v1.WorldRef_KIND_LIBRARY_BINDING, ServerId: server.ID, LevelName: "a-custom-level", BindingId: f.binding.ID,
	}
	first, err := resolveNativeScope(ctx, f, server, ref)
	if err != nil || first.dataRoot != server.DataPath || first.level != ref.LevelName ||
		first.node.ID != server.NodeID || first.nodeGeneration != 2 || first.revisionID != f.binding.RevisionID {
		t.Fatalf("resolved scope = %+v, error = %v", first, err)
	}

	for _, tc := range []struct {
		name string
		ref  *v1.WorldRef
	}{
		{"another server", &v1.WorldRef{ServerId: "different-server"}},
		{"another level", &v1.WorldRef{LevelName: "other-level"}},
		{"another revision binding", &v1.WorldRef{Kind: v1.WorldRef_KIND_LIBRARY_BINDING, BindingId: "old"}},
		{"a static snapshot", &v1.WorldRef{Kind: v1.WorldRef_KIND_STATIC_IMPORT, SnapshotId: "archive"}},
		{"a binding disguised as server local", &v1.WorldRef{Kind: v1.WorldRef_KIND_SERVER_LOCAL, BindingId: f.binding.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if scope, err := resolveNativeScope(ctx, f, server, tc.ref); !errors.Is(err, errNativeWorldMismatch) || scope != (nativeScope{}) {
				t.Fatalf("unexpected asset scope %+v (%v)", scope, err)
			}
		})
	}

	f.binding.RevisionID = "replacement-revision"
	second, err := resolveNativeScope(ctx, f, server, ref)
	if err != nil || second == first {
		t.Fatalf("same-folder replacement retained previous scope: %+v (%v)", second, err)
	}
	f.node.EndpointGeneration++
	if _, err := resolveNativeScope(ctx, f, server, ref); !errors.Is(err, errNativeWorldMismatch) {
		t.Fatalf("stale binding should be refused, got %v", err)
	}
}

func TestNativeScopeRefusesUnsafeOrUnavailableStorage(t *testing.T) {
	ctx := context.Background()
	server := &storage.Server{ID: "fixture-server", DataPath: t.TempDir(), NodeID: "default"}
	f := &nativeScopeFixture{fakeStore: newFakeStore()}
	f.addServer(server.ID, server.NodeID)
	f.setLevelName(server.ID, "world")
	got, err := resolveNativeScope(ctx, f, server, nil)
	if err != nil || !got.node.IsLocal || got.nodeGeneration != 1 {
		t.Fatalf("legacy server should resolve controller storage: %+v (%v)", got, err)
	}
	f.busy = true
	if _, err := resolveNativeScope(ctx, f, server, nil); !errors.Is(err, errNativeWorldBusy) {
		t.Fatalf("active operation should refuse rendering, got %v", err)
	}
	f.busy = false

	f.setLevelName(server.ID, "../../outside")
	if _, err := resolveNativeScope(ctx, f, server, nil); !errors.Is(err, errNativeSourceAbsent) {
		t.Fatalf("invalid server level should refuse rendering, got %v", err)
	}
	f.setLevelName(server.ID, "world")
	server.NodeID = "registered-node"

	for _, tc := range []*storage.Node{
		nil,
		{ID: server.NodeID, Enabled: true, IsLocal: false, EndpointGeneration: 1},
		{ID: server.NodeID, Enabled: false, IsLocal: true, EndpointGeneration: 1},
		{ID: server.NodeID, Enabled: true, IsLocal: true, EndpointGeneration: 0},
	} {
		f.node = tc
		if _, err := resolveNativeScope(ctx, f, server, nil); !errors.Is(err, errNativeSourceAbsent) {
			t.Fatalf("unqualified owner %+v should refuse rendering, got %v", tc, err)
		}
	}
}
