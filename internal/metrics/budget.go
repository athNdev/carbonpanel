// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package metrics

import (
	"context"
	"os"
	"strings"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/pkg/files"
)

// Budget is a measured resource budget or an explicit unknown state. Value is
// bytes for byte budgets and a free-inode count for inode budgets.
type Budget struct {
	Known  bool
	Value  int64
	Reason string
}

func unknownBudget(reason string) Budget {
	return Budget{Known: false, Reason: reason}
}

func knownBudget(value int64) Budget {
	return Budget{Known: true, Value: value}
}

func backupStagingBudget(cfg *config.Config) Budget {
	if cfg == nil {
		return unknownBudget("panel config unavailable")
	}
	dir := strings.TrimSpace(cfg.Storage.TempDir)
	if dir == "" {
		return unknownBudget("storage.temp_dir unset")
	}
	size, err := files.CalculateDirSize(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return knownBudget(0)
		}
		return unknownBudget("backup staging directory unreadable")
	}
	return knownBudget(size)
}

func worldInodeBudget(root string) Budget {
	if strings.TrimSpace(root) == "" {
		return unknownBudget("world path unset")
	}
	free, err := files.GetFreeInodes(root)
	if err != nil {
		return unknownBudget("inode count unavailable on this platform")
	}
	return knownBudget(free)
}

func (c *Collector) imageCacheBudget(ctx context.Context, nodeID string) Budget {
	if c == nil || c.imageDiskUsage == nil {
		return unknownBudget("docker client unavailable")
	}
	bytes, err := c.imageDiskUsage.ImageStoreBytes(ctx, nodeID)
	if err != nil {
		return unknownBudget("image store measurement failed")
	}
	return knownBudget(bytes)
}
