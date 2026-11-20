// SPDX-License-Identifier: MPL-2.0

package worldterrain

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/athNdev/carbon-panel/internal/worldlib"
)

const (
	sectorBytes          = 4096
	regionChunks         = 32
	regionHeaderBytes    = 2 * sectorBytes
	maxCompressedChunk   = 512 << 10
	maxDecompressedChunk = 2 << 20
	maxCompressedTile    = 8 << 20
	maxDecompressedTile  = 24 << 20
)

type readBudget struct {
	compressed   int
	decompressed int
}

func readAt(r io.ReaderAt, buf []byte, offset int64) error {
	n, err := r.ReadAt(buf, offset)
	if n == len(buf) && (err == nil || errors.Is(err, io.EOF)) {
		return nil
	}
	if err == nil {
		return io.ErrUnexpectedEOF
	}
	return err
}

// readSurface reads the selected sector range only. The first region sector
// contains the 1024 packed locations and the second their Unix timestamps.
func readSurface(ctx context.Context, r io.ReaderAt, entry []byte, budget *readBudget, decode func([]byte) (worldlib.JavaChunkSurface, error)) (worldlib.JavaChunkSurface, error) {
	var result worldlib.JavaChunkSurface
	location := binary.BigEndian.Uint32(entry)
	first, count := int64(location>>8), int(location&0xff)
	if first < 2 || count == 0 {
		return result, errors.New("invalid Anvil chunk sector location")
	}
	var header [5]byte
	offset := first * sectorBytes
	if err := readAt(r, header[:], offset); err != nil {
		return result, fmt.Errorf("read Anvil chunk header: %w", err)
	}
	size := int64(binary.BigEndian.Uint32(header[:4])) - 1
	if size < 0 || size > int64(count*sectorBytes-5) {
		return result, errors.New("invalid Anvil chunk length")
	}
	if header[4]&0x80 != 0 || header[4] < 1 || header[4] > 3 {
		return result, worldlib.ErrJavaChunkUnsupported
	}
	if size == 0 {
		return result, errors.New("empty Anvil chunk payload")
	}
	if size > maxCompressedChunk {
		return result, errors.New("compressed Anvil chunk exceeds the size limit")
	}
	if int(size) > maxCompressedTile-budget.compressed || budget.decompressed >= maxDecompressedTile {
		return result, ErrTileBudget
	}
	budget.compressed += int(size)
	packed := make([]byte, size)
	if err := readAt(r, packed, offset+5); err != nil {
		return result, fmt.Errorf("read Anvil chunk payload: %w", err)
	}
	raw, err := unpackChunk(ctx, header[4], packed, budget)
	if err != nil {
		return result, err
	}
	return decode(raw)
}

func unpackChunk(ctx context.Context, compression byte, packed []byte, budget *readBudget) ([]byte, error) {
	var data io.Reader = bytes.NewReader(packed)
	if compression == 1 || compression == 2 {
		var (
			decoded io.ReadCloser
			err     error
		)
		if compression == 1 {
			decoded, err = gzip.NewReader(data)
		} else {
			decoded, err = zlib.NewReader(data)
		}
		if err != nil {
			return nil, fmt.Errorf("open Anvil chunk compression: %w", err)
		}
		defer func() { _ = decoded.Close() }()
		data = decoded
	}
	remaining := maxDecompressedTile - budget.decompressed
	limit := min(maxDecompressedChunk, remaining)
	raw, err := io.ReadAll(io.LimitReader(cancelReader{ctx: ctx, source: data}, int64(limit)+1))
	budget.decompressed += len(raw)
	if err != nil {
		return nil, fmt.Errorf("decode Anvil chunk: %w", err)
	}
	if len(raw) > remaining {
		return nil, ErrTileBudget
	}
	if len(raw) > maxDecompressedChunk {
		return nil, errors.New("decompressed Anvil chunk exceeds the size limit")
	}
	return raw, nil
}

type cancelReader struct {
	ctx    context.Context
	source io.Reader
}

func (r cancelReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}
