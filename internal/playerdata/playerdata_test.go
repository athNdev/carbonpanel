// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerdata

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	db "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
)

const (
	testUUID     = "00000000-0000-0000-0000-000000000001"
	testUUIDFile = "00000000-0000-0000-0000-000000000001" // Minecraft playerdata/stats/advancements use dashed UUID filenames.
)

func localSetup(t *testing.T) (*Reader, string) {
	t.Helper()
	dir := t.TempDir()
	return NewReader(nil, nodestorage.NewResolver(nil)), dir
}

func writeRel(t *testing.T, dir, rel string, data []byte) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func stoppedServer(dir string) *db.Server {
	return &db.Server{ID: "srv-1", NodeID: "local", DataPath: dir, Status: db.StatusStopped}
}

func runningServer(dir string) *db.Server {
	return &db.Server{ID: "srv-1", NodeID: "local", DataPath: dir, Status: db.StatusRunning}
}

func fieldNamed(fields []Field, name string) (Field, bool) {
	for _, f := range fields {
		if f.Name == name {
			return f, true
		}
	}
	return Field{}, false
}

// ---------------------------------------------------------------------------
// stats JSON
// ---------------------------------------------------------------------------

func TestReadStatsFields(t *testing.T) {
	r, dir := localSetup(t)
	stats := `{
	  "DataVersion": 3953,
	  "stats": {
	    "minecraft:custom": {
	      "minecraft:play_time": 3723,
	      "minecraft:deaths": 4,
	      "minecraft:jump": 27,
	      "minecraft:walk_distance": 12345,
	      "minecraft:fly_distance": 500
	    },
	    "minecraft:item": {
	      "minecraft:dirt": 12,
	      "minecraft:diamond": 3
	    },
	    "minecraft:entity": {
	      "minecraft:zombie": 5
	    }
	  }
	}`
	writeRel(t, dir, "stats/"+testUUIDFile+".json", []byte(stats))

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindStats)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, tc := range []struct{ name, value, unit string }{
		{"data_version", "3953", ""},
		{"play_time", "3m6s", ""},
		{"deaths", "4", "count"},
		{"jump", "27", "count"},
		{"walk_distance", "123.45", "m"},
		{"fly_distance", "5.00", "m"},
	} {
		f, ok := fieldNamed(res.Fields, tc.name)
		if !ok {
			t.Fatalf("missing field %q; got %+v", tc.name, res.Fields)
		}
		if f.Value != tc.value || f.Unit != tc.unit {
			t.Errorf("field %s = %q/%q, want %q/%q", tc.name, f.Value, f.Unit, tc.value, tc.unit)
		}
		if f.Source != SourceOnDiskJSON || f.Confidence != ConfidenceDetected {
			t.Errorf("field %s provenance = %s/%s", tc.name, f.Source, f.Confidence)
		}
		if f.ObservedAt.IsZero() {
			t.Errorf("field %s has zero ObservedAt", tc.name)
		}
	}
	items, ok := fieldNamed(res.Fields, "item_stats")
	if !ok || !strings.Contains(items.Value, "dirt=12") || !strings.Contains(items.Value, "diamond=3") {
		t.Errorf("item_stats collapsed field = %+v", items)
	}
	if ents, ok := fieldNamed(res.Fields, "entity_stats"); !ok || !strings.Contains(ents.Value, "zombie=5") {
		t.Errorf("entity_stats collapsed field = %+v", ents)
	}
	if !strings.Contains(res.SnapshotNote, "safe snapshot") {
		t.Errorf("stopped snapshot note = %q", res.SnapshotNote)
	}
}

// ---------------------------------------------------------------------------
// advancements JSON
// ---------------------------------------------------------------------------

