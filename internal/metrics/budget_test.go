// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package metrics

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
)

func TestBackupStagingBudgetUnset(t *testing.T) {
	cfg := &config.Config{}
	cfg.Storage.TempDir = ""
	got := backupStagingBudget(cfg)
	if got.Known || got.Reason == "" {
		t.Fatalf("unset staging dir = %+v, want unknown", got)
	}
}

func TestBackupStagingBudgetMeasured(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "archive.tar"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Storage.TempDir = dir
	got := backupStagingBudget(cfg)
	if !got.Known || got.Value < 5 {
		t.Fatalf("staging budget = %+v, want measured bytes", got)
	}
}

func TestWorldFilesystemFreeBudget(t *testing.T) {
	got := worldFilesystemFreeBudget(t.TempDir())
	if !got.Known || got.Value < 0 {
		t.Fatalf("free bytes = %+v, want measured non-negative", got)
	}
}

func TestWorldInodeBudget(t *testing.T) {
	got := worldInodeBudget(t.TempDir())
	if got.Known && got.Value < 0 {
		t.Fatalf("free inodes = %d, want non-negative", got.Value)
	}
	if !got.Known && got.Reason == "" {
		t.Fatalf("unknown inode budget must include a reason, got %+v", got)
	}
}
