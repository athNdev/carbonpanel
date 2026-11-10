// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package blueprint

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
)

func testStore(t *testing.T) *storage.Store {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{Path: ":memory:", AutoMigrate: true, MaxConnections: 1},
	}
	store, err := storage.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestSeedCountAndEnv(t *testing.T) {
	seeds := Seed()
	if len(seeds) != 6 {
		t.Fatalf("expected 6 seeds, got %d", len(seeds))
	}
	for _, s := range seeds {
		var env map[string]string
		if err := json.Unmarshal([]byte(s.DefaultEnv), &env); err != nil {
			t.Fatalf("seed %s has invalid DefaultEnv: %v", s.ID, err)
		}
		if env["EULA"] != "TRUE" {
			t.Fatalf("seed %s missing EULA=TRUE", s.ID)
		}
	}
}

func TestInitIdempotent(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	if err := InitBuiltinBlueprints(store); err != nil {
		t.Fatal(err)
	}
	if err := InitBuiltinBlueprints(store); err != nil {
		t.Fatalf("second init: %v", err)
	}
	list, err := store.ListBlueprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 6 {
		t.Fatalf("expected 6 blueprints, got %d", len(list))
	}
	bp, err := store.GetBlueprint(ctx, "builtin-paper")
	if err != nil {
		t.Fatal(err)
	}
	if string(bp.ModLoader) != "paper" {
		t.Fatalf("wrong loader: %s", bp.ModLoader)
	}
}

func TestBuiltinBlueprintImagesStayOnAllowedRepositories(t *testing.T) {
	for _, seed := range Seed() {
		if seed.DockerImage == "" {
			continue
		}
		stored, err := docker.CanonicalServerImage(seed.DockerImage)
		if err != nil {
			t.Fatalf("seed %s image %q: %v", seed.ID, seed.DockerImage, err)
		}
		resolved, err := docker.ResolveServerImage(stored)
		if err != nil {
			t.Fatalf("seed %s resolved image: %v", seed.ID, err)
		}
		if resolved != "itzg/minecraft-bedrock-server:latest" {
			t.Fatalf("seed %s resolves to %q", seed.ID, resolved)
		}
	}
}

func TestResolveSelectsImageEnvAndProtocol(t *testing.T) {
	qualified := 0
	for _, seed := range Seed() {
		spec, err := Resolve(seed)
		if err != nil {
			t.Fatalf("resolve %s: %v", seed.ID, err)
		}
		if spec.Env["EULA"] != "TRUE" {
			t.Fatalf("%s env EULA = %q", seed.ID, spec.Env["EULA"])
		}
		if spec.Image == "" || spec.StoredImage == "" {
			t.Fatalf("%s did not resolve an image", seed.ID)
		}
		if spec.Qualified {
			qualified++
			if spec.Protocol != ProtocolJava {
				t.Fatalf("%s protocol = %s, want java", seed.ID, spec.Protocol)
			}
			if spec.GamePort != 25565 {
				t.Fatalf("%s game port = %d, want 25565", seed.ID, spec.GamePort)
			}
			wantType := storage.NormalizeModLoaderForType(seed.ModLoader)
			if spec.Env["TYPE"] != wantType {
				t.Fatalf("%s TYPE = %q, want %q", seed.ID, spec.Env["TYPE"], wantType)
			}
			if !strings.HasPrefix(spec.Image, "itzg/minecraft-server:") {
				t.Fatalf("%s image %q is not a Java server tag", seed.ID, spec.Image)
			}
			if spec.Limitation != "" {
				t.Fatalf("%s qualified seed must not carry a limitation", seed.ID)
			}
			continue
		}
		if spec.Protocol != ProtocolBedrock {
			t.Fatalf("%s unqualified protocol = %s, want bedrock", seed.ID, spec.Protocol)
		}
		if spec.Limitation != NativeBedrockLimitation {
			t.Fatalf("%s limitation = %q", seed.ID, spec.Limitation)
		}
		if spec.Image != "itzg/minecraft-bedrock-server:latest" {
			t.Fatalf("%s image %q must not be advertised as a Java edition", seed.ID, spec.Image)
		}
	}
	if qualified != 5 {
		t.Fatalf("qualified Java seeds = %d, want 5", qualified)
	}
}
