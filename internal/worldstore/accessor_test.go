// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldstore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

func TestBackupToRestoreFromAccessorRoundTrip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "worker")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFileRel(t, root, "world/level.dat", "level")
	writeFileRel(t, root, "server.properties", "motd=hello")
	writeFileRel(t, root, "logs/latest.log", "noisy")

	acc, err := nodestorage.NewLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = acc.Close() })

	archive := filepath.Join(t.TempDir(), "backup.zip")
	if _, err := BackupToAccessor(acc, archive, func(rel string) bool {
		return strings.HasPrefix(rel, "logs/")
	}); err != nil {
		t.Fatalf("BackupToAccessor: %v", err)
	}
	if !HasWorldstoreManifest(archive) {
		t.Fatal("backup archive must carry the worldstore manifest")
	}

	dest := filepath.Join(t.TempDir(), "restored")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFileRel(t, dest, "stale.txt", "left-over")
	out, err := nodestorage.NewLocal(dest)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = out.Close() })
	if err := RestoreFromAccessor(out, archive); err != nil {
		t.Fatalf("RestoreFromAccessor: %v", err)
	}
	if got := readFileRel(t, dest, "world/level.dat"); got != "level" {
		t.Fatalf("level.dat = %q", got)
	}
	if got := readFileRel(t, dest, "server.properties"); got != "motd=hello" {
		t.Fatalf("server.properties = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dest, "stale.txt")); !os.IsNotExist(err) {
		t.Fatal("stale file survived accessor restore")
	}
	if _, err := os.Stat(filepath.Join(dest, "logs", "latest.log")); !os.IsNotExist(err) {
		t.Fatal("excluded log must not be restored")
	}
}

func TestBackupToAccessorNilAccessorUnavailable(t *testing.T) {
	if _, err := BackupToAccessor(nil, filepath.Join(t.TempDir(), "x.zip"), nil); !nodestorage.IsUnavailable(err) {
		t.Fatalf("nil accessor error = %v, want unavailable", err)
	}
	if err := RestoreFromAccessor(nil, filepath.Join(t.TempDir(), "x.zip")); !nodestorage.IsUnavailable(err) {
		t.Fatalf("nil restore error = %v, want unavailable", err)
	}
}
