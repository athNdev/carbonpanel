// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerdata

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	db "github.com/athNdev/carbon-panel/internal/db"
)

// Minimal Named Binary Tag reader. It is deliberately read-only, bounded and
// schema-tolerant: it parses the whole tree with hard limits on depth, byte
// consumption, name/string length, list/array length and node count, then
// extracts only known scalar tags. Unknown tags are parsed over and ignored;
// no raw binary is ever returned to the caller.
//
// Tag ids follow the official NBT specification:
// 0 End, 1 Byte, 2 Short, 3 Int, 4 Long, 5 Float, 6 Double, 7 ByteArray,
// 8 String, 9 List, 10 Compound, 11 IntArray, 12 LongArray.
const (
	nbtEnd byte = iota
	nbtByte
	nbtShort
	nbtInt
	nbtLong
	nbtFloat
	nbtDouble
	nbtByteArray
	nbtString
	nbtList
	nbtCompound
	nbtIntArray
	nbtLongArray
)

const (
	maxNBTDepth       = 16
	maxNBTNodes       = 200000
	maxNBTString      = 512
	maxNBTArray       = 1 << 20
	maxNBTList        = 1 << 16
	maxNBTCompoundKey = 1 << 16
	maxItemIDFields   = 16
)

type nbtTag struct {
	id       byte
	name     string
	value    any
	itemType byte // TAG_List element type
}

type nbtReader struct {
	b     []byte
	i     int
	nodes int
}

func (r *nbtReader) need(n int) error {
	if n < 0 || r.i > len(r.b)-n {
		return fmt.Errorf("nbt: truncated at byte %d", r.i)
	}
	return nil
}

func (r *nbtReader) u1() (byte, error) {
	if err := r.need(1); err != nil {
		return 0, err
	}
	v := r.b[r.i]
	r.i++
	return v, nil
}

func (r *nbtReader) u2() (uint16, error) {
	if err := r.need(2); err != nil {
		return 0, err
	}
	v := binary.BigEndian.Uint16(r.b[r.i:])
	r.i += 2
	return v, nil
}

func (r *nbtReader) i1() (int8, error) {
	v, err := r.u1()
	return int8(v), err
}

func (r *nbtReader) i2() (int16, error) {
	v, err := r.u2()
	return int16(v), err
}

func (r *nbtReader) i4() (int32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := int32(binary.BigEndian.Uint32(r.b[r.i:]))
	r.i += 4
	return v, nil
}

func (r *nbtReader) i8() (int64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := int64(binary.BigEndian.Uint64(r.b[r.i:]))
	r.i += 8
	return v, nil
}

func (r *nbtReader) f4() (float32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := math.Float32frombits(binary.BigEndian.Uint32(r.b[r.i:]))
	r.i += 4
	return v, nil
}

func (r *nbtReader) f8() (float64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := math.Float64frombits(binary.BigEndian.Uint64(r.b[r.i:]))
	r.i += 8
	return v, nil
}

func (r *nbtReader) str() (string, error) {
	n, err := r.u2()
	if err != nil {
		return "", err
	}
	if int(n) > maxNBTString {
		return "", fmt.Errorf("nbt: string length %d exceeds bound %d", n, maxNBTString)
	}
	if err := r.need(int(n)); err != nil {
		return "", err
	}
	s := string(r.b[r.i : r.i+int(n)])
	r.i += int(n)
	return s, nil
}

func (r *nbtReader) tag(depth int, named bool) (*nbtTag, error) {
	if depth > maxNBTDepth {
		return nil, fmt.Errorf("nbt: depth exceeds bound %d", maxNBTDepth)
	}
	r.nodes++
	if r.nodes > maxNBTNodes {
		return nil, fmt.Errorf("nbt: node count exceeds bound %d", maxNBTNodes)
	}
	id, err := r.u1()
	if err != nil {
		return nil, err
	}
	// TAG_End terminates a compound and carries neither name nor payload.
	if id == nbtEnd {
		return &nbtTag{id: nbtEnd}, nil
	}
	if id > nbtLongArray {
		return nil, fmt.Errorf("nbt: unknown tag type %d", id)
	}
	t := &nbtTag{id: id}
	if named {
		t.name, err = r.str()
		if err != nil {
			return nil, err
		}
	}
	if err := r.payload(t, depth); err != nil {
		return nil, err
	}
	return t, nil
}