func TestReadAdvancementsFields(t *testing.T) {
	r, dir := localSetup(t)
	data := `{
	  "minecraft:story/root": {
	    "done": true,
	    "criteria": {"tick": [2023, 5, 12, 9, 30, 0]}
	  },
	  "minecraft:story/mine_stone": {
	    "done": false,
	    "criteria": {
	      "a": [2023, 5, 12, 9, 35, 0],
	      "b": []
	    }
	  }
	}`
	writeRel(t, dir, "advancements/"+testUUIDFile+".json", []byte(data))

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindAdvancements)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	root, ok := fieldNamed(res.Fields, "advancement_minecraft:story/root")
	if !ok || !strings.Contains(root.Value, "done at 2023-05-12T09:30:00Z") {
		t.Errorf("root advancement = %+v", root)
	}
	mine, ok := fieldNamed(res.Fields, "advancement_minecraft:story/mine_stone")
	if !ok || !strings.Contains(mine.Value, "1/2 criteria") {
		t.Errorf("mine_stone advancement = %+v", mine)
	}
	summary, ok := fieldNamed(res.Fields, "advancements_summary")
	if !ok || summary.Value != "1 completed / 2 recorded" {
		t.Errorf("summary = %+v", summary)
	}
}

// ---------------------------------------------------------------------------
// malformed JSON
// ---------------------------------------------------------------------------

func TestMalformedJSONReturnsUnknownField(t *testing.T) {
	for _, kind := range []string{KindStats, KindAdvancements} {
		t.Run(kind, func(t *testing.T) {
			r, dir := localSetup(t)
			ext := ""
			base := "stats"
			if kind == KindAdvancements {
				base = "advancements"
			}
			writeRel(t, dir, base+"/"+testUUIDFile+".json", []byte(`{"stats": {"x": `+ext))
			res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, kind)
			if err != nil {
				t.Fatalf("Read should not fail for malformed JSON: %v", err)
			}
			if len(res.Fields) != 1 || res.Fields[0].Confidence != ConfidenceUnknown {
				t.Fatalf("expected one unknown field, got %+v", res.Fields)
			}
			if !strings.Contains(res.Fields[0].ConflictNote, "malformed") || res.Fields[0].Source != SourceOnDiskJSON {
				t.Errorf("unknown field = %+v", res.Fields[0])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NBT
// ---------------------------------------------------------------------------

func nameBytes(s string) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, uint16(len(s)))
	return append(b, []byte(s)...)
}

func i4Bytes(v int32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, uint32(v))
	return b
}

func nbtTagBytes(id byte, name string, payload []byte) []byte {
	b := []byte{id}
	b = append(b, nameBytes(name)...)
	return append(b, payload...)
}

func nbtRoot(children ...[]byte) []byte {
	b := []byte{nbtCompound}
	b = append(b, nameBytes("")...)
	for _, c := range children {
		b = append(b, c...)
	}
	return append(b, nbtEnd)
}

func tagInt(name string, v int32) []byte {
	p := i4Bytes(v)
	return nbtTagBytes(nbtInt, name, p)
}

func tagShort(name string, v int16) []byte {
	p := make([]byte, 2)
	binary.BigEndian.PutUint16(p, uint16(v))
	return nbtTagBytes(nbtShort, name, p)
}

func nbtByteTag(name string, v byte) []byte {
	return nbtTagBytes(nbtByte, name, []byte{v})
}

func tagFloat(name string, v float32) []byte {
	p := make([]byte, 4)
	binary.BigEndian.PutUint32(p, math.Float32bits(v))
	return nbtTagBytes(nbtFloat, name, p)
}

func nbtStringTag(name, val string) []byte {
	return nbtTagBytes(nbtString, name, nameBytes(val))
}

func nbtDoubleList(name string, vals ...float64) []byte {
	p := []byte{nbtDouble}
	p = append(p, i4Bytes(int32(len(vals)))...)
	for _, v := range vals {
		b := make([]byte, 8)
		binary.BigEndian.PutUint64(b, math.Float64bits(v))
		p = append(p, b...)
	}
	return nbtTagBytes(nbtList, name, p)
}

func nbtItem(id string, count, slot byte) []byte {
	// List elements carry no tag id and no name; only their payload follows.
	b := []byte{}
	b = append(b, nbtStringTag("id", id)...)
	b = append(b, nbtByteTag("Count", count)...)
	b = append(b, nbtByteTag("Slot", slot)...)
	return append(b, nbtEnd)
}

func nbtItemList(name string, items ...[]byte) []byte {
	p := []byte{nbtCompound}
	p = append(p, i4Bytes(int32(len(items)))...)
	for _, it := range items {
		p = append(p, it...)
	}
	return nbtTagBytes(nbtList, name, p)
}

func tagByteArray(name string, n int) []byte {
	p := i4Bytes(int32(n))
	p = append(p, make([]byte, n)...)
	return nbtTagBytes(nbtByteArray, name, p)
}

func fullNBT() []byte {
	return nbtRoot(
		tagInt("playerGameType", 1),
		nbtStringTag("Dimension", "minecraft:overworld"),
		nbtDoubleList("Pos", 12.5, 64.0, -3.25),
		tagInt("foodLevel", 17),
		tagInt("XpLevel", 9),
		tagInt("XpTotal", 321),
		tagFloat("Health", 18.5),
		tagShort("Air", 290),
		nbtItemList("Inventory",
			nbtItem("minecraft:diamond", 5, 0),
			nbtItem("minecraft:dirt", 3, 1)),
		nbtItemList("EnderItems",
			nbtItem("minecraft:ender_pearl", 2, 0)),
		tagByteArray("UnrecognizedModPayload", 8),
		tagInt("SomeNewTag", 42),
	)
}

func TestReadCharacterNBT(t *testing.T) {
	r, dir := localSetup(t)
	writeRel(t, dir, "playerdata/"+testUUIDFile+".dat", fullNBT())

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindCharacter)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	for _, tc := range []struct{ name, value, unit string }{
		{"player_game_type", "1", ""},
		{"dimension", "minecraft:overworld", ""},
		{"position", "12.50, 64.00, -3.25", "x,y,z"},
		{"food_level", "17", ""},
		{"xp_level", "9", ""},
		{"xp_total", "321", ""},
		{"health", "18", ""},
		{"air", "290", ""},
		{"inventory_slots", "2", "slots"},
		{"inventory_items", "minecraft:diamond=5, minecraft:dirt=3", ""},
		{"ender_chest_slots", "1", "slots"},
		{"ender_chest_items", "minecraft:ender_pearl=2", ""},
	} {
		f, ok := fieldNamed(res.Fields, tc.name)
		if !ok {
			t.Fatalf("missing field %q; got %+v", tc.name, res.Fields)
		}
		if f.Value != tc.value || f.Unit != tc.unit {
			t.Errorf("field %s = %q/%q, want %q/%q", tc.name, f.Value, f.Unit, tc.value, tc.unit)
		}
		if f.Source != SourceOnDiskNBT || f.Confidence != ConfidenceDetected {
			t.Errorf("field %s provenance = %s/%s", tc.name, f.Source, f.Confidence)
		}
	}
	for _, f := range res.Fields {
		if strings.Contains(f.Value, "\x00") || strings.Contains(f.Name, "UnrecognizedModPayload") {
			t.Errorf("raw binary/unknown tag leaked into field %+v", f)
		}
	}
}

