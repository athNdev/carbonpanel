// SPDX-License-Identifier: MPL-2.0

package worldterrain

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
)

const testSave = 1_779_000_000

type savedChunk struct {
	slotX, slotZ int
	raw          []byte
	compression  byte
	savedAt      uint32
}

func chunkData(x, z, version int32, status string) []byte {
	var b bytes.Buffer
	write := func(v any) { _ = binary.Write(&b, binary.BigEndian, v) }
	str := func(s string) {
		write(uint16(len(s)))
		b.WriteString(s)
	}
	tag := func(kind byte, name string) {
		b.WriteByte(kind)
		str(name)
	}
	tag(10, "")
	for _, field := range []struct {
		name  string
		value int32
	}{{"DataVersion", version}, {"xPos", x}, {"yPos", -4}, {"zPos", z}} {
		tag(3, field.name)
		write(field.value)
	}
	tag(8, "Status")
	str(status)
	tag(10, "Heightmaps")
	tag(12, "WORLD_SURFACE")
	var packed [37]uint64
	for i := range 256 {
		// The stored height is one above the highest block relative to -64.
		// Distinct horizontal and vertical gradients establish orientation.
		height := uint64(1 + (i%16)*7 + (i/16)*13)
		packed[i/7] |= height << ((i % 7) * 9)
	}
	write(int32(len(packed)))
	for _, value := range packed {
		write(value)
	}
	b.Write([]byte{0, 0})
	return b.Bytes()
}

func regionData(t *testing.T, chunks ...savedChunk) []byte {
	t.Helper()
	result := make([]byte, regionHeaderBytes)
	for _, chunk := range chunks {
		var packed bytes.Buffer
		var writer io.WriteCloser
		switch chunk.compression {
		case 1:
			writer = gzip.NewWriter(&packed)
		case 2:
			writer = zlib.NewWriter(&packed)
		default:
			_, _ = packed.Write(chunk.raw)
		}
		if writer != nil {
			if _, err := writer.Write(chunk.raw); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
		}
		size := packed.Len() + 5
		sectors := (size + sectorBytes - 1) / sectorBytes
		index := (chunk.slotZ*regionChunks + chunk.slotX) * 4
		start := len(result)
		binary.BigEndian.PutUint32(result[index:index+4], uint32(start/sectorBytes<<8|sectors))
		binary.BigEndian.PutUint32(result[index+sectorBytes:index+sectorBytes+4], chunk.savedAt)
		result = append(result, make([]byte, sectors*sectorBytes)...)
		binary.BigEndian.PutUint32(result[start:start+4], uint32(size-4))
		result[start+4] = chunk.compression
		copy(result[start+5:], packed.Bytes())
	}
	return result
}

func regionStore(t *testing.T, fileName string, data []byte) (string, ReadStorage) {
	t.Helper()
	root := t.TempDir()
	region := filepath.Join(root, "fixture_world", "region")
	if err := os.MkdirAll(region, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(region, fileName)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := nodestorage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return file, store
}

func TestRenderOverworldTileFromConfinedRegion(t *testing.T) {
	original := regionData(t,
		savedChunk{24, 24, chunkData(-8, -8, 3955, "minecraft:full"), 2, testSave},
		savedChunk{25, 24, chunkData(-7, -8, 4440, "full"), 1, testSave + 10},
		savedChunk{26, 24, chunkData(-6, -8, 2730, "full"), 3, testSave + 30},
		savedChunk{27, 24, chunkData(-5, -8, 3955, "minecraft:noise"), 2, testSave + 50},
		savedChunk{28, 24, chunkData(33, 32, 3955, "full"), 2, testSave + 70},
		savedChunk{29, 24, nil, 0x82, testSave + 80},
	)
	file, store := regionStore(t, "r.-1.-1.mca", original)
	coord := TileCoord{X: -1, Z: -1}
	tile, err := RenderOverworldTile(t.Context(), store, "fixture_world", coord)
	if err != nil {
		t.Fatal(err)
	}
	if tile.RenderedChunks != 2 || tile.MissingChunks != 59 ||
		tile.UnsupportedChunks != 2 || tile.UnreadableChunks != 1 || tile.TimestampedChunks != 2 ||
		!tile.OldestSavedAt.Equal(time.Unix(testSave, 0)) || !tile.NewestSavedAt.Equal(time.Unix(testSave+10, 0)) {
		t.Fatalf("unexpected observed coverage: %+v", tile)
	}
	img, err := png.Decode(bytes.NewReader(tile.PNG))
	if err != nil {
		t.Fatal(err)
	}
	if img.Bounds().Dx() != TileBlocks || img.Bounds().Dy() != TileBlocks {
		t.Fatalf("unexpected tile size %s", img.Bounds())
	}
	base, across, down := img.At(0, 0), img.At(15, 0), img.At(0, 15)
	if base == across || base == down || across == down {
		t.Fatal("the tile did not preserve terrain height variation and orientation")
	}
	for _, x := range []int{0, 16, 32, 48, 64, 80, 96} {
		_, _, _, alpha := img.At(x, 0).RGBA()
		if (x < 32) != (alpha != 0) {
			t.Fatalf("opaque coverage at %d = %d", x, alpha)
		}
	}
	again, err := RenderOverworldTile(t.Context(), store, "fixture_world", coord)
	if err != nil || !bytes.Equal(tile.PNG, again.PNG) {
		t.Fatalf("same saved tile did not reproduce bit-for-bit: %v", err)
	}
	after, err := os.ReadFile(file)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("region changed after render: %v", err)
	}
}

