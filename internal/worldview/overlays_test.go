// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"errors"
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func fixtureRef(serverID, level string) *v1.WorldRef {
	return &v1.WorldRef{Kind: v1.WorldRef_KIND_SERVER_LOCAL, ServerId: serverID, LevelName: level}
}

// TestOverlayStoreScopingFixture confirms overlays are isolated by
// server/world/dimension and get generated ids when absent.
func TestOverlayStoreScopingFixture(t *testing.T) {
	store := NewOverlayStore()
	ref := fixtureRef("srv-a", "world")
	a, err := store.Set(&v1.MapOverlay{
		ServerId: "srv-a", WorldRef: ref, Dimension: "overworld", Kind: "marker", X: 10, Z: -20,
	})
	if err != nil || a.GetId() == "" {
		t.Fatalf("Set = %+v, %v", a, err)
	}
	if _, err := store.Set(nil); !errors.Is(err, ErrInvalidOverlay) {
		t.Fatalf("Set(nil) error = %v, want ErrInvalidOverlay", err)
	}

	otherServer, _ := store.Set(&v1.MapOverlay{
		ServerId: "srv-b", WorldRef: fixtureRef("srv-b", "world"), Dimension: "overworld",
	})
	if otherServer.GetId() == a.GetId() {
		t.Fatal("different servers produced the same overlay id")
	}
	if got := store.List("srv-b", worldKey(ref), "overworld"); len(got) != 0 {
		t.Fatalf("cross-server overlay leaked: %+v", got)
	}
	if got := store.List("srv-a", worldKey(ref), "nether"); len(got) != 0 {
		t.Fatalf("cross-dimension overlay leaked: %+v", got)
	}

	updated, err := store.Set(&v1.MapOverlay{
		Id: a.GetId(), ServerId: "srv-a", WorldRef: ref, Dimension: "overworld", Kind: "poi", Label: "updated",
	})
	if err != nil || updated.GetId() != a.GetId() {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	listed := store.List("srv-a", worldKey(ref), "overworld")
	if len(listed) != 1 || listed[0].GetLabel() != "updated" {
		t.Fatalf("listed = %+v, want one updated overlay", listed)
	}

	// A foreign id cannot overwrite another server's overlay.
	hijack, err := store.Set(&v1.MapOverlay{
		Id: a.GetId(), ServerId: "srv-b", WorldRef: fixtureRef("srv-b", "world"), Dimension: "overworld",
	})
	if err != nil || hijack.GetId() == a.GetId() {
		t.Fatalf("foreign id was accepted: %+v, %v", hijack, err)
	}
}

// TestWorldKeyDoesNotCollapseDistinctRefsFixture makes a folder name alone
// insufficient as identity (ADR 0015).
func TestWorldKeyDoesNotCollapseDistinctRefsFixture(t *testing.T) {
	base := worldKey(fixtureRef("srv-a", "world"))
	if base == "" {
		t.Fatal("empty world key")
	}
	if base == worldKey(fixtureRef("srv-a", "creative")) {
		t.Fatal("different level names produced the same key")
	}
	binding := &v1.WorldRef{Kind: v1.WorldRef_KIND_LIBRARY_BINDING, ServerId: "srv-a", BindingId: "bind-1"}
	if base == worldKey(binding) || worldKey(binding) == worldKey(&v1.WorldRef{
		Kind: v1.WorldRef_KIND_LIBRARY_BINDING, ServerId: "srv-a", BindingId: "bind-2",
	}) {
		t.Fatal("world key collapsed distinct tagged references")
	}
}
