// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, you can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	"github.com/athNdev/carbon-panel/internal/worldterrain"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const (
	nativeSource               = "worldview.native.terrain"
	nativeWorkSlots            = 2
	nativeTimeout              = 5 * time.Second
	nativeLevelLimit           = 1 << 20
	nativeInventoryProbeLimit  = 12
	nativeInventoryProbeWindow = 250 * time.Millisecond
	// regionModifiedHeader carries the region file's opaque freshness
	// validator (size + modification time). The proxy strips standard
	// validators from renderer responses but this custom header passes
	// through the panel-owned native path, and live clients echo it back in
	// If-None-Match to skip re-rendering unchanged tiles.
	regionModifiedHeader = "X-CarbonPanel-Region-Modified"
)

var (
	errNativeProbeBusy     = errors.New("built-in map checks are busy; retry shortly")
	errNativeProbeDeferred = errors.New("open this world to check the built-in map format")
)

// nativeInventoryBudget bounds extra format checks across one inventory
// response. An unchecked row still links to its individual world status.
type nativeInventoryBudget struct {
	remaining int
	timeLeft  time.Duration
}

type nativeMapStore interface {
	nativeScopeStore
	GetServer(context.Context, string) (*storage.Server, error)
}

// NativeMap reads the saved Java Overworld using the node's confined storage
// accessor. It has no external endpoint, additional daemon, or module.
type NativeMap struct {
	store   nativeMapStore
	storage *nodestorage.Resolver
	slots   chan struct{}
}

func NewNativeMap(store nativeMapStore, resolver *nodestorage.Resolver) *NativeMap {
	if store == nil || resolver == nil {
		return nil
	}
	return &NativeMap{store: store, storage: resolver, slots: make(chan struct{}, nativeWorkSlots)}
}

type nativeSpawn struct {
	X int64 `json:"x"`
	Z int64 `json:"z"`
}

type nativeManifest struct {
	TileBlocks int          `json:"tileBlocks"`
	Dimension  string       `json:"dimension"`
	Spawn      *nativeSpawn `json:"spawn"`
	// Dimensions lists the saved vanilla dimensions this world offers tiles
	// for. It is additive JSON: clients that predate dimension support read
	// the overworld prefix exactly as before.
	Dimensions []string `json:"dimensions,omitempty"`
}

