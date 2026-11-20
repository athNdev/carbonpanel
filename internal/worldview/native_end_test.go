// SPDX-License-Identifier: MPL-2.0

package worldview

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/worldterrain"
)

func TestNativeEndSaveQualification(t *testing.T) {
	for _, test := range []struct {
		name      string
		version   int32
		packs     []any
		dimension string
		settings  string
		biome     string
		want      bool
	}{
		{"qualified Vanilla", 4554, []any{"vanilla"}, "minecraft:the_end", "minecraft:end", "minecraft:the_end", true},
		{"older save", 4553, []any{"vanilla"}, "minecraft:the_end", "minecraft:end", "minecraft:the_end", false},
		{"newer save", 4555, []any{"vanilla"}, "minecraft:the_end", "minecraft:end", "minecraft:the_end", false},
		{"custom pack", 4554, []any{"vanilla", "file/custom"}, "minecraft:the_end", "minecraft:end", "minecraft:the_end", false},
		{"custom dimension", 4554, []any{"vanilla"}, "fixture:custom", "minecraft:end", "minecraft:the_end", false},
		{"custom generator", 4554, []any{"vanilla"}, "minecraft:the_end", "fixture:custom", "minecraft:the_end", false},
		{"unknown packs", 4554, nil, "minecraft:the_end", "minecraft:end", "minecraft:the_end", false},
		{"fixed biome", 4554, []any{"vanilla"}, "minecraft:the_end", "minecraft:end", "minecraft:fixed", false},
		{"missing biome source", 4554, []any{"vanilla"}, "minecraft:the_end", "minecraft:end", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			generator := map[string]any{"type": "minecraft:noise", "settings": test.settings}
			if test.biome != "" {
				generator["biome_source"] = map[string]any{"type": test.biome}
			}
			facts := map[string]any{
				"DataVersion": test.version,
				"DataPacks":   map[string]any{"Enabled": test.packs},
				"WorldGenSettings": map[string]any{
					"dimensions": map[string]any{
						"minecraft:the_end": map[string]any{
							"type":      test.dimension,
							"generator": generator,
						},
					},
				},
			}
			if got := qualifiesNativeEnd(facts); got != test.want {
				t.Fatalf("qualifiesNativeEnd = %t, want %t", got, test.want)
			}
		})
	}
}

func TestNativeEndReadPinsRegularRegionFile(t *testing.T) {
	root := t.TempDir()
	regions, err := os.OpenRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = regions.Close() })
	source := &endSource{regions: regions, prefix: "level/DIM1/region/"}
	if _, err := source.OpenRead(context.Background(), "level/region/r.0.0.mca"); !errors.Is(err, errNativeWorldMismatch) {
		t.Fatalf("wrong dimension read: %v", err)
	}

	name := filepath.Join(root, "r.0.0.mca")
	if err := os.Mkdir(name, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := source.OpenRead(context.Background(), source.prefix+"r.0.0.mca"); !errors.Is(err, worldterrain.ErrTerrainUnsupported) {
		t.Fatalf("non-regular region file was accepted: %v", err)
	}
}

func TestNativeEndRendererPreferenceIsRechecked(t *testing.T) {
	server := &storage.Server{ID: "end-fixture", NodeID: "local"}
	resolver := &Resolver{}
	if err := checkNativeRenderer(context.Background(), resolver, server); err != nil {
		t.Fatalf("native should be available with no adapter: %v", err)
	}
	resolver.cfg.Endpoints = []Endpoint{{
		NodeID: "local", ServerID: server.ID, Kind: "bluemap", Host: "127.0.0.1", Port: 8100,
	}}
	if err := checkNativeRenderer(context.Background(), resolver, server); !errors.Is(err, errNativeWorldMismatch) {
		t.Fatalf("configured renderer must take precedence: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := checkNativeRenderer(ctx, resolver, server); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled selection must stay a cancellation: %v", err)
	}
}

type nativeEndTestStore struct {
	*nativeScopeFixture
	server *storage.Server
}

func (s *nativeEndTestStore) GetServer(context.Context, string) (*storage.Server, error) {
	return s.server, nil
}

func nativeEndLevelNBT() []byte {
	var b bytes.Buffer
	write := func(value any) { _ = binary.Write(&b, binary.BigEndian, value) }
	text := func(value string) {
		write(uint16(len(value)))
		b.WriteString(value)
	}
	tag := func(kind byte, name string) {
		b.WriteByte(kind)
		text(name)
	}
	stringTag := func(name, value string) {
		tag(8, name)
		text(value)
	}
	tag(10, "")
	tag(10, "Data")
	tag(3, "DataVersion")
	write(int32(4554))
	tag(10, "DataPacks")
	tag(9, "Enabled")
	b.WriteByte(8)
	write(int32(1))
	text("vanilla")
	b.WriteByte(0)
	tag(10, "WorldGenSettings")
	tag(10, "dimensions")
	tag(10, "minecraft:the_end")
	stringTag("type", "minecraft:the_end")
	tag(10, "generator")
	stringTag("type", "minecraft:noise")
	stringTag("settings", "minecraft:end")
	tag(10, "biome_source")
	stringTag("type", "minecraft:the_end")
	b.Write(make([]byte, 7))
	return b.Bytes()
}

func TestNativeEndPublicationFence(t *testing.T) {
	const level = "active_world"
	root := t.TempDir()
	regions := filepath.Join(root, level, "DIM1", "region")
	if err := os.MkdirAll(regions, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, level, "level.dat"), nativeEndLevelNBT(), 0o600); err != nil {
		t.Fatal(err)
	}
	tile := filepath.Join(regions, "r.0.0.mca")
	if err := os.WriteFile(tile, []byte("synthetic source"), 0o600); err != nil {
		t.Fatal(err)
	}

	fixture := &nativeScopeFixture{fakeStore: newFakeStore()}
	server := &storage.Server{ID: "end-fence", DataPath: root, NodeID: "local", ModLoader: storage.ModLoaderVanilla}
	fixture.setLevelName(server.ID, level)
	m := &NativeMap{store: &nativeEndTestStore{nativeScopeFixture: fixture, server: server}}
	ctx := context.Background()
	scope, err := resolveNativeScope(ctx, m.store, server, nil)
	if err != nil {
		t.Fatal(err)
	}
	source, err := openNativeEnd(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.regions.Close() }()
	handle, err := source.OpenRead(ctx, source.prefix+"r.0.0.mca")
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}

	resolver := &Resolver{}
	if err := m.recheckEnd(ctx, scope, resolver, source); err != nil {
		t.Fatalf("current source failed publication fence: %v", err)
	}

	fixture.restoreID = "completed-restore"
	if err := m.recheckEnd(ctx, scope, resolver, source); !errors.Is(err, errNativeWorldMismatch) {
		t.Fatalf("restore failed to invalidate the existing End observation: %v", err)
	}
	fixture.restoreID = ""

	resolver.cfg.Endpoints = []Endpoint{{NodeID: "local", ServerID: server.ID, Kind: "bluemap", Host: "127.0.0.1", Port: 8100}}
	if err := m.recheckEnd(ctx, scope, resolver, source); !errors.Is(err, errNativeWorldMismatch) {
		t.Fatalf("configured renderer did not invalidate the End observation: %v", err)
	}
	resolver.cfg.Endpoints = nil

	if err := os.Rename(tile, filepath.Join(regions, "saved-source")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tile, []byte("replacement tile"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.recheckEnd(ctx, scope, resolver, source); !errors.Is(err, worldterrain.ErrRegionChanged) {
		t.Fatalf("replaced region still qualified for publication: %v", err)
	}
}
