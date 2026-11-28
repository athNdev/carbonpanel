// SPDX-License-Identifier: MPL-2.0

// Package worldterrain renders bounded observations of saved Java terrain.
package worldterrain

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/athNdev/carbon-panel/internal/worldlib"
)

const (
	// TileBlocks is one tile edge at one pixel per block. X grows right and
	// Z grows down. Tile coordinates are zero-based on the world origin.
	TileBlocks     = 128
	chunkBlocks    = 16
	chunksPerTile  = TileBlocks / chunkBlocks
	tilesPerRegion = regionChunks / chunksPerTile
	worldLimit     = 30_000_000 / TileBlocks
)

var (
	ErrTileCoordinate     = errors.New("terrain tile coordinate is outside the Java world bounds")
	ErrTileLevel          = errors.New("terrain level must be a single relative world directory")
	ErrTerrainMissing     = errors.New("no saved terrain is available in this tile")
	ErrTerrainUnsupported = errors.New("this tile has no supported Java terrain chunks")
	ErrTerrainUnreadable  = errors.New("this tile has no readable Java terrain chunks")
	ErrRegionChanged      = errors.New("region changed while reading the Anvil tile")
	ErrTileBudget         = errors.New("tile exceeds the Anvil read or decode budget")
)

// ReadStorage supplies confined, read-only file access scoped to the owning
// server. The caller resolves the canonical world and checks its permissions.
type ReadStorage interface {
	OpenRead(context.Context, string) (storageworker.ReadHandle, error)
}

// regionStater is implemented by storage backends that can report file
// metadata. When the backend cannot, tiles simply carry no freshness
// validator and clients fall back to unconditional refetches.
type regionStater interface {
	Stat(rel string) (os.FileInfo, error)
}

// TileCoord addresses a TileBlocks-square tile of one saved dimension.
type TileCoord struct{ X, Z int }

// Tile contains a top-down PNG and its source coverage. Unavailable
// chunks and void columns are transparent. Save timestamps come from the
// region header and are zero when the rendered chunks have no timestamp.
// RegionModified is the opaque region-file freshness validator at render
// time; it is empty when the storage cannot report one.
type Tile struct {
	PNG                                                                []byte
	RenderedChunks, MissingChunks, UnsupportedChunks, UnreadableChunks int
	ProvisionalChunks                                                  int
	TimestampedChunks                                                  int
	OldestSavedAt, NewestSavedAt                                       time.Time
	RegionModified                                                     string
}

// SupportsDataVersion reports the Java save-version interval the core can read.
func SupportsDataVersion(version int64) bool {
	return worldlib.SupportsJavaOverworldDataVersion(version)
}

// RenderOverworldTile reads one vanilla-height Java Overworld region through
// the owning server's confined storage. It neither changes the save nor
// requests game assets. The caller owns concurrency, authorization and cache
// policy.
func RenderOverworldTile(ctx context.Context, store ReadStorage, level string, coord TileCoord) (Tile, error) {
	return RenderDimensionTile(ctx, store, level, Overworld, coord)
}