func (m *NativeMap) tryWork() bool {
	select {
	case m.slots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (m *NativeMap) inspect(ctx context.Context, server *storage.Server, ref *v1.WorldRef, budget *nativeInventoryBudget) (nativeScope, error) {
	scope, err := resolveNativeScope(ctx, m.store, server, ref)
	if err != nil {
		return nativeScope{}, err
	}
	started := time.Now()
	limit := nativeTimeout
	if budget != nil {
		if budget.remaining == 0 || budget.timeLeft <= 0 {
			return nativeScope{}, errNativeProbeDeferred
		}
		limit = min(limit, budget.timeLeft)
	}
	if !m.tryWork() {
		return nativeScope{}, errNativeProbeBusy
	}
	defer func() { <-m.slots }()
	if budget != nil {
		budget.remaining--
		defer func() { budget.timeLeft -= time.Since(started) }()
	}

	ctx, stop := context.WithDeadline(ctx, started.Add(limit))
	defer stop()
	acc, err := m.storage.ForNode(ctx, scope.node, scope.dataRoot)
	if err != nil {
		err = errNativeSourceAbsent
	} else {
		defer func() { _ = acc.Close() }()
		_, err = readNativeLevel(ctx, acc, scope.level)
	}
	if ctx.Err() != nil {
		if budget != nil && time.Since(started) >= limit {
			return nativeScope{}, errNativeProbeDeferred
		}
		return nativeScope{}, errNativeProbeBusy
	}
	if err != nil {
		return nativeScope{}, err
	}
	return scope, nil
}

// readNativeLevel validates the active level for qualification and every
// asset. Region chunks may retain their old format after level.dat upgrades.
func readNativeLevel(ctx context.Context, acc nodestorage.Accessor, level string) (map[string]any, error) {
	f, err := acc.OpenRead(ctx, path.Join(level, "level.dat"))
	if err != nil {
		return nil, errNativeSourceAbsent
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, nativeLevelLimit+1))
	if err != nil || len(raw) > nativeLevelLimit {
		return nil, errNativeSourceAbsent
	}
	facts, err := worldlib.LevelDataFacts(raw)
	if err != nil {
		return nil, worldterrain.ErrTerrainUnsupported
	}
	version, ok := worldlib.FactInt(facts, "DataVersion")
	if !ok || !worldterrain.SupportsDataVersion(version) {
		return nil, worldterrain.ErrTerrainUnsupported
	}
	return facts, ctx.Err()
}

func makeNativeManifest(facts map[string]any) nativeManifest {
	manifest := nativeManifest{TileBlocks: worldterrain.TileBlocks, Dimension: "overworld"}
	x, hasX := worldlib.FactInt(facts, "SpawnX")
	z, hasZ := worldlib.FactInt(facts, "SpawnZ")
	if !hasX || !hasZ {
		// Modern saves can store spawn as a dimension-tagged position.
		dimension, _ := worldlib.FactNested(facts, "spawn", "dimension")
		pos, _ := worldlib.FactNested(facts, "spawn", "pos")
		if location, ok := pos.([]int32); ok && len(location) == 3 && dimension == "minecraft:overworld" {
			x, z, hasX, hasZ = int64(location[0]), int64(location[2]), true, true
		}
	}
	if hasX && hasZ && x >= -30_000_000 && x < 30_000_000 && z >= -30_000_000 && z < 30_000_000 {
		manifest.Spawn = &nativeSpawn{X: x, Z: z}
	}
	return manifest
}

// serveAsset runs only after the existing map session and server ACL checks.
// The server's current binding, node generation and restore ID must match the lease.
func (m *NativeMap) serveAsset(w http.ResponseWriter, r *http.Request, server *storage.Server, issued nativeScope, asset string) {
	tileAsset, isTile := parseNativeTileAsset(asset)
	if !isTile && asset != nativeManifestPath {
		http.NotFound(w, r)
		return
	}

	ctx, stop := context.WithTimeout(r.Context(), nativeTimeout)
	defer stop()
	scope, err := resolveNativeScope(ctx, m.store, server, nil)
	if err != nil || scope != issued {
		http.Error(w, "world changed or is temporarily unavailable; reopen the map", http.StatusConflict)
		return
	}
	if !m.tryWork() {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "map rendering is busy; retry the tile", http.StatusTooManyRequests)
		return
	}
	defer func() { <-m.slots }()

	acc, err := m.storage.ForNode(ctx, scope.node, scope.dataRoot)
	if err != nil {
		http.Error(w, "saved terrain is unavailable", http.StatusServiceUnavailable)
		return
	}
	defer func() { _ = acc.Close() }()
	facts, err := readNativeLevel(ctx, acc, scope.level)
	if err != nil {
		writeNativeAssetError(w, err)
		return
	}

	if asset == nativeManifestPath {
		manifest := makeNativeManifest(facts)
		manifest.Dimensions = availableDimensions(acc, scope.level)
		if !m.currentScopeMatches(ctx, scope) {
			http.Error(w, "world changed; reopen the map", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(manifest)
		}
		return
	}

	// A live client revalidates a fully rendered tile by echoing the region
	// file's freshness validator. When the region was not rewritten since the
	// render, answer 304 before spending any render budget: the client keeps
	// its current image and re-asks on the next TTL.
	if validator := r.Header.Get("If-None-Match"); validator != "" {
		if current := regionFreshness(acc, tileAsset.dimension.RegionPath(scope.level, worldterrain.TileCoord{X: tileAsset.x, Z: tileAsset.z})); current != "" && current == validator {
			if !m.currentScopeMatches(ctx, scope) {
				http.Error(w, "world changed; reopen the map", http.StatusConflict)
				return
			}
			w.Header().Set(regionModifiedHeader, current)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	tile, err := worldterrain.RenderDimensionTile(ctx, acc, scope.level, tileAsset.dimension, worldterrain.TileCoord{X: tileAsset.x, Z: tileAsset.z})
	if err != nil {
		writeNativeAssetError(w, err)
		return
	}
	if !m.currentScopeMatches(ctx, scope) {
		http.Error(w, "world changed; reopen the map", http.StatusConflict)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(tile.PNG)))
	w.Header().Set("X-CarbonPanel-Rendered-Chunks", strconv.Itoa(tile.RenderedChunks))
	w.Header().Set("X-CarbonPanel-Provisional-Chunks", strconv.Itoa(tile.ProvisionalChunks))
	if tile.RegionModified != "" {
		w.Header().Set(regionModifiedHeader, tile.RegionModified)
	}
	if !tile.NewestSavedAt.IsZero() {
		w.Header().Set("X-CarbonPanel-Tile-Saved-At", tile.NewestSavedAt.UTC().Format(time.RFC3339))
	}
	if r.Method == http.MethodGet {
		_, _ = w.Write(tile.PNG)
	}
}

// regionFreshness returns the region file's freshness validator, or "" when
// the storage cannot report one.
func regionFreshness(acc nodestorage.Accessor, regionPath string) string {
	info, err := acc.Stat(regionPath)
	if err != nil {
		return ""
	}
	return worldterrain.RegionFreshnessValidator(info)
}

// availableDimensions reports the vanilla dimension folders that hold a
// region directory in the active level. The overworld is always listed
// (readNativeLevel already validated it); the Nether and End appear only
// when their saves exist. Bukkit-style sibling worlds and modded dimension
// folders are not listed: their layout is loader-specific and out of scope.
func availableDimensions(acc nodestorage.Accessor, level string) []string {
	dimensions := make([]string, 0, 3)
	for _, id := range worldterrain.Dimensions() {
		dimension, ok := worldterrain.DimensionByID(id)
		if !ok {
			continue
		}
		if id != worldterrain.Overworld.ID {
			if info, err := acc.Stat(path.Join(level, dimension.Folder)); err != nil || !info.IsDir() {
				continue
			}
		}
		dimensions = append(dimensions, id)
	}
	return dimensions
}

func writeNativeAssetError(w http.ResponseWriter, err error) {
	status, message := http.StatusServiceUnavailable, "saved terrain is temporarily unavailable"
	switch {
	case errors.Is(err, worldterrain.ErrTerrainMissing), errors.Is(err, worldterrain.ErrTileCoordinate):
		status, message = http.StatusNotFound, "no saved terrain at these coordinates"
	case errors.Is(err, worldterrain.ErrTerrainUnsupported):
		status, message = http.StatusUnprocessableEntity, "this saved terrain format is unsupported"
	case errors.Is(err, worldterrain.ErrRegionChanged):
		w.Header().Set("Retry-After", "1")
		message = "this region is being saved; retry the tile"
	}
	http.Error(w, message, status)
}

func (m *NativeMap) currentScopeMatches(ctx context.Context, scope nativeScope) bool {
	server, err := m.store.GetServer(ctx, scope.serverID)
	if err != nil {
		return false
	}
	current, err := resolveNativeScope(ctx, m.store, server, nil)
	return err == nil && current == scope
}
