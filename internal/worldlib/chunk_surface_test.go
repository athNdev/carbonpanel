// SPDX-License-Identifier: MPL-2.0

package worldlib

import (
	"bytes"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

func TestJavaChunkNBTCumulativeValueBudget(t *testing.T) {
	t.Run("compound members", func(t *testing.T) {
		var raw bytes.Buffer
		raw.Write([]byte{byte(nbtCompound), 0, 0})
		for range maxChunkNBTValues {
			raw.Write([]byte{byte(nbtByte), 0, 1, 'x', 0})
		}
		raw.WriteByte(byte(nbtEnd))
		if _, err := ParseJavaChunkSurface(raw.Bytes()); err == nil || !strings.Contains(err.Error(), "value budget") {
			t.Fatalf("tag-heavy compound was not rejected by the cumulative budget: %v", err)
		}
	})
	t.Run("list capacity checked before allocation", func(t *testing.T) {
		var raw bytes.Buffer
		raw.Write([]byte{byte(nbtCompound), 0, 0, byte(nbtList), 0, 1, 'x', byte(nbtInt)})
		_ = binary.Write(&raw, binary.BigEndian, int32(maxChunkNBTValues))
		if _, err := ParseJavaChunkSurface(raw.Bytes()); err == nil || !strings.Contains(err.Error(), "value budget") {
			t.Fatalf("requested list allocation was not refused before reading its items: %v", err)
		}
	})
}

// TestSupportsJavaDataVersionBoundaries pins the accepted DataVersion range:
// 2860 (1.18) through 5023 (26.3). Values above the ceiling stay honestly
// unsupported until they are verified against game-generated saves.
func TestSupportsJavaDataVersionBoundaries(t *testing.T) {
	if LastSupportedJavaDataVersion != 5023 {
		t.Fatalf("ceiling drifted without a fixture qualification: %d", LastSupportedJavaDataVersion)
	}
	for _, tc := range []struct {
		version int64
		want    bool
	}{
		{2729, false}, // 1.16.5, below the floor
		{2859, false},
		{2860, true}, // 1.18
		{3955, true}, // 1.20.1
		{4554, true}, // 1.21.9
		{4556, true}, // 1.21.10
		{4671, true}, // 1.21.11
		{4786, true}, // 26.1
		{4903, true}, // 26.2
		{5023, true}, // 26.3
		{5024, false},
	} {
		if got := SupportsJavaOverworldDataVersion(tc.version); got != tc.want {
			t.Errorf("DataVersion %d accepted = %t, want %t", tc.version, got, tc.want)
		}
	}
}

// TestJavaDimensionSpanLayouts pins the separate storage and logical layouts
// for all three vanilla dimensions. The Nether's storage height determines a
// nine-bit heightmap, while its logical height determines eight indexed
// sections; the Overworld and End keep their full storage/logical heights.
func TestJavaDimensionSpanLayouts(t *testing.T) {
	for _, tc := range []struct {
		name           string
		span           JavaDimensionSpan
		storageHeight  int
		logicalHeight  int
		sectionCount   int
		heightmapBits  int
		heightmapLongs int
	}{
		{"overworld", JavaOverworldSpan, 384, 384, 24, 9, 37},
		{"nether", JavaNetherSpan, 256, 128, 8, 9, 37},
		{"end", JavaEndSpan, 256, 256, 16, 9, 37},
	} {
		t.Run(tc.name, func(t *testing.T) {
			layout, err := tc.span.layout()
			if err != nil {
				t.Fatal(err)
			}
			if layout.storageHeight != tc.storageHeight ||
				layout.logicalHeight != tc.logicalHeight ||
				layout.sectionCount != tc.sectionCount ||
				layout.heightmapBits != tc.heightmapBits ||
				layout.heightmapLongs != tc.heightmapLongs {
				t.Fatalf("layout = %+v; want storage=%d logical=%d sections=%d bits=%d longs=%d", layout, tc.storageHeight, tc.logicalHeight, tc.sectionCount, tc.heightmapBits, tc.heightmapLongs)
			}
		})
	}
}

// TestParseJavaChunkSurfaceInSpanChecksLayout pins the fail-closed geometry:
// a chunk whose yPos does not match the span, or whose heightmap packing does
// not match the span's bit width, is refused instead of misrendered.
func TestParseJavaChunkSurfaceInSpanChecksLayout(t *testing.T) {
	if _, err := ParseJavaChunkSurfaceInSpan(nil, JavaDimensionSpan{MinY: 3, MaxY: 200, LogicalHeight: 128}, false); err == nil || !strings.Contains(err.Error(), "span") {
		t.Fatalf("unaligned span accepted: %v", err)
	}
	if _, err := ParseJavaChunkSurfaceInSpan(nil, JavaDimensionSpan{MinY: 100, MaxY: 64, LogicalHeight: 128}, false); err == nil || !strings.Contains(err.Error(), "span") {
		t.Fatalf("inverted span accepted: %v", err)
	}
	overworld := buildSurfaceChunk(0, 0, 3955, JavaOverworldSpan)
	if _, err := ParseJavaChunkSurfaceInSpan(overworld, JavaNetherSpan, false); !errors.Is(err, ErrJavaChunkUnsupported) {
		t.Fatalf("overworld layout accepted as nether: %v", err)
	}
	if _, err := ParseJavaChunkSurface(overworld); err != nil {
		t.Fatalf("matching span refused: %v", err)
	}
}

// TestParseJavaChunkSurfaceInSpanAcceptsVanillaNetherLayout qualifies the
// vanilla Nether storage layout without a binary fixture. The Nether stores
// a 256-block heightmap as nine-bit values in 37 longs, while its logical
// height is 128 blocks (eight sections) and its roof is at y=127.
func TestParseJavaChunkSurfaceInSpanAcceptsVanillaNetherLayout(t *testing.T) {
	const (
		heightmapBits   = 9
		valuesPerLong   = 7
		expectedLongs   = 37
		roofHeight      = 128
		netherSectionY  = 7
		terrainSectionY = 4
		terrainBlockY   = 79
	)
	heights := make([]uint64, 256)
	for i := range heights {
		heights[i] = roofHeight
	}
	packed := packSurfaceHeightmap(heights, heightmapBits, valuesPerLong)
	if len(packed) != expectedLongs {
		t.Fatalf("packed heightmap has %d longs, want %d", len(packed), expectedLongs)
	}

	sections := make([]surfaceTestSection, 0, 8)
	for y := int8(0); y < 8; y++ {
		section := surfaceTestSection{y: y, block: "minecraft:air"}
		switch y {
		case terrainSectionY:
			section.block = "minecraft:netherrack"
		case netherSectionY:
			section.block = "minecraft:bedrock"
		}
		sections = append(sections, section)
	}
	raw := buildSurfaceChunkWithHeightmap(0, 0, 3955, JavaNetherSpan, packed, sections)
	surface, err := ParseJavaChunkSurfaceInSpan(raw, JavaNetherSpan, true)
	if err != nil {
		t.Fatalf("vanilla Nether layout refused: %v", err)
	}
	for _, column := range []int{0, 42, 255} {
		if surface.Heights[column] != terrainBlockY || surface.Blocks[column] != "minecraft:netherrack" {
			t.Fatalf("column %d surfaced at %d as %q; want netherrack at y=%d below the roof", column, surface.Heights[column], surface.Blocks[column], terrainBlockY)
		}
	}
}

// TestParseJavaChunkSurfaceInSpanRejectsLegacyNetherHeightmapPacking keeps
// the format check fail-closed: the Nether's vanilla 9-bit/37-long layout is
// accepted, but the old 8-bit/32-long logical-height packing is not.
func TestParseJavaChunkSurfaceInSpanRejectsLegacyNetherHeightmapPacking(t *testing.T) {
	heights := make([]uint64, 256)
	for i := range heights {
		heights[i] = 128
	}
	packed := packSurfaceHeightmap(heights, 8, 8)
	if len(packed) != 32 {
		t.Fatalf("legacy packed heightmap has %d longs, want 32", len(packed))
	}
	raw := buildSurfaceChunkWithHeightmap(0, 0, 3955, JavaNetherSpan, packed, nil)
	if _, err := ParseJavaChunkSurfaceInSpan(raw, JavaNetherSpan, false); !errors.Is(err, ErrJavaChunkUnsupported) {
		t.Fatalf("legacy Nether heightmap packing accepted: %v", err)
	}
}

// TestParseJavaChunkSurfaceInSpanChecksNetherLogicalHeight proves that the
// storage-height heightmap cannot address a block above the Nether's logical
// section range. This keeps section indexing inside the eight logical
// sections instead of treating the upper storage span as saved terrain.
func TestParseJavaChunkSurfaceInSpanChecksNetherLogicalHeight(t *testing.T) {
	heights := make([]uint64, 256)
	for i := range heights {
		heights[i] = 129
	}
	packed := packSurfaceHeightmap(heights, 9, 7)
	raw := buildSurfaceChunkWithHeightmap(0, 0, 3955, JavaNetherSpan, packed, nil)
	if _, err := ParseJavaChunkSurfaceInSpan(raw, JavaNetherSpan, false); err == nil || !strings.Contains(err.Error(), "out-of-range") {
		t.Fatalf("height above the Nether logical height accepted: %v", err)
	}
}

type surfaceTestSection struct {
	y     int8
	block string
}

func packSurfaceHeightmap(heights []uint64, bits, valuesPerLong int) []uint64 {
	packed := make([]uint64, (len(heights)+valuesPerLong-1)/valuesPerLong)
	for i, height := range heights {
		packed[i/valuesPerLong] |= height << ((i % valuesPerLong) * bits)
	}
	return packed
}

func buildSurfaceChunkWithHeightmap(x, z, version int32, span JavaDimensionSpan, packed []uint64, sections []surfaceTestSection) []byte {
	var raw bytes.Buffer
	write := func(v any) { _ = binary.Write(&raw, binary.BigEndian, v) }
	str := func(s string) {
		write(uint16(len(s)))
		raw.WriteString(s)
	}
	tag := func(kind byte, name string) {
		raw.WriteByte(kind)
		str(name)
	}
	tag(10, "")
	for _, field := range []struct {
		name  string
		value int32
	}{{"DataVersion", version}, {"xPos", x}, {"yPos", int32(span.MinY / 16)}, {"zPos", z}} {
		tag(3, field.name)
		write(field.value)
	}
	tag(8, "Status")
	str("full")
	tag(10, "Heightmaps")
	tag(12, "WORLD_SURFACE")
	write(int32(len(packed)))
	for _, value := range packed {
		write(value)
	}
	raw.WriteByte(0) // end Heightmaps
	tag(9, "sections")
	raw.WriteByte(10)
	write(int32(len(sections)))
	for _, section := range sections {
		tag(1, "Y")
		raw.WriteByte(byte(section.y))
		tag(10, "block_states")
		tag(9, "palette")
		raw.WriteByte(10)
		write(int32(1))
		tag(8, "Name")
		str(section.block)
		raw.Write([]byte{0, 0, 0}) // end palette entry, block_states and section
	}
	raw.WriteByte(0) // end root compound
	return raw.Bytes()
}

// buildSurfaceChunk assembles a minimal surface chunk for the span: DataVersion,
// position, yPos, Status and one packed WORLD_SURFACE heightmap.
func buildSurfaceChunk(x, z, version int32, span JavaDimensionSpan) []byte {
	var raw bytes.Buffer
	write := func(v any) { _ = binary.Write(&raw, binary.BigEndian, v) }
	str := func(s string) {
		write(uint16(len(s)))
		raw.WriteString(s)
	}
	tag := func(kind byte, name string) {
		raw.WriteByte(kind)
		str(name)
	}
	tag(10, "")
	for _, field := range []struct {
		name  string
		value int32
	}{{"DataVersion", version}, {"xPos", x}, {"yPos", int32(span.MinY / 16)}, {"zPos", z}} {
		tag(3, field.name)
		write(field.value)
	}
	tag(8, "Status")
	str("full")
	tag(10, "Heightmaps")
	tag(12, "WORLD_SURFACE")
	heightSpan := span.MaxY - span.MinY + 1
	bitsOf := func(v uint) int {
		n := 0
		for v > 0 {
			n++
			v >>= 1
		}
		return n
	}
	bits := bitsOf(uint(heightSpan))
	perLong := 64 / bits
	packed := make([]uint64, (256+perLong-1)/perLong)
	for i := range 256 {
		height := uint64(1 + (i%16)*3 + (i/16)*5)
		packed[i/perLong] |= height << ((i % perLong) * bits)
	}
	write(int32(len(packed)))
	for _, value := range packed {
		write(value)
	}
	raw.Write([]byte{0, 0})
	return raw.Bytes()
}
