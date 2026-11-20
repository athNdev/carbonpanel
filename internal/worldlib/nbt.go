// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// This is a deliberately small, read-only NBT decoder. It exists so the
// compatibility inspector can read Java level.dat facts without depending on
// a third-party parser. It never writes files, never mutates its input and
// never guesses: malformed, truncated or unsupported input is an error.

// Decoder limits. level.dat is a small file; anything approaching these bounds
// is either corrupt or an attack, and is refused rather than partially parsed.
const (
	maxNBTDecompressed = 8 << 20  // 8 MiB of decompressed NBT
	maxNBTString       = 64 << 10 // 64 KiB per NBT string
	maxNBTEntries      = 100_000  // list/array elements and compound readers
	maxNBTDepth        = 16       // nested compound/list depth
)

// NBT tag types (the Java edition wire format).
type nbtTag byte

const (
	nbtEnd nbtTag = iota
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

func (t nbtTag) valid() bool { return t <= nbtLongArray }

// nbtCursor parses a bounded decompressed NBT payload. All positions are
// offsets into the already-bounded buffer, so no allocation is driven by an
// untrusted length without first checking the remaining input.
type nbtCursor struct {
	data      []byte
	pos       int
	maxValues int
	values    int
}

func (c *nbtCursor) checkValueBudget(count int) error {
	limit := c.maxValues
	if limit == 0 {
		limit = maxNBTEntries + 1
	}
	if count > limit-c.values {
		return fmt.Errorf("nbt: value budget of %d exceeded", limit)
	}
	return nil
}

func (c *nbtCursor) take(n int) ([]byte, error) {
	if n < 0 {
		return nil, errors.New("nbt: negative length")
	}
	remaining := len(c.data) - c.pos
	if n > remaining {
		return nil, fmt.Errorf("nbt: truncated payload at offset %d (need %d bytes, have %d)", c.pos, n, remaining)
	}
	b := c.data[c.pos : c.pos+n]
	c.pos += n
	return b, nil
}

func (c *nbtCursor) u8() (byte, error) {
	b, err := c.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (c *nbtCursor) u16() (uint16, error) {
	b, err := c.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (c *nbtCursor) i32() (int32, error) {
	b, err := c.take(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.BigEndian.Uint32(b)), nil
}

func (c *nbtCursor) i64() (int64, error) {
	b, err := c.take(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b)), nil
}

func (c *nbtCursor) f32() (float32, error) {
	b, err := c.take(4)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(binary.BigEndian.Uint32(b)), nil
}

func (c *nbtCursor) f64() (float64, error) {
	b, err := c.take(8)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
}

func (c *nbtCursor) stringValue() (string, error) {
	n, err := c.u16()
	if err != nil {
		return "", err
	}
	if int(n) > maxNBTString {
		return "", fmt.Errorf("nbt: string length %d exceeds %d bytes", n, maxNBTString)
	}
	b, err := c.take(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// count reads a non-negative, bounded element count for arrays and lists.
func (c *nbtCursor) count() (int, error) {
	n, err := c.i32()
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("nbt: negative element count %d at offset %d", n, c.pos-4)
	}
	if int(n) > maxNBTEntries {
		return 0, fmt.Errorf("nbt: element count %d exceeds %d", n, maxNBTEntries)
	}
	return int(n), nil
}

// array checks that count elements of size bytes are present before any
// allocation, then returns a copy of the raw bytes.
func (c *nbtCursor) array(count, size int) ([]byte, error) {
	if size > 0 && count > (len(c.data)-c.pos)/size {
		return nil, fmt.Errorf("nbt: truncated array at offset %d (need %d elements of %d bytes)", c.pos, count, size)
	}
	total := count * size
	b, err := c.take(total)
	if err != nil {
		return nil, err
	}
	out := make([]byte, total)
	copy(out, b)
	return out, nil
}

// tagHeader reads a tag type and (except for End) its name.
func (c *nbtCursor) tagHeader(depth int) (nbtTag, string, error) {
	raw, err := c.u8()
	if err != nil {
		return 0, "", err
	}
	typ := nbtTag(raw)
	if !typ.valid() {
		return 0, "", fmt.Errorf("nbt: unknown tag type %d at offset %d", raw, c.pos-1)
	}
	if typ == nbtEnd {
		return typ, "", nil
	}
	if depth > maxNBTDepth {
		return 0, "", fmt.Errorf("nbt: nesting depth exceeds %d", maxNBTDepth)
	}
	name, err := c.stringValue()
	if err != nil {
		return 0, "", fmt.Errorf("nbt: tag name: %w", err)
	}
	return typ, name, nil
}

// payload reads the value of a tag whose type has already been read. For list
// elements this is called directly (they are unnamed); for compound members
// the caller has already read the member name with tagHeader.
func (c *nbtCursor) payload(typ nbtTag, depth int) (any, error) {
	if !typ.valid() || typ == nbtEnd {
		return nil, fmt.Errorf("nbt: tag type %d has no payload", typ)
	}
	if depth > maxNBTDepth {
		return nil, fmt.Errorf("nbt: nesting depth exceeds %d", maxNBTDepth)
	}

	if err := c.checkValueBudget(1); err != nil {
		return nil, err
	}
	c.values++

	switch typ {
	case nbtByte:
		v, err := c.u8()
		return int8(v), err
	case nbtShort:
		v, err := c.u16()
		return int16(v), err
	case nbtInt:
		v, err := c.i32()
		return v, err
	case nbtLong:
		v, err := c.i64()
		return v, err
	case nbtFloat:
		return c.f32()
	case nbtDouble:
		return c.f64()
	case nbtByteArray:
		n, err := c.count()
		if err != nil {
			return nil, err
		}
		b, err := c.array(n, 1)
		return b, err
	case nbtString:
		return c.stringValue()
	case nbtList:
		raw, err := c.u8()
		if err != nil {
			return nil, err
		}
		elem := nbtTag(raw)
		if !elem.valid() {
			return nil, fmt.Errorf("nbt: unknown list element type %d at offset %d", raw, c.pos-1)
		}
		n, err := c.count()
		if err != nil {
			return nil, err
		}
		if elem == nbtEnd {
			if n != 0 {
				return nil, fmt.Errorf("nbt: list of End tags declares %d elements", n)
			}
			return []any{}, nil
		}
		// Every list element consumes at least one value. Check the shared
		// budget before reserving space from an untrusted length.
		if err := c.checkValueBudget(n); err != nil {
			return nil, err
		}
		values := make([]any, 0, n)
		for i := 0; i < n; i++ {
			v, err := c.payload(elem, depth+1)
			if err != nil {
				return nil, fmt.Errorf("nbt: list element %d: %w", i, err)
			}
			values = append(values, v)
		}
		return values, nil
	case nbtCompound:
		values := make(map[string]any)
		for {
			childType, name, err := c.tagHeader(depth + 1)
			if err != nil {
				return nil, fmt.Errorf("nbt: compound member: %w", err)
			}
			if childType == nbtEnd {
				break
			}
			v, err := c.payload(childType, depth+1)
			if err != nil {
				return nil, fmt.Errorf("nbt: compound member %q: %w", name, err)
			}
			values[name] = v
		}
		return values, nil
	case nbtIntArray:
		n, err := c.count()
		if err != nil {
			return nil, err
		}
		b, err := c.array(n, 4)
		if err != nil {
			return nil, err
		}
		out := make([]int32, n)
		for i := range out {
			out[i] = int32(binary.BigEndian.Uint32(b[i*4 : i*4+4]))
		}
		return out, nil
	case nbtLongArray:
		n, err := c.count()
		if err != nil {
			return nil, err
		}
		b, err := c.array(n, 8)
		if err != nil {
			return nil, err
		}
		out := make([]int64, n)
		for i := range out {
			out[i] = int64(binary.BigEndian.Uint64(b[i*8 : i*8+8]))
		}
		return out, nil
	default:
		return nil, fmt.Errorf("nbt: unsupported tag type %d", typ)
	}
}

// decompressNBT accepts gzip-compressed (normal level.dat) or plain NBT and
// bounds the decompressed size before the parser sees any of it.
func decompressNBT(data []byte) ([]byte, error) {
	var r io.Reader = bytes.NewReader(data)
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("nbt: invalid gzip header: %w", err)
		}
		defer func() { _ = gz.Close() }()
		r = gz
	}
	out, err := io.ReadAll(io.LimitReader(r, maxNBTDecompressed+1))
	if err != nil {
		return nil, fmt.Errorf("nbt: decompress: %w", err)
	}
	if len(out) > maxNBTDecompressed {
		return nil, fmt.Errorf("nbt: decompressed payload exceeds %d bytes", maxNBTDecompressed)
	}
	if len(out) == 0 {
		return nil, errors.New("nbt: empty payload")
	}
	return out, nil
}

// LevelDataFacts parses raw level.dat bytes (gzip or plain NBT) and returns
// the typed facts in its Data compound. Callers get only what was on disk;
// absent or unreadable fields are absent from the map, never guessed.
func LevelDataFacts(data []byte) (map[string]any, error) {
	raw, err := decompressNBT(data)
	if err != nil {
		return nil, err
	}
	root, name, err := parseNBTDocument(&nbtCursor{data: raw})
	if err != nil {
		return nil, err
	}
	// Both shapes occur in the wild: an unnamed root holding a Data compound,
	// and a root compound that is itself named "Data".
	if stringsEqualFold(name, "Data") {
		return root, nil
	}
	if dataValue, exists := root["Data"]; exists {
		dataCompound, ok := dataValue.(map[string]any)
		if !ok {
			return nil, errors.New("nbt: root Data tag is not a compound")
		}
		return dataCompound, nil
	}
	return root, nil
}

// parseNBTDocument reads the named root compound. The caller owns and bounds
// the decompressed payload before calling it.
func parseNBTDocument(c *nbtCursor) (map[string]any, string, error) {
	typ, name, err := c.tagHeader(1)
	if err != nil {
		return nil, "", fmt.Errorf("nbt: root tag: %w", err)
	}
	if typ != nbtCompound {
		return nil, "", fmt.Errorf("nbt: root tag has type %d, want compound", typ)
	}
	rootValue, err := c.payload(typ, 1)
	if err != nil {
		return nil, "", fmt.Errorf("nbt: root payload: %w", err)
	}
	root, ok := rootValue.(map[string]any)
	if !ok {
		return nil, "", errors.New("nbt: root payload is not a compound")
	}
	return root, name, nil
}

func stringsEqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		x, y := a[i], b[i]
		if x >= 'A' && x <= 'Z' {
			x += 'a' - 'A'
		}
		if y >= 'A' && y <= 'Z' {
			y += 'a' - 'A'
		}
		if x != y {
			return false
		}
	}
	return true
}

// FactInt returns an integer fact as int64, accepting all signed NBT integer
// tags. Booleans stored as TAG_Byte are included.
func FactInt(facts map[string]any, key string) (int64, bool) {
	v, ok := facts[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case int8:
		return int64(n), true
	case int16:
		return int64(n), true
	case int32:
		return int64(n), true
	case int64:
		return n, true
	default:
		return 0, false
	}
}

// FactString returns a string fact.
func FactString(facts map[string]any, key string) (string, bool) {
	v, ok := facts[key].(string)
	return v, ok
}

// FactNested walks nested compounds, e.g. FactNested(facts, "Version", "Id").
func FactNested(facts map[string]any, keys ...string) (any, bool) {
	current := any(facts)
	for _, key := range keys {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}