// RenderDimensionTile reads one region of the named saved dimension through
// the owning server's confined storage. Dimensions outside the vanilla set
// are refused; their saves would need game-generated fixtures before they
// can be trusted.
func RenderDimensionTile(ctx context.Context, store ReadStorage, level string, dimension Dimension, coord TileCoord) (Tile, error) {
	var tile Tile
	if !dimension.valid() {
		return tile, ErrTileLevel
	}
	if err := ctx.Err(); err != nil {
		return tile, err
	}
	if coord.X < -worldLimit || coord.X >= worldLimit || coord.Z < -worldLimit || coord.Z >= worldLimit {
		return tile, ErrTileCoordinate
	}
	if !fs.ValidPath(level) || level == "." || strings.ContainsAny(level, "/\\:\x00") {
		return tile, ErrTileLevel
	}
	if store == nil {
		return tile, errors.New("world terrain storage is unavailable")
	}
	regionPath := dimension.RegionPath(level, coord)
	// The freshness validator is captured BEFORE the read: any later region
	// rewrite then advances past it, so a conditional revalidation can never
	// answer 304 for content older than the validator. (Capturing after the
	// render could hide a save that landed between render and Stat.)
	if stater, ok := store.(regionStater); ok {
		if info, statErr := stater.Stat(regionPath); statErr == nil && info != nil {
			tile.RegionModified = RegionFreshnessValidator(info)
		}
	}
	source, err := store.OpenRead(ctx, regionPath)
	if errors.Is(err, fs.ErrNotExist) {
		return tile, ErrTerrainMissing
	}
	if err != nil {
		return tile, fmt.Errorf("open Anvil region: %w", err)
	}
	defer func() { _ = source.Close() }()

	var original [regionHeaderBytes]byte
	if err := readAt(source, original[:], 0); err != nil {
		if ctx.Err() != nil {
			return tile, ctx.Err()
		}
		return tile, fmt.Errorf("%w: read Anvil header: %w", ErrTerrainUnreadable, err)
	}

	noSurface := int16(dimension.Span.MinY - 1)
	grid := make([]int16, TileBlocks*TileBlocks)
	blocks := make([]string, len(grid))
	waterDepths := make([]uint8, len(grid))
	for i := range grid {
		grid[i] = noSurface
	}
	rx, rz := floorDiv(coord.X, tilesPerRegion), floorDiv(coord.Z, tilesPerRegion)
	startX := (coord.X - rx*tilesPerRegion) * chunksPerTile
	startZ := (coord.Z - rz*tilesPerRegion) * chunksPerTile
	var budget readBudget
	var provisional [chunksPerTile * chunksPerTile]bool
	for z := 0; z < chunksPerTile; z++ {
		for x := 0; x < chunksPerTile; x++ {
			if err := ctx.Err(); err != nil {
				return Tile{}, err
			}
			index := ((startZ+z)*regionChunks + startX + x) * 4
			entry := original[index : index+4]
			if binary.BigEndian.Uint32(entry) == 0 {
				tile.MissingChunks++
				continue
			}
			chunk, err := readSurfaceInSpan(ctx, source, entry, &budget, dimension.Span, dimension.SurfaceUnderRoof)
			if ctx.Err() != nil {
				return Tile{}, ctx.Err()
			}
			switch {
			case errors.Is(err, ErrTileBudget):
				return Tile{}, err
			case errors.Is(err, worldlib.ErrJavaChunkUnsupported):
				tile.UnsupportedChunks++
			case errors.Is(err, worldlib.ErrJavaChunkUnfinished):
				tile.MissingChunks++
			case err != nil || int(chunk.X) != coord.X*chunksPerTile+x || int(chunk.Z) != coord.Z*chunksPerTile+z:
				tile.UnreadableChunks++
			default:
				tile.RenderedChunks++
				if chunk.Provisional {
					tile.ProvisionalChunks++
					provisional[z*chunksPerTile+x] = true
				}
				tile.recordSave(binary.BigEndian.Uint32(original[sectorBytes+index : sectorBytes+index+4]))
				for row := 0; row < chunkBlocks; row++ {
					start := (z*chunkBlocks+row)*TileBlocks + x*chunkBlocks
					first, last := row*chunkBlocks, (row+1)*chunkBlocks
					copy(grid[start:start+chunkBlocks], chunk.Heights[first:last])
					copy(blocks[start:start+chunkBlocks], chunk.Blocks[first:last])
					copy(waterDepths[start:start+chunkBlocks], chunk.WaterDepths[first:last])
				}
			}
		}
	}

	var current [regionHeaderBytes]byte
	err = readAt(source, current[:], 0)
	if ctx.Err() != nil {
		return Tile{}, ctx.Err()
	}
	if err != nil || !sameTileEntries(original[:], current[:], startX, startZ) {
		return Tile{}, ErrRegionChanged
	}
	if tile.RenderedChunks == 0 {
		switch {
		case tile.UnreadableChunks > 0:
			return tile, ErrTerrainUnreadable
		case tile.UnsupportedChunks > 0:
			return tile, ErrTerrainUnsupported
		default:
			return tile, ErrTerrainMissing
		}
	}

	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := encoder.Encode(&out, shadeGrid(dimension, noSurface, grid, blocks, waterDepths, provisional)); err != nil {
		return Tile{}, fmt.Errorf("encode terrain tile: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Tile{}, err
	}
	tile.PNG = out.Bytes()
	return tile, nil
}

// RegionFreshnessValidator returns the opaque freshness validator for a
// region file: its size and modification time. Tile responses expose it and
// conditional tile requests echo it back; an unchanged validator means the
// region file was not rewritten since the render, so the tile is still
// current.
func RegionFreshnessValidator(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	modified := info.ModTime()
	if modified.IsZero() {
		return ""
	}
	return `"` + strconv.FormatInt(info.Size(), 10) + "-" +
		strconv.FormatInt(modified.UnixNano(), 10) + `"`
}

func sameTileEntries(before, after []byte, startX, startZ int) bool {
	for z := 0; z < chunksPerTile; z++ {
		first := ((startZ+z)*regionChunks + startX) * 4
		last := first + chunksPerTile*4
		if !bytes.Equal(before[first:last], after[first:last]) ||
			!bytes.Equal(before[sectorBytes+first:sectorBytes+last], after[sectorBytes+first:sectorBytes+last]) {
			return false
		}
	}
	return true
}

func floorDiv(n, divisor int) int {
	quotient := n / divisor
	if n%divisor < 0 {
		quotient--
	}
	return quotient
}

func (t *Tile) recordSave(seconds uint32) {
	if seconds == 0 {
		return
	}
	at := time.Unix(int64(seconds), 0).UTC()
	t.TimestampedChunks++
	if t.OldestSavedAt.IsZero() || at.Before(t.OldestSavedAt) {
		t.OldestSavedAt = at
	}
	if at.After(t.NewestSavedAt) {
		t.NewestSavedAt = at
	}
}

// Elevation gradients keep chunks visible when block data is unavailable.
// Each table spans its dimension's Y range with hues that read at one pixel
// per block.
type elevationStop struct {
	y       int
	r, g, b int
}

var (
	overworldElevation = [...]elevationStop{
		{worldlib.JavaOverworldMinY, 27, 57, 73},
		{16, 44, 91, 111},
		{63, 66, 125, 109},
		{96, 108, 148, 91},
		{170, 159, 153, 114},
		{240, 188, 174, 147},
		{worldlib.JavaOverworldMaxY, 239, 235, 221},
	}
	netherElevation = [...]elevationStop{
		{0, 38, 16, 18},
		{16, 79, 30, 29},
		{32, 97, 37, 33},
		{64, 121, 53, 44},
		{96, 148, 82, 62},
		{127, 176, 120, 92},
	}
	endElevation = [...]elevationStop{
		{0, 26, 26, 36},
		{32, 72, 70, 92},
		{64, 118, 116, 128},
		{128, 168, 166, 152},
		{192, 196, 196, 172},
		{255, 222, 222, 192},
	}
)

func elevationFor(dimension Dimension) []elevationStop {
	switch dimension {
	case Nether:
		return netherElevation[:]
	case End:
		return endElevation[:]
	default:
		return overworldElevation[:]
	}
}

func shadeGrid(dimension Dimension, noSurface int16, grid []int16, blocks []string, waterDepths []uint8, provisional [chunksPerTile * chunksPerTile]bool) *image.NRGBA {
	gradient := elevationFor(dimension)
	img := image.NewNRGBA(image.Rect(0, 0, TileBlocks, TileBlocks))
	for z := 0; z < TileBlocks; z++ {
		for x := 0; x < TileBlocks; x++ {
			index := z*TileBlocks + x
			height := int(grid[index])
			if height == int(noSurface) {
				continue
			}
			l := neighborHeight(grid, x-1, z, height, noSurface)
			r := neighborHeight(grid, x+1, z, height, noSurface)
			n := neighborHeight(grid, x, z-1, height, noSurface)
			s := neighborHeight(grid, x, z+1, height, noSurface)
			shade := max(-26, min(26, (l-r)*3+(n-s)*2))
			pixel := surfaceColor(blocks[index], int(waterDepths[index]), height, shade, gradient)
			if provisional[(z/chunkBlocks)*chunksPerTile+x/chunkBlocks] {
				pixel.A = 176
			}
			img.SetNRGBA(x, z, pixel)
		}
	}
	return img
}

// The palette is authored for Carbon Panel. Block states provide the material
// categories, while the heightmap gives them relief and keeps unknown blocks visible.
func surfaceColor(block string, waterDepth, height, shade int, gradient []elevationStop) color.NRGBA {
	const vanilla = "minecraft:"
	name, vanillaBlock := strings.CutPrefix(block, vanilla)
	if !vanillaBlock {
		return terrainColor(height, shade, gradient)
	}
	var base [3]int
	switch {
	case waterDepth > 0:
		depth := max(3, min(10, waterDepth))
		base = [3]int{60 - depth*3, 155 - depth*6, 182 - depth*4}
		shade /= 5
	case name == "lava":
		base = [3]int{237, 106, 38}
	case name == "snow" || name == "snow_block" || name == "powder_snow":
		base = [3]int{231, 238, 234}
	case name == "ice" || name == "packed_ice" || name == "blue_ice":
		base = [3]int{151, 201, 221}
	case name == "sand" || name == "sandstone":
		base = [3]int{220, 198, 144}
	case name == "red_sand" || name == "red_sandstone" || name == "terracotta":
		base = [3]int{183, 111, 66}
	case name == "gravel" || name == "clay":
		base = [3]int{146, 139, 128}
	case name == "stone" || name == "cobblestone" || name == "andesite" || name == "tuff" || name == "stone_bricks":
		base = [3]int{129, 132, 124}
	case name == "diorite" || name == "calcite":
		base = [3]int{187, 186, 174}
	case name == "deepslate" || name == "cobbled_deepslate":
		base = [3]int{87, 88, 90}
	case name == "granite" || name == "bricks":
		base = [3]int{148, 103, 84}
	case name == "dirt" || name == "coarse_dirt" || name == "rooted_dirt" || name == "podzol" || name == "farmland":
		base = [3]int{122, 88, 60}
	case name == "dirt_path" || strings.HasSuffix(name, "_planks"):
		base = [3]int{162, 131, 85}
	case strings.HasSuffix(name, "_log") || strings.HasSuffix(name, "_wood"):
		base = [3]int{113, 87, 54}
	case name == "cherry_leaves" || name == "pink_petals":
		base = [3]int{211, 149, 172}
	case name == "spruce_leaves":
		base = [3]int{53, 90, 69}
		shade /= 2
	case name == "birch_leaves":
		base = [3]int{105, 142, 66}
		shade /= 2
	case strings.HasSuffix(name, "_leaves") || name == "vine":
		base = [3]int{69, 111, 52}
		shade /= 2
	case name == "grass_block" || name == "short_grass" || name == "grass" || name == "tall_grass" ||
		name == "fern" || name == "large_fern" || name == "moss_block" || name == "moss_carpet":
		base = [3]int{105, 151, 77}
	default:
		return terrainColor(height, shade, gradient)
	}
	channel := func(v int) uint8 { return uint8(max(0, min(255, v+shade))) }
	return color.NRGBA{R: channel(base[0]), G: channel(base[1]), B: channel(base[2]), A: 255}
}

func neighborHeight(grid []int16, x, z, fallback int, noSurface int16) int {
	if x >= 0 && x < TileBlocks && z >= 0 && z < TileBlocks {
		v := grid[z*TileBlocks+x]
		if v != noSurface {
			return int(v)
		}
	}
	return fallback
}

func terrainColor(y, shade int, gradient []elevationStop) color.NRGBA {
	hi := 1
	for hi < len(gradient)-1 && y > gradient[hi].y {
		hi++
	}
	a, b := gradient[hi-1], gradient[hi]
	mix := func(left, right int) uint8 {
		blended := left + (right-left)*(y-a.y)/(b.y-a.y) + shade
		return uint8(max(0, min(255, blended)))
	}
	return color.NRGBA{R: mix(a.r, b.r), G: mix(a.g, b.g), B: mix(a.b, b.b), A: 255}
}
