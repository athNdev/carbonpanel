// SPDX-License-Identifier: MPL-2.0

package worldterrain

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math/bits"
	"testing"

	lz4 "github.com/pierrec/lz4/v4"
)

// TestXXH32KnownVectors pins the checksum against the published XXH32
// reference vectors (seed 0). The lz4-java block stream uses seed
// 0x9747b28c, which only shifts the initializers.
func TestXXH32KnownVectors(t *testing.T) {
	for _, tc := range []struct {
		sum  uint32
		data string
	}{
		{0x02cc5d05, ""},
		{0x550d7456, "a"},
		{0x4999fc53, "ab"},
		{0x32d153ff, "abc"},
		{0xa3643705, "abcd"},
		{0x9738f19b, "abcde"},
		{0x8b7cd587, "abcdef"},
		{0x9dd093b3, "abcdefg"},
		{0x0bb3c6bb, "abcdefgh"},
		{0xd03c13fd, "abcdefghi"},
		{0x8b988cfe, "abcdefghij"},
		{0x9d2d8b62, "abcdefghijklmnop"},
		{0x42ae804d, "abcdefghijklmnopqrstuvwxyz0123456789"},
	} {
		if got := xxh32([]byte(tc.data), 0); got != tc.sum {
			t.Errorf("xxh32(%q) = %#x, want %#x", tc.data, got, tc.sum)
		}
	}
}

// lz4JavaBlock writes one framed block in the lz4-java block-stream layout.
// The token carries the compression level nibble like the Java writer.
func lz4JavaBlock(method byte, compressed, original []byte, check uint32) []byte {
	level := 0
	if len(original) > 0 {
		level = max(0, bits.Len(uint(len(original)-1))-lz4JavaLevelBase)
	}
	block := []byte(lz4JavaMagic)
	block = append(block, method|byte(level))
	var word [4]byte
	binary.LittleEndian.PutUint32(word[:], uint32(len(compressed)))
	block = append(block, word[:]...)
	binary.LittleEndian.PutUint32(word[:], uint32(len(original)))
	block = append(block, word[:]...)
	binary.LittleEndian.PutUint32(word[:], check)
	block = append(block, word[:]...)
	block = append(block, compressed...)
	return block
}

// lz4JavaEncode frames data as a complete lz4-java block stream: one block
// (LZ4-compressed when that shrinks, raw otherwise) plus the end mark.
func lz4JavaEncode(t *testing.T, data []byte) []byte {
	t.Helper()
	var stream []byte
	var method byte
	var payload []byte
	if compressed := make([]byte, lz4.CompressBlockBound(len(data))); len(data) > 0 {
		n, err := lz4.CompressBlock(data, compressed, nil)
		if err != nil {
			t.Fatalf("lz4 compress: %v", err)
		}
		if n > 0 && n < len(data) {
			method, payload = lz4JavaMethodLZ4, compressed[:n]
		}
	}
	if payload == nil {
		method, payload = lz4JavaMethodRaw, data
	}
	stream = append(stream, lz4JavaBlock(method, payload, data, xxh32(data, lz4JavaDefaultSeed)&0x0FFFFFFF)...)
	stream = append(stream, lz4JavaBlock(lz4JavaMethodRaw, nil, nil, 0)...)
	return stream
}

