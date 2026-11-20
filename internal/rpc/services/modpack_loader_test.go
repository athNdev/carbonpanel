// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestCreateServerResolvesPinnedModrinthRelease(t *testing.T) {
	for _, tt := range []struct {
		name, selector, wantPin string
		indexed, offline        bool
		wantLoader              v1.ModLoader
		wantRequests            int32
	}{
		{"fresh version ID", "neo-id", "neo-id", false, false, v1.ModLoader_MOD_LOADER_NEOFORGE, 1},
		{"legacy version number", "1.8.4", "neo-id", false, false, v1.ModLoader_MOD_LOADER_NEOFORGE, 1},
		{"indexed version ID", "neo-id", "neo-id", true, true, v1.ModLoader_MOD_LOADER_NEOFORGE, 0},
		{"provider unavailable", "1.8.4", "1.8.4", false, true, v1.ModLoader_MOD_LOADER_FORGE, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/project/test-project/version" {
					http.NotFound(w, r)
					return
				}
				if tt.offline {
					http.Error(w, "unavailable", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[
					{"id":"forge-id","version_number":"1.7.0","loaders":["forge"],"files":[{"primary":true}]},
					{"id":"neo-id","version_number":"1.8.4","loaders":["neoforge"],"files":[{"primary":true}]}
				]`))
			}))
			t.Cleanup(provider.Close)
			baseURL := modrinth.BaseURL
			modrinth.BaseURL = provider.URL
			t.Cleanup(func() { modrinth.BaseURL = baseURL })

			ctx := ctxWithRole("operator")
			svc, _ := newDockerPrivilegeTestService(t)
			pack := &storage.IndexedModpack{
				ID: "modrinth-test-project", Indexer: "modrinth", IndexerID: "test-project",
				Name: "Mixed Loader Pack", ModLoaders: `["forge","neoforge"]`,
			}
			if err := svc.store.UpsertIndexedModpack(ctx, pack); err != nil {
				t.Fatal(err)
			}
			if tt.indexed {
				err := svc.store.UpsertIndexedModpackFile(ctx, &storage.IndexedModpackFile{
					ID: "neo-id", ModpackID: pack.ID, ModLoader: "neoforge",
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			resp, err := svc.CreateServer(ctx, connect.NewRequest(&v1.CreateServerRequest{
				Name: "pinned-pack", McVersion: "1.21.1", DockerImage: "java21", Memory: 2048,
				NodeId: "test-node", Port: 25582, ModpackId: pack.ID, ModpackVersionId: tt.selector,
			}))
			if err != nil {
				t.Fatalf("CreateServer: %v", err)
			}
			server := resp.Msg.Server
			if server.ModLoader != v1.ModLoader_MOD_LOADER_MODRINTH || server.ModpackLoader != tt.wantLoader {
				t.Fatalf("loaders = %v/%v, want Modrinth/%v", server.ModLoader, server.ModpackLoader, tt.wantLoader)
			}
			cfg, err := svc.store.GetServerConfig(context.Background(), server.Id)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.ModrinthVersion == nil || *cfg.ModrinthVersion != tt.wantPin {
				t.Fatalf("stored Modrinth version = %v, want %q", cfg.ModrinthVersion, tt.wantPin)
			}
			if got := requests.Load(); got != tt.wantRequests {
				t.Fatalf("provider requests = %d, want %d", got, tt.wantRequests)
			}
		})
	}
}

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