// payload parses the payload for an already-decoded tag id. List elements
// carry only a payload (the list declares their type once), so they go
// through this function rather than tag().
func (r *nbtReader) payload(t *nbtTag, depth int) error {
	if depth > maxNBTDepth {
		return fmt.Errorf("nbt: depth exceeds bound %d", maxNBTDepth)
	}
	var err error
	switch t.id {
	case nbtByte:
		t.value, err = r.i1()
	case nbtShort:
		t.value, err = r.i2()
	case nbtInt:
		t.value, err = r.i4()
	case nbtLong:
		t.value, err = r.i8()
	case nbtFloat:
		t.value, err = r.f4()
	case nbtDouble:
		t.value, err = r.f8()
	case nbtByteArray:
		var n int32
		n, err = r.i4()
		if err == nil {
			err = r.checkLength(int64(n), maxNBTArray, "byte array")
		}
		if err == nil {
			if e := r.need(int(n)); e != nil {
				err = e
			} else {
				// Contents are intentionally not retained; only the bound is.
				r.i += int(n)
				t.value = int64(n)
			}
		}
	case nbtString:
		t.value, err = r.str()
	case nbtList:
		var elem byte
		elem, err = r.u1()
		if err != nil {
			return err
		}
		if elem > nbtLongArray {
			return fmt.Errorf("nbt: unknown list element type %d", elem)
		}
		var n int32
		n, err = r.i4()
		if err == nil {
			err = r.checkLength(int64(n), maxNBTList, "list")
		}
		if err != nil {
			return err
		}
		t.itemType = elem
		r.nodes += int(n)
		if r.nodes > maxNBTNodes {
			return fmt.Errorf("nbt: node count exceeds bound %d", maxNBTNodes)
		}
		items := make([]*nbtTag, 0, int(n))
		for j := int32(0); j < n; j++ {
			item := &nbtTag{id: elem}
			if e := r.payload(item, depth+1); e != nil {
				return e
			}
			items = append(items, item)
		}
		t.value = items
	case nbtCompound:
		children := []*nbtTag{}
		for {
			child, e := r.tag(depth+1, true)
			if e != nil {
				return e
			}
			if child.id == nbtEnd {
				break
			}
			if len(children) >= maxNBTCompoundKey {
				return fmt.Errorf("nbt: compound key count exceeds bound %d", maxNBTCompoundKey)
			}
			children = append(children, child)
		}
		t.value = children
	case nbtIntArray:
		var n int32
		n, err = r.i4()
		if err == nil {
			err = r.checkLength(int64(n), maxNBTArray, "int array")
		}
		if err == nil {
			if e := r.need(int(n) * 4); e != nil {
				err = e
			} else {
				r.i += int(n) * 4
				t.value = int64(n)
			}
		}
	case nbtLongArray:
		var n int32
		n, err = r.i4()
		if err == nil {
			err = r.checkLength(int64(n), maxNBTArray, "long array")
		}
		if err == nil {
			if e := r.need(int(n) * 8); e != nil {
				err = e
			} else {
				r.i += int(n) * 8
				t.value = int64(n)
			}
		}
	}
	return err
}

func (r *nbtReader) checkLength(n int64, max int, what string) error {
	if n < 0 || n > int64(max) {
		return fmt.Errorf("nbt: %s length %d exceeds bound %d", what, n, max)
	}
	return nil
}

