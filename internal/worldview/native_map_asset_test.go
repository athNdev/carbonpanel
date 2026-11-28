// SPDX-License-Identifier: MPL-2.0

package worldview

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureChunkNBT assembles a minimal modern chunk for the given vertical
// layout: DataVersion, position, yPos, Status and one packed WORLD_SURFACE
// heightmap at the span's bit width.
func fixtureChunkNBT(x, z, version int32, status string, minY, maxY int) []byte {
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
	}{{"DataVersion", version}, {"xPos", x}, {"yPos", int32(minY / 16)}, {"zPos", z}} {
		tag(3, field.name)
		write(field.value)
	}
	tag(8, "Status")
	str(status)
	tag(10, "Heightmaps")
	tag(12, "WORLD_SURFACE")
	heightSpan := maxY - minY + 1
	bits := 0
	for v := heightSpan; v > 0; v >>= 1 {
		bits++
	}
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

// fixtureRegion packs chunks into an Anvil region file (zlib payload).
func fixtureRegion(t *testing.T, chunks map[[2]int32][]byte) []byte {
	t.Helper()
	const sectorBytes = 4096
	result := make([]byte, 2*sectorBytes)
	for slot, raw := range chunks {
		var packed bytes.Buffer
		writer := zlib.NewWriter(&packed)
		if _, err := writer.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		size := packed.Len() + 5
		sectors := (size + sectorBytes - 1) / sectorBytes
		index := (int(slot[1])*32 + int(slot[0])) * 4
		start := len(result)
		binary.BigEndian.PutUint32(result[index:index+4], uint32(start/sectorBytes<<8|sectors))
		binary.BigEndian.PutUint32(result[index+sectorBytes:index+sectorBytes+4], 1_779_000_000)
		result = append(result, make([]byte, sectors*sectorBytes)...)
		binary.BigEndian.PutUint32(result[start:start+4], uint32(size-4))
		result[start+4] = 2
		copy(result[start+5:], packed.Bytes())
	}
	return result
}

// writeFixtureRegion stores a region file under the level folder of a native
// fixture server.
func writeFixtureRegion(t *testing.T, service *Service, serverID, relative string, data []byte) {
	t.Helper()
	f := service.deps.Store.(*nativeScopeFixture)
	server, err := f.GetServer(context.Background(), serverID)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(server.DataPath, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestNativeAssetServesDimensionsAndFreshnessFixture drives the native asset
// transport end to end against region files on controller-local storage: the
// manifest discovers offered dimensions, tiles carry the region freshness
// validator, and a conditional request answers 304 without re-rendering.
func TestNativeAssetServesDimensionsAndFreshnessFixture(t *testing.T) {
	service := nativeProbeServiceFixture(t, 1)
	ctx := svcCtx("native-viewer", "user")
	const serverID = "native-server-0"
	f := service.deps.Store.(*nativeScopeFixture)
	server, err := f.GetServer(ctx, serverID)
	if err != nil {
		t.Fatal(err)
	}
	const level = "actual-world"
	writeFixtureRegion(t, service, serverID, filepath.Join(level, "region", "r.0.0.mca"),
		fixtureRegion(t, map[[2]int32][]byte{{0, 0}: fixtureChunkNBT(0, 0, 3953, "full", -64, 319)}))
	writeFixtureRegion(t, service, serverID, filepath.Join(level, "DIM-1", "region", "r.0.0.mca"),
		fixtureRegion(t, map[[2]int32][]byte{{0, 0}: fixtureChunkNBT(0, 0, 4903, "full", 0, 255)}))

	native := service.deps.Native
	scope, err := resolveNativeScope(ctx, f, server, nil)
	if err != nil {
		t.Fatalf("fixture scope: %v", err)
	}
	serve := func(asset string, mutate func(*http.Request)) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, ProxyPrefix+"key/"+asset, nil)
		if mutate != nil {
			mutate(request)
		}
		native.serveAsset(recorder, request, server, scope, asset)
		return recorder
	}

	manifestResponse := serve(nativeManifestPath, nil)
	if manifestResponse.Code != http.StatusOK {
		t.Fatalf("manifest: %d %s", manifestResponse.Code, manifestResponse.Body)
	}
	var manifest struct {
		TileBlocks int      `json:"tileBlocks"`
		Dimension  string   `json:"dimension"`
		Dimensions []string `json:"dimensions"`
	}
	if err := json.Unmarshal(manifestResponse.Body.Bytes(), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.TileBlocks != 128 || manifest.Dimension != "overworld" {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	if len(manifest.Dimensions) != 2 || manifest.Dimensions[0] != "overworld" || manifest.Dimensions[1] != "nether" {
		t.Fatalf("manifest did not discover the offered dimensions: %+v", manifest)
	}

	tileResponse := serve("native/overworld/0/0.png", nil)
	if tileResponse.Code != http.StatusOK {
		t.Fatalf("overworld tile: %d %s", tileResponse.Code, tileResponse.Body)
	}
	if got := tileResponse.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("tile content type %q", got)
	}
	validator := tileResponse.Header().Get(regionModifiedHeader)
	if validator == "" {
		t.Fatal("tile response carried no region freshness validator")
	}
	if tileResponse.Body.Len() == 0 {
		t.Fatal("tile response carried no body")
	}

	conditional := serve("native/overworld/0/0.png", func(request *http.Request) {
		request.Header.Set("If-None-Match", validator)
	})
	if conditional.Code != http.StatusNotModified {
		t.Fatalf("conditional tile with the current validator: %d %s", conditional.Code, conditional.Body)
	}
	if conditional.Body.Len() != 0 {
		t.Fatalf("a 304 must not carry a body: %d bytes", conditional.Body.Len())
	}
	if got := conditional.Header().Get(regionModifiedHeader); got != validator {
		t.Fatalf("304 validator %q != %q", got, validator)
	}

	stale := serve("native/overworld/0/0.png", func(request *http.Request) {
		request.Header.Set("If-None-Match", `"0-0"`)
	})
	if stale.Code != http.StatusOK || stale.Body.Len() == 0 {
		t.Fatalf("stale validator must re-render: %d", stale.Code)
	}

	netherTile := serve("native/nether/0/0.png", nil)
	if netherTile.Code != http.StatusOK {
		t.Fatalf("nether tile: %d %s", netherTile.Code, netherTile.Body)
	}
	if netherTile.Header().Get(regionModifiedHeader) == "" {
		t.Fatal("nether tile carried no freshness validator")
	}

	// A dimension whose region folder does not exist stays a missing tile.
	if end := serve("native/end/0/0.png", nil); end.Code != http.StatusNotFound {
		t.Fatalf("end tile without DIM1: %d %s", end.Code, end.Body)
	}

	// A matching validator must not answer 304 when the world scope changes
	// after the tile was issued: the scope check has to run on every response
	// path, not only after a render. Mutate the level during this request's
	// first scope resolution, after its config read, so the issued scope still
	// matches while the re-check sees the replacement world.
	readsBefore := f.worldBindingReads
	f.onWorldBindingRead = func(read int) {
		if read == readsBefore+1 {
			f.setLevelName(serverID, "replacement-world")
		}
	}
	changed := serve("native/overworld/0/0.png", func(request *http.Request) {
		request.Header.Set("If-None-Match", validator)
	})
	f.onWorldBindingRead = nil
	if changed.Code != http.StatusConflict {
		t.Fatalf("matching validator after a scope change must not answer 304: %d %s", changed.Code, changed.Body)
	}
	if !strings.Contains(changed.Body.String(), "world changed; reopen the map") {
		t.Fatalf("scope-change refusal = %q", changed.Body)
	}
}
