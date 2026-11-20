// SPDX-License-Identifier: MPL-2.0

package worldlib

import (
	"errors"
	"fmt"
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

// JavaChunkSurface contains saved heightmap values indexed with X increasing
// first, then Z. A column without a surface is one block below the dimension's
// minimum block height.
type JavaChunkSurface struct {
	X, Z    int32
	Heights [javaChunkWidth * javaChunkWidth]int16
}

// SupportsJavaOverworldDataVersion reports the supported modern chunk interval.
// The decoded chunk must also use the standard Overworld layout.
func SupportsJavaOverworldDataVersion(version int64) bool {
	return version >= firstModernJava && version <= lastSupportedJava
}

type javaSurfaceProfile struct {
	minY, maxY                     int
	minDataVersion, maxDataVersion int32
}

// ParseJavaChunkSurface reads saved heights from an Overworld Anvil chunk.
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
	if status != "full" && status != "minecraft:full" {
		return surface, ErrJavaChunkUnfinished
	}
	minSection, ok := root["yPos"].(int32)
	if !ok || minSection != int32(profile.minY/16) {
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
	// Both the 384-block Overworld and 256-block End profiles need nine
	// bits for the relative height and empty value. Modern Java stores seven
	// whole values in each 64-bit entry.
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
	return surface, nil
}
