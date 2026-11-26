// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/scheduler"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func setupTaskTestService(t *testing.T) (*TaskService, *storage.Server) {
	t.Helper()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	log := logger.New()

	sched := scheduler.NewScheduler(store, nil, nil, nil, nil, log)
	taskSvc := NewTaskService(store, sched, log)

	server := &storage.Server{
		ID:       uuid.New().String(),
		Name:     "task-test-server",
		DataPath: t.TempDir(),
		Status:   storage.StatusStopped,
	}
	require.NoError(t, store.CreateServer(context.Background(), server))

	return taskSvc, server
}

func TestModUpdateTaskCreateHonorsOptInAndPolicyEditsKeepStatus(t *testing.T) {
	svc, server := setupTaskTestService(t)
	ctx := context.Background()
	for _, tc := range []struct {
		config string
		status v1.TaskStatus
	}{
		{`{"enabled":false,"window":"next_restart"}`, v1.TaskStatus_TASK_STATUS_DISABLED},
		{`{"enabled":true,"window":"next_restart"}`, v1.TaskStatus_TASK_STATUS_ENABLED},
	} {
		created, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
			ServerId: server.ID, Name: "Automatic mod updates", TaskType: v1.TaskType_TASK_TYPE_MOD_UPDATE,
			Schedule: v1.ScheduleType_SCHEDULE_TYPE_CRON, CronExpr: "0 3 * * *", Config: tc.config,
		}))
		require.NoError(t, err)
		task := created.Msg.GetTask()
		require.Equal(t, tc.status, task.GetStatus())
		if tc.status == v1.TaskStatus_TASK_STATUS_DISABLED {
			require.Nil(t, task.GetNextRun())
		} else {
			require.NotNil(t, task.GetNextRun())
		}
		for _, status := range []v1.TaskStatus{v1.TaskStatus_TASK_STATUS_PAUSED, v1.TaskStatus_TASK_STATUS_DISABLED} {
			_, err := svc.ToggleTask(ctx, connect.NewRequest(&v1.ToggleTaskRequest{Id: task.GetId(), Status: status}))
			require.NoError(t, err)
			editedConfig := `{"enabled":true,"window":"idle","idle_minutes":15}`
			updated, err := svc.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{Id: task.GetId(), Config: &editedConfig}))
			require.NoError(t, err)
			require.Equal(t, status, updated.Msg.GetTask().GetStatus())
			stored, err := svc.store.GetScheduledTask(ctx, task.GetId())
			require.NoError(t, err)
			require.Equal(t, status, dbTaskStatusToProto(stored.Status))
		}
	}
}

func TestModUpdatePolicyEditCannotOverwriteConcurrentPause(t *testing.T) {
	svc, server := setupTaskTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	created, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID, Name: "Automatic mod updates", TaskType: v1.TaskType_TASK_TYPE_MOD_UPDATE,
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_CRON, CronExpr: "0 3 * * *", Config: `{"enabled":true}`,
	}))
	require.NoError(t, err)
	taskID := created.Msg.GetTask().GetId()
	var paused bool
	require.NoError(t, svc.store.DB().Callback().Update().Before("gorm:update").Register("fixture:pause-before-policy-update", func(tx *gorm.DB) {
		if paused {
			return
		}
		if _, ok := tx.Statement.Dest.(*storage.ScheduledTask); !ok {
			return
		}
		paused = true
		_, err := svc.ToggleTask(ctx, connect.NewRequest(&v1.ToggleTaskRequest{
			Id: taskID, Status: v1.TaskStatus_TASK_STATUS_PAUSED,
		}))
		require.NoError(t, err)
	}))
	editedConfig := `{"enabled":true,"window":"idle","idle_minutes":15}`
	request := connect.NewRequest(&v1.UpdateTaskRequest{Id: taskID, Config: &editedConfig})
	_, err = svc.UpdateTask(ctx, request)
	require.Equal(t, connect.CodeAborted, connect.CodeOf(err))
	require.True(t, paused)
	stored, err := svc.store.GetScheduledTask(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusPaused, stored.Status)
	require.Equal(t, `{"enabled":true}`, stored.Config)
	updated, err := svc.UpdateTask(ctx, request)
	require.NoError(t, err)
	require.Equal(t, v1.TaskStatus_TASK_STATUS_PAUSED, updated.Msg.GetTask().GetStatus())
	require.Nil(t, updated.Msg.GetTask().GetNextRun())
}

