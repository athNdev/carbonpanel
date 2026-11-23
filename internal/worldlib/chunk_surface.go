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
// MinY is the world Y of the chunk's bottom section; every stored chunk of
// the dimension reports yPos = MinY/16 and only saves sections between MinY
// and MaxY. Modded dimension types may declare other spans; those chunks stay
// unsupported until their span is verified against a game-generated save.
type JavaDimensionSpan struct {
	MinY, MaxY int
}

// The vanilla dimension spans (dimension_type min_y/height). All are section
// aligned: a negative MinY must stay a multiple of 16.
var (
	JavaOverworldSpan = JavaDimensionSpan{MinY: -64, MaxY: 319}
	JavaNetherSpan    = JavaDimensionSpan{MinY: 0, MaxY: 127}
	JavaEndSpan       = JavaDimensionSpan{MinY: 0, MaxY: 255}
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
// bounded NBT decoder.
func ParseJavaChunkSurface(raw []byte) (JavaChunkSurface, error) {
	return ParseJavaChunkSurfaceInSpan(raw, JavaOverworldSpan)
}

// ParseJavaChunkSurfaceInSpan reads saved terrain heights and surface blocks
// from one decompressed modern Java Anvil chunk belonging to the given
// dimension span. A chunk saved under any other layout is refused rather
// than misrendered.
func ParseJavaChunkSurfaceInSpan(raw []byte, span JavaDimensionSpan) (JavaChunkSurface, error) {
	var surface JavaChunkSurface
	if span.MaxY <= span.MinY || span.MinY%javaChunkWidth != 0 {
		return surface, errors.New("unsupported Java dimension span")
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
	// block. The width is the smallest bit count holding the span plus the
	// empty value, and modern Java never straddles long boundaries.
	const bitsPerLong = 64
	bits := bits.Len(uint(span.MaxY - span.MinY + 1))
	perLong := bitsPerLong / bits
	valueMask := uint64(1<<bits) - 1
	heightSpan := span.MaxY - span.MinY + 1
	if !ok || len(packed) != (len(surface.Heights)+perLong-1)/perLong {
		return surface, ErrJavaChunkUnsupported
	}
	for i := range surface.Heights {
		relative := int((uint64(packed[i/perLong]) >> ((i % perLong) * bits)) & valueMask)
		if relative > heightSpan {
			return surface, fmt.Errorf("out-of-range Java surface height at column %d", i)
		}
		surface.Heights[i] = int16(span.MinY + relative - 1)
	}
	surface.readSurfaceBlocks(root["sections"], span)
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

func (s *JavaChunkSurface) readSurfaceBlocks(value any, span JavaDimensionSpan) {
	minSection := span.MinY / javaChunkWidth
	sectionCount := span.MaxY/javaChunkWidth - minSection + 1
	var sections [24]javaBlockSection
	if sectionCount > len(sections) {
		// The guarded spans above keep this unreachable; refuse rather than
		// silently misindexing an oversized span.
		return
	}
	var seen [24]bool
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
	for column, y := range s.Heights {
		if y < int16(span.MinY) {
			continue
		}
		s.Blocks[column] = blockAt(column, int(y))
		if !javaWaterBlock(s.Blocks[column]) {
			continue
		}
		// Ten blocks separate the shallow and deep hues. Stop sooner
		// when the next block has a different or unreadable state.
		for depth := 1; depth <= 10 && int(y)-depth+1 >= span.MinY; depth++ {
			if !javaWaterBlock(blockAt(column, int(y)-depth+1)) {
				break
			}
			s.WaterDepths[column] = uint8(depth)
		}
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
