// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestResolveModpackGameLoader(t *testing.T) {
	pack := func(loaders string) *storage.IndexedModpack {
		return &storage.IndexedModpack{ID: "modrinth-abc", Indexer: "modrinth", ModLoaders: loaders}
	}
	files := []*storage.IndexedModpackFile{
		{ID: "ver-neo", ModLoader: "neoforge"},
		{ID: "ver-fabric", ModLoader: "fabric"},
	}

	if got := resolveModpackGameLoader(nil, nil, ""); got != "" {
		t.Fatalf("nil pack = %q, want empty", got)
	}
	if got := resolveModpackGameLoader(pack(`["neoforge"]`), nil, ""); got != storage.ModLoaderNeoForge {
		t.Fatalf("pack list = %q, want neoforge", got)
	}
	if got := resolveModpackGameLoader(pack(`["neoforge"]`), files, "ver-fabric"); got != storage.ModLoaderFabric {
		t.Fatalf("pinned file = %q, want fabric", got)
	}
	if got := resolveModpackGameLoader(pack(`["fabric"]`), files, "no-such-version"); got != storage.ModLoaderFabric {
		t.Fatalf("unknown version falls back to pack = %q, want fabric", got)
	}
	if got := resolveModpackGameLoader(pack(`["modrinth","forge"]`), nil, ""); got != storage.ModLoaderForge {
		t.Fatalf("platform entries skipped = %q, want forge", got)
	}
	if got := resolveModpackGameLoader(pack(`[]`), nil, ""); got != "" {
		t.Fatalf("empty list = %q, want empty", got)
	}
	if got := resolveModpackGameLoader(pack(`not-json`), nil, ""); got != "" {
		t.Fatalf("bad JSON = %q, want empty", got)
	}
}

func TestIsModpackPlatformLoader(t *testing.T) {
	for _, l := range []storage.ModLoader{
		storage.ModLoaderModrinth,
		storage.ModLoaderAutoCurseForge,
		storage.ModLoaderCurseForge,
		storage.ModLoaderFTBA,
	} {
		if !isModpackPlatformLoader(l) {
			t.Fatalf("%q must be a platform", l)
		}
	}
	for _, l := range []storage.ModLoader{
		storage.ModLoaderForge,
		storage.ModLoaderNeoForge,
		storage.ModLoaderFabric,
		storage.ModLoaderVanilla,
		"",
	} {
		if isModpackPlatformLoader(l) {
			t.Fatalf("%q must not be a platform", l)
		}
	}
}

func TestDbServerToProtoModpackLoader(t *testing.T) {
	plain := dbServerToProto(&storage.Server{ID: "s1", ModLoader: storage.ModLoaderFabric})
	if plain.ModpackLoader != v1.ModLoader_MOD_LOADER_UNSPECIFIED {
		t.Fatalf("empty display loader = %v, want UNSPECIFIED", plain.ModpackLoader)
	}
	packed := dbServerToProto(&storage.Server{
		ID: "s2", ModLoader: storage.ModLoaderModrinth, ModpackLoader: storage.ModLoaderNeoForge,
	})
	if packed.ModpackLoader != v1.ModLoader_MOD_LOADER_NEOFORGE {
		t.Fatalf("display loader = %v, want NEOFORGE", packed.ModpackLoader)
	}
	if packed.ModLoader != v1.ModLoader_MOD_LOADER_MODRINTH {
		t.Fatalf("provisioning loader = %v, want MODRINTH", packed.ModLoader)
	}
}
