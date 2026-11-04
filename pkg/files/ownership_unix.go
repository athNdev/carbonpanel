// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

//go:build !windows

package files

import (
	"os"
	"syscall"
)

// ownerOf reports the uid/gid of path. The boolean is false when the owner
// cannot be established, in which case callers must not chown anything.
func ownerOf(path string) (ownership, bool) {
	if path == "" {
		return ownership{}, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return ownership{}, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ownership{}, false
	}
	return ownership{uid: int(stat.Uid), gid: int(stat.Gid)}, true
}
