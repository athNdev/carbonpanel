// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"testing"
	"time"
)

func TestInvite_UseCountAndDelete(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	if err := store.DB().Create(&RegistrationInvite{ID: "inv-1", Code: "C1", CreatedBy: "u1"}).Error; err != nil {
		t.Fatalf("seed failed: %v", err)
	}

	for i := 0; i < 3; i++ {
		if err := store.IncrementInviteUseCount(ctx, "inv-1"); err != nil {
			t.Fatalf("increment failed: %v", err)
		}
	}
	got, err := store.GetRegistrationInvite(ctx, "inv-1")
	if err != nil || got.UseCount != 3 {
		t.Fatalf("use count = %+v, %v", got, err)
	}

	if err := store.DeleteRegistrationInvite(ctx, "inv-1"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if _, err := store.GetRegistrationInvite(ctx, "inv-1"); err == nil {
		t.Error("deleted invite fetched")
	}
}

func TestSystemSetting_RoundTrip(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	if _, err := store.GetSystemSetting(ctx, "missing"); err == nil {
		t.Error("missing setting fetched")
	}
	if err := store.SetSystemSetting(ctx, "k", "v1"); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	got, err := store.GetSystemSetting(ctx, "k")
	if err != nil || got != "v1" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if err := store.SetSystemSetting(ctx, "k", "v2"); err != nil {
		t.Fatalf("overwrite failed: %v", err)
	}
	got, err = store.GetSystemSetting(ctx, "k")
	if err != nil || got != "v2" {
		t.Fatalf("after overwrite = %q, %v", got, err)
	}
}

func TestUpdateModUpdatePolicyRejectsStaleDefinitionWrite(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer func() { _ = store.Close() }()

	next := time.Now().Add(6 * time.Hour).UTC().Truncate(time.Second)
	observed := &ScheduledTask{
		ID: "policy-stale", ServerID: "srv-1", Name: "policy",
		TaskType: TaskTypeModUpdate, Status: TaskStatusEnabled,
		Schedule: ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "UTC",
		NextRun: &next, Config: `{"enabled":true,"excluded_projects":["old"]}`,
	}
	if err := store.CreateScheduledTask(ctx, observed); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// A concurrent policy-only edit commits after this writer observed the row.
	concurrent := *observed
	concurrent.Config = `{"enabled":true,"excluded_projects":["concurrent"]}`
	if err := store.UpdateScheduledTask(ctx, &concurrent); err != nil {
		t.Fatalf("concurrent edit failed: %v", err)
	}

	stale := *observed
	stale.Config = `{"enabled":true,"excluded_projects":["stale"]}`
	saved, err := store.UpdateModUpdatePolicy(ctx, observed, &stale, false)
	if err != nil {
		t.Fatalf("stale update failed: %v", err)
	}
	if saved {
		t.Fatal("a stale definition write must lose the race")
	}
	stored, err := store.GetScheduledTask(ctx, observed.ID)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if stored.Config != concurrent.Config {
		t.Fatalf("config = %q, want the concurrent definition", stored.Config)
	}
	if stored.NextRun == nil || !stored.NextRun.Equal(next) {
		t.Fatalf("next_run = %v, want the scheduler-owned deadline %v", stored.NextRun, next)
	}

	// The current definition still saves and keeps the scheduler deadline.
	current := *stored
	current.Config = `{"enabled":true,"excluded_projects":["new"]}`
	saved, err = store.UpdateModUpdatePolicy(ctx, stored, &current, false)
	if err != nil {
		t.Fatalf("current update failed: %v", err)
	}
	if !saved {
		t.Fatal("the current definition must save")
	}
	stored, err = store.GetScheduledTask(ctx, observed.ID)
	if err != nil {
		t.Fatalf("reload failed: %v", err)
	}
	if stored.Config != current.Config {
		t.Fatalf("config = %q, want %q", stored.Config, current.Config)
	}
	if stored.NextRun == nil || !stored.NextRun.Equal(next) {
		t.Fatalf("policy-only edit changed next_run to %v", stored.NextRun)
	}
}

func TestScheduledTask_CRUD(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer store.Close()

	mk := func(id, serverID string) *ScheduledTask {
		return &ScheduledTask{
			ID: id, ServerID: serverID, Name: "n",
			TaskType: TaskTypeBackup, Status: TaskStatusEnabled,
			Schedule: ScheduleTypeInterval, IntervalSecs: 3600,
		}
	}
	for _, task := range []*ScheduledTask{mk("t1", "srv-1"), mk("t2", "srv-1"), mk("t3", "srv-2")} {
		if err := store.CreateScheduledTask(ctx, task); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	got, err := store.GetScheduledTask(ctx, "t1")
	if err != nil || got.Name != "n" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := store.GetScheduledTask(ctx, "missing"); err == nil {
		t.Error("missing task fetched")
	}

	listed, err := store.ListScheduledTasks(ctx, "srv-1")
	if err != nil || len(listed) != 2 {
		t.Fatalf("per-server list = %d, %v", len(listed), err)
	}
	all, err := store.ListAllScheduledTasks(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("all list = %d, %v", len(all), err)
	}

	got.Status = TaskStatusDisabled
	if err := store.UpdateScheduledTask(ctx, got); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	after, err := store.GetScheduledTask(ctx, "t1")
	if err != nil || after.Status != TaskStatusDisabled {
		t.Fatalf("after update = %+v, %v", got, err)
	}
}