func TestModUpdatePolicyEditPreservesConcurrentDeadline(t *testing.T) {
	svc, server := setupTaskTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	created, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID, Name: "Automatic mod updates", TaskType: v1.TaskType_TASK_TYPE_MOD_UPDATE,
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_INTERVAL, IntervalSecs: 60, Config: `{"enabled":true}`,
	}))
	require.NoError(t, err)
	before, err := svc.store.GetScheduledTask(ctx, created.Msg.GetTask().GetId())
	require.NoError(t, err)
	run := time.Now().UTC().Truncate(time.Second)
	next := run.Add(2 * time.Hour)
	var advanced bool
	require.NoError(t, svc.store.DB().Callback().Update().Before("gorm:update").Register("fixture:advance-before-policy-update", func(tx *gorm.DB) {
		if advanced {
			return
		}
		if _, ok := tx.Statement.Dest.(*storage.ScheduledTask); !ok {
			return
		}
		advanced = true
		require.NoError(t, svc.store.AdvanceModUpdateSchedule(ctx, before, &next, run, "", nil))
	}))
	config := `{"enabled":true,"window":"idle","idle_minutes":18}`
	unchangedInterval := int32(60)
	response, err := svc.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{
		Id: before.ID, IntervalSecs: &unchangedInterval, Config: &config,
	}))
	require.NoError(t, err)
	require.True(t, advanced)
	require.True(t, next.Equal(response.Msg.GetTask().GetNextRun().AsTime()))
	require.True(t, run.Equal(response.Msg.GetTask().GetLastRun().AsTime()))
	require.Equal(t, config, response.Msg.GetTask().GetConfig())

	newInterval := int32(900)
	response, err = svc.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{
		Id: before.ID, IntervalSecs: &newInterval,
	}))
	require.NoError(t, err)
	recalculated := response.Msg.GetTask().GetNextRun().AsTime()
	require.WithinDuration(t, time.Now().Add(15*time.Minute), recalculated, time.Minute)
	require.ErrorIs(t, svc.store.AdvanceModUpdateSchedule(ctx, before, &next, run, "", nil), storage.ErrModUpdateScheduleChanged)
	stored, err := svc.store.GetScheduledTask(ctx, before.ID)
	require.NoError(t, err)
	require.True(t, recalculated.Equal(*stored.NextRun))
}

func TestTaskService_CreateTask_CronValidation(t *testing.T) {
	svc, server := setupTaskTestService(t)
	ctx := context.Background()

	// 1. Cron task with empty CronExpr must fail with CodeInvalidArgument (SCH-7)
	_, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID,
		Name:     "empty-cron-task",
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_CRON,
		CronExpr: "",
		TaskType: v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.Error(t, err)
	connectErr, ok := err.(*connect.Error)
	require.True(t, ok)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "cron_expr is required")

	// 2. Cron task with invalid cron syntax must fail with CodeInvalidArgument (SCH-7)
	_, err = svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID,
		Name:     "invalid-cron-task",
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_CRON,
		CronExpr: "not-a-valid-cron-spec",
		TaskType: v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.Error(t, err)
	connectErr, ok = err.(*connect.Error)
	require.True(t, ok)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "invalid cron expression")

	// 3. Cron task with valid syntax but no future occurrence must fail.
	_, err = svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID,
		Name:     "impossible-cron-task",
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_CRON,
		CronExpr: "0 3 31 2 *",
		TaskType: v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.Error(t, err)
	connectErr, ok = err.(*connect.Error)
	require.True(t, ok)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "no future occurrence")

	// 4. Cron task with valid cron syntax must succeed and calculate NextRun
	res, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID,
		Name:     "valid-cron-task",
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_CRON,
		CronExpr: "0 0 * * *",
		TaskType: v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, "valid-cron-task", res.Msg.Task.Name)
	assert.NotNil(t, res.Msg.Task.NextRun)
}

func TestTaskService_CreateTask_IntervalValidation(t *testing.T) {
	svc, server := setupTaskTestService(t)
	ctx := context.Background()

	// 1. Interval <= 0 must fail with CodeInvalidArgument
	_, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId:     server.ID,
		Name:         "bad-interval-task",
		Schedule:     v1.ScheduleType_SCHEDULE_TYPE_INTERVAL,
		IntervalSecs: 0,
		TaskType:     v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.Error(t, err)
	connectErr, ok := err.(*connect.Error)
	require.True(t, ok)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "interval_secs must be greater than 0")

	// 2. Valid interval must succeed
	res, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId:     server.ID,
		Name:         "valid-interval-task",
		Schedule:     v1.ScheduleType_SCHEDULE_TYPE_INTERVAL,
		IntervalSecs: 300,
		TaskType:     v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.Equal(t, int32(300), res.Msg.Task.IntervalSecs)
	assert.NotNil(t, res.Msg.Task.NextRun)
}

