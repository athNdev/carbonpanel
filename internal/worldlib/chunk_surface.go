// SPDX-License-Identifier: MPL-2.0

package worldlib

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"
)

const (
	JavaOverworldMinY = -64
	JavaOverworldMaxY = 319
	javaChunkWidth    = 16
	javaChunkColumns  = javaChunkWidth * javaChunkWidth
	maxJavaSections   = 24
	maxChunkNBTValues = 32_768
	firstModernJava   = 2860 // Java 1.18
	lastSupportedJava = 5023 // Java 26.3
)

// LastSupportedJavaDataVersion is the highest chunk DataVersion accepted by
// the surface parser. Anything above stays honestly unsupported.
const LastSupportedJavaDataVersion = lastSupportedJava

var (
	ErrJavaChunkUnsupported = errors.New("unsupported Java chunk format")
	ErrJavaChunkUnfinished  = errors.New("incomplete Java chunk")
)

// JavaDimensionSpan is the saved vertical layout of one vanilla dimension.
// MinY is the world Y of the chunk's bottom section, and every stored chunk
// reports yPos = MinY/16. MaxY is the inclusive top of the storage range:
// it determines the heightmap's bit width and packed-long count. LogicalHeight
// is the dimension's logical block height and determines how many populated
// sections can be indexed. The two differ in the Nether: storage spans
// y=0..255, while logical height 128 covers eight sections ending at the
// bedrock roof y=127. Modded dimension types may declare other spans; those
// chunks stay unsupported until their span is verified against a
// game-generated save.
type JavaDimensionSpan struct {
	MinY, MaxY    int
	LogicalHeight int
}

type javaDimensionLayout struct {
	storageHeight          int
	logicalHeight          int
	sectionCount           int
	heightmapBits          int
	heightmapValuesPerLong int
	heightmapLongs         int
}

func (span JavaDimensionSpan) layout() (javaDimensionLayout, error) {
	storageHeight := span.MaxY - span.MinY + 1
	if span.MaxY <= span.MinY ||
		span.MinY%javaChunkWidth != 0 ||
		storageHeight%javaChunkWidth != 0 ||
		span.LogicalHeight <= 0 ||
		span.LogicalHeight%javaChunkWidth != 0 ||
		span.LogicalHeight > storageHeight ||
		span.LogicalHeight/javaChunkWidth > maxJavaSections {
		return javaDimensionLayout{}, errors.New("unsupported Java dimension span")
	}
	// A heightmap stores one above the highest block, so the storage span's
	// inclusive value range needs ceil(log2(storageHeight+1)) bits. Modern
	// Java packs each value wholly within one long and leaves the unused
	// high bits clear.
	heightmapBits := bits.Len(uint(storageHeight))
	valuesPerLong := 64 / heightmapBits
	return javaDimensionLayout{
		storageHeight:          storageHeight,
		logicalHeight:          span.LogicalHeight,
		sectionCount:           span.LogicalHeight / javaChunkWidth,
		heightmapBits:          heightmapBits,
		heightmapValuesPerLong: valuesPerLong,
		heightmapLongs:         (javaChunkColumns + valuesPerLong - 1) / valuesPerLong,
	}, nil
}

// The vanilla dimension spans (dimension_type min_y/height/logical_height).
// All heights are section aligned: a negative MinY must stay a multiple of 16.
var (
	JavaOverworldSpan = JavaDimensionSpan{MinY: -64, MaxY: 319, LogicalHeight: 384}
	JavaNetherSpan    = JavaDimensionSpan{MinY: 0, MaxY: 255, LogicalHeight: 128}
	JavaEndSpan       = JavaDimensionSpan{MinY: 0, MaxY: 255, LogicalHeight: 256}
)

// JavaChunkSurface is one saved dimension's top-down surface. Highest block
// coordinates are indexed with X increasing first, then Z. A column with no
// surface has its dimension span's MinY - 1.
type JavaChunkSurface struct {
	X, Z        int32
	Heights     [javaChunkWidth * javaChunkWidth]int16
	Blocks      [javaChunkWidth * javaChunkWidth]string
	WaterDepths [javaChunkWidth * javaChunkWidth]uint8
	Provisional bool
}

