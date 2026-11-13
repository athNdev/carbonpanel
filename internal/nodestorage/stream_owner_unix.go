// SPDX-License-Identifier: MPL-2.0
//go:build !windows

package nodestorage

import (
	"os"
	"syscall"
)

func adoptStreamOwnership(file *os.File, reference os.FileInfo) {
	if reference == nil {
		return
	}
	owner, ok := reference.Sys().(*syscall.Stat_t)
	if !ok {
		return
	}
	current, err := file.Stat()
	if err != nil {
		return
	}
	stat, ok := current.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid == owner.Uid && stat.Gid == owner.Gid {
		return
	}
	// Ownership follows the existing best-effort platform contract. Work through
	// pinned handles so adoption cannot follow a replaced path outside the root.
	_ = file.Chown(int(owner.Uid), int(owner.Gid))
}
