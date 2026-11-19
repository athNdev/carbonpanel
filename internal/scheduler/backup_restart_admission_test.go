// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"context"
	"strings"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// TestBackupRestartIsGated proves the restart after a backup acquires the
// shared node gate before any Docker call (OSS-405).
func TestBackupRestartIsGated(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	coordinator := serverruntime.NewAdmissionCoordinator()
	release, err := coordinator.Acquire(ctx, "node-backup")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	s := NewScheduler(store, nil, nil, nil, nil, logger.New(), Config{Admission: coordinator})
	gatedCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	err = s.startServerAfterBackup(gatedCtx, &storage.Server{ID: "srv", NodeID: "node-backup", ContainerID: "c1"})
	if err == nil || !strings.Contains(err.Error(), "node node-backup start admission") {
		t.Fatalf("startServerAfterBackup error = %v, want node admission refusal", err)
	}
}
