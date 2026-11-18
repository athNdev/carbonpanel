// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"math"
	"strings"
	"testing"
)

// nbtWriter is a small test-only writer used to build level.dat fixtures.
// It is intentionally independent of the read-only production parser.
type nbtWriter struct{ buf bytes.Buffer }

func (w *nbtWriter) u8(v byte)           { _ = w.buf.WriteByte(v) }
func (w *nbtWriter) u16(v uint16)        { var b [2]byte; binary.BigEndian.PutUint16(b[:], v); w.buf.Write(b[:]) }
func (w *nbtWriter) i32(v int32)         { var b [4]byte; binary.BigEndian.PutUint32(b[:], uint32(v)); w.buf.Write(b[:]) }
func (w *nbtWriter) i64(v int64)         { var b [8]byte; binary.BigEndian.PutUint64(b[:], uint64(v)); w.buf.Write(b[:]) }
func (w *nbtWriter) f32(v float32)       { w.i32(int32(math.Float32bits(v))) }
func (w *nbtWriter) f64(v float64)       { w.i64(int64(math.Float64bits(v))) }
func (w *nbtWriter) raw(v []byte)        { w.buf.Write(v) }
func (w *nbtWriter) named(name string)   { w.u16(uint16(len(name))); w.raw([]byte(name)) }
func (w *nbtWriter) tag(t nbtTag, name string) { w.u8(byte(t)); w.named(name) }

func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// buildLevelDat builds a realistic Java level.dat: an unnamed root compound
// with a Data compound (the shape Minecraft actually writes).
func buildLevelDat(t *testing.T, dataVersion int32, versionID string, compressed bool) []byte {
	t.Helper()
	w := &nbtWriter{}
	w.tag(nbtCompound, "") // root
	w.tag(nbtCompound, "Data")
	w.tag(nbtInt, "DataVersion")
	w.i32(dataVersion)
	w.tag(nbtCompound, "Version")
	w.tag(nbtString, "Id")
	w.named(versionID)
	w.u8(byte(nbtEnd)) // end Version
	w.tag(nbtByte, "GameType")
	w.u8(1)
	w.tag(nbtByte, "Difficulty")
	w.u8(2)
	w.tag(nbtLong, "RandomSeed")
	w.i64(-90210938475921)
	w.tag(nbtInt, "SpawnX")
	w.i32(112)
	w.tag(nbtInt, "SpawnY")
	w.i32(64)
	w.tag(nbtInt, "SpawnZ")
	w.i32(-207)
	w.tag(nbtString, "generatorName")
	w.named("flat")
	w.tag(nbtString, "generatorOptions")
	w.named("{\"layers\":[]}")
	w.tag(nbtDouble, "BorderCenterX")
	w.f64(7.5)
	w.tag(nbtDouble, "BorderSize")
	w.f64(59999968)
	w.u8(byte(nbtEnd)) // end Data
	w.u8(byte(nbtEnd)) // end root
	if compressed {
		return gzipBytes(t, w.buf.Bytes())
	}
	return w.buf.Bytes()
}

func TestLevelDataFactsExtractsKnownValues(t *testing.T) {
	facts, err := LevelDataFacts(buildLevelDat(t, 3953, "1.21", true))
	if err != nil {
		t.Fatalf("LevelDataFacts: %v", err)
	}
	dv, ok := FactInt(facts, "DataVersion")
	if !ok || dv != 3953 {
		t.Fatalf("DataVersion = %d, %v; want 3953, true", dv, ok)
	}
	version, ok := FactNested(facts, "Version", "Id")
	if !ok || version != "1.21" {
		t.Fatalf("Version.Id = %v, %v; want 1.21, true", version, ok)
	}
	for key, want := range map[string]int64{
		"GameType": 1, "Difficulty": 2, "RandomSeed": -90210938475921,
		"SpawnX": 112, "SpawnY": 64, "SpawnZ": -207,
	} {
		got, ok := FactInt(facts, key)
		if !ok || got != want {
			t.Errorf("%s = %d, %v; want %d, true", key, got, ok, want)
		}
	}
	if gen, ok := FactString(facts, "generatorName"); !ok || gen != "flat" {
		t.Errorf("generatorName = %q, %v; want flat, true", gen, ok)
	}
	if opts, ok := FactString(facts, "generatorOptions"); !ok || opts != `{"layers":[]}` {
		t.Errorf("generatorOptions = %q, %v", opts, ok)
	}
	if border, ok := FactNested(facts, "BorderSize"); !ok || border != float64(59999968) {
		t.Errorf("BorderSize = %v, %v", border, ok)
	}
}

