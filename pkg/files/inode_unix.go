//go:build !windows

package files

import (
	"fmt"
	"syscall"
)

// GetFreeInodes returns free inodes on the filesystem hosting path.
func GetFreeInodes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", path, err)
	}
	return int64(stat.Ffree), nil
}