// SupportsJavaOverworldDataVersion reports the supported modern chunk
// interval. The decoded chunk must also use a known dimension span layout.
// The ceiling is the fail-closed guard: 1.21.10 = 4556, 1.21.11 = 4671,
// 26.1 = 4786, 26.2 = 4903 and 26.3 = 5023 all keep the chunk structure this
// parser reads (yPos, Status, WORLD_SURFACE packing, section block_states),
// so they are accepted after wiki-verified format research, and the
// per-chunk structural checks below reject anything that drifts with the
// honest unsupported state. Game-save qualification per release remains the
// operator-run fixture harness (TestRenderOverworldTileGameSave).
func SupportsJavaOverworldDataVersion(version int64) bool {
	return version >= firstModernJava && version <= lastSupportedJava
}

// ParseJavaChunkSurface reads saved terrain heights and surface blocks from
// one decompressed modern Java Overworld chunk. It shares the World Library's
// bounded NBT decoder. The Overworld has no bedrock roof: a bedrock
// WORLD_SURFACE is the column's visible surface.
func ParseJavaChunkSurface(raw []byte) (JavaChunkSurface, error) {
	return ParseJavaChunkSurfaceInSpan(raw, JavaOverworldSpan, false)
}

// ParseJavaChunkSurfaceInSpan reads saved terrain heights and surface blocks
// from one decompressed modern Java Anvil chunk belonging to the given
// dimension span. A chunk saved under any other layout is refused rather
// than misrendered. With surfaceUnderRoof, a bedrock WORLD_SURFACE is an
// opaque roof (the Nether's) instead of the visible surface: the column's
// surface becomes the first terrain block under the roof, and a column that
// holds only roof bedrock and air keeps no surface.
func ParseJavaChunkSurfaceInSpan(raw []byte, span JavaDimensionSpan, surfaceUnderRoof bool) (JavaChunkSurface, error) {
	var surface JavaChunkSurface
	layout, err := span.layout()
	if err != nil {
		return surface, err
	}
	if len(raw) > maxNBTDecompressed {
		return surface, errors.New("chunk NBT exceeds the Java decoder limit")
	}
	cursor := &nbtCursor{data: raw, maxValues: maxChunkNBTValues}
	root, _, err := parseNBTDocument(cursor)
	if err != nil {
		return surface, err
	}
	if cursor.pos != len(raw) {
		return surface, errors.New("trailing bytes in Java chunk NBT")
	}
	version, ok := root["DataVersion"].(int32)
	if !ok || !SupportsJavaOverworldDataVersion(int64(version)) {
		return surface, ErrJavaChunkUnsupported
	}
	status, ok := root["Status"].(string)
	if !ok {
		return surface, errors.New("missing generation status in Java chunk")
	}
	switch strings.TrimPrefix(status, "minecraft:") {
	case "full":
	case "surface", "carvers", "liquid_carvers", "features", "initialize_light", "light", "spawn", "heightmaps":
		surface.Provisional = true
	default:
		return surface, ErrJavaChunkUnfinished
	}
	minSection, ok := root["yPos"].(int32)
	if !ok || int(minSection) != span.MinY/javaChunkWidth {
		return surface, ErrJavaChunkUnsupported
	}
	x, hasX := root["xPos"].(int32)
	z, hasZ := root["zPos"].(int32)
	if !hasX || !hasZ {
		return surface, errors.New("missing position in Java chunk")
	}
	surface.X, surface.Z = x, z
	heights, ok := FactNested(root, "Heightmaps", "WORLD_SURFACE")
	if !ok && surface.Provisional {
		heights, ok = FactNested(root, "Heightmaps", "WORLD_SURFACE_WG")
		if !ok {
			return surface, ErrJavaChunkUnfinished
		}
	}
	if !ok {
		return surface, errors.New("missing saved surface heightmap in Java chunk")
	}
	packed, ok := heights.([]int64)
	// The packed values are heights relative to MinY, one above the highest
	// block. The bit width follows the storage range, while the saved value
	// must remain inside the dimension's logical section range. Modern Java
	// never straddles a long boundary.
	bits := layout.heightmapBits
	perLong := layout.heightmapValuesPerLong
	valueMask := uint64(1<<bits) - 1
	if !ok || len(packed) != layout.heightmapLongs {
		return surface, ErrJavaChunkUnsupported
	}
	for i := range surface.Heights {
		relative := int((uint64(packed[i/perLong]) >> ((i % perLong) * bits)) & valueMask)
		if relative > layout.logicalHeight {
			return surface, fmt.Errorf("out-of-range Java surface height at column %d", i)
		}
		surface.Heights[i] = int16(span.MinY + relative - 1)
	}
	surface.readSurfaceBlocks(root["sections"], span, layout, surfaceUnderRoof)
	return surface, nil
}

const javaSectionVolume = javaChunkWidth * javaChunkWidth * javaChunkWidth