func TestDecodeLZ4JavaBlockStreamRoundTrip(t *testing.T) {
	// Repeating data compresses; the 100-byte run crosses one full block.
	data := bytes.Repeat([]byte{0x5A, 0x00, 0x00, 0x00, 0x01}, 100)
	decoded, err := decodeLZ4JavaBlockStream(lz4JavaEncode(t, data), maxDecompressedChunk)
	if err != nil {
		t.Fatalf("compressed block stream: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatalf("compressed round trip mismatch: %d bytes", len(decoded))
	}

	// Incompressible data falls back to the raw block method.
	raw := make([]byte, 40)
	for i := range raw {
		raw[i] = byte(i * 37)
	}
	decoded, err = decodeLZ4JavaBlockStream(lz4JavaEncode(t, raw), maxDecompressedChunk)
	if err != nil {
		t.Fatalf("raw block stream: %v", err)
	}
	if !bytes.Equal(decoded, raw) {
		t.Fatal("raw round trip mismatch")
	}
}

func TestDecodeLZ4JavaBlockStreamMultiBlockAndPadding(t *testing.T) {
	// 64 KiB block size like the Java default: two full blocks plus a tail.
	data := make([]byte, 64<<10+64<<10+11)
	for i := range data {
		data[i] = byte(i >> 4)
	}
	var stream []byte
	for start := 0; start < len(data); {
		end := min(start+(64<<10), len(data))
		block := data[start:end]
		compressed := make([]byte, lz4.CompressBlockBound(len(block)))
		n, err := lz4.CompressBlock(block, compressed, nil)
		if err != nil {
			t.Fatalf("lz4 compress: %v", err)
		}
		if n > 0 && n < len(block) {
			stream = append(stream, lz4JavaBlock(lz4JavaMethodLZ4, compressed[:n], block, xxh32(block, lz4JavaDefaultSeed)&0x0FFFFFFF)...)
		} else {
			stream = append(stream, lz4JavaBlock(lz4JavaMethodRaw, block, block, xxh32(block, lz4JavaDefaultSeed)&0x0FFFFFFF)...)
		}
		start = end
	}
	stream = append(stream, lz4JavaBlock(lz4JavaMethodRaw, nil, nil, 0)...)
	// The region's sector padding trails the end mark and must be ignored.
	stream = append(stream, make([]byte, 512)...)

	decoded, err := decodeLZ4JavaBlockStream(stream, maxDecompressedChunk)
	if err != nil {
		t.Fatalf("multi-block stream: %v", err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatalf("multi-block round trip mismatch: %d of %d bytes", len(decoded), len(data))
	}
}

func TestDecodeLZ4JavaBlockStreamRefusals(t *testing.T) {
	valid := lz4JavaEncode(t, bytes.Repeat([]byte("carbon"), 40))
	block := valid[:len(valid)-lz4JavaHeaderLen] // stream minus the end mark

	for _, tc := range []struct {
		name    string
		payload []byte
		limit   int
		want    error
	}{
		{"zero-limit", valid, 0, ErrTileBudget},
		{"empty", nil, maxDecompressedChunk, errLZ4StreamShort},
		{
			"bad-magic",
			append(append([]byte{}, block...), lz4JavaBlock(lz4JavaMethodRaw, nil, nil, 0)...),
			maxDecompressedChunk, errLZ4StreamCorrupt,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := tc.payload
			if tc.name == "bad-magic" {
				payload = append([]byte{}, tc.payload...)
				payload[3] = 'X'
			}
			if _, err := decodeLZ4JavaBlockStream(payload, tc.limit); !errors.Is(err, tc.want) {
				t.Fatalf("expected %v, got %v", tc.want, err)
			}
		})
	}

	corrupt := func(name string, mutate func(stream []byte) []byte, want error) {
		t.Run(name, func(t *testing.T) {
			stream := append([]byte{}, block...)
			stream = mutate(stream)
			stream = append(stream, lz4JavaBlock(lz4JavaMethodRaw, nil, nil, 0)...)
			if _, err := decodeLZ4JavaBlockStream(stream, maxDecompressedChunk); !errors.Is(err, want) {
				t.Fatalf("expected %v, got %v", want, err)
			}
		})
	}
	corrupt("bad-token", func(stream []byte) []byte { stream[len(lz4JavaMagic)] = 0x40; return stream }, errLZ4StreamCorrupt)
	corrupt("negative-length", func(stream []byte) []byte {
		binary.LittleEndian.PutUint32(stream[len(lz4JavaMagic)+5:], 0xFFFFFFF0)
		return stream
	}, errLZ4StreamCorrupt)
	corrupt("block-over-budget", func(stream []byte) []byte {
		binary.LittleEndian.PutUint32(stream[len(lz4JavaMagic)+5:], 1<<26)
		return stream
	}, errLZ4StreamCorrupt)
	corrupt("checksum-mismatch", func(stream []byte) []byte {
		stream[lz4JavaHeaderLen] ^= 0xFF
		return stream
	}, errLZ4StreamCorrupt)
	corrupt("truncated-data", func(stream []byte) []byte { return stream[:len(stream)-4] }, errLZ4StreamCorrupt)
	corrupt("end-mark-with-checksum", func(stream []byte) []byte {
		return append(stream, lz4JavaBlock(lz4JavaMethodRaw, nil, nil, 7)...)
	}, errLZ4StreamCorrupt)

	// A stream cut inside the last block's header is truncated, not corrupt.
	if _, err := decodeLZ4JavaBlockStream(block[:len(block)-4], maxDecompressedChunk); !errors.Is(err, errLZ4StreamShort) {
		t.Fatalf("truncated stream header: expected %v, got %v", errLZ4StreamShort, err)
	}
	// A stream whose end mark is missing never finishes.
	if _, err := decodeLZ4JavaBlockStream(block, maxDecompressedChunk); !errors.Is(err, errLZ4StreamShort) {
		t.Fatalf("missing end mark: expected %v, got %v", errLZ4StreamShort, err)
	}

	t.Run("output-budget", func(t *testing.T) {
		data := bytes.Repeat([]byte{9, 8, 7, 6}, 64)
		if _, err := decodeLZ4JavaBlockStream(lz4JavaEncode(t, data), len(data)-1); !errors.Is(err, ErrTileBudget) {
			t.Fatalf("expected budget refusal, got %v", err)
		}
	})
}

// TestRenderOverworldTileDecodesLZ4Chunk qualifies compression byte 4 at the
// tile seam: a region whose chunk is stored with the lz4-java block stream
// must render identically to the same chunk stored uncompressed.
func TestRenderOverworldTileDecodesLZ4Chunk(t *testing.T) {
	lz4Region := regionData(t, savedChunk{0, 0, chunkData(0, 0, 3955, "full"), 4, testSave})
	plainRegion := regionData(t, savedChunk{0, 0, chunkData(0, 0, 3955, "full"), 3, testSave})
	_, lz4Store := regionStore(t, "r.0.0.mca", lz4Region)
	_, plainStore := regionStore(t, "r.0.0.mca", plainRegion)
	lz4Tile, err := RenderOverworldTile(t.Context(), lz4Store, "fixture_world", TileCoord{})
	if err != nil {
		t.Fatalf("lz4 chunk did not render: %v", err)
	}
	plainTile, err := RenderOverworldTile(t.Context(), plainStore, "fixture_world", TileCoord{})
	if err != nil {
		t.Fatalf("uncompressed chunk did not render: %v", err)
	}
	if !bytes.Equal(lz4Tile.PNG, plainTile.PNG) {
		t.Fatal("lz4 decode did not reproduce the uncompressed tile")
	}
	if lz4Tile.RenderedChunks != 1 {
		t.Fatalf("unexpected coverage: %+v", lz4Tile)
	}
}