func TestRenderOverworldTileRefusals(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"no-saved-chunks", regionData(t), ErrTerrainMissing},
		{"unsupported-version", regionData(t, savedChunk{0, 0, chunkData(0, 0, 4555, "full"), 3, 0}), ErrTerrainUnsupported},
		{"unsupported-compression", regionData(t, savedChunk{0, 0, []byte{1}, 4, 0}), ErrTerrainUnsupported},
		{"external-chunk", regionData(t, savedChunk{0, 0, nil, 0x82, 0}), ErrTerrainUnsupported},
		{"malformed-NBT", regionData(t, savedChunk{0, 0, []byte{10, 0}, 3, 0}), ErrTerrainUnreadable},
		{"truncated-header", []byte{0}, ErrTerrainUnreadable},
		{"truncated-sector", regionData(t, savedChunk{0, 0, []byte{1}, 3, 0})[:regionHeaderBytes], ErrTerrainUnreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store := regionStore(t, "r.0.0.mca", tc.data)
			tile, err := RenderOverworldTile(t.Context(), store, "fixture_world", TileCoord{})
			if !errors.Is(err, tc.want) || len(tile.PNG) != 0 {
				t.Fatalf("expected typed refusal %v, got %v with %d bytes", tc.want, err, len(tile.PNG))
			}
		})
	}

	_, store := regionStore(t, "r.0.0.mca", regionData(t))
	if _, err := RenderOverworldTile(t.Context(), store, "fixture_world", TileCoord{X: worldLimit}); !errors.Is(err, ErrTileCoordinate) {
		t.Fatalf("out of bounds: %v", err)
	}
	if _, err := RenderOverworldTile(t.Context(), store, "../other-world", TileCoord{}); !errors.Is(err, ErrTileLevel) {
		t.Fatalf("escaped world name: %v", err)
	}
	if _, err := RenderOverworldTile(t.Context(), store, "fixture_world", TileCoord{X: -1}); !errors.Is(err, ErrTerrainMissing) {
		t.Fatalf("missing region: %v", err)
	}
}

func TestSavedSurfaceOnUnfinishedChunks(t *testing.T) {
	early := chunkData(0, 0, 3955, "minecraft:biomes")
	carvers := chunkData(1, 0, 3955, "minecraft:carvers")
	light := chunkData(2, 0, 3955, "minecraft:initialize_light")
	wg := bytes.Replace(chunkData(3, 0, 3955, "surface"), []byte("\x00\x0dWORLD_SURFACE"), []byte("\x00\x10WORLD_SURFACE_WG"), 1)
	noHeight := bytes.Replace(chunkData(4, 0, 3955, "features"), []byte("WORLD_SURFACE"), []byte("OTHER_SURFACE"), 1)
	full := chunkData(5, 0, 3955, "full")
	fixture := [][]byte{early, carvers, light, wg, noHeight, full}
	var chunks []savedChunk
	for x, raw := range fixture {
		chunks = append(chunks, savedChunk{x, 0, raw, 2, testSave})
	}
	_, store := regionStore(t, "r.0.0.mca", regionData(t, chunks...))
	tile, err := RenderOverworldTile(t.Context(), store, "fixture_world", TileCoord{})
	if err != nil || tile.RenderedChunks != 4 || tile.ProvisionalChunks != 3 || tile.MissingChunks != 60 {
		t.Fatalf("saved surface counts: rendered %d, provisional %d, missing %d, err %v", tile.RenderedChunks, tile.ProvisionalChunks, tile.MissingChunks, err)
	}
	img, err := png.Decode(bytes.NewReader(tile.PNG))
	if err != nil {
		t.Fatal(err)
	}
	for i, alpha := range []uint8{0, 176, 176, 176, 0, 255} {
		if got := color.NRGBAModel.Convert(img.At(i*chunkBlocks, 0)).(color.NRGBA).A; got != alpha {
			t.Fatalf("chunk %d alpha = %d, want %d", i, got, alpha)
		}
	}
}

type inMemoryRegion struct {
	*bytes.Reader
	headerReads int
	changeIndex int
	cancel      context.CancelFunc
}

func (r *inMemoryRegion) ReadAt(p []byte, offset int64) (int, error) {
	n, err := r.Reader.ReadAt(p, offset)
	if offset == 0 {
		r.headerReads++
		if r.headerReads == 2 && n == regionHeaderBytes && r.changeIndex >= 0 {
			p[r.changeIndex] ^= 1
		}
		if r.cancel != nil {
			r.cancel()
		}
	}
	return n, err
}

func (*inMemoryRegion) Close() error { return nil }

type openFunc func(context.Context, string) (storageworker.ReadHandle, error)

