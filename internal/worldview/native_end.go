// SPDX-License-Identifier: MPL-2.0

package worldview

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	"github.com/athNdev/carbon-panel/internal/worldterrain"
)

// nativeEndUnavailable describes an optional End failure after the active
// level was opened. The level identity still participates in revalidation.
type nativeEndUnavailable struct {
	levelID fs.FileInfo
	cause   error
}

func (e *nativeEndUnavailable) Error() string { return "optional End terrain is unavailable" }
func (e *nativeEndUnavailable) Unwrap() error { return e.cause }

func qualifiesNativeEnd(facts map[string]any) bool {
	version, ok := worldlib.FactInt(facts, "DataVersion")
	if !ok || !worldlib.SupportsJavaEndDataVersion(version) {
		return false
	}
	enabled, _ := worldlib.FactNested(facts, "DataPacks", "Enabled")
	packs, ok := enabled.([]any)
	if !ok || len(packs) != 1 || packs[0] != "vanilla" {
		return false
	}
	end, _ := worldlib.FactNested(facts, "WorldGenSettings", "dimensions", "minecraft:the_end")
	dimension, ok := end.(map[string]any)
	if !ok {
		return false
	}
	generator, _ := dimension["generator"].(map[string]any)
	biomeType, _ := worldlib.FactNested(generator, "biome_source", "type")
	return dimension["type"] == "minecraft:the_end" &&
		generator["type"] == "minecraft:noise" && generator["settings"] == "minecraft:end" &&
		biomeType == "minecraft:the_end"
}

// endSource pins the saved dimension under the active local level. Its read
// interface exposes only region files, with no sibling or symlink lookup.
type endSource struct {
	regions    *os.Root
	levelID    fs.FileInfo
	regionsID  fs.FileInfo
	prefix     string
	tileFile   string
	tileFileID fs.FileInfo
}

func openNativeEnd(ctx context.Context, scope nativeScope) (*endSource, error) {
	if !scope.node.IsLocal || scope.loader != storage.ModLoaderVanilla {
		return nil, worldterrain.ErrTerrainUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	parent, err := os.OpenRoot(filepath.Dir(scope.dataRoot))
	if err != nil {
		return nil, err
	}
	defer func() { _ = parent.Close() }()
	data, _, err := openNativeDirectory(parent, filepath.Base(scope.dataRoot))
	if err != nil {
		return nil, err
	}
	defer func() { _ = data.Close() }()
	level, levelID, err := openNativeDirectory(data, scope.level)
	if err != nil {
		return nil, err
	}
	defer func() { _ = level.Close() }()

	metadata, _, err := openNativeFile(level, "level.dat")
	if err != nil {
		return nil, err
	}
	facts, err := decodeNativeLevel(ctx, metadata)
	_ = metadata.Close()
	if err != nil {
		return nil, err
	}
	if !qualifiesNativeEnd(facts) {
		return nil, &nativeEndUnavailable{levelID: levelID, cause: worldterrain.ErrTerrainUnsupported}
	}
	regions, regionsID, err := openNativeEndRegions(level)
	if err != nil {
		if errors.Is(err, errNativeWorldMismatch) {
			return nil, err
		}
		return nil, &nativeEndUnavailable{levelID: levelID, cause: err}
	}
	return &endSource{
		regions: regions, levelID: levelID, regionsID: regionsID,
		prefix: path.Join(scope.level, "DIM1", "region") + "/",
	}, nil
}

func openNativeEndRegions(level *os.Root) (*os.Root, fs.FileInfo, error) {
	dimension, _, err := openNativeDirectory(level, "DIM1")
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = dimension.Close() }()
	return openNativeDirectory(dimension, "region")
}

func openNativeDirectory(parent *os.Root, name string) (*os.Root, fs.FileInfo, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.IsDir() {
		return nil, nil, worldterrain.ErrTerrainUnsupported
	}
	dir, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := dir.Stat(".")
	if err != nil {
		_ = dir.Close()
		return nil, nil, err
	}
	if !os.SameFile(info, opened) {
		_ = dir.Close()
		return nil, nil, errNativeWorldMismatch
	}
	return dir, opened, nil
}

func openNativeFile(root *os.Root, name string) (*os.File, fs.FileInfo, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, worldterrain.ErrTerrainUnsupported
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		return nil, nil, errNativeWorldMismatch
	}
	return file, opened, nil
}

