// SPDX-License-Identifier: MPL-2.0

package worldterrain

import "encoding/binary"

// xxh32 implements the XXH32 hash from the xxHash specification
// (https://github.com/Cyan4973/xxHash/blob/dev/doc/xxhash_spec.md). The
// lz4-java block stream that Minecraft writes for LZ4 region chunks frames
// every block with an XXH32 checksum of the original data, so the decoder
// needs the exact algorithm rather than a stand-in checksum.
func xxh32(data []byte, seed uint32) uint32 {
	const (
		prime1 = 2654435761
		prime2 = 2246822519
		prime3 = 3266489917
		prime4 = 668265263
		prime5 = 374761393
	)
	// The total length participates in the mix after convergence, so capture
	// it before the stripe loop consumes the slice.
	length := uint32(len(data))
	var h uint32
	if len(data) >= 16 {
		v1 := seed + prime1 + prime2
		v2 := seed + prime2
		v3 := seed
		v4 := seed - prime1
		for ; len(data) >= 16; data = data[16:] {
			v1 = rotl32(v1+binary.LittleEndian.Uint32(data[0:])*prime2, 13) * prime1
			v2 = rotl32(v2+binary.LittleEndian.Uint32(data[4:])*prime2, 13) * prime1
			v3 = rotl32(v3+binary.LittleEndian.Uint32(data[8:])*prime2, 13) * prime1
			v4 = rotl32(v4+binary.LittleEndian.Uint32(data[12:])*prime2, 13) * prime1
		}
		h = rotl32(v1, 1) + rotl32(v2, 7) + rotl32(v3, 12) + rotl32(v4, 18)
	} else {
		h = seed + prime5
	}
	h += length
	for ; len(data) >= 4; data = data[4:] {
		h += binary.LittleEndian.Uint32(data) * prime3
		h = rotl32(h, 17) * prime4
	}
	for ; len(data) > 0; data = data[1:] {
		h += uint32(data[0]) * prime5
		h = rotl32(h, 11) * prime1
	}
	h ^= h >> 15
	h *= prime2
	h ^= h >> 13
	h *= prime3
	h ^= h >> 16
	return h
}

func rotl32(x uint32, r uint) uint32 { return x<<r | x>>(32-r) }
