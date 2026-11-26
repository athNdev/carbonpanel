// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package bluemapdiscovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/docker"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/hostmount"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

// fixtureWorld writes a realistic, non-trivial server data root. The world and
// database files are the persistent user data that an adapter install/remove
// must never modify. All content is deterministic so byte equality is exact.
func fixtureWorld(t *testing.T, root, level string) {
	t.Helper()
	files := map[string]string{
		level + "/level.dat":                                  "level-binary-sentinel",
		level + "/region/r.0.0.mca":                           "overworld-region-sentinel",
		level + "/DIM-1/region/r.0.0.mca":                     "nether-region-sentinel",
		level + "/DIM1/region/r.0.0.mca":                      "end-region-sentinel",
		level + "/dimensions/custom/space/region/r.0.0.mca":   "custom-dimension-sentinel",
		level + "/datapacks/my-pack.zip":                      "datapack-sentinel",
		"server.properties":                                   "level-name=" + level + "\n",
		"world_db/carbonpanel.db":                             "sqlite-sentinel",
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fixtureSnapshot hashes every file outside the renderer's own module tree so
// the assertion covers world data, datapacks and the panel database sentinel.
func fixtureSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(full string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, full)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "modules/bluemap/") {
			return nil // renderer-owned staging/config, intentionally mutable
		}
		raw, err := os.ReadFile(full)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(raw)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func assertSameSnapshot(t *testing.T, before, after map[string]string) {
	t.Helper()
	keys := make([]string, 0, len(before))
	for k := range before {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		got, ok := after[k]
		if !ok {
			t.Fatalf("persistent file disappeared during renderer lifecycle: %s", k)
		}
		if got != before[k] {
			t.Fatalf("persistent file was modified during renderer lifecycle: %s", k)
		}
	}
	if len(before) != len(after) {
		t.Fatalf("persistent file count changed: before=%d after=%d", len(before), len(after))
	}
}

func managedFactsFixture() ManagedWorldFacts {
	return ManagedWorldFacts{Level: "actual_world", Dimensions: []ManagedDimension{
		{MapID: "actual_world", WorldPath: "/app/world", Dimension: "minecraft:overworld"},
		{MapID: "actual_world_nether", WorldPath: "/app/world/DIM-1", Dimension: "minecraft:the_nether"},
	}}
}

// TestManagedRendererInstallPreservesWorldDataFixture exercises the real local
// node-owned accessor: world inspection, effective read-only volume derivation
// and staged config commit. No Docker daemon, renderer container or world write
// is involved; the world files are real bytes on a temp filesystem.
func TestManagedRendererInstallPreservesWorldDataFixture(t *testing.T) {
	dataRoot := t.TempDir()
	const level = "actual_world"
	fixtureWorld(t, dataRoot, level)
	before := fixtureSnapshot(t, dataRoot)
	server := &storage.Server{ID: "srv-a", DataPath: dataRoot}

	resolver := nodestorage.NewResolver(nil)
	facts, err := InspectManagedWorld(context.Background(), resolver, nodestorage.LocalNode(), server, level)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts.Dimensions) < 4 {
		t.Fatalf("expected overworld + 3 dimensions, got %d", len(facts.Dimensions))
	}

	rawVols, err := ManagedVolumes(server, level)
	if err != nil {
		t.Fatal(err)
	}
	var vols []docker.ModuleVolumeMount
	if err := json.Unmarshal([]byte(rawVols), &vols); err != nil {
		t.Fatal(err)
	}
	var worldReadOnly bool
	for _, v := range vols {
		if v.Target == "/app/world" {
			worldReadOnly = v.ReadOnly
			wantSource := strings.TrimRight(strings.ReplaceAll(hostmount.Translate(filepath.Join(dataRoot, level)), `\`, "/"), "/")
			if filepath.ToSlash(v.Source) != wantSource {
				t.Fatalf("world bind source = %q, want %q", v.Source, wantSource)
			}
		}
	}
	if !worldReadOnly {
		t.Fatal("renderer world bind must be read-only")
	}

	stager, err := StageManagedConfig(context.Background(), resolver, nodestorage.LocalNode(), server, facts)
	if err != nil {
		t.Fatal(err)
	}
	if err := stager.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = stager.Close()
	assertSameSnapshot(t, before, fixtureSnapshot(t, dataRoot))

	core := filepath.Join(dataRoot, "modules", "bluemap", "config", "core.conf")
	if raw, err := os.ReadFile(core); err != nil || !strings.Contains(string(raw), managedConfigMarker) {
		t.Fatalf("managed config was not committed: %q %v", raw, err)
	}
	t.Logf("MEASURED adapter-install fixture: %d persistent files byte-identical, world bind read-only, config committed", len(before))
}

// TestManagedRendererAbortAndRemovePreserveWorldDataFixture covers both an
// aborted install (staging rollback) and renderer config removal: in neither
// case may a world, datapack or database file change.
func TestManagedRendererAbortAndRemovePreserveWorldDataFixture(t *testing.T) {
	dataRoot := t.TempDir()
	const level = "actual_world"
	fixtureWorld(t, dataRoot, level)
	before := fixtureSnapshot(t, dataRoot)
	server := &storage.Server{ID: "srv-a", DataPath: dataRoot}
	resolver := nodestorage.NewResolver(nil)

	// Aborted install: injected commit failure must roll back staging only.
	ManagedConfigCommitHook = func() error { return errors.New("fixture commit failure") }
	defer func() { ManagedConfigCommitHook = nil }()
	aborted, err := StageManagedConfig(context.Background(), resolver, nodestorage.LocalNode(), server, managedFactsFixture())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = aborted.Close() }()
	if err := aborted.Commit(); err == nil {
		t.Fatal("injected commit failure was ignored")
	}
	ManagedConfigCommitHook = nil
	assertSameSnapshot(t, before, fixtureSnapshot(t, dataRoot))
	if _, err := os.Stat(filepath.Join(dataRoot, "modules", "bluemap", "config", "core.conf")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted install left a config file behind: %v", err)
	}

	// Successful install, then renderer removal deletes only renderer-owned
	// files. The world/data tree stays byte-identical.
	installed, err := StageManagedConfig(context.Background(), resolver, nodestorage.LocalNode(), server, managedFactsFixture())
	if err != nil {
		t.Fatal(err)
	}
	if err := installed.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = installed.Close()
	if err := os.RemoveAll(filepath.Join(dataRoot, "modules", "bluemap")); err != nil {
		t.Fatal(err)
	}
	assertSameSnapshot(t, before, fixtureSnapshot(t, dataRoot))
	t.Logf("MEASURED adapter abort/remove fixture: %d persistent files byte-identical before and after", len(before))
}

// TestRemoteNodeHasNoControllerFallbackFixture proves the node-local executor
// boundary: a remote node with no transport cannot open a controller-local data
// accessor, so a remote renderer install cannot fall back to controller paths.
// The RPC-level refusal (prepareBuiltinBlueMap) is covered in
// internal/rpc/services/module_bluemap_test.go ("remote node" case).
func TestRemoteNodeHasNoControllerFallbackFixture(t *testing.T) {
	localRoot := t.TempDir()
	fixtureWorld(t, localRoot, "actual_world")
	before := fixtureSnapshot(t, localRoot)
	remote := &storage.Server{ID: "srv-remote", NodeID: "node-2", DataPath: localRoot}

	_, err := nodestorage.NewResolver(nil).ForNode(
		context.Background(),
		nodestorage.NodeInfo{Name: remote.NodeID, IsLocal: false},
		remote.DataPath,
	)
	if err == nil || !nodestorage.IsUnavailable(err) {
		t.Fatalf("remote node must be refused without a node-local transport, got %v", err)
	}
	assertSameSnapshot(t, before, fixtureSnapshot(t, localRoot))
}
