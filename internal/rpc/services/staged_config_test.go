// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestStagedConfigServiceUsesSharedOperation verifies the generated service
// delegates to the same StagedConfigOperations the legacy JSON adapter uses:
// the record staged through the RPC is visible to the store and a later RPC
// applies it, with the proto mapping carrying the stored fields.
func TestStagedConfigServiceUsesSharedOperation(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()
	server := &storage.Server{ID: "srv-stage", Name: "srv-stage", NodeID: "local", DataPath: filepath.Join(t.TempDir(), "data")}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	ops := handlers.NewStagedConfigOperations(store)
	svc := NewStagedConfigService(store, ops, nil)

	stagedResp, err := svc.StageConfigChange(ctx, connect.NewRequest(&v1.StageConfigChangeRequest{
		ServerId:  server.ID,
		Changes:   `{"difficulty":"hard"}`,
		ApplyMode: "on_restart",
	}))
	if err != nil {
		t.Fatalf("StageConfigChange: %v", err)
	}
	staged := stagedResp.Msg.Change
	if staged == nil || staged.Id == "" || staged.ServerId != server.ID || staged.ApplyMode != "on_restart" {
		t.Fatalf("unexpected staged record: %v", staged)
	}

	listResp, err := svc.ListStagedChanges(ctx, connect.NewRequest(&v1.ListStagedChangesRequest{ServerId: server.ID}))
	if err != nil {
		t.Fatalf("ListStagedChanges: %v", err)
	}
	if listResp.Msg.Count != 1 || len(listResp.Msg.Changes) != 1 || listResp.Msg.Changes[0].Id != staged.Id {
		t.Fatalf("unexpected list: %v", listResp.Msg)
	}

	applyResp, err := svc.ApplyStagedChange(ctx, connect.NewRequest(&v1.ApplyStagedChangeRequest{ServerId: server.ID, StagedId: staged.Id}))
	if err != nil {
		t.Fatalf("ApplyStagedChange: %v", err)
	}
	if !applyResp.Msg.Success || applyResp.Msg.Change == nil {
		t.Fatalf("unexpected apply response: %v", applyResp.Msg)
	}

	record, err := store.GetStagedConfigChange(ctx, staged.Id)
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != storage.StagedStatusApplied {
		t.Fatalf("staged status = %q after apply", record.Status)
	}
}

// TestStagedConfigServiceRejectsForeignStagedID verifies a mutation cannot act
// on a record owned by another server without an explicit owner grant: with no
// enforcer the service fails closed rather than using the request's server ID.
func TestStagedConfigServiceRejectsForeignStagedID(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"srv-owner", "srv-caller"} {
		server := &storage.Server{ID: id, Name: id, NodeID: "local", DataPath: filepath.Join(t.TempDir(), "data")}
		if err := store.CreateServer(ctx, server); err != nil {
			t.Fatal(err)
		}
	}
	ops := handlers.NewStagedConfigOperations(store)
	staged, err := ops.Stage(ctx, "srv-owner", map[string]any{"difficulty": "hard"}, storage.StagedApplyOnRestart, "")
	if err != nil {
		t.Fatal(err)
	}
	svc := NewStagedConfigService(store, ops, nil) // nil enforcer fails closed
	_, err = svc.DiscardStagedChange(ctx, connect.NewRequest(&v1.DiscardStagedChangeRequest{ServerId: "srv-caller", StagedId: staged.ID}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("foreign owner code = %v, want permission_denied (err=%v)", connect.CodeOf(err), err)
	}
	// The record must still exist: no mutation ran.
	if _, err := store.GetStagedConfigChange(ctx, staged.ID); err != nil {
		t.Fatalf("staged record was mutated despite denial: %v", err)
	}
}

func TestStagedWorldLevelRPCRefusesApplyAndRollbackDuringBind(t *testing.T) {
	store := newWorldRPCStore(t)
	ctx := context.Background()
	server := &storage.Server{ID: "srv-stage-bind", Name: "srv-stage-bind", NodeID: "local"}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	ops := handlers.NewStagedConfigOperations(store)
	svc := NewStagedConfigService(store, ops, nil)
	staged, err := ops.Stage(ctx, server.ID, map[string]any{"level": "new"}, storage.StagedApplyOnRestart, "")
	if err != nil {
		t.Fatal(err)
	}
	apply := connect.NewRequest(&v1.ApplyStagedChangeRequest{ServerId: server.ID, StagedId: staged.ID})
	rollback := connect.NewRequest(&v1.RollbackStagedChangeRequest{ServerId: server.ID, StagedId: staged.ID})
	begin := func(key string) *storage.ServerOperation {
		op, _, err := store.BeginServerOperation(ctx, server.ID, server.NodeID, "", key, "world-bind", key)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	release := func(op *storage.ServerOperation) {
		if err := store.AdvanceServerOperation(ctx, op.ID, storage.ServerOperationPrepared, storage.ServerOperationFailed, "fixture finished"); err != nil {
			t.Fatal(err)
		}
	}

	op := begin("apply")
	if _, err := svc.ApplyStagedChange(ctx, apply); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("apply during world bind = %v", err)
	}
	release(op)
	if reply, err := svc.ApplyStagedChange(ctx, apply); err != nil || !reply.Msg.Success {
		t.Fatalf("apply after world bind released = %+v, %v", reply, err)
	}
	op = begin("rollback")
	if _, err := svc.RollbackStagedChange(ctx, rollback); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("rollback during world bind = %v", err)
	}
	release(op)
	if reply, err := svc.RollbackStagedChange(ctx, rollback); err != nil || !reply.Msg.Success {
		t.Fatalf("rollback after world bind released = %+v, %v", reply, err)
	}
}