// TestReadCharacterFindsLevelFolder is the regression guard for the layout a
// real Minecraft server uses: the player snapshot lives in the level folder
// (world/playerdata/<uuid>.dat) and the file name keeps the UUID dashes.
func TestReadCharacterFindsLevelFolder(t *testing.T) {
	r, dir := localSetup(t)
	writeRel(t, dir, "world/playerdata/"+testUUID+".dat", fullNBT())

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindCharacter)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Fields) == 0 {
		t.Fatal("Read() returned no fields for a level-folder player snapshot")
	}
	if _, ok := fieldNamed(res.Fields, "dimension"); !ok {
		t.Fatalf("missing dimension field; got %+v", res.Fields)
	}
}

// TestReadStatsFindsLevelFolder proves the stats snapshot is found in the
// level folder too.
func TestReadStatsFindsLevelFolder(t *testing.T) {
	r, dir := localSetup(t)
	stats := `{"DataVersion": 3953, "stats": {"minecraft:custom": {"minecraft:play_time": 60}}}`
	writeRel(t, dir, "world/stats/"+testUUID+".json", []byte(stats))

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindStats)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	f, ok := fieldNamed(res.Fields, "play_time")
	if !ok {
		t.Fatalf("missing play_time field; got %+v", res.Fields)
	}
	if f.Value != "3s" {
		t.Fatalf("play_time = %q, want %q", f.Value, "3s")
	}
}