// parseCharacter extracts a bounded set of well-known tags from the player's
// NBT snapshot. Files are optionally gzip-compressed (the common format for
// playerdata/*.dat). Tag names are version-dependent: some were added or
// renamed across Minecraft releases, and unset tags are often absent, so
// missing tags are skipped rather than reported as zero.
func parseCharacter(data []byte, server *db.Server) []Field {
	raw, err := maybeGunzip(data, nbtLimit)
	if err != nil {
		return []Field{unknownField("character", SourceOnDiskNBT,
			fmt.Sprintf("cannot decompress player NBT: %v", err), server)}
	}
	r := &nbtReader{b: raw}
	root, err := r.tag(0, true)
	if err != nil {
		return []Field{unknownField("character", SourceOnDiskNBT,
			fmt.Sprintf("malformed or truncated player NBT: %v", err), server)}
	}
	if root.id != nbtCompound {
		return []Field{unknownField("character", SourceOnDiskNBT,
			"player NBT root is not a compound tag", server)}
	}

	now := time.Now().UTC()
	conf := diskConfidence(server)
	fields := make([]Field, 0, 12)
	add := func(name, value, unit string) {
		fields = append(fields, Field{
			Name: name, Value: value, Unit: unit,
			Source: SourceOnDiskNBT, ObservedAt: now, Confidence: conf,
		})
	}
	get := func(name string) *nbtTag { return findChild(root, name) }

	// The following names are those used by modern Java Edition. playerGameType
	// (replacing the older "playerGameType"/"gameType" spellings), Dimension
	// (a string id in 1.16+, an integer dimension id in older versions), Pos
	// (three doubles) and the food/XP/health/air tags are all version-dependent.
	if v, ok := intValue(get("playerGameType")); ok {
		add("player_game_type", strconv.FormatInt(v, 10), "")
	}
	if v, ok := stringValue(get("Dimension")); ok {
		add("dimension", v, "")
	}
	if v, ok := posValue(get("Pos")); ok {
		add("position", fmt.Sprintf("%.2f, %.2f, %.2f", v[0], v[1], v[2]), "x,y,z")
	}
	for _, spec := range []struct {
		tag, name string
	}{
		{"foodLevel", "food_level"},
		{"XpLevel", "xp_level"},
		{"XpTotal", "xp_total"},
		{"Health", "health"},
		{"Air", "air"},
		{"Score", "score"},
	} {
		if v, ok := intValue(get(spec.tag)); ok {
			add(spec.name, strconv.FormatInt(v, 10), "")
		}
	}

	// Inventory and EnderItems are lists of item compounds with "id" (string),
	// "Count" (byte) and "Slot" (byte) tags. Older versions used lowercase
	// "id"/"Count" as well; both are resolved by findChild.
	for _, inv := range []struct {
		tag, slots, items string
	}{
		{"Inventory", "inventory_slots", "inventory_items"},
		{"EnderItems", "ender_chest_slots", "ender_chest_items"},
	} {
		occupied, items := inventorySummary(get(inv.tag))
		if occupied >= 0 {
			add(inv.slots, strconv.Itoa(occupied), "slots")
		}
		if items != "" {
			add(inv.items, items, "")
		}
	}

	if len(fields) == 0 {
		fields = append(fields, unknownField("character", SourceOnDiskNBT,
			"no recognized NBT tags found; names and presence vary by Minecraft version", server))
	}
	return fields
}

func maybeGunzip(data []byte, limit int64) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(io.LimitReader(zr, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > limit {
		return nil, fmt.Errorf("%w: decompressed NBT exceeds %d bytes", ErrTooLarge, limit)
	}
	return out, nil
}

func findChild(parent *nbtTag, name string) *nbtTag {
	if parent == nil || parent.id != nbtCompound {
		return nil
	}
	children, ok := parent.value.([]*nbtTag)
	if !ok {
		return nil
	}
	for _, c := range children {
		if c.name == name {
			return c
		}
	}
	return nil
}

func intValue(t *nbtTag) (int64, bool) {
	if t == nil {
		return 0, false
	}
	switch v := t.value.(type) {
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float32:
		return int64(v), true
	case float64:
		return int64(v), true
	}
	return 0, false
}

func stringValue(t *nbtTag) (string, bool) {
	if t == nil || t.id != nbtString {
		return "", false
	}
	s, ok := t.value.(string)
	return s, ok
}

func posValue(t *nbtTag) ([3]float64, bool) {
	var out [3]float64
	if t == nil || t.id != nbtList {
		return out, false
	}
	items, ok := t.value.([]*nbtTag)
	if !ok || len(items) != 3 {
		return out, false
	}
	for i, item := range items {
		v, ok := floatValue(item)
		if !ok {
			return [3]float64{}, false
		}
		out[i] = v
	}
	return out, true
}

func floatValue(t *nbtTag) (float64, bool) {
	if t == nil {
		return 0, false
	}
	switch v := t.value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int32:
		return float64(v), true
	case int64:
		return float64(v), true
	}
	return 0, false
}

func inventorySummary(t *nbtTag) (occupied int, summary string) {
	if t == nil || t.id != nbtList {
		return -1, ""
	}
	items, ok := t.value.([]*nbtTag)
	if !ok {
		return -1, ""
	}
	counts := map[string]int64{}
	for _, item := range items {
		if item.id != nbtCompound {
			continue
		}
		id, ok := stringValue(findChild(item, "id"))
		if !ok || id == "" {
			continue
		}
		count, ok := intValue(findChild(item, "Count"))
		if !ok || count <= 0 {
			count = 1
		}
		occupied++
		counts[id] += count
	}
	if len(counts) == 0 {
		return occupied, ""
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for i, k := range keys {
		if i >= maxItemIDFields {
			parts = append(parts, fmt.Sprintf("(+%d more)", len(keys)-i))
			break
		}
		parts = append(parts, k+"="+strconv.FormatInt(counts[k], 10))
	}
	return occupied, strings.Join(parts, ", ")
}
