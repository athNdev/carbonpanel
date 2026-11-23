// SPDX-License-Identifier: MPL-2.0

package worldterrain

import (
	"bytes"
	"errors"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldlib"
)

// dimensionRegionStore writes a region file under a dimension folder of a
// fixture world and returns the confined store over the data root.
func dimensionRegionStore(t *testing.T, dir, fileName string, data []byte) ReadStorage {
	t.Helper()
	root := t.TempDir()
	region := filepath.Join(root, "fixture_world", dir)
	if err := os.MkdirAll(region, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(region, fileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := nodestorage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// TestRenderDimensionTileNetherAndEnd qualifies the Nether and End spans at
// the tile seam: chunk layouts differ in yPos, heightmap bit width and
// section count, so each dimension must resolve its own geometry.
func TestRenderDimensionTileNetherAndEnd(t *testing.T) {
	nether := regionData(t, savedChunk{0, 0, chunkDataInSpan(0, 0, 3955, "full", worldlib.JavaNetherSpan), 2, testSave})
	store := dimensionRegionStore(t, Nether.Folder, "r.0.0.mca", nether)
	tile, err := RenderDimensionTile(t.Context(), store, "fixture_world", Nether, TileCoord{})
	if err != nil {
		t.Fatalf("nether tile did not render: %v", err)
	}
	if tile.RenderedChunks != 1 || tile.RegionModified == "" {
		t.Fatalf("unexpected nether tile: %+v", tile)
	}
	img, err := png.Decode(bytes.NewReader(tile.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, alpha := img.At(0, 0).RGBA(); alpha == 0 {
		t.Fatal("a nether column without bedrock must render")
	}

	end := regionData(t, savedChunk{0, 0, chunkDataInSpan(0, 0, worldlib.LastSupportedJavaDataVersion, "full", worldlib.JavaEndSpan), 2, testSave})
	store = dimensionRegionStore(t, End.Folder, "r.0.0.mca", end)
	tile, err = RenderDimensionTile(t.Context(), store, "fixture_world", End, TileCoord{})
	if err != nil {
		t.Fatalf("end tile did not render: %v", err)
	}
	if tile.RenderedChunks != 1 {
		t.Fatalf("unexpected end tile: %+v", tile)
	}
}

func TestRenderDimensionTileRefusals(t *testing.T) {
	// An Overworld chunk (yPos -4, 24 sections) saved in the Nether's region
	// folder must stay unsupported rather than misrender with wrong geometry.
	overworld := regionData(t, savedChunk{0, 0, chunkData(0, 0, 3955, "full"), 2, testSave})
	store := dimensionRegionStore(t, Nether.Folder, "r.0.0.mca", overworld)
	if _, err := RenderDimensionTile(t.Context(), store, "fixture_world", Nether, TileCoord{}); !errors.Is(err, ErrTerrainUnsupported) {
		t.Fatalf("overworld chunk in nether dimension: %v", err)
	}

	// Dimensions outside the verified vanilla set are refused outright.
	if _, err := RenderDimensionTile(t.Context(), store, "fixture_world", Dimension{ID: "custom", Folder: Nether.Folder, Span: worldlib.JavaNetherSpan}, TileCoord{}); !errors.Is(err, ErrTileLevel) {
		t.Fatalf("unverified dimension accepted: %v", err)
	}

	// A missing dimension region folder stays a missing-tile error.
	if _, err := RenderDimensionTile(t.Context(), store, "fixture_world", End, TileCoord{}); !errors.Is(err, ErrTerrainMissing) {
		t.Fatalf("missing end region: %v", err)
	}
}

// TestShadeGridNetherRoofStaysTransparent pins the Nether roof policy: the
// bedrock sheet at the top of the dimension must not paint the map opaque.
func TestShadeGridNetherRoofStaysTransparent(t *testing.T) {
	grid := make([]int16, TileBlocks*TileBlocks)
	blocks := make([]string, len(grid))
	for i := range grid {
		grid[i] = 120
		blocks[i] = "minecraft:bedrock"
	}
	var provisional [chunksPerTile * chunksPerTile]bool
	nether := shadeGrid(Nether, int16(Nether.Span.MinY-1), grid, blocks, make([]uint8, len(grid)), provisional)
	if _, _, _, alpha := nether.At(0, 0).RGBA(); alpha != 0 {
		t.Fatal("nether bedrock roof rendered opaque")
	}
	overworld := shadeGrid(Overworld, int16(Overworld.Span.MinY-1), grid, blocks, make([]uint8, len(grid)), provisional)
	if _, _, _, alpha := overworld.At(0, 0).RGBA(); alpha == 0 {
		t.Fatal("overworld bedrock column lost its surface")
	}
	end := shadeGrid(End, int16(End.Span.MinY-1), grid, blocks, make([]uint8, len(grid)), provisional)
	if _, _, _, alpha := end.At(0, 0).RGBA(); alpha == 0 {
		t.Fatal("end bedrock column lost its surface")
	}
}
