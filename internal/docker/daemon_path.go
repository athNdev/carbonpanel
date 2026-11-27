// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"path"
	"strings"
)

// DataPathOverlaps reports whether either daemon path contains the other.
// Bind mounts are effective in both directions: a nested mount can write into
// its parent, and an ancestor mount can write into the data directory. The
// comparison is lexical because these paths belong to the daemon, which may
// use a different platform and separator convention than the controller.
func DataPathOverlaps(a, b string) bool {
	return daemonPathWithin(a, b) || daemonPathWithin(b, a)
}

// daemonPathWithin reports whether target is root itself or below it. Paths are
// compared on separator boundaries so /data/srv-evil is not inside /data/srv.
func daemonPathWithin(root, target string) bool {
	rootPath, rootWindows := normalizeDaemonPath(root)
	targetPath, targetWindows := normalizeDaemonPath(target)
	if rootPath == "" || targetPath == "" {
		return false
	}
	if rootWindows || targetWindows {
		// Windows daemon paths are case-insensitive. A controller on another
		// platform cannot prove the daemon's case behaviour, so choose the
		// conservative comparison when either side is a Windows path.
		rootPath = strings.ToLower(rootPath)
		targetPath = strings.ToLower(targetPath)
	}
	if rootPath == targetPath {
		return true
	}
	if !strings.HasSuffix(rootPath, "/") {
		rootPath += "/"
	}
	return strings.HasPrefix(targetPath, rootPath)
}

// normalizeDaemonPath accepts POSIX and Windows daemon paths on any controller
// platform. Windows-style separators are folded to '/', repeated separators and
// dot segments are cleaned, and a UNC prefix is preserved.
func normalizeDaemonPath(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	windows := isWindowsAbsoluteHostPath(raw) || strings.HasPrefix(raw, `\\`)
	unc := strings.HasPrefix(raw, `\\`) || strings.HasPrefix(raw, "//")
	if windows || unc || strings.Contains(raw, `\`) {
		// A backslash is a separator on a Windows daemon; treating it as one
		// here also keeps a Windows path presented to a POSIX controller from
		// being compared as one opaque name.
		windows = true
		raw = strings.ReplaceAll(raw, `\`, "/")
	}
	if unc {
		trimmed := strings.TrimLeft(raw, "/")
		return "//" + strings.TrimPrefix(path.Clean("/"+trimmed), "/"), true
	}
	return path.Clean("/" + strings.TrimLeft(raw, "/")), windows
}
