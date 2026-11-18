// SPDX-License-Identifier: MPL-2.0
package worldlib

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

func TestFreshWorldArchiveContainsReadableLevelData(t *testing.T) {
	archive, err := WriteFreshWorldArchive(map[string]string{
		"level-name": "world", "level-seed": "42", "gamemode": "creative", "difficulty": "hard",
		"level-type": "minecraft:flat", "generator-settings": "flat-preset",
	}, "3953")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	var level []byte
	for _, f := range zr.File {
		if f.Name == "world/level.dat" {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			level, err = io.ReadAll(rc)
			if closeErr := rc.Close(); closeErr != nil {
				t.Fatal(closeErr)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(level) == 0 {
		t.Fatal("archive has no level.dat")
	}
	zr2, err := gzip.NewReader(bytes.NewReader(level))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr2)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"LevelName", "RandomSeed", "GameType", "Difficulty", "DataVersion", "generatorName"} {
		if !bytes.Contains(plain, []byte(want)) {
			t.Fatalf("level.dat missing %q", want)
		}
	}
}

func TestFreshWorldOmitsUnknownDataVersion(t *testing.T) {
	level, err := WriteFreshLevelData(map[string]string{"level-name": "world"}, "")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(level))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(plain, []byte("DataVersion")) {
		t.Fatal("unknown data version must be omitted, not guessed")
	}
}