// TestReadCharacterAcceptsUndashedFileName keeps the undashed file name
// working for tooling that copies the snapshot under that name.
func TestReadCharacterAcceptsUndashedFileName(t *testing.T) {
	r, dir := localSetup(t)
	undashed := "00000000000000000000000000000001"
	writeRel(t, dir, "world/playerdata/"+undashed+".dat", fullNBT())

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindCharacter)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if f, ok := fieldNamed(res.Fields, "dimension"); !ok || f.Value != "minecraft:overworld" {
		t.Fatalf("undashed snapshot dimension field = %+v, ok=%v", f, ok)
	}
}

func TestReadCharacterGzipNBT(t *testing.T) {
	r, dir := localSetup(t)
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(fullNBT()); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	writeRel(t, dir, "playerdata/"+testUUIDFile+".dat", buf.Bytes())

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindCharacter)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if f, ok := fieldNamed(res.Fields, "dimension"); !ok || f.Value != "minecraft:overworld" {
		t.Errorf("gzip NBT dimension field = %+v", f)
	}
}

func TestTruncatedNBTReturnsUnknownField(t *testing.T) {
	r, dir := localSetup(t)
	raw := fullNBT()
	writeRel(t, dir, "playerdata/"+testUUIDFile+".dat", raw[:len(raw)-6])

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindCharacter)
	if err != nil {
		t.Fatalf("Read should not fail for truncated NBT: %v", err)
	}
	if len(res.Fields) != 1 || res.Fields[0].Confidence != ConfidenceUnknown ||
		!strings.Contains(res.Fields[0].ConflictNote, "truncated") {
		t.Fatalf("expected truncated unknown field, got %+v", res.Fields)
	}
}

func TestUnknownNBTTagsSkipped(t *testing.T) {
	r, dir := localSetup(t)
	writeRel(t, dir, "playerdata/"+testUUIDFile+".dat", nbtRoot(
		tagByteArray("UnknownBlob", 4),
		nbtStringTag("ModData", "opaque"),
		tagInt("playerGameType", 0),
	))

	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindCharacter)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if f, ok := fieldNamed(res.Fields, "player_game_type"); !ok || f.Value != "0" {
		t.Errorf("known tag not extracted alongside unknown tags: %+v", res.Fields)
	}
	for _, f := range res.Fields {
		if strings.Contains(f.Name, "Unknown") || strings.Contains(f.Value, "opaque") {
			t.Errorf("unknown tag leaked: %+v", f)
		}
	}

	// A file with only unknown tags yields a single unknown field, not binary.
	writeRel(t, dir, "playerdata/"+testUUIDFile+".dat", nbtRoot(tagByteArray("UnknownBlob", 4)))
	res, err = r.Read(context.Background(), stoppedServer(dir), testUUID, KindCharacter)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(res.Fields) != 1 || res.Fields[0].Confidence != ConfidenceUnknown {
		t.Fatalf("expected unknown field for unknown-only NBT, got %+v", res.Fields)
	}
}

// ---------------------------------------------------------------------------
// bounds, cancellation, remote refusal
// ---------------------------------------------------------------------------

func TestLargeFileBound(t *testing.T) {
	r, dir := localSetup(t)
	writeRel(t, dir, "stats/"+testUUIDFile+".json", make([]byte, int(statsLimit)+1))

	_, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindStats)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize stats: got %v, want ErrTooLarge", err)
	}

	writeRel(t, dir, "playerdata/"+testUUIDFile+".dat", make([]byte, int(nbtLimit)+1))
	_, err = r.Read(context.Background(), stoppedServer(dir), testUUID, KindCharacter)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize NBT: got %v, want ErrTooLarge", err)
	}
}