func TestLevelDataFactsAcceptsPlainAndRootNamedData(t *testing.T) {
	plain := buildLevelDat(t, 3120, "1.18.2", false)
	facts, err := LevelDataFacts(plain)
	if err != nil {
		t.Fatalf("plain NBT: %v", err)
	}
	if dv, _ := FactInt(facts, "DataVersion"); dv != 3120 {
		t.Fatalf("plain DataVersion = %d; want 3120", dv)
	}

	// Some tools emit the root compound itself named "Data".
	w := &nbtWriter{}
	w.tag(nbtCompound, "Data")
	w.tag(nbtInt, "DataVersion")
	w.i32(2586)
	w.u8(byte(nbtEnd))
	facts, err = LevelDataFacts(gzipBytes(t, w.buf.Bytes()))
	if err != nil {
		t.Fatalf("root named Data: %v", err)
	}
	if dv, _ := FactInt(facts, "DataVersion"); dv != 2586 {
		t.Fatalf("root-named DataVersion = %d; want 2586", dv)
	}
}

func TestLevelDataFactsAllTagTypes(t *testing.T) {
	w := &nbtWriter{}
	w.tag(nbtCompound, "")
	w.tag(nbtByte, "b"); w.u8(0xFE)
	w.tag(nbtShort, "s"); w.u16(0xFFFE)
	w.tag(nbtInt, "i"); w.i32(-123456)
	w.tag(nbtLong, "l"); w.i64(-9876543210)
	w.tag(nbtFloat, "f"); w.f32(1.5)
	w.tag(nbtDouble, "d"); w.f64(2.25)
	w.tag(nbtByteArray, "ba"); w.i32(3); w.raw([]byte{1, 2, 3})
	w.tag(nbtString, "str"); w.named("hello")
	w.tag(nbtList, "list"); w.u8(byte(nbtInt)); w.i32(3); w.i32(7); w.i32(8); w.i32(9)
	w.tag(nbtCompound, "nested"); w.tag(nbtInt, "x"); w.i32(42); w.u8(byte(nbtEnd))
	w.tag(nbtIntArray, "ia"); w.i32(2); w.i32(11); w.i32(12)
	w.tag(nbtLongArray, "la"); w.i32(1); w.i64(1234567890123)
	w.u8(byte(nbtEnd)) // root
	facts, err := LevelDataFacts(w.buf.Bytes())
	if err != nil {
		t.Fatalf("LevelDataFacts: %v", err)
	}
	if v, _ := FactInt(facts, "b"); v != -2 { t.Errorf("TAG_Byte = %d; want -2", v) }
	if v, _ := FactInt(facts, "s"); v != -2 { t.Errorf("TAG_Short = %d; want -2", v) }
	if v, _ := FactInt(facts, "i"); v != -123456 { t.Errorf("TAG_Int = %d", v) }
	if v, _ := FactInt(facts, "l"); v != -9876543210 { t.Errorf("TAG_Long = %d", v) }
	if v, ok := facts["f"].(float32); !ok || v != 1.5 { t.Errorf("TAG_Float = %v", facts["f"]) }
	if v, ok := facts["d"].(float64); !ok || v != 2.25 { t.Errorf("TAG_Double = %v", facts["d"]) }
	if v, ok := facts["ba"].([]byte); !ok || !bytes.Equal(v, []byte{1, 2, 3}) { t.Errorf("TAG_Byte_Array = %v", facts["ba"]) }
	if v, ok := FactString(facts, "str"); !ok || v != "hello" { t.Errorf("TAG_String = %v", facts["str"]) }
	if v, ok := facts["list"].([]any); !ok || len(v) != 3 || v[2] != int32(9) { t.Errorf("TAG_List = %v", facts["list"]) }
	if v, ok := FactNested(facts, "nested", "x"); !ok || v != int32(42) { t.Errorf("nested.x = %v", v) }
	if v, ok := facts["ia"].([]int32); !ok || len(v) != 2 || v[1] != 12 { t.Errorf("TAG_Int_Array = %v", facts["ia"]) }
	if v, ok := facts["la"].([]int64); !ok || len(v) != 1 || v[0] != 1234567890123 { t.Errorf("TAG_Long_Array = %v", facts["la"]) }
}

