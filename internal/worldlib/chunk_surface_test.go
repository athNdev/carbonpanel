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

// TestParseJavaChunkSurfaceInSpanChecksLayout pins the fail-closed geometry:
// a chunk whose yPos does not match the span, or whose heightmap packing does
// not match the span's bit width, is refused instead of misrendered.
func TestParseJavaChunkSurfaceInSpanChecksLayout(t *testing.T) {
	if _, err := ParseJavaChunkSurfaceInSpan(nil, JavaDimensionSpan{MinY: 3, MaxY: 200}); err == nil || !strings.Contains(err.Error(), "span") {
		t.Fatalf("unaligned span accepted: %v", err)
	}
	if _, err := ParseJavaChunkSurfaceInSpan(nil, JavaDimensionSpan{MinY: 100, MaxY: 64}); err == nil || !strings.Contains(err.Error(), "span") {
		t.Fatalf("inverted span accepted: %v", err)
	}
	overworld := buildSurfaceChunk(0, 0, 3955, JavaOverworldSpan)
	if _, err := ParseJavaChunkSurfaceInSpan(overworld, JavaNetherSpan); !errors.Is(err, ErrJavaChunkUnsupported) {
		t.Fatalf("overworld layout accepted as nether: %v", err)
	}
	if _, err := ParseJavaChunkSurface(overworld); err != nil {
		t.Fatalf("matching span refused: %v", err)
	}
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
