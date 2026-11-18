// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// This file writes the small subset of NBT needed for a fresh level.dat. It
// does not edit an existing world's NBT: a new world is generated from a
// validated draft and the server populates chunks on first start. When the
// target data version is unknown the field is omitted and the server writes
// it, rather than guessing a version.

type wgTag struct {
	name string
	kind byte
	body []byte
}

const (
	wgEnd byte = iota
	wgByte
	wgShort
	wgInt
	wgLong
	wgFloat
	wgDouble
	wgByteArray
	wgString
	wgList
	wgCompound
	wgIntArray
	wgLongArray
)

func wgStringTag(name, value string) wgTag {
	b := []byte(value)
	body := make([]byte, 2+len(b))
	binary.BigEndian.PutUint16(body[:2], uint16(len(b)))
	copy(body[2:], b)
	return wgTag{name: name, kind: wgString, body: body}
}

func wgIntTag(name string, value int32) wgTag {
	body := make([]byte, 4)
	binary.BigEndian.PutUint32(body, uint32(value))
	return wgTag{name: name, kind: wgInt, body: body}
}

func wgLongTag(name string, value int64) wgTag {
	body := make([]byte, 8)
	binary.BigEndian.PutUint64(body, uint64(value))
	return wgTag{name: name, kind: wgLong, body: body}
}

func wgByteTag(name string, value byte) wgTag {
	return wgTag{name: name, kind: wgByte, body: []byte{value}}
}

func wgCompoundTag(name string, tags ...wgTag) wgTag {
	var body bytes.Buffer
	for _, tag := range tags {
		writeWGTag(&body, tag)
	}
	body.WriteByte(wgEnd)
	return wgTag{name: name, kind: wgCompound, body: body.Bytes()}
}

func writeWGTag(w io.Writer, tag wgTag) {
	name := []byte(tag.name)
	header := make([]byte, 3+len(name))
	header[0] = tag.kind
	binary.BigEndian.PutUint16(header[1:3], uint16(len(name)))
	copy(header[3:], name)
	_, _ = w.Write(header)
	_, _ = w.Write(tag.body)
}

// WriteFreshLevelData returns gzip-compressed NBT for a created world.
func WriteFreshLevelData(values map[string]string, dataVersion string) ([]byte, error) {
	levelName := values["level-name"]
	if levelName == "" {
		levelName = "world"
	}
	if strings.ContainsAny(levelName, `/\:`) {
		return nil, fmt.Errorf("invalid level name %q", levelName)
	}
	seedText := values["level-seed"]
	var seed int64
	if seedText != "" {
		parsed, err := strconv.ParseInt(seedText, 10, 64)
		if err != nil {
			// Strings are valid seeds too; hash them deterministically rather
			// than substituting a plausible-looking number.
			for _, r := range seedText {
				seed = seed*31 + int64(r)
			}
		} else {
			seed = parsed
		}
	}
	gameType := map[string]int32{"survival": 0, "creative": 1, "adventure": 2, "spectator": 3}[values["gamemode"]]
	difficulty := map[string]byte{"peaceful": 0, "easy": 1, "normal": 2, "hard": 3}[values["difficulty"]]
	tags := []wgTag{
		wgStringTag("LevelName", levelName),
		wgLongTag("RandomSeed", seed),
		wgIntTag("GameType", gameType),
		wgByteTag("Difficulty", difficulty),
		wgStringTag("generatorName", values["level-type"]),
		wgStringTag("generatorOptions", values["generator-settings"]),
		wgByteTag("allowCommands", 0),
	}
	if dataVersion != "" {
		parsed, err := strconv.ParseInt(dataVersion, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid data version %q: %w", dataVersion, err)
		}
		tags = append(tags, wgIntTag("DataVersion", int32(parsed)))
	}
	root := wgCompoundTag("", wgCompoundTag("Data", tags...))

	var raw bytes.Buffer
	writeWGTag(&raw, root)
	var compressed bytes.Buffer
	zw := gzip.NewWriter(&compressed)
	if _, err := zw.Write(raw.Bytes()); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

// WriteFreshWorldArchive returns a deterministic-worldstore-shaped archive for
// a freshly created world. The archive contains only generated metadata and
// empty region/datapack folders; the server creates chunks on first start.
func WriteFreshWorldArchive(values map[string]string, dataVersion string) ([]byte, error) {
	levelData, err := WriteFreshLevelData(values, dataVersion)
	if err != nil {
		return nil, err
	}
	levelName := values["level-name"]
	if levelName == "" {
		levelName = "world"
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	files := map[string][]byte{
		levelName + "/level.dat":       levelData,
		levelName + "/region/.keep":    {},
		levelName + "/datapacks/.keep": {},
	}
	for _, name := range []string{levelName + "/level.dat", levelName + "/region/.keep", levelName + "/datapacks/.keep"} {
		w, err := zw.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