func TestCancellationMidRead(t *testing.T) {
	dir := t.TempDir()
	store := testStoreWithRemoteNode(t, "node-x", filepath.Join(t.TempDir(), "node-only"))
	r := NewReader(store, nodestorage.NewResolver(blockingTransport{}))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	_, err := r.Read(ctx, &db.Server{ID: "s", NodeID: "node-x", DataPath: dir, Status: db.StatusStopped},
		testUUID, KindStats)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mid-read cancellation: got %v, want DeadlineExceeded", err)
	}

	pre, preCancel := context.WithCancel(context.Background())
	preCancel()
	_, err = r.Read(pre, stoppedServer(dir), testUUID, KindStats)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled context: got %v, want Canceled", err)
	}
}

func TestRemoteNodeRefusesWithoutControllerFallback(t *testing.T) {
	dir := t.TempDir()
	nodeRoot := filepath.Join(t.TempDir(), "node-only-root")
	store := testStoreWithRemoteNode(t, "remote-node", nodeRoot)

	// A decoy controller-local file must never be read for a transport-only node.
	writeRel(t, dir, "stats/"+testUUIDFile+".json",
		[]byte(`{"stats":{"minecraft:custom":{"minecraft:deaths":1}}}`))
	server := &db.Server{ID: "s", NodeID: "remote-node", DataPath: dir, Status: db.StatusStopped}

	_, err := NewReader(store, nil).Read(context.Background(), server, testUUID, KindStats)
	if !errors.Is(err, nodestorage.ErrUnavailable) {
		t.Fatalf("remote refusal: got %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "remote-node") {
		t.Errorf("refusal should name node: %v", err)
	}

	// With a transport the same logical read succeeds from the node-owned root.
	nodeDir := t.TempDir()
	writeRel(t, nodeDir, "stats/"+testUUIDFile+".json",
		[]byte(`{"stats":{"minecraft:custom":{"minecraft:deaths":7}}}`))
	res, err := NewReader(store, nodestorage.NewResolver(dirTransport{root: nodeDir})).Read(
		context.Background(), server, testUUID, KindStats)
	if err != nil {
		t.Fatalf("read via transport: %v", err)
	}
	if f, ok := fieldNamed(res.Fields, "deaths"); !ok || f.Value != "7" {
		t.Errorf("transport read deaths = %+v", f)
	}
}

func TestStoppedVsRunningSnapshotNote(t *testing.T) {
	r, dir := localSetup(t)
	writeRel(t, dir, "stats/"+testUUIDFile+".json",
		[]byte(`{"stats":{"minecraft:custom":{"minecraft:deaths":1}}}`))
	writeRel(t, dir, "playerdata/"+testUUIDFile+".dat", fullNBT())

	for _, kind := range []string{KindStats, KindCharacter} {
		stopped, err := r.Read(context.Background(), stoppedServer(dir), testUUID, kind)
		if err != nil {
			t.Fatalf("%s stopped: %v", kind, err)
		}
		if !strings.Contains(stopped.SnapshotNote, "safe snapshot") ||
			!strings.Contains(stopped.SnapshotNote, "last saved at 20") {
			t.Errorf("%s stopped note = %q", kind, stopped.SnapshotNote)
		}
		for _, f := range stopped.Fields {
			if f.Confidence != ConfidenceDetected {
				t.Errorf("%s stopped confidence = %s", kind, f.Confidence)
			}
		}

		running, err := r.Read(context.Background(), runningServer(dir), testUUID, kind)
		if err != nil {
			t.Fatalf("%s running: %v", kind, err)
		}
		if !strings.Contains(running.SnapshotNote, "server running, may be inconsistent") ||
			!strings.Contains(running.SnapshotNote, "live view requires bridge/management adapter") {
			t.Errorf("%s running note = %q", kind, running.SnapshotNote)
		}
		for _, f := range running.Fields {
			if f.Confidence != ConfidenceStale {
				t.Errorf("%s running confidence = %s", kind, f.Confidence)
			}
		}
	}
}

func TestMissingFileReturnsUnknownField(t *testing.T) {
	r, dir := localSetup(t)
	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindStats)
	if err != nil {
		t.Fatalf("missing file: %v", err)
	}
	if len(res.Fields) != 1 || res.Fields[0].Name != "availability" ||
		res.Fields[0].Value != "not_present" || res.Fields[0].Confidence != ConfidenceUnknown {
		t.Fatalf("missing file result = %+v", res.Fields)
	}
}

