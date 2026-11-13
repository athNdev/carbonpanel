// SPDX-License-Identifier: MPL-2.0
//go:build windows

package nodestorage

import "os"

func adoptStreamOwnership(ownershipFile, os.FileInfo) {}
