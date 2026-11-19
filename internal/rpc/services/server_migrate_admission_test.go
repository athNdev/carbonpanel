// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestMigrateServerTargetNodeIsGated proves a migration contends on the target
// node's in-process admission gate before it transfers any world (OSS-405). A
// held target-node gate makes the RPC fail closed rather than start an
// unaccounted container there.
func TestMigrateServerTargetNodeIsGated(t *testing.T) {
	f := newMigrationFixture(t)

	release, err := f.svc.admission.Acquire(context.Background(), "node-2")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err = f.svc.MigrateServer(ctx, connect.NewRequest(&v1.MigrateServerRequest{
		Id:           "server-1",
		TargetNodeId: "node-2",
		Force:        boolPtr(true),
	}))
	if err == nil || !strings.Contains(err.Error(), "node node-2 start admission") {
		t.Fatalf("MigrateServer error = %v, want target-node admission refusal", err)
	}
	if files := f.worldFiles(t, f.worldB); len(files) != 0 {
		t.Fatalf("refused migration transferred world files: %v", files)
	}
	if f.server.NodeID != "node-1" || f.server.ContainerID != "container-old-123" {
		t.Fatalf("refused migration changed server identity: %+v", f.server)
	}
}

// TestMigrateServerSourceNodeIsGated proves the source node is gated too, so a
// rollback that restarts the source container cannot start it ungated (OSS-405).
func TestMigrateServerSourceNodeIsGated(t *testing.T) {
	f := newMigrationFixture(t)

	release, err := f.svc.admission.Acquire(context.Background(), "node-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err = f.svc.MigrateServer(ctx, connect.NewRequest(&v1.MigrateServerRequest{
		Id:           "server-1",
		TargetNodeId: "node-2",
		Force:        boolPtr(true),
	}))
	if err == nil || !strings.Contains(err.Error(), "node node-1 start admission") {
		t.Fatalf("MigrateServer error = %v, want source-node admission refusal", err)
	}
	if files := f.worldFiles(t, f.worldB); len(files) != 0 {
		t.Fatalf("refused migration transferred world files: %v", files)
	}
}

// TestMigrationNodeGatesDedupeAliases proves two raw IDs that fold to the same
// canonical key are acquired once, not self-deadlocked against their own token.
func TestMigrationNodeGatesDedupeAliases(t *testing.T) {
	f := newMigrationFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	release, err := f.svc.acquireMigrationNodeGates(ctx, "", "local")
	if err != nil {
		t.Fatalf("alias-colliding gates deadlocked or failed: %v", err)
	}
	release()
}
