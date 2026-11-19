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
	matches, err := filepath.Glob(filepath.Join(dir, ".atomic-staging-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("staging files left behind: %v", matches)
	}
}

// TestAtomicWriterStagingIsPrivateUntilPublish proves a partial write is never
// readable through the staged file or its directory. The test-only stagingHook
// observes the exact in-progress path; no exploit reproduction is involved.
func TestAtomicWriterStagingIsPrivateUntilPublish(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "installed.jar")
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	writer, err := NewAtomicWriter(target, 64)
	if err != nil {
		t.Fatal(err)
	}
	var stagedPath string
	writer.stagingHook = func(path string) { stagedPath = path }

	if _, err := writer.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if stagedPath == "" {
		t.Fatal("staging hook was not called")
	}
	stagedInfo, err := os.Stat(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := stagedInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("staged file mode = %o, want 0600", perm)
	}
	stagingDir := filepath.Dir(stagedPath)
	dirInfo, err := os.Stat(stagingDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("staging directory mode = %o, want 0700", perm)
	}
	partial, err := os.ReadFile(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(partial) != "partial" {
		t.Fatalf("staged partial content = %q", partial)
	}
	before, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != "old" {
		t.Fatalf("destination changed during staging: %q", before)
	}

	if _, err := writer.Write([]byte("-rest")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stagingDir); !os.IsNotExist(err) {
		t.Fatalf("staging directory survived publish: %v", err)
	}
	published, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(published) != "partial-rest" {
		t.Fatalf("published content = %q", published)
	}
	publishedInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := publishedInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("stricter destination was broadened to %o", perm)
	}
}

// TestAtomicWriterNewDestinationStagingPrivateAndUmaskFinalMode covers a new
// destination: the staged file stays private while the final mode comes from
// the restrictive umask only at publish time.
func TestAtomicWriterNewDestinationStagingPrivateAndUmaskFinalMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "new.jar")

	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })

	writer, err := NewAtomicWriter(target, 64)
	if err != nil {
		t.Fatal(err)
	}
	var stagedPath string
	writer.stagingHook = func(path string) { stagedPath = path }
	if _, err := writer.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("destination exists before publish: %v", err)
	}
	stagedInfo, err := os.Stat(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := stagedInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("staged file mode = %o, want 0600", perm)
	}
	dirInfo, err := os.Stat(filepath.Dir(stagedPath))
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0o700 {
		t.Fatalf("staging directory mode = %o, want 0700", perm)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	publishedInfo, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if perm := publishedInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("final new-destination mode = %o, want 0600 under umask 077", perm)
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
