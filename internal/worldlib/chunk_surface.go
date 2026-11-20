// SPDX-License-Identifier: MPL-2.0

package worldlib

import (
	"errors"
	"fmt"
)

const (
	JavaOverworldMinY = -64
	JavaOverworldMaxY = 319
	javaChunkWidth    = 16
	maxChunkNBTValues = 32_768
	firstModernJava   = 2860 // Java 1.18
	lastSupportedJava = 4554 // Java 1.21.9
)

var (
	ErrJavaChunkUnsupported = errors.New("unsupported Java Overworld chunk format")
	ErrJavaChunkUnfinished  = errors.New("incomplete Java Overworld chunk")
)

// JavaChunkSurface is the vanilla-height Overworld's saved surface. Highest
// block coordinates are indexed with X increasing first, then Z. A column
// with no surface has JavaOverworldMinY - 1.
type JavaChunkSurface struct {
	X, Z    int32
	Heights [javaChunkWidth * javaChunkWidth]int16
}

// SupportsJavaOverworldDataVersion reports the supported modern chunk interval.
// The decoded chunk must also use the standard Overworld layout.
func SupportsJavaOverworldDataVersion(version int64) bool {
	return version >= firstModernJava && version <= lastSupportedJava
}

// ParseJavaChunkSurface reads saved terrain heights from one decompressed
// modern Java Anvil chunk. It shares the World Library's bounded NBT decoder.
func ParseJavaChunkSurface(raw []byte) (JavaChunkSurface, error) {
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
	if !ok || !SupportsJavaOverworldDataVersion(int64(version)) {
		return surface, ErrJavaChunkUnsupported
	}
	status, ok := root["Status"].(string)
	if !ok {
		return surface, errors.New("missing generation status in Java chunk")
	}
	if status != "full" && status != "minecraft:full" {
		return surface, ErrJavaChunkUnfinished
	}
	minSection, ok := root["yPos"].(int32)
	if !ok || minSection != JavaOverworldMinY/16 {
		return surface, ErrJavaChunkUnsupported
	}
	x, hasX := root["xPos"].(int32)
	z, hasZ := root["zPos"].(int32)
	if !hasX || !hasZ {
		return surface, errors.New("missing position in Java chunk")
	}
	surface.X, surface.Z = x, z
	heights, ok := FactNested(root, "Heightmaps", "WORLD_SURFACE")
	if !ok {
		return surface, errors.New("missing saved surface heightmap in Java chunk")
	}
	packed, ok := heights.([]int64)
	// 384 blocks plus the empty value fit in 9 bits. Modern Java never
	// straddles long boundaries, so seven values fit in one 64-bit entry.
	const (
		bits       = 9
		perLong    = 64 / bits
		valueMask  = (1 << bits) - 1
		heightSpan = JavaOverworldMaxY - JavaOverworldMinY + 1
	)
	if !ok || len(packed) != (len(surface.Heights)+perLong-1)/perLong {
		return surface, ErrJavaChunkUnsupported
	}
	for i := range surface.Heights {
		relative := int((uint64(packed[i/perLong]) >> ((i % perLong) * bits)) & valueMask)
		if relative > heightSpan {
			return surface, fmt.Errorf("out-of-range Java surface height at column %d", i)
		}
		surface.Heights[i] = int16(JavaOverworldMinY + relative - 1)
	}
	return surface, nil
}
