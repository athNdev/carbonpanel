// SPDX-License-Identifier: MPL-2.0

package worldterrain

import (
	"encoding/binary"
	"errors"

	lz4 "github.com/pierrec/lz4/v4"
)

// Minecraft 1.20.5+ writes region-file-compression=lz4 chunks (Anvil
// compression byte 4) as the block stream of the lz4-java library
// (net.jpountz.lz4.LZ4BlockOutputStream). This is lz4-java's own framing,
// not the standard LZ4 frame format: a magic-prefixed header precedes every
// block, and the checksums are XXH32 of the original data.
//
// Per block: "LZ4Block" magic (8 bytes), a token byte whose high nibble is
// the compression method (0x10 raw, 0x20 LZ4) and low nibble the compression
// level, then three little-endian uint32 words (compressed length, original
// length, checksum). The stream ends with an empty block whose lengths and
// checksum are zero, followed by the sector's zero padding.

const (
	lz4JavaMagic       = "LZ4Block"
	lz4JavaHeaderLen   = len(lz4JavaMagic) + 13
	lz4JavaMethodRaw   = 0x10
	lz4JavaMethodLZ4   = 0x20
	lz4JavaLevelBase   = 10
	lz4JavaMaxBlockLen = 1 << 25 // 32 MiB, the Java writer's largest block size
	lz4JavaDefaultSeed = 0x9747b28c
)

var (
	errLZ4StreamCorrupt = errors.New("corrupt lz4 chunk stream")
	errLZ4StreamShort   = errors.New("truncated lz4 chunk stream")
)

// decodeLZ4JavaBlockStream unpacks the framed payload into at most limit
// bytes. A stream that exceeds the limit is refused with ErrTileBudget so a
// hostile or damaged region cannot allocate past the tile budget.
func decodeLZ4JavaBlockStream(payload []byte, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, ErrTileBudget
	}
	out := make([]byte, 0, min(limit, maxDecompressedChunk))
	for offset := 0; ; {
		if len(payload)-offset < lz4JavaHeaderLen {
			return nil, errLZ4StreamShort
		}
		header := payload[offset : offset+lz4JavaHeaderLen]
		if string(header[:len(lz4JavaMagic)]) != lz4JavaMagic {
			return nil, errLZ4StreamCorrupt
		}
		token := header[len(lz4JavaMagic)]
		level := lz4JavaLevelBase + int(token&0x0F)
		switch token & 0xF0 {
		case lz4JavaMethodRaw, lz4JavaMethodLZ4:
		default:
			return nil, errLZ4StreamCorrupt
		}
		compressedLen := int(int32(binary.LittleEndian.Uint32(header[len(lz4JavaMagic)+1:])))
		originalLen := int(int32(binary.LittleEndian.Uint32(header[len(lz4JavaMagic)+5:])))
		check := binary.LittleEndian.Uint32(header[len(lz4JavaMagic)+9:])
		offset += lz4JavaHeaderLen
		if compressedLen < 0 || originalLen < 0 || originalLen > 1<<level ||
			originalLen > lz4JavaMaxBlockLen ||
			(originalLen == 0) != (compressedLen == 0) {
			return nil, errLZ4StreamCorrupt
		}
		if originalLen == 0 {
			if check != 0 || len(out) == 0 {
				return nil, errLZ4StreamCorrupt
			}
			return out, nil
		}
		if compressedLen > len(payload)-offset {
			return nil, errLZ4StreamShort
		}
		if len(out)+originalLen > limit {
			return nil, ErrTileBudget
		}
		start := len(out)
		out = out[:start+originalLen]
		switch token & 0xF0 {
		case lz4JavaMethodRaw:
			if compressedLen != originalLen {
				return nil, errLZ4StreamCorrupt
			}
			copy(out[start:], payload[offset:offset+compressedLen])
		case lz4JavaMethodLZ4:
			n, err := lz4.UncompressBlock(payload[offset:offset+compressedLen], out[start:])
			if err != nil || n != originalLen {
				return nil, errLZ4StreamCorrupt
			}
		}
		// Java's LZ4BlockOutputStream stores XXH32 masked to the low 28 bits
		// (StreamingXXHash32.asChecksum), so accept the masked value.
		if xxh32(out[start:], lz4JavaDefaultSeed)&0x0FFFFFFF != check {
			return nil, errLZ4StreamCorrupt
		}
		offset += compressedLen
	}
}

// unpackLZ4Chunk decodes one LZ4 region chunk within the existing tile
// budget. Compression is detected per chunk, so mixed regions still render.
func unpackLZ4Chunk(packed []byte, budget *readBudget) ([]byte, error) {
	remaining := maxDecompressedTile - budget.decompressed
	limit := min(maxDecompressedChunk, remaining)
	raw, err := decodeLZ4JavaBlockStream(packed, limit)
	if err != nil {
		return nil, err
	}
	budget.decompressed += len(raw)
	return raw, nil
}
