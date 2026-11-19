// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !windows

package services

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestRemoteSavePreservesInstalledOwnerAndMode checks the installed-file
// expectation for the remote-save path: replacing a world/mod file must leave
// the uid/gid and permission bits the game container relies on. The controller
// commonly runs as root while the container runs unprivileged, so a widened or
// re-owned replacement would break the server on next write.
func TestRemoteSavePreservesInstalledOwnerAndMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "installed.jar")
	if err := os.WriteFile(target, []byte("old"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, ok := before.Sys().(*syscall.Stat_t)
	if !ok {
		t.Skip("platform has no uid/gid ownership model")
	}

	if err := saveRemoteArchiveAtomically(target, bytes.NewReader([]byte("new")), 64); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	afterStat, ok := after.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("ownership info vanished after staged save")
	}
	if afterStat.Uid != beforeStat.Uid || afterStat.Gid != beforeStat.Gid {
		t.Fatalf("staged save changed owner: got %d:%d, want %d:%d",
			afterStat.Uid, afterStat.Gid, beforeStat.Uid, beforeStat.Gid)
	}
	if got, want := after.Mode().Perm(), before.Mode().Perm(); got != want {
		t.Fatalf("staged save changed mode: got %o, want %o", got, want)
	}
}
