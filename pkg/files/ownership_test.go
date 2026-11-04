// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !windows

package files

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAdoptOwnershipCopiesTheReferenceOwner is the regression for the modpack
// crash-loop: a pack extracted by a root panel into a uid-1000 data directory
// left root-owned server.properties, and the game could not rewrite it.
func TestAdoptOwnershipCopiesTheReferenceOwner(t *testing.T) {
	dir := t.TempDir()
	reference := filepath.Join(dir, "data")
	if err := os.Mkdir(reference, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "extracted")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(target, "server.properties")
	if err := os.WriteFile(nested, []byte("motd=x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	want, ok := ownerOf(reference)
	if !ok {
		t.Skip("this platform has no uid/gid")
	}

	if err := AdoptTreeOwnership(reference, target); err != nil {
		t.Fatalf("AdoptTreeOwnership: %v", err)
	}

	for _, path := range []string{target, nested} {
		got, ok := ownerOf(path)
		if !ok {
			t.Fatalf("owner of %s unavailable", path)
		}
		if got != want {
			t.Errorf("%s owner = %+v, want %+v", path, got, want)
		}
	}
}

func TestAdoptOwnershipIgnoresAMissingReference(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "f")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No reference means "do not touch anything", not "make it root's".
	if err := AdoptOwnership(filepath.Join(dir, "does-not-exist"), target, false); err != nil {
		t.Fatalf("AdoptOwnership: %v", err)
	}
}

func TestAdoptOwnershipLeavesMatchingOwnersAlone(t *testing.T) {
	dir := t.TempDir()
	reference := filepath.Join(dir, "data")
	if err := os.Mkdir(reference, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(reference, "server.properties")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, ok := ownerOf(target)
	if !ok {
		t.Skip("this platform has no uid/gid")
	}
	if err := AdoptOwnership(reference, target, false); err != nil {
		t.Fatalf("AdoptOwnership: %v", err)
	}
	after, _ := ownerOf(target)
	if after != before {
		t.Errorf("owner changed from %+v to %+v", before, after)
	}
}
