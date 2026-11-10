// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorldBytesCountsWorldDirectory(t *testing.T) {
	root := t.TempDir()
	world := filepath.Join(root, "world")
	if err := os.Mkdir(world, 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("level-dat-payload")
	if err := os.WriteFile(filepath.Join(world, "level.dat"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := WorldBytes(root)
	if err != nil {
		t.Fatalf("WorldBytes: %v", err)
	}
	if got != int64(len(payload)) {
		t.Fatalf("WorldBytes = %d, want %d", got, len(payload))
	}
}

func TestWorldBytesMissingWorldIsZero(t *testing.T) {
	got, err := WorldBytes(t.TempDir())
	if err != nil {
		t.Fatalf("WorldBytes empty tree: %v", err)
	}
	if got != 0 {
		t.Fatalf("WorldBytes = %d, want 0", got)
	}
	got, err = WorldBytes(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("WorldBytes missing dir: %v", err)
	}
	if got != 0 {
		t.Fatalf("missing dir WorldBytes = %d, want 0", got)
	}
}
