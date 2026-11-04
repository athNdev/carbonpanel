// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostmount

import (
	"errors"
	"strings"
	"testing"
)

// A real mountinfo line for a bind mount has the host path in the root field
// and the container path in the mount point field. Only the point matters here.
const sampleMountinfo = `1052 1049 8:1 / / rw,relatime - ext4 /dev/sda1 rw
1053 1052 0:50 / /proc rw,nosuid,nodev,noexec,relatime - proc proc rw
1060 1052 8:1 /srv/carbon-panel/data /app/data rw,relatime - ext4 /dev/sda1 rw
1061 1052 8:1 /srv/carbon-panel/backups /app/backups ro,relatime - ext4 /dev/sda1 ro
1062 1052 0:52 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw,size=65536k
1063 1052 0:53 / /app/data/scratch rw,nosuid - tmpfs tmpfs rw
1064 1052 0:40 / /dev/shm rw,nosuid,nodev - tmpfs shm rw
`

func TestParseMountinfoKeepsOnlyPersistentMounts(t *testing.T) {
	entries := parseMountinfo(strings.NewReader(sampleMountinfo))

	var points []string
	for _, entry := range entries {
		points = append(points, entry.point)
	}
	joined := strings.Join(points, ",")
	for _, want := range []string{"/app/data", "/app/backups"} {
		if !strings.Contains(joined, want) {
			t.Errorf("mount points = %v, want %s", points, want)
		}
	}
	for _, unwanted := range []string{"/proc", "/tmp", "/dev/shm", "/app/data/scratch"} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("mount points = %v, want %s excluded", points, unwanted)
		}
	}
	if len(entries) != 2 {
		t.Errorf("entries = %d, want 2 persistent mounts", len(entries))
	}
}

func TestParseMountinfoUnescapesSpaces(t *testing.T) {
	line := `1052 1049 8:1 / /srv/my\040data rw,relatime - ext4 /dev/sda1 rw`
	entries := parseMountinfo(strings.NewReader(line))
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one", entries)
	}
	if entries[0].point != "/srv/my data" {
		t.Fatalf("point = %q, want the unescaped path", entries[0].point)
	}
}

// TestCheckMountedOnlyRefusesTheDataDirectory is the contract: losing the world
// is fatal, losing scratch space is not.
func TestCheckMountedOnlyRefusesTheDataDirectory(t *testing.T) {
	withMountinfo(t, sampleMountinfo)

	if err := CheckMounted(Dirs{DataDir: "/app/data", BackupDir: "/app/backups", TempDir: "/var/tmp"}); err != nil {
		t.Fatalf("CheckMounted refused a mounted data directory: %v", err)
	}

	err := CheckMounted(Dirs{DataDir: "/var/lib/panel", BackupDir: "/app/backups", TempDir: "/app/data/tmp"})
	if err == nil {
		t.Fatal("error = nil for an unmounted data directory")
	}
	var notMounted *NotMountedError
	if !errors.As(err, &notMounted) {
		t.Fatalf("error = %T, want NotMountedError", err)
	}
	if notMounted.Dir != "/var/lib/panel" || notMounted.Setting != "storage.data_dir" {
		t.Errorf("error = %+v, want it to name the data directory", notMounted)
	}
	if !errors.Is(err, ErrNotMounted) {
		t.Error("error does not match ErrNotMounted")
	}
}

func TestUnmountedWarningsNameBackupsAndScratchOnly(t *testing.T) {
	withMountinfo(t, sampleMountinfo)

	warnings := UnmountedWarnings(Dirs{DataDir: "/app/data", BackupDir: "/app/backups", TempDir: "/var/tmp"})
	if len(warnings) != 1 || !strings.Contains(warnings[0], "storage.temp_dir") {
		t.Fatalf("warnings = %v, want only the temp dir", warnings)
	}

	full := UnmountedWarnings(Dirs{DataDir: "/app/data", BackupDir: "/app/backups", TempDir: "/app/data/tmp"})
	if len(full) != 0 {
		t.Fatalf("warnings = %v, want none when everything is mounted", full)
	}
}

func TestCheckMountedIsANoOpWhenMountinfoIsUnavailable(t *testing.T) {
	previousDetect, previousRead := detectContainer, readMountTable
	detectContainer = func() bool { return true }
	readMountTable = func() ([]mountEntry, bool) { return nil, false }
	t.Cleanup(func() {
		detectContainer, readMountTable = previousDetect, previousRead
	})

	if err := CheckMounted(Dirs{DataDir: "/app/data"}); err != nil {
		t.Fatalf("CheckMounted refused without a mount table: %v", err)
	}
}

func TestCoveredByMountUsesAPrefixOfWholeSegments(t *testing.T) {
	entries := []mountEntry{{point: "/app"}}
	if !coveredByMount(entries, "/app/data") {
		t.Error("/app/data is not covered by a mount at /app")
	}
	// "/application" must not be treated as inside "/app".
	if coveredByMount(entries, "/application") {
		t.Error("/application was treated as inside /app")
	}
}

func TestNotMountedErrorExplainsTheConsequence(t *testing.T) {
	err := &NotMountedError{Dir: "/app/data", Setting: "storage.data_dir"}
	message := err.Error()
	for _, want := range []string{"/app/data", "storage.data_dir", "lost when it is recreated"} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q, want %q", message, want)
		}
	}
}

// withMountinfo installs a mount table for the duration of a test and makes the
// checks believe they run in a container.
func withMountinfo(t *testing.T, text string) {
	t.Helper()
	previousDetect, previousRead := detectContainer, readMountTable
	detectContainer = func() bool { return true }
	readMountTable = func() ([]mountEntry, bool) {
		return parseMountinfo(strings.NewReader(text)), true
	}
	t.Cleanup(func() {
		detectContainer, readMountTable = previousDetect, previousRead
	})
}