func TestUnsupportedKindAndInvalidUUID(t *testing.T) {
	r, dir := localSetup(t)
	if _, err := r.Read(context.Background(), stoppedServer(dir), testUUID, "inventory"); !errors.Is(err, ErrUnsupportedKind) {
		t.Errorf("unsupported kind: %v", err)
	}
	if _, err := r.Read(context.Background(), stoppedServer(dir), "../../etc/passwd", KindStats); !errors.Is(err, ErrInvalidUUID) {
		t.Errorf("invalid uuid: %v", err)
	}
	if err := r.SetBase(KindStats, "../escape"); err == nil {
		t.Error("SetBase should reject traversal")
	}
}

func TestSetBase(t *testing.T) {
	r, dir := localSetup(t)
	if err := r.SetBase(KindStats, "world/stats"); err != nil {
		t.Fatalf("SetBase: %v", err)
	}
	writeRel(t, dir, "world/stats/"+testUUIDFile+".json",
		[]byte(`{"stats":{"minecraft:custom":{"minecraft:jump":3}}}`))
	res, err := r.Read(context.Background(), stoppedServer(dir), testUUID, KindStats)
	if err != nil {
		t.Fatalf("Read with custom base: %v", err)
	}
	if f, ok := fieldNamed(res.Fields, "jump"); !ok || f.Value != "3" {
		t.Errorf("custom base jump = %+v", f)
	}
	base, _ := r.Base(KindStats)
	if base != "world/stats" {
		t.Errorf("Base = %q", base)
	}
}

// ---------------------------------------------------------------------------
// test doubles for node routing and cancellation
// ---------------------------------------------------------------------------

func testStoreWithRemoteNode(t *testing.T, id, storageRoot string) *db.Store {
	t.Helper()
	cfg := &config.Config{Database: config.DatabaseConfig{Path: ":memory:", AutoMigrate: true, MaxConnections: 1}}
	store, err := db.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := store.CreateNode(context.Background(), &db.Node{
		ID: id, Name: id, StorageRoot: storageRoot, IsLocal: false,
	}); err != nil {
		t.Fatalf("create node: %v", err)
	}
	return store
}

// dirTransport stands in for a remote worker; it returns a local accessor
// rooted at the node-side fixture, proving the resolver's transport is used.
type dirTransport struct{ root string }

func (d dirTransport) Open(_ context.Context, _ nodestorage.NodeInfo, _ string) (nodestorage.Accessor, error) {
	return nodestorage.NewLocal(d.root)
}

type blockingTransport struct{}

func (blockingTransport) Open(ctx context.Context, _ nodestorage.NodeInfo, _ string) (nodestorage.Accessor, error) {
	return &blockingAccessor{handle: &blockingHandle{ctx: ctx}}, nil
}

type blockingAccessor struct {
	nodestorage.Accessor
	handle *blockingHandle
}

func (b *blockingAccessor) OpenRead(context.Context, string) (storageworker.ReadHandle, error) {
	return b.handle, nil
}

func (b *blockingAccessor) Stat(string) (os.FileInfo, error) {
	return fakeFileInfo{size: 16, mod: time.Now()}, nil
}

func (b *blockingAccessor) Close() error { return nil }

type blockingHandle struct{ ctx context.Context }

func (h *blockingHandle) Read([]byte) (int, error) {
	<-h.ctx.Done()
	return 0, h.ctx.Err()
}

func (h *blockingHandle) ReadAt([]byte, int64) (int, error) { return 0, h.ctx.Err() }
func (h *blockingHandle) Close() error                      { return nil }

type fakeFileInfo struct {
	size int64
	mod  time.Time
}

func (f fakeFileInfo) Name() string       { return "fixture" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() os.FileMode  { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return f.mod }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }
