// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostmount

import (
	"bufio"
	"io"
	"os"
	"path"
	"strings"
)

// mountEntry is a persistent mount point from /proc/self/mountinfo. The
// filesystem type is used only to drop virtual mounts, so it is not kept.
type mountEntry struct {
	point string
}

// virtualFilesystems never hold server data: a mount of one of these must not
// be mistaken for a persistent volume.
var virtualFilesystems = map[string]bool{
	"proc":        true,
	"sysfs":       true,
	"tmpfs":       true,
	"devpts":      true,
	"devtmpfs":    true,
	"mqueue":      true,
	"cgroup":      true,
	"cgroup2":     true,
	"overlay":     true,
	"ramfs":       true,
	"securityfs":  true,
	"debugfs":     true,
	"tracefs":     true,
	"configfs":    true,
	"fusectl":     true,
	"binfmt_misc": true,
}

// parseMountinfo extracts the persistent mount points from mountinfo text.
//
// A line is "id parent major:minor root point options [optional] - fs source
// super-options". Only the mount point and the filesystem type are needed: the
// point says which paths are persistent, the type excludes the virtual ones.
func parseMountinfo(r io.Reader) []mountEntry {
	var entries []mountEntry
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		fields := strings.Fields(line[:sep])
		if len(fields) < 5 {
			continue
		}
		point := unescapeMountPath(fields[4])
		after := strings.Fields(line[sep+3:])
		if len(after) == 0 {
			continue
		}
		fs := after[0]
		if virtualFilesystems[fs] || point == "/" {
			continue
		}
		entries = append(entries, mountEntry{point: point})
	}
	return entries
}

// unescapeMountPath decodes the octal escapes the kernel uses for spaces and
// other special characters in mount paths.
func unescapeMountPath(p string) string {
	if !strings.Contains(p, `\`) {
		return p
	}
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '\\' && i+3 < len(p) {
			if oct, ok := parseOctal(p[i+1 : i+4]); ok {
				b.WriteByte(byte(oct))
				i += 3
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return b.String()
}

func parseOctal(s string) (int, bool) {
	value := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '7' {
			return 0, false
		}
		value = value*8 + int(s[i]-'0')
	}
	return value, true
}

// inContainer reports whether this process is running in a container. The
// marker files are created by the runtime, so the check does not depend on the
// Docker daemon being reachable.
func inContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

// CheckMounted refuses a data directory that is not on a persistent mount. It
// runs before the database is created, so a compose file with a missing or
// misspelt volume fails without writing anything into the container layer.
//
// Only the data directory is fatal. Backups and scratch space may legitimately
// live somewhere this check cannot see (an S3-only deployment keeps a local
// scratch directory), so those are reported by UnmountedWarnings instead.
//
// A panel running directly on a host has nothing to check and returns nil. So
// does a platform where mountinfo is not available; the daemon-backed check in
// Resolve still applies there.
func CheckMounted(dirs Dirs) error {
	entries, ok := containerMounts()
	if !ok {
		return nil
	}
	if coveredByMount(entries, dirs.DataDir) {
		return nil
	}
	return &NotMountedError{Dir: dirs.DataDir, Setting: "storage.data_dir"}
}

// UnmountedWarnings names the non-critical directories that are not on a
// persistent mount. They are usable, but their contents do not survive a
// container recreate.
func UnmountedWarnings(dirs Dirs) []string {
	entries, ok := containerMounts()
	if !ok {
		return nil
	}
	var warnings []string
	for _, entry := range []struct {
		name string
		path string
	}{
		{"storage.backup_dir", dirs.BackupDir},
		{"storage.temp_dir", dirs.TempDir},
	} {
		if entry.path == "" || coveredByMount(entries, entry.path) {
			continue
		}
		warnings = append(warnings, entry.name+" "+entry.path)
	}
	return warnings
}

// Indirections so a test can drive the checks with a fixture instead of the
// real container it happens to run in.
var (
	detectContainer = inContainer
	readMountTable  = hostMounts
)

// containerMounts returns the persistent mounts of this process's container, or
// false when the process is not in a container or the table cannot be read.
func containerMounts() ([]mountEntry, bool) {
	if !detectContainer() {
		return nil, false
	}
	return readMountTable()
}

// coveredByMount reports whether a persistent mount covers dir.
func coveredByMount(entries []mountEntry, dir string) bool {
	cleaned := path.Clean(strings.TrimSpace(dir))
	for _, entry := range entries {
		point := path.Clean(entry.point)
		if cleaned == point || strings.HasPrefix(cleaned, strings.TrimSuffix(point, "/")+"/") {
			return true
		}
	}
	return false
}
