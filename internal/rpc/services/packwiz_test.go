// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type packwizServiceFixtureTransport func(*http.Request) (*http.Response, error)

func (f packwizServiceFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

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

// TestPackwizServiceModControlUsesSharedOperation exercises the generated mod
// control methods against a real pack manager. The generated service and the
// compatibility adapter hold the same PackwizOperations, so a mutation made
// through the generated contract must be visible through the shared operation.
func TestPackwizServiceModControlUsesSharedOperation(t *testing.T) {
	transport := packwizServiceFixtureTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("fixture-jar-bytes")),
			Header:     make(http.Header),
		}, nil
	})
	mgr := packwiz.NewManager(t.TempDir(), logger.New(), packwiz.ManagerOptions{HTTPClient: &http.Client{Transport: transport}})
	created, err := mgr.CreatePack(&packwiz.Pack{Name: "Mod Control", MCVersion: "1.21.1", ModLoader: "fabric"})
	if err != nil {
		t.Fatal(err)
	}
	packID := created.ID
	svc := NewPackwizService(handlers.NewPackwizOperations(mgr))
	ctx := context.Background()

	added, err := svc.AddPackMod(ctx, connect.NewRequest(&v1.AddPackModRequest{
		PackId: packID,
		Mod:    &v1.PackMod{Slug: "alpha", Name: "Alpha", FileName: "alpha.jar", Side: "both", Platform: "modrinth"},
	}))
	if err != nil || !added.Msg.Success {
		t.Fatalf("AddPackMod: %v %v", added, err)
	}

	updated, err := svc.UpdatePackMod(ctx, connect.NewRequest(&v1.UpdatePackModRequest{
		PackId: packID, Slug: "alpha", Side: "server", Pinned: true,
	}))
	if err != nil || !updated.Msg.Success {
		t.Fatalf("UpdatePackMod: %v %v", updated, err)
	}

	got, err := svc.GetPack(ctx, connect.NewRequest(&v1.GetPackRequest{PackId: packID}))
	if err != nil {
		t.Fatal(err)
	}
	findMod := func(slug string) *v1.PackMod {
		for _, m := range got.Msg.Pack.Mods {
			if m.Slug == slug {
				return m
			}
		}
		return nil
	}
	if m := findMod("alpha"); m == nil || m.Side != "server" || !m.Pinned {
		t.Fatalf("generated update not visible through shared operation: %v", m)
	}

	batch, err := svc.BatchPackMods(ctx, connect.NewRequest(&v1.BatchPackModsRequest{
		PackId: packID, Action: "unpin", Slugs: []string{"alpha"},
	}))
	if err != nil || !batch.Msg.Success || batch.Msg.Count != 1 {
		t.Fatalf("BatchPackMods: %v %v", batch, err)
	}
	got, err = svc.GetPack(ctx, connect.NewRequest(&v1.GetPackRequest{PackId: packID}))
	if err != nil {
		t.Fatal(err)
	}
	if m := findMod("alpha"); m == nil || m.Pinned {
		t.Fatalf("batch unpin not visible through shared operation: %v", m)
	}

	urlMod, err := svc.AddUrlMod(ctx, connect.NewRequest(&v1.AddUrlModRequest{
		PackId: packID, Name: "Beta", FileName: "beta.jar",
		DownloadUrl: "https://fixture.invalid/beta.jar", Side: "client", Pinned: true,
	}))
	if err != nil || urlMod.Msg.Mod == nil || urlMod.Msg.Mod.Slug == "" {
		t.Fatalf("AddUrlMod: %v %v", urlMod, err)
	}

	removed, err := svc.DeletePackMod(ctx, connect.NewRequest(&v1.DeletePackModRequest{PackId: packID, Slug: "alpha"}))
	if err != nil || !removed.Msg.Success {
		t.Fatalf("DeletePackMod: %v %v", removed, err)
	}
	got, err = svc.GetPack(ctx, connect.NewRequest(&v1.GetPackRequest{PackId: packID}))
	if err != nil {
		t.Fatal(err)
	}
	if findMod("alpha") != nil {
		t.Fatal("alpha still present after generated DeletePackMod")
	}
	if findMod(urlMod.Msg.Mod.Slug) == nil {
		t.Fatal("AddUrlMod entry missing from shared operation")
	}
}

// TestPackDependencyRefToProtoIncludesPlatform guards the field-for-field
// parity claim for dependency-aware adds: the legacy DependencyRef carries a
// platform discriminator and a generated consumer must be able to read it.
func TestPackDependencyRefToProtoIncludesPlatform(t *testing.T) {
	got := dependencyRefToProto(packwiz.DependencyRef{
		Platform: "curseforge", ProjectID: "12345", VersionID: "v-1",
		Title: "Dependency", DependencyType: "required",
	})
	if got.Platform != "curseforge" || got.ProjectId != "12345" || got.VersionId != "v-1" ||
		got.Title != "Dependency" || got.DependencyType != "required" {
		t.Fatalf("dependency ref lost fields in generated contract: %v", got)
	}
}

// TestPackwizServiceDependencyResolutionFailureIsSoft covers the resolve branch
// the plain add test does not exercise. A provider failure is swallowed by the
// shared resolver into an empty detail report: the RPC must still succeed and
// keep the already-installed requested mod (ADR 0012 parity with the adapter).
func TestPackwizServiceDependencyResolutionFailureIsSoft(t *testing.T) {
	transport := packwizServiceFixtureTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Body:       io.NopCloser(strings.NewReader(`{"error":"fixture provider failure"}`)),
			Header:     make(http.Header),
		}, nil
	})
	mgr := packwiz.NewManager(t.TempDir(), logger.New(), packwiz.ManagerOptions{HTTPClient: &http.Client{Transport: transport}})
	created, err := mgr.CreatePack(&packwiz.Pack{Name: "Resolve", MCVersion: "1.21.1", ModLoader: "fabric"})
	if err != nil {
		t.Fatal(err)
	}
	svc := NewPackwizService(handlers.NewPackwizOperations(mgr))

	resp, err := svc.AddPackMod(context.Background(), connect.NewRequest(&v1.AddPackModRequest{
		PackId: created.ID, ResolveDependencies: true, Loader: "fabric", McVersion: "1.21.1",
		Mod: &v1.PackMod{Slug: "root-mod", Name: "Root", FileName: "root.jar", Platform: "modrinth", ProjectId: "root-id"},
	}))
	if err != nil {
		t.Fatalf("resolver failure must be a soft result, got RPC error: %v", err)
	}
	if !resp.Msg.Success {
		t.Fatalf("resolver failure must not fail the request: %v", resp.Msg)
	}
	if len(resp.Msg.AddedDependencies) != 0 || len(resp.Msg.Unresolved) != 0 || len(resp.Msg.Skipped) != 0 {
		t.Fatalf("provider failure should yield an empty resolver detail report: %v", resp.Msg)
	}

	got, err := svc.GetPack(context.Background(), connect.NewRequest(&v1.GetPackRequest{PackId: created.ID}))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range got.Msg.Pack.Mods {
		if m.Slug == "root-mod" {
			found = true
		}
	}
	if !found {
		t.Fatal("requested mod was rolled back after a resolver failure")
	}
}
