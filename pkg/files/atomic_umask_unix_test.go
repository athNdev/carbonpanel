// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !windows

package files

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestAtomicWriterNewDestinationHonorsRestrictiveUmask is the regression for
// the staged-save permission policy: a new destination must not be broadened
// past what the process umask allows. The old os.Create path emitted
// 0666 &^ umask; the staging writer must do the same.
func TestAtomicWriterNewDestinationHonorsRestrictiveUmask(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new-mod.jar")

	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	writer, err := NewAtomicWriter(target, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("new destination mode = %o, want 0600 under umask 077", perm)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".atomic-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staging files left behind: %v", matches)
	}
}

// TestAtomicWriterExistingDestinationKeepsModeUnderRestrictiveUmask keeps the
// preservation contract: replacing an installed file restores its previous
// permissions even when the current umask is stricter.
func TestAtomicWriterExistingDestinationKeepsModeUnderRestrictiveUmask(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "installed-mod.jar")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}

	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	writer, err := NewAtomicWriter(target, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o640 {
		t.Fatalf("existing destination mode = %o, want preserved 0640", perm)
	}
}
