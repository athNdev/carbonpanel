// SPDX-License-Identifier: MPL-2.0

package serverarchive

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

var fixtureRuntime = Runtime{
	MinecraftVersion: "1.21.1",
	ModLoader:        "paper",
	MemoryMB:         4096,
	MaxPlayers:       20,
}

func TestNativeRoundTrip(t *testing.T) {
	ctx := context.Background()
	want := map[string]string{
		"world/level.dat":           "world metadata",
		"world/region/r.0.0.mca":    strings.Repeat("chunk\x00", 50_000),
		"mods/fixture.jar":          "mod contents",
		"plugins/fixture.jar":       "plugin contents",
		"config/paper-global.yml":   "fixture: retained\nproxies: {}\n",
		"config/fixture.toml":       "setting = true\n",
		"plugins/fixture/empty.txt": "",
		"server.properties": "motd=Portable world\r\nlevel-name=world\r\ndifficulty=hard\r\n" +
			"gamemode=creative\r\nsync-chunk-writes=true\r\npause-when-empty-seconds=0\r\n",
		"plugins/fixture/" + strings.Repeat("x", 105) + ".txt": "long name",
	}
	for _, format := range []Format{ZIP, TarGzip} {
		t.Run(string(format), func(t *testing.T) {
			source, destination := t.TempDir(), t.TempDir()
			for rel, content := range want {
				if rel == "config/paper-global.yml" {
					content = "fixture: retained\nproxies:\n  velocity:\n    enabled: true\n    secret: fixture-secret\n"
				}
				if rel == "server.properties" {
					content += "rcon.password=fixture-secret\r\nmanagement-server-secret=fixture-token\r\n"
				}
				put(t, source, rel, content)
			}
			put(t, source, ".env", "fixture-secret")
			put(t, source, "plugins/fixture/tls.pem", "fixture-secret")
			put(t, source, "world/session.lock", "lock")
			put(t, source, ".carbon-panel/undo/world/old.dat", "recovery data")
			original, err := nodestorage.NewLocal(source)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = original.Close() }()
			target, err := nodestorage.NewLocal(destination)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = target.Close() }()

			f, err := os.CreateTemp(t.TempDir(), "export")
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			if err := Write(ctx, original, f, format, fixtureRuntime, "fixture"); err != nil {
				t.Fatal(err)
			}
			verified, err := Inspect(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			if verified.Format != format || verified.Manifest.Runtime != fixtureRuntime || len(verified.Manifest.Files) != len(want) {
				t.Fatalf("unexpected archive report: %+v", verified)
			}
			wantGameplay := Gameplay{
				LevelName: "world", Difficulty: "hard", GameMode: "creative",
				SyncChunkWrites: boolPointer(true), PauseWhenEmptySeconds: intPointer(0),
			}
			if !verified.Gameplay.Equal(wantGameplay) {
				t.Fatalf("gameplay = %+v, want %+v", verified.Gameplay, wantGameplay)
			}
			if err := CopyTo(ctx, f, verified, target, "."); err != nil {
				t.Fatal(err)
			}
			for rel, content := range want {
				got, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(rel)))
				if err != nil || string(got) != content {
					t.Errorf("%s differs after restore (read error %v)", rel, err)
				}
			}
			for _, rel := range []string{".env", ".carbon-panel", "world/session.lock", "plugins/fixture/tls.pem"} {
				if _, err := os.Stat(filepath.Join(destination, filepath.FromSlash(rel))); !os.IsNotExist(err) {
					t.Errorf("%s should be excluded: %v", rel, err)
				}
			}
			originalProps, err := os.ReadFile(filepath.Join(source, "server.properties"))
			if err != nil || !bytes.Contains(originalProps, []byte("fixture-secret")) {
				t.Fatalf("source settings were modified: %v", err)
			}
		})
	}
}

func TestInspectRequiresMatchingManifest(t *testing.T) {
	const manifest = `{"schema_version":1,"exporter_version":"fixture","runtime":{"minecraft_version":"1.21.1","mod_loader":"paper","memory_mb":4096,"max_players":20},"files":[{"path":"world/level.dat","size":8,"sha256":"wrong"}]}`
	f, err := os.CreateTemp(t.TempDir(), "broken")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	w := zip.NewWriter(f)
	for _, pair := range [][2]string{{"data/world/level.dat", "testdata"}, {"manifest.json", manifest}} {
		entry, err := w.Create(pair[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.WriteString(entry, pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), f); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
}

func TestParseGameplaySettings(t *testing.T) {
	tests := []struct {
		name string
		data string
		want Gameplay
	}{
		{
			name: "valid nondefault values",
			data: "level-name=custom_world\ndifficulty=hard\ngamemode=creative\n" +
				"sync-chunk-writes=true\npause-when-empty-seconds=0\n",
			want: Gameplay{
				LevelName: "custom_world", Difficulty: "hard", GameMode: "creative",
				SyncChunkWrites: boolPointer(true), PauseWhenEmptySeconds: intPointer(0),
			},
		},
		{
			name: "absent and empty values keep defaults",
			data: "level-seed=\ndifficulty=\ngamemode=\nsync-chunk-writes=\npause-when-empty-seconds=\n",
			want: Gameplay{},
		},
		{
			name: "unsupported values are not persisted",
			data: "difficulty=nightmare\ngamemode=hard\nsync-chunk-writes=yes\npause-when-empty-seconds=-1\n",
			want: Gameplay{},
		},
		{
			name: "values are case insensitive and trimmed",
			data: "difficulty=HARD\ngamemode= Creative \nsync-chunk-writes=TRUE\npause-when-empty-seconds=120\n",
			want: Gameplay{
				Difficulty: "hard", GameMode: "creative",
				SyncChunkWrites: boolPointer(true), PauseWhenEmptySeconds: intPointer(120),
			},
		},
		{
			name: "last duplicate wins",
			data: "difficulty=hard\ndifficulty=peaceful\nsync-chunk-writes=true\nsync-chunk-writes=no\n",
			want: Gameplay{Difficulty: "peaceful"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := parseGameplay([]byte(test.data)); !got.Equal(test.want) {
				t.Fatalf("parseGameplay = %+v, want %+v", got, test.want)
			}
		})
	}
}

func boolPointer(value bool) *bool { return &value }

func intPointer(value int) *int { return &value }

func TestPortablePathIndex(t *testing.T) {
	for _, invalid := range []string{".", "", "/absolute", "a/../b", "a//b", "a\\b", "a./b", "a/NUL.txt", "a/COM9.txt"} {
		if portablePath(invalid) {
			t.Errorf("accepted nonportable path %q", invalid)
		}
	}
	for _, conflicting := range []string{"world/level.dat", "WORLD/chunk", "world", "world/level.dat/child"} {
		seen := pathIndex{}
		if err := seen.add("world/level.dat"); err != nil {
			t.Fatal(err)
		}
		if err := seen.add(conflicting); err == nil {
			t.Errorf("accepted conflicting path %q", conflicting)
		}
	}
}

func put(t *testing.T, root, rel, content string) {
	t.Helper()
	file := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
