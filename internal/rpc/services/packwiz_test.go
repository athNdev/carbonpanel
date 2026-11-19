// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestPackwizServiceUsesSharedOperation verifies the generated service reads
// through the same PackwizOperations the compatibility adapter uses and maps
// the stored pack (including content lists) into the generated message.
func TestPackwizServiceUsesSharedOperation(t *testing.T) {
	mgr := packwiz.NewManager(t.TempDir(), logger.New())
	_, err := mgr.CreatePack(&packwiz.Pack{
		Name: "QA Pack", Author: "qa", MCVersion: "1.21.1", ModLoader: "fabric",
		Mods: []packwiz.ModItem{{Slug: "qa-mod", Name: "QA Mod", FileName: "qa.jar", Side: "both", Platform: "modrinth", FileSize: 10}},
	})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(handlers.NewPackwizOperations(mgr))
	ctx := context.Background()

	list, err := svc.ListPacks(ctx, connect.NewRequest(&v1.ListPacksRequest{}))
	if err != nil {
		t.Fatalf("ListPacks: %v", err)
	}
	if len(list.Msg.Packs) != 1 || list.Msg.Packs[0].Name != "QA Pack" {
		t.Fatalf("unexpected list: %v", list.Msg)
	}
	packID := list.Msg.Packs[0].Id

	got, err := svc.GetPack(ctx, connect.NewRequest(&v1.GetPackRequest{PackId: packID}))
	if err != nil {
		t.Fatalf("GetPack: %v", err)
	}
	if got.Msg.Pack == nil || got.Msg.Pack.McVersion != "1.21.1" || len(got.Msg.Pack.Mods) != 1 || got.Msg.Pack.Mods[0].Slug != "qa-mod" {
		t.Fatalf("unexpected pack: %v", got.Msg.Pack)
	}

	cloned, err := svc.ClonePack(ctx, connect.NewRequest(&v1.ClonePackRequest{PackId: packID, Name: "QA Copy"}))
	if err != nil {
		t.Fatalf("ClonePack: %v", err)
	}
	if cloned.Msg.Pack == nil || cloned.Msg.Pack.Name != "QA Copy" {
		t.Fatalf("unexpected clone: %v", cloned.Msg.Pack)
	}

	if _, err := svc.RefreshPack(ctx, connect.NewRequest(&v1.RefreshPackRequest{PackId: packID})); err != nil {
		t.Fatalf("RefreshPack: %v", err)
	}
	if _, err := svc.CheckUpdates(ctx, connect.NewRequest(&v1.CheckUpdatesRequest{PackId: packID, Kind: "mod"})); err != nil {
		t.Fatalf("CheckUpdates: %v", err)
	}
	if _, err := svc.DeletePack(ctx, connect.NewRequest(&v1.DeletePackRequest{PackId: packID})); err != nil {
		t.Fatalf("DeletePack: %v", err)
	}
	if _, err := mgr.GetPack(packID); err == nil {
		t.Fatal("pack still present after generated DeletePack")
	}
}
