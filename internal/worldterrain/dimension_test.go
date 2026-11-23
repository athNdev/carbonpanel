// SPDX-License-Identifier: MPL-2.0

package worldterrain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image/color"
	"image/png"
	"math/bits"
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

// netherRoofChunk builds one full Nether chunk whose WORLD_SURFACE is the
// vanilla bedrock roof: a complete bedrock sheet at y=127 over alternating
// partial layers at y=123-126, air below, and — with terrain — netherrack
// at y=64-70. Palette index 0 is air, so unset data nibbles stay air.
func netherRoofChunk(x, z, version int32, terrain bool) []byte {
	var b bytes.Buffer
	write := func(v any) { _ = binary.Write(&b, binary.BigEndian, v) }
	str := func(s string) {
		write(uint16(len(s)))
		b.WriteString(s)
	}
	tag := func(kind byte, name string) {
		b.WriteByte(kind)
		str(name)
	}
	section := func(y byte, palette []string, states []uint64) {
		tag(1, "Y")
		b.WriteByte(y)
		tag(10, "block_states")
		tag(9, "palette")
		b.WriteByte(10)
		write(int32(len(palette)))
		for _, name := range palette {
			tag(8, "Name")
			str(name)
			b.WriteByte(0)
		}
		tag(12, "data")
		write(int32(len(states)))
		for _, value := range states {
			write(value)
		}
		b.Write([]byte{0, 0}) // end block_states, end section compound
	}
	tag(10, "")
	for _, field := range []struct {
		name  string
		value int32
	}{{"DataVersion", version}, {"xPos", x}, {"yPos", int32(worldlib.JavaNetherSpan.MinY / 16)}, {"zPos", z}} {
		tag(3, field.name)
		write(field.value)
	}
	tag(8, "Status")
	str("full")
	tag(10, "Heightmaps")
	tag(12, "WORLD_SURFACE")
	// The roof is the highest block in every column: each stored height is
	// one above y=127 relative to MinY 0, packed at the Nether span's bit
	// width (9 bits for the vanilla 0-255 storage span).
	bitsPerValue := bits.Len(uint(worldlib.JavaNetherSpan.MaxY - worldlib.JavaNetherSpan.MinY + 1))
	perLong := 64 / bitsPerValue
	packed := make([]uint64, (256+perLong-1)/perLong)
	for column := range 256 {
		packed[column/perLong] |= uint64(128) << ((column % perLong) * bitsPerValue)
	}
	write(int32(len(packed)))
	for _, value := range packed {
		write(value)
	}
	b.WriteByte(0) // end Heightmaps; the section list follows inside the root
	// Section Y=7 saves the roof: a complete bedrock sheet at y=127 over
	// partial alternating layers at y=123-126.
	roof := make([]uint64, 256)
	for localY := 11; localY <= 15; localY++ {
		pattern := uint64(0x0101010101010101) // bedrock on every other column
		if localY == 15 {
			pattern = 0x1111111111111111 // the sheet at 127 is complete
		}
		for long := localY * 16; long < (localY+1)*16; long++ {
			roof[long] = pattern
		}
	}
	// Section Y=4 saves terrain: netherrack at y=64-70 over air.
	terrainStates := make([]uint64, 256)
	for localY := 0; localY <= 6; localY++ {
		for long := localY * 16; long < (localY+1)*16; long++ {
			terrainStates[long] = 0x1111111111111111
		}
	}
	tag(9, "sections")
	b.WriteByte(10) // list item type: compound
	if terrain {
		write(int32(2))
		section(7, []string{"minecraft:air", "minecraft:bedrock"}, roof)
		section(4, []string{"minecraft:air", "minecraft:netherrack"}, terrainStates)
	} else {
		write(int32(1))
		section(7, []string{"minecraft:air", "minecraft:bedrock"}, roof)
	}
	b.WriteByte(0) // end root compound
	return b.Bytes()
}

// TestRenderDimensionTileNetherRoofShowsTerrainBelow pins the roofed-Nether
// surface scan. The Nether's roof spans the whole dimension, so every
// column's WORLD_SURFACE block is bedrock: the renderer must descend to the
// terrain below the roof instead of dropping every column as transparent
// while still reporting the chunks as rendered.
func TestRenderDimensionTileNetherRoofShowsTerrainBelow(t *testing.T) {
	// The parser replaces the roofed surface with the terrain under it and
	// keeps genuinely roof-only columns honestly empty.
	surface, err := worldlib.ParseJavaChunkSurfaceInSpan(netherRoofChunk(0, 0, 3955, true), worldlib.JavaNetherSpan, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []int{0, 42, 255} {
		if surface.Heights[column] != 70 || surface.Blocks[column] != "minecraft:netherrack" {
			t.Fatalf("column %d surfaced at %d as %q; want netherrack at y=70 below the roof", column, surface.Heights[column], surface.Blocks[column])
		}
	}
	roofOnly, err := worldlib.ParseJavaChunkSurfaceInSpan(netherRoofChunk(0, 0, 3955, false), worldlib.JavaNetherSpan, true)
	if err != nil {
		t.Fatal(err)
	}
	if roofOnly.Heights[0] != int16(worldlib.JavaNetherSpan.MinY-1) || roofOnly.Blocks[0] != "" {
		t.Fatalf("a roof-and-air-only column surfaced at %d as %q; want no surface", roofOnly.Heights[0], roofOnly.Blocks[0])
	}

	store := dimensionRegionStore(t, Nether.Folder, "r.0.0.mca",
		regionData(t, savedChunk{0, 0, netherRoofChunk(0, 0, 3955, true), 2, testSave}))
	tile, err := RenderDimensionTile(t.Context(), store, "fixture_world", Nether, TileCoord{})
	if err != nil {
		t.Fatal(err)
	}
	if tile.RenderedChunks != 1 {
		t.Fatalf("unexpected nether roof coverage: %+v", tile)
	}
	img, err := png.Decode(bytes.NewReader(tile.PNG))
	if err != nil {
		t.Fatal(err)
	}
	// The saved chunk covers the tile's first 16x16 blocks: the whole patch
	// must show the netherrack below the roof, not stay transparent.
	want := color.NRGBAModel.Convert(terrainColor(70, 0, netherElevation[:]))
	for z := 0; z < chunkBlocks; z++ {
		for x := 0; x < chunkBlocks; x++ {
			if got := color.NRGBAModel.Convert(img.At(x, z)); got != want {
				t.Fatalf("pixel %d, %d = %v; want the terrain below the roof %v", x, z, got, want)
			}
		}
	}
	if _, _, _, alpha := img.At(chunkBlocks, 0).RGBA(); alpha != 0 {
		t.Fatal("a missing chunk must stay transparent")
	}
}
