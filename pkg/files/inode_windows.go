//go:build windows

package files

import (
	"fmt"
)

// GetFreeInodes returns free inodes on the filesystem hosting path.
func GetFreeInodes(path string) (int64, error) {
	return 0, fmt.Errorf("inode counts are not available on Windows")
}
