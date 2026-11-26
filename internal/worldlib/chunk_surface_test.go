// SPDX-License-Identifier: MPL-2.0

package worldlib

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestJavaChunkNBTCumulativeValueBudget(t *testing.T) {
	t.Run("compound members", func(t *testing.T) {
		var raw bytes.Buffer
		raw.Write([]byte{byte(nbtCompound), 0, 0})
		for range maxChunkNBTValues {
			raw.Write([]byte{byte(nbtByte), 0, 1, 'x', 0})
		}
		raw.WriteByte(byte(nbtEnd))
		if _, err := ParseJavaChunkSurface(raw.Bytes()); err == nil || !strings.Contains(err.Error(), "value budget") {
			t.Fatalf("tag-heavy compound was not rejected by the cumulative budget: %v", err)
		}
	})
	t.Run("list capacity checked before allocation", func(t *testing.T) {
		var raw bytes.Buffer
		raw.Write([]byte{byte(nbtCompound), 0, 0, byte(nbtList), 0, 1, 'x', byte(nbtInt)})
		_ = binary.Write(&raw, binary.BigEndian, int32(maxChunkNBTValues))
		if _, err := ParseJavaChunkSurface(raw.Bytes()); err == nil || !strings.Contains(err.Error(), "value budget") {
			t.Fatalf("requested list allocation was not refused before reading its items: %v", err)
		}
	})
}