func (s *endSource) OpenRead(ctx context.Context, relative string) (storageworker.ReadHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, ok := strings.CutPrefix(relative, s.prefix)
	if !ok || !strings.HasPrefix(name, "r.") || !strings.HasSuffix(name, ".mca") || name != path.Base(name) || strings.ContainsAny(name, "\\\\:") {
		return nil, errNativeWorldMismatch
	}
	file, info, err := openNativeFile(s.regions, name)
	if err != nil {
		return nil, err
	}
	s.tileFile, s.tileFileID = name, info
	return file, nil
}

func checkNativeRenderer(ctx context.Context, resolver *Resolver, server *storage.Server) error {
	nodeID := canonicalNodeID(server.NodeID)
	resolved, resolveErr := resolver.Resolve(ctx, nodeID, server.ID)
	if err := ctx.Err(); err != nil {
		return err
	}
	if resolved != nil || !resolver.allowsNativeFallback(nodeID, server.ID, resolveErr) {
		return errNativeWorldMismatch
	}
	return nil
}

func (m *NativeMap) recheckEnd(ctx context.Context, scope nativeScope, resolver *Resolver, source *endSource) error {
	fresh, err := openNativeEnd(ctx, scope)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var unavailable *nativeEndUnavailable
		if errors.As(err, &unavailable) && os.SameFile(source.levelID, unavailable.levelID) {
			return err
		}
		return errNativeWorldMismatch
	}
	defer func() { _ = fresh.regions.Close() }()
	if !os.SameFile(source.levelID, fresh.levelID) || !os.SameFile(source.regionsID, fresh.regionsID) {
		return errNativeWorldMismatch
	}
	if source.tileFileID != nil {
		info, err := fresh.regions.Lstat(source.tileFile)
		if err != nil || !os.SameFile(source.tileFileID, info) ||
			info.Size() != source.tileFileID.Size() || !info.ModTime().Equal(source.tileFileID.ModTime()) {
			return worldterrain.ErrRegionChanged
		}
	}
	return m.recheckNativeRenderer(ctx, scope, resolver)
}

func (m *NativeMap) recheckNativeRenderer(ctx context.Context, scope nativeScope, resolver *Resolver) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	current := m.currentNativeServer(ctx, scope)
	if current == nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errNativeWorldMismatch
	}
	return checkNativeRenderer(ctx, resolver, current)
}

func (m *NativeMap) endDimensions(ctx context.Context, server *storage.Server, scope nativeScope, resolver *Resolver) ([]nativeDimension, error) {
	if err := checkNativeRenderer(ctx, resolver, server); err != nil {
		return nil, err
	}
	source, err := openNativeEnd(ctx, scope)
	if err == nil {
		defer func() { _ = source.regions.Close() }()
		err = m.recheckEnd(ctx, scope, resolver, source)
	}
	var unavailable *nativeEndUnavailable
	if errors.As(err, &unavailable) {
		// Only the optional dimension may be dropped. The shared manifest
		// still needs the current world and native renderer to agree.
		return nil, m.recheckNativeRenderer(ctx, scope, resolver)
	}
	if err != nil {
		return nil, err
	}
	return []nativeDimension{nativeOverworld, nativeEnd}, nil
}

func (m *NativeMap) renderEndTile(ctx context.Context, server *storage.Server, scope nativeScope, resolver *Resolver, asset nativeTileAsset) (worldterrain.Tile, error) {
	if scope.loader != storage.ModLoaderVanilla {
		return worldterrain.Tile{}, worldterrain.ErrTerrainUnsupported
	}
	if err := checkNativeRenderer(ctx, resolver, server); err != nil {
		return worldterrain.Tile{}, err
	}
	source, err := openNativeEnd(ctx, scope)
	if errors.Is(err, fs.ErrNotExist) {
		return worldterrain.Tile{}, worldterrain.ErrTerrainMissing
	}
	if err != nil {
		return worldterrain.Tile{}, err
	}
	defer func() { _ = source.regions.Close() }()
	tile, err := worldterrain.RenderVanillaEndTile(ctx, source, scope.level, worldterrain.TileCoord{X: asset.x, Z: asset.z})
	if err != nil {
		return worldterrain.Tile{}, err
	}
	if err := m.recheckEnd(ctx, scope, resolver, source); err != nil {
		var unavailable *nativeEndUnavailable
		if errors.As(err, &unavailable) {
			err = errNativeWorldMismatch
		}
		return worldterrain.Tile{}, err
	}
	return tile, nil
}