func (f openFunc) OpenRead(ctx context.Context, rel string) (storageworker.ReadHandle, error) {
	return f(ctx, rel)
}

func TestRenderOverworldTileRejectsChangingSaveAndHonorsCancellation(t *testing.T) {
	data := regionData(t, savedChunk{0, 0, chunkData(0, 0, 3955, "full"), 2, testSave})
	run := func(ctx context.Context, r *inMemoryRegion) (Tile, error) {
		return RenderOverworldTile(ctx, openFunc(func(_ context.Context, rel string) (storageworker.ReadHandle, error) {
			if rel != "fixture_world/region/r.0.0.mca" {
				t.Fatalf("unexpected relative path %s", rel)
			}
			return r, nil
		}), "fixture_world", TileCoord{})
	}
	if tile, err := run(t.Context(), &inMemoryRegion{Reader: bytes.NewReader(data), changeIndex: sectorBytes}); !errors.Is(err, ErrRegionChanged) || len(tile.PNG) > 0 {
		t.Fatalf("changing selected chunk: %v", err)
	}
	if _, err := run(t.Context(), &inMemoryRegion{Reader: bytes.NewReader(data), changeIndex: sectorBytes + 31*4}); err != nil {
		t.Fatalf("save elsewhere in same region should not invalidate the tile: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := run(ctx, &inMemoryRegion{Reader: bytes.NewReader(data), changeIndex: -1, cancel: cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: %v", err)
	}
}

func TestRenderOverworldTileBoundsAggregateWork(t *testing.T) {
	chunks := make([]savedChunk, 17)
	raw := make([]byte, maxCompressedChunk)
	for i := range chunks {
		chunks[i] = savedChunk{i % chunksPerTile, i / chunksPerTile, raw, 3, 0}
	}
	_, store := regionStore(t, "r.0.0.mca", regionData(t, chunks...))
	tile, err := RenderOverworldTile(t.Context(), store, "fixture_world", TileCoord{})
	if !errors.Is(err, ErrTileBudget) || len(tile.PNG) > 0 {
		t.Fatalf("expected aggregate limit, got %v with %d bytes", err, len(tile.PNG))
	}
}

// TestRenderOverworldTileGameSave optionally qualifies a disposable save
// generated by the real game. The repository contains no world or game file.
func TestRenderOverworldTileGameSave(t *testing.T) {
	root, level := os.Getenv("CARBON_TERRAIN_FIXTURE_ROOT"), os.Getenv("CARBON_TERRAIN_FIXTURE_LEVEL")
	if root == "" || level == "" {
		t.Skip("real save qualification requires CARBON_TERRAIN_FIXTURE_ROOT and CARBON_TERRAIN_FIXTURE_LEVEL")
	}
	store, err := nodestorage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()

	canvas := image.NewNRGBA(image.Rect(0, 0, 3*TileBlocks, 3*TileBlocks))
	chunks, provisional, timestamps := 0, 0, 0
	for z := -1; z <= 1; z++ {
		for x := -1; x <= 1; x++ {
			tile, err := RenderOverworldTile(t.Context(), store, level, TileCoord{X: x, Z: z})
			if errors.Is(err, ErrTerrainMissing) {
				continue
			}
			if err != nil {
				t.Fatalf("tile (%d, %d): %v", x, z, err)
			}
			if tile.UnreadableChunks != 0 || tile.UnsupportedChunks != 0 {
				t.Fatalf("tile (%d, %d) unreadable=%d unsupported=%d", x, z, tile.UnreadableChunks, tile.UnsupportedChunks)
			}
			chunks += tile.RenderedChunks
			provisional += tile.ProvisionalChunks
			timestamps += tile.TimestampedChunks
			img, err := png.Decode(bytes.NewReader(tile.PNG))
			if err != nil {
				t.Fatal(err)
			}
			origin := image.Pt((x+1)*TileBlocks, (z+1)*TileBlocks)
			draw.Draw(canvas, img.Bounds().Add(origin), img, image.Point{}, draw.Src)
		}
	}
	colors := make(map[color.NRGBA]struct{})
	for y := 0; y < canvas.Bounds().Dy(); y++ {
		for x := 0; x < canvas.Bounds().Dx(); x++ {
			p := canvas.NRGBAAt(x, y)
			if p.A != 0 {
				colors[p] = struct{}{}
			}
		}
	}
	if chunks == 0 || timestamps != chunks || len(colors) < 8 {
		t.Fatalf("real save yielded %d chunks, %d timestamps and %d colors", chunks, timestamps, len(colors))
	}
	t.Logf("qualified %d saved chunks (%d provisional) with %d timestamps and %d terrain colors", chunks, provisional, timestamps, len(colors))

	if output := os.Getenv("CARBON_TERRAIN_FIXTURE_PNG"); output != "" {
		// The qualification operator supplies a separate artifact path.
		var out bytes.Buffer
		if err := png.Encode(&out, canvas); err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write(out.Bytes())
		if err := errors.Join(writeErr, file.Close()); err != nil {
			t.Fatal(err)
		}
	}
}