type javaBlockSection struct {
	names []string
	data  []int64
	bits  int
}

func parseJavaBlockSection(value any) javaBlockSection {
	states, _ := value.(map[string]any)
	palette, _ := states["palette"].([]any)
	if len(palette) == 0 || len(palette) > javaSectionVolume {
		return javaBlockSection{}
	}
	width := max(4, bits.Len(uint(len(palette)-1)))
	perLong := 64 / width
	data, _ := states["data"].([]int64)
	if len(palette) > 1 && len(data) != (javaSectionVolume+perLong-1)/perLong {
		return javaBlockSection{}
	}
	names := make([]string, len(palette))
	for i, state := range palette {
		entry, _ := state.(map[string]any)
		names[i], _ = entry["Name"].(string)
	}
	return javaBlockSection{names: names, data: data, bits: width}
}

func (s javaBlockSection) block(index int) string {
	switch len(s.names) {
	case 0:
		return ""
	case 1:
		return s.names[0]
	}
	perLong := 64 / s.bits
	state := int((uint64(s.data[index/perLong]) >> (index % perLong * s.bits)) & ((1 << s.bits) - 1))
	if state >= len(s.names) {
		return ""
	}
	return s.names[state]
}

func (s *JavaChunkSurface) readSurfaceBlocks(value any, span JavaDimensionSpan, layout javaDimensionLayout, surfaceUnderRoof bool) {
	minSection := span.MinY / javaChunkWidth
	sectionCount := layout.sectionCount
	sections := make([]javaBlockSection, sectionCount)
	seen := make([]bool, sectionCount)
	stored, _ := value.([]any)
	for _, item := range stored {
		section, _ := item.(map[string]any)
		y, ok := section["Y"].(int8)
		index := int(y) - minSection
		if !ok || index < 0 || index >= sectionCount {
			continue
		}
		if seen[index] {
			sections[index] = javaBlockSection{}
			continue
		}
		seen[index] = true
		sections[index] = parseJavaBlockSection(section["block_states"])
	}
	blockAt := func(column, y int) string {
		relativeY := y - span.MinY
		return sections[relativeY/javaChunkWidth].block((relativeY%javaChunkWidth)*len(s.Heights) + column)
	}
	for column, top := range s.Heights {
		if top < int16(span.MinY) {
			continue
		}
		y := int(top)
		block := blockAt(column, y)
		if surfaceUnderRoof && javaBedrockBlock(block) {
			// A roofed dimension (the Nether) saves WORLD_SURFACE as the
			// bedrock sheet at the top of its logical height (y=127), not
			// at the top of the 256-block storage range (y=255). Every
			// column's surface block is therefore bedrock. The visible
			// surface is the first terrain block under the roof: the walk
			// passes roof bedrock, air and unsaved sections, and a column
			// that holds only roof and air keeps no surface. Like the water
			// scan below, the walk is bounded by the logical span.
			block = ""
			for scan := y - 1; scan >= span.MinY; scan-- {
				if found := blockAt(column, scan); !javaAirBlock(found) && !javaBedrockBlock(found) {
					y, block = scan, found
					break
				}
			}
			if block == "" {
				s.Heights[column] = int16(span.MinY - 1)
				s.Blocks[column] = ""
				continue
			}
			s.Heights[column] = int16(y)
		}
		s.Blocks[column] = block
		if !javaWaterBlock(block) {
			continue
		}
		// Ten blocks separate the shallow and deep hues. Stop sooner
		// when the next block has a different or unreadable state.
		for depth := 1; depth <= 10 && y-depth+1 >= span.MinY; depth++ {
			if !javaWaterBlock(blockAt(column, y-depth+1)) {
				break
			}
			s.WaterDepths[column] = uint8(depth)
		}
	}
}

func javaBedrockBlock(name string) bool {
	return name == "minecraft:bedrock"
}

// javaAirBlock reports whether the resolved state is air. An empty state is
// an unsaved or unreadable section position; the heightmap already says the
// column's blocks below its roof surface are not terrain, so the roof walk
// and refusals treat it like air rather than an honest surface.
func javaAirBlock(name string) bool {
	switch name {
	case "", "minecraft:air", "minecraft:cave_air", "minecraft:void_air":
		return true
	default:
		return false
	}
}

func javaWaterBlock(name string) bool {
	switch name {
	case "minecraft:water", "minecraft:kelp", "minecraft:kelp_plant", "minecraft:seagrass",
		"minecraft:tall_seagrass", "minecraft:bubble_column":
		return true
	default:
		return false
	}
}
