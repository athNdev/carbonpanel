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
	"path"
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
	noSurface      = worldlib.JavaOverworldMinY - 1
)

var (
	ErrTileCoordinate     = errors.New("terrain tile coordinate is outside the Java world bounds")
	ErrTileLevel          = errors.New("terrain level must be a single relative world directory")
	ErrTerrainMissing     = errors.New("no saved terrain is available in this tile")
	ErrTerrainUnsupported = errors.New("this tile has no supported Java Overworld chunks")
	ErrTerrainUnreadable  = errors.New("this tile has no readable Java Overworld chunks")
	ErrRegionChanged      = errors.New("region changed while reading the Anvil tile")
	ErrTileBudget         = errors.New("tile exceeds the Anvil read or decode budget")
)

// ReadStorage supplies confined, read-only file access scoped to the owning
// server. The caller resolves the canonical world and checks its permissions.
type ReadStorage interface {
	OpenRead(context.Context, string) (storageworker.ReadHandle, error)
}

// TileCoord addresses a TileBlocks-square tile of the vanilla-height Overworld.
type TileCoord struct{ X, Z int }

// Tile contains a height-shaded PNG and its source coverage. Unavailable
// chunks and void columns are transparent. Save timestamps come from the
// region header and are zero when the rendered chunks have no timestamp.
type Tile struct {
	PNG                                                                []byte
	RenderedChunks, MissingChunks, UnsupportedChunks, UnreadableChunks int
	TimestampedChunks                                                  int
	OldestSavedAt, NewestSavedAt                                       time.Time
}

// SupportsDataVersion reports the Java save-version interval the core can read.
func SupportsDataVersion(version int64) bool {
	return worldlib.SupportsJavaOverworldDataVersion(version)
}

// RenderOverworldTile reads one vanilla-height Java region through the
// owning server's confined storage. It neither changes the save nor requests
// game assets. The caller owns concurrency, authorization and cache policy.
func RenderOverworldTile(ctx context.Context, store ReadStorage, level string, coord TileCoord) (Tile, error) {
	var tile Tile
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
	rx, rz := floorDiv(coord.X, tilesPerRegion), floorDiv(coord.Z, tilesPerRegion)
	name := fmt.Sprintf("r.%d.%d.mca", rx, rz)
	source, err := store.OpenRead(ctx, path.Join(level, "region", name))
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

	grid := make([]int16, TileBlocks*TileBlocks)
	for i := range grid {
		grid[i] = noSurface
	}
	startX := (coord.X - rx*tilesPerRegion) * chunksPerTile
	startZ := (coord.Z - rz*tilesPerRegion) * chunksPerTile
	var budget readBudget
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
			chunk, err := readSurface(ctx, source, entry, &budget)
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
				tile.recordSave(binary.BigEndian.Uint32(original[sectorBytes+index : sectorBytes+index+4]))
				for row := 0; row < chunkBlocks; row++ {
					start := (z*chunkBlocks+row)*TileBlocks + x*chunkBlocks
					copy(grid[start:start+chunkBlocks], chunk.Heights[row*chunkBlocks:(row+1)*chunkBlocks])
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
	if err := encoder.Encode(&out, shadeGrid(grid)); err != nil {
		return Tile{}, fmt.Errorf("encode terrain tile: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Tile{}, err
	}
	tile.PNG = out.Bytes()
	return tile, nil
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

// An original elevation palette. Colors describe height, independent of
// biome, surface block, resource pack or game textures.
var elevation = [...]struct {
	y       int
	r, g, b int
}{
	{worldlib.JavaOverworldMinY, 27, 57, 73},
	{16, 44, 91, 111},
	{63, 66, 125, 109},
	{96, 108, 148, 91},
	{170, 159, 153, 114},
	{240, 188, 174, 147},
	{worldlib.JavaOverworldMaxY, 239, 235, 221},
}

func shadeGrid(grid []int16) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, TileBlocks, TileBlocks))
	for z := 0; z < TileBlocks; z++ {
		for x := 0; x < TileBlocks; x++ {
			height := int(grid[z*TileBlocks+x])
			if height == noSurface {
				continue
			}
			l := neighborHeight(grid, x-1, z, height)
			r := neighborHeight(grid, x+1, z, height)
			n := neighborHeight(grid, x, z-1, height)
			s := neighborHeight(grid, x, z+1, height)
			shade := max(-38, min(38, (l-r)*4+(n-s)*3))
			img.SetNRGBA(x, z, terrainColor(height, shade))
		}
	}
	return img
}

func neighborHeight(grid []int16, x, z, fallback int) int {
	if x >= 0 && x < TileBlocks && z >= 0 && z < TileBlocks {
		v := int(grid[z*TileBlocks+x])
		if v != noSurface {
			return v
		}
	}
	return fallback
}

func terrainColor(y, shade int) color.NRGBA {
	hi := 1
	for y > elevation[hi].y {
		hi++
	}
	a, b := elevation[hi-1], elevation[hi]
	mix := func(left, right int) uint8 {
		blended := left + (right-left)*(y-a.y)/(b.y-a.y) + shade
		return uint8(max(0, min(255, blended)))
	}
	return color.NRGBA{R: mix(a.r, b.r), G: mix(a.g, b.g), B: mix(a.b, b.b), A: 255}
}