func TestLevelDataFactsRejectsTruncatedMalformedAndOversize(t *testing.T) {
	good := buildLevelDat(t, 3953, "1.21", true)

	t.Run("truncated", func(t *testing.T) {
		if _, err := LevelDataFacts(good[:len(good)-5]); err == nil {
			t.Fatal("truncated gzip input was accepted")
		}
		if _, err := LevelDataFacts(good[:4]); err == nil {
			t.Fatal("short gzip input was accepted")
		}
	})

	t.Run("unknown tag type", func(t *testing.T) {
		raw := []byte{byte(nbtCompound), 0, 0, 99, 0, 0, byte(nbtEnd)}
		if _, err := LevelDataFacts(raw); err == nil || !strings.Contains(err.Error(), "unknown tag type") {
			t.Fatalf("want unknown tag type error, got %v", err)
		}
	})

	t.Run("root not compound", func(t *testing.T) {
		raw := []byte{byte(nbtInt), 0, 0, 0, 0, 0, 1}
		if _, err := LevelDataFacts(raw); err == nil || !strings.Contains(err.Error(), "want compound") {
			t.Fatalf("want root type error, got %v", err)
		}
	})

	t.Run("Data not compound", func(t *testing.T) {
		raw := []byte{byte(nbtCompound), 0, 0, byte(nbtInt), 0, 4, 'D', 'a', 't', 'a', 0, 0, 0, 1, byte(nbtEnd)}
		if _, err := LevelDataFacts(raw); err == nil || !strings.Contains(err.Error(), "not a compound") {
			t.Fatalf("want Data compound error, got %v", err)
		}
	})

	t.Run("depth limit", func(t *testing.T) {
		w := &nbtWriter{}
		w.tag(nbtCompound, "")
		for i := 0; i < maxNBTDepth+3; i++ {
			w.tag(nbtCompound, "n")
		}
		for i := 0; i < maxNBTDepth+3; i++ {
			w.u8(byte(nbtEnd))
		}
		w.u8(byte(nbtEnd))
		if _, err := LevelDataFacts(w.buf.Bytes()); err == nil || !strings.Contains(err.Error(), "depth") {
			t.Fatalf("want depth error, got %v", err)
		}
	})

	t.Run("oversize decompressed", func(t *testing.T) {
		// A small compressed input that expands past the 8 MiB bound.
		w := &nbtWriter{}
		w.tag(nbtCompound, "")
		w.tag(nbtByteArray, "blob")
		w.i32(maxNBTDecompressed + 16)
		w.raw(make([]byte, maxNBTDecompressed+16))
		w.u8(byte(nbtEnd))
		w.u8(byte(nbtEnd))
		if _, err := LevelDataFacts(gzipBytes(t, w.buf.Bytes())); err == nil {
			t.Fatal("oversize decompressed payload was accepted")
		}
	})

	t.Run("element count bound", func(t *testing.T) {
		w := &nbtWriter{}
		w.tag(nbtCompound, "")
		w.tag(nbtList, "big")
		w.u8(byte(nbtInt))
		w.i32(maxNBTEntries + 1)
		w.u8(byte(nbtEnd))
		if _, err := LevelDataFacts(w.buf.Bytes()); err == nil {
			t.Fatal("oversize list was accepted")
		}
	})

	t.Run("negative count", func(t *testing.T) {
		w := &nbtWriter{}
		w.tag(nbtCompound, "")
		w.tag(nbtByteArray, "bad")
		w.i32(-1)
		w.u8(byte(nbtEnd))
		if _, err := LevelDataFacts(w.buf.Bytes()); err == nil {
			t.Fatal("negative array length was accepted")
		}
	})
}

func TestFactHelpersFailClosed(t *testing.T) {
	facts := map[string]any{"count": int32(3), "label": "ok", "nested": map[string]any{"k": "v"}}
	if _, ok := FactInt(facts, "label"); ok {
		t.Error("FactInt accepted a string")
	}
	if _, ok := FactString(facts, "count"); ok {
		t.Error("FactString accepted an integer")
	}
	if _, ok := FactNested(facts, "nested", "missing"); ok {
		t.Error("FactNested accepted a missing key")
	}
	if _, ok := FactNested(facts, "count", "k"); ok {
		t.Error("FactNested walked through a scalar")
	}
}