func TestTaskService_CreateTask_OnceValidation(t *testing.T) {
	svc, server := setupTaskTestService(t)
	ctx := context.Background()

	// 1. Once schedule without RunAt must fail
	_, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID,
		Name:     "missing-run-at-task",
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_ONCE,
		TaskType: v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.Error(t, err)
	connectErr, ok := err.(*connect.Error)
	require.True(t, ok)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())
	assert.Contains(t, connectErr.Message(), "run_at is required")

	// 2. Once schedule with future RunAt must succeed
	future := time.Now().Add(2 * time.Hour)
	res, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID,
		Name:     "valid-once-task",
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_ONCE,
		RunAt:    timestamppb.New(future),
		TaskType: v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.NoError(t, err)
	require.NotNil(t, res)
	assert.NotNil(t, res.Msg.Task.NextRun)
}

func TestTaskService_UpdateTask_CronValidation(t *testing.T) {
	svc, server := setupTaskTestService(t)
	ctx := context.Background()

	// Create valid initial task
	res, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID,
		Name:     "updatable-task",
		Schedule: v1.ScheduleType_SCHEDULE_TYPE_CRON,
		CronExpr: "0 12 * * *",
		TaskType: v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.NoError(t, err)
	taskID := res.Msg.Task.Id

	// 1. Update with invalid cron expression must fail with CodeInvalidArgument
	invalidCron := "bad-cron"
	_, err = svc.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{
		Id:       taskID,
		CronExpr: &invalidCron,
	}))
	require.Error(t, err)
	connectErr, ok := err.(*connect.Error)
	require.True(t, ok)
	assert.Equal(t, connect.CodeInvalidArgument, connectErr.Code())

	// 2. Update with valid cron expression must succeed
	validCron := "*/10 * * * *"
	updateRes, err := svc.UpdateTask(ctx, connect.NewRequest(&v1.UpdateTaskRequest{
		Id:       taskID,
		CronExpr: &validCron,
	}))
	require.NoError(t, err)
	assert.Equal(t, validCron, updateRes.Msg.Task.CronExpr)
}

func TestTaskService_TriggerTask_MissingMapsToNotFound(t *testing.T) {
	svc, _ := setupTaskTestService(t)
	ctx := context.Background()

	_, err := svc.TriggerTask(ctx, connect.NewRequest(&v1.TriggerTaskRequest{
		Id: "no-such-task",
	}))
	require.Error(t, err)
	connectErr, ok := err.(*connect.Error)
	require.True(t, ok, "expected connect error, got %T", err)
	assert.Equal(t, connect.CodeNotFound, connectErr.Code())
}

// Regression: backing up a stopped server must not be gated on the server
// being online. require_online is unset for most task types and must default
// per type (only RCON commands need a live server) instead of a blanket true.
func TestTaskService_CreateTask_RequireOnlineDefaults(t *testing.T) {
	svc, server := setupTaskTestService(t)
	ctx := context.Background()

	backup, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID, Name: "backup", Schedule: v1.ScheduleType_SCHEDULE_TYPE_INTERVAL,
		IntervalSecs: 3600, TaskType: v1.TaskType_TASK_TYPE_BACKUP,
	}))
	require.NoError(t, err)
	assert.False(t, backup.Msg.Task.RequireOnline, "backups must run while the server is stopped")

	cmd, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID, Name: "cmd", Schedule: v1.ScheduleType_SCHEDULE_TYPE_INTERVAL,
		IntervalSecs: 3600, TaskType: v1.TaskType_TASK_TYPE_COMMAND, Config: `{"command":"list"}`,
	}))
	require.NoError(t, err)
	assert.True(t, cmd.Msg.Task.RequireOnline, "RCON commands need a live server")

	cmd2, err := svc.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
		ServerId: server.ID, Name: "cmd2", Schedule: v1.ScheduleType_SCHEDULE_TYPE_INTERVAL,
		IntervalSecs: 3600, TaskType: v1.TaskType_TASK_TYPE_COMMAND, Config: `{"command":"list"}`,
		RequireOnline: false,
	}))
	require.NoError(t, err)
	assert.True(t, cmd2.Msg.Task.RequireOnline, "RCON while offline is nonsensical and cannot be configured")
}
