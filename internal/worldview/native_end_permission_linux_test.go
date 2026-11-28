// SPDX-License-Identifier: MPL-2.0

package worldview

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

func TestNativeManifestSurvivesUnreadableEndStorage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("directory permission test requires an unprivileged user")
	}

	ctx := context.Background()
	const level = "fixture-world"
	root := t.TempDir()
	dimension := filepath.Join(root, level, "DIM1")
	if err := os.MkdirAll(filepath.Join(dimension, "region"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, level, "level.dat"), nativeEndLevelNBT(), 0o600); err != nil {
		t.Fatal(err)
	}

	fixture := &nativeScopeFixture{fakeStore: newFakeStore()}
	server := &storage.Server{
		ID: "end-permission-test", DataPath: root, NodeID: "local", ModLoader: storage.ModLoaderVanilla,
	}
	fixture.setLevelName(server.ID, level)
	m := NewNativeMap(&nativeEndTestStore{nativeScopeFixture: fixture, server: server}, nodestorage.NewResolver(nil))
	scope, err := resolveNativeScope(ctx, m.store, server, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolver := &Resolver{}
	request := func(asset string) *httptest.ResponseRecorder {
		result := httptest.NewRecorder()
		m.serveAsset(result, httptest.NewRequest(http.MethodGet, "/"+asset, nil), server, scope, asset, resolver)
		return result
	}
	manifest := func() nativeManifest {
		t.Helper()
		result := request(nativeManifestPath)
		if result.Code != http.StatusOK {
			t.Fatalf("manifest status = %d: %s", result.Code, result.Body)
		}
		var decoded nativeManifest
		if err := json.Unmarshal(result.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	healthy := manifest()
	if !slices.Equal(healthy.Dimensions, []nativeDimension{nativeOverworld, nativeEnd}) {
		t.Fatalf("healthy fixture did not qualify both dimensions: %+v", healthy)
	}
	pinned, err := openNativeEnd(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pinned.regions.Close() })
	if err := os.Chmod(dimension, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dimension, 0o700) })
	if _, err := os.Lstat(filepath.Join(dimension, "region")); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("fixture did not restrict End storage: %v", err)
	}

	degraded := manifest()
	if degraded.Dimension != healthy.Dimension || degraded.TileBlocks != healthy.TileBlocks || degraded.Spawn != nil || degraded.Dimensions != nil {
		t.Fatalf("unreadable End storage must retain the default manifest: %+v", degraded)
	}
	if err := m.recheckEnd(ctx, scope, resolver, pinned); !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("unreadable End in the same world lost its optional error: %v", err)
	}
	if tile := request("native/end/0/0.png"); tile.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable End tile status = %d: %s", tile.Code, tile.Body)
	}
	fixture.restoreID = "restored-world"
	if result := request(nativeManifestPath); result.Code != http.StatusConflict {
		t.Fatalf("changed world status = %d: %s", result.Code, result.Body)
	}
	fixture.restoreID = ""
	resolver.cfg.Endpoints = []Endpoint{{
		NodeID: "local", ServerID: server.ID, Kind: "bluemap", Host: "127.0.0.1", Port: 8100,
	}}
	if result := request(nativeManifestPath); result.Code != http.StatusConflict {
		t.Fatalf("changed renderer status = %d: %s", result.Code, result.Body)
	}
	resolver.cfg.Endpoints = nil

	if err := os.Chmod(dimension, 0o700); err != nil {
		t.Fatal(err)
	}
	levelPath := filepath.Join(root, level)
	if err := os.Rename(levelPath, filepath.Join(root, "previous-world")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(levelPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(levelPath, "level.dat"), nativeEndLevelNBT(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.recheckEnd(ctx, scope, resolver, pinned); !errors.Is(err, errNativeWorldMismatch) {
		t.Fatalf("replacement world with no End must invalidate the pinned source: %v", err)
	}
}
