// SPDX-License-Identifier: MPL-2.0

package worldlib

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"
)

const (
	JavaOverworldMinY   = -64
	JavaOverworldMaxY   = 319
	JavaEndMinY         = 0
	JavaEndMaxY         = 255
	javaChunkWidth      = 16
	maxChunkNBTValues   = 32_768
	firstModernJava     = 2860 // Java 1.18
	java1219DataVersion = 4554
	lastSupportedJava   = java1219DataVersion
)

var (
	ErrJavaChunkUnsupported = errors.New("unsupported Java terrain chunk format")
	ErrJavaChunkUnfinished  = errors.New("incomplete Java terrain chunk")
)

// JavaChunkSurface contains saved heightmap values and the saved surface block
// for each column, indexed with X increasing first, then Z. A column without a
// surface is one block below the dimension's minimum block height.
type JavaChunkSurface struct {
	X, Z        int32
	Heights     [javaChunkWidth * javaChunkWidth]int16
	Blocks      [javaChunkWidth * javaChunkWidth]string
	WaterDepths [javaChunkWidth * javaChunkWidth]uint8
	Provisional bool
}

// SupportsJavaOverworldDataVersion reports the supported modern chunk interval.
// The decoded chunk must also use the standard Overworld layout.
func SupportsJavaOverworldDataVersion(version int64) bool {
	return version >= firstModernJava && version <= lastSupportedJava
}

// SupportsJavaEndDataVersion reports the End save-version qualification.
func SupportsJavaEndDataVersion(version int64) bool {
	return version == java1219DataVersion
}

type javaSurfaceProfile struct {
	minY, maxY                     int
	minDataVersion, maxDataVersion int32
}

// ParseJavaChunkSurface reads saved terrain heights and surface blocks from one decompressed
// modern Java Overworld Anvil chunk. It shares the World Library's bounded NBT decoder.
func ParseJavaChunkSurface(raw []byte) (JavaChunkSurface, error) {
	return parseJavaChunkSurface(raw, javaSurfaceProfile{
		minY: JavaOverworldMinY, maxY: JavaOverworldMaxY,
		minDataVersion: firstModernJava, maxDataVersion: lastSupportedJava,
	})
}

// ParseJavaEndChunkSurface reads the standard-height Java 1.21.9 End profile.
// The caller owns the dimension's storage identity.
func ParseJavaEndChunkSurface(raw []byte) (JavaChunkSurface, error) {
	return parseJavaChunkSurface(raw, javaSurfaceProfile{
		minY: JavaEndMinY, maxY: JavaEndMaxY,
		minDataVersion: java1219DataVersion, maxDataVersion: java1219DataVersion,
	})
}

func parseJavaChunkSurface(raw []byte, profile javaSurfaceProfile) (JavaChunkSurface, error) {
	var surface JavaChunkSurface
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
	if !ok || version < profile.minDataVersion || version > profile.maxDataVersion {
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
	if !ok || minSection != int32(profile.minY/javaChunkWidth) {
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
	// Both the 384-block Overworld and 256-block End profiles need nine
	// bits for the relative height and empty value. Modern Java never
	// straddles long boundaries, so seven values fit in one 64-bit entry.
	const (
		bits      = 9
		perLong   = 64 / bits
		valueMask = (1 << bits) - 1
	)
	heightSpan := profile.maxY - profile.minY + 1
	if !ok || len(packed) != (len(surface.Heights)+perLong-1)/perLong {
		return surface, ErrJavaChunkUnsupported
	}
	for i := range surface.Heights {
		relative := int((uint64(packed[i/perLong]) >> ((i % perLong) * bits)) & valueMask)
		if relative > heightSpan {
			return surface, fmt.Errorf("out-of-range Java surface height at column %d", i)
		}
		surface.Heights[i] = int16(profile.minY + relative - 1)
	}
	surface.readSurfaceBlocks(root["sections"], profile.minY, profile.maxY)
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

func (s *JavaChunkSurface) readSurfaceBlocks(value any, minY, maxY int) {
	minSection := minY / javaChunkWidth
	sectionCount := maxY/javaChunkWidth - minSection + 1
	sections := make([]javaBlockSection, sectionCount)
	seen := make([]bool, sectionCount)
	stored, _ := value.([]any)
	for _, item := range stored {
		section, _ := item.(map[string]any)
		y, ok := section["Y"].(int8)
		index := int(y) - minSection
		if !ok || index < 0 || index >= len(sections) {
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
		relativeY := y - minY
		return sections[relativeY/javaChunkWidth].block((relativeY%javaChunkWidth)*len(s.Heights) + column)
	}
	for column, y := range s.Heights {
		if int(y) < minY {
			continue
		}
		s.Blocks[column] = blockAt(column, int(y))
		if !javaWaterBlock(s.Blocks[column]) {
			continue
		}
		// Ten blocks separate the shallow and deep hues. Stop sooner
		// when the next block has a different or unreadable state.
		for depth := 1; depth <= 10 && int(y)-depth+1 >= minY; depth++ {
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
