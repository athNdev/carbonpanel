// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTransientNextRunFailureLeavesTaskEnabledAndDue(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	ctx := context.Background()

	overdue := time.Now().Add(-time.Minute)
	task := &storage.ScheduledTask{
		ID: "task-transient-next-run", ServerID: "srv-transient", Name: "transient write",
		TaskType: storage.TaskTypeCommand, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeInterval, IntervalSecs: 60, NextRun: &overdue,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	transient := errors.New("temporary storage failure")
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register(
		"fixture:fail-next-run-write",
		func(tx *gorm.DB) {
			if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "scheduled_tasks" {
				return
			}
			updates, ok := tx.Statement.Dest.(map[string]any)
			if !ok {
				return
			}
			if _, updatingNextRun := updates["next_run"]; updatingNextRun {
				_ = tx.AddError(transient)
			}
		},
	))

	s.checkAndRunDueTasks()
	s.wg.Wait()

	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusEnabled, stored.Status, "a transient write failure must not disable a valid task")
	require.NotNil(t, stored.NextRun)
	require.True(t, stored.NextRun.Equal(overdue), "a transient write failure must leave the task due")
	require.False(t, s.IsTaskRunning(task.ID), "the failed claim must be released")
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Empty(t, executions)
}

func TestInvalidTimezoneDisableDoesNotClobberCorrectedSchedule(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	ctx := context.Background()

	require.NoError(t, store.CreateServer(ctx, &storage.Server{
		ID: "srv-timezone-race", Name: "timezone-race", Status: storage.StatusStopped,
	}))
	overdue := time.Now().Add(-time.Minute)
	task := &storage.ScheduledTask{
		ID: "task-timezone-race", ServerID: "srv-timezone-race", Name: "timezone race",
		TaskType: storage.TaskTypeCommand, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "Bad/Zone", NextRun: &overdue,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	corrected := false
	correctedDeadline := time.Now().Add(time.Hour)
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register(
		"fixture:correct-schedule-before-invalid-disable",
		func(tx *gorm.DB) {
			if corrected || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "scheduled_tasks" {
				return
			}
			updates, ok := tx.Statement.Dest.(map[string]any)
			if !ok {
				return
			}
			status, updatingStatus := updates["status"]
			if !updatingStatus || status != storage.TaskStatusDisabled {
				return
			}

			corrected = true
			result := store.DB().WithContext(ctx).Model(&storage.ScheduledTask{}).
				Where("id = ?", task.ID).
				Updates(map[string]any{"timezone": "UTC", "next_run": correctedDeadline})
			require.NoError(t, result.Error)
			require.Equal(t, int64(1), result.RowsAffected)
		},
	))

	s.checkAndRunDueTasks()
	s.wg.Wait()

	require.True(t, corrected, "the fixture must correct the schedule before the disable write")
	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusEnabled, stored.Status, "a corrected schedule must not be disabled")
	require.Equal(t, "UTC", stored.Timezone)
	require.NotNil(t, stored.NextRun)
	require.True(t, stored.NextRun.Equal(correctedDeadline))
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Empty(t, executions)
}

func TestCalculateNextRunUnknownTimezoneIsActionable(t *testing.T) {
	s := &Scheduler{
		cronParser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		log:        logger.New(),
	}

	_, err := s.CalculateNextRun(&storage.ScheduledTask{
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "Not/AZone",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "Not/AZone")
	require.Contains(t, err.Error(), "IANA")
	require.Contains(t, err.Error(), "tzdata")
}

func TestCalculateNextRunCronTimezonePrecedence(t *testing.T) {
	s := &Scheduler{
		cronParser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		log:        logger.New(),
	}

	selectedZone, err := time.LoadLocation("Australia/Sydney")
	require.NoError(t, err)
	for _, test := range []struct {
		name     string
		expr     string
		timezone string
	}{
		{name: "bare expression uses task timezone", expr: "0 3 * * *", timezone: "Australia/Sydney"},
		{name: "CRON_TZ prefix wins", expr: "CRON_TZ=Australia/Sydney 0 3 * * *", timezone: "UTC"},
		{name: "TZ prefix wins", expr: "TZ=Australia/Sydney 0 3 * * *", timezone: "UTC"},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, err := s.CalculateNextRun(&storage.ScheduledTask{
				Schedule: storage.ScheduleTypeCron, CronExpr: test.expr, Timezone: test.timezone,
			})
			require.NoError(t, err)
			require.Equal(t, 3, next.In(selectedZone).Hour(), "the cron slot must remain 03:00 in the selected zone")
		})
	}
}
