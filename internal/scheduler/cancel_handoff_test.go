// SPDX-License-Identifier: MPL-2.0
package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// A cancellation that arrives after the claimed row is inserted but before the
// worker registers its cancel function must win the handoff: the planner is
// never invoked and the row keeps the cancelled outcome.
func TestCancelDuringClaimHandoffDoesNotRunPlanner(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{ID: "cancel-handoff", Name: "cancel-handoff", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	task := &storage.ScheduledTask{
		ID: "cancel-handoff-task", ServerID: server.ID, Name: "cancel handoff",
		TaskType: storage.TaskTypeModUpdate, Schedule: storage.ScheduleTypeInterval,
		Status: storage.TaskStatusEnabled, Config: `{"enabled":true}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	s := NewScheduler(store, nil, nil, nil, nil, logger.New())
	planner := &fakeModPlanner{}
	s.SetModUpdatePlanner(planner)

	claim := newScheduledExecution(task, time.Now())
	require.NoError(t, store.CreateTaskExecution(ctx, claim))
	s.registerPendingExecution(claim.ID)
	require.NoError(t, s.CancelExecution(claim.ID))

	execution, err := s.executeTask(ctx, task, "scheduled", 0, nil, claim)
	require.ErrorIs(t, err, context.Canceled)
	require.NotNil(t, execution)
	require.Zero(t, planner.calls, "the planner must not run after a cancellation won the handoff")
	stored, err := store.GetTaskExecution(ctx, claim.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusCancelled, stored.Status)
	require.Equal(t, "execution cancelled", stored.Error)
	require.NotNil(t, stored.EndedAt)
	running, err := store.HasRunningExecution(ctx, task.ID)
	require.NoError(t, err)
	require.False(t, running)
}

// A late worker terminal write must not overwrite an outcome a cancellation or
// another writer already recorded.
func TestTerminalWriteKeepsCancelledOutcome(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{ID: "terminal-cancel", Name: "terminal-cancel", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	task := &storage.ScheduledTask{
		ID: "terminal-cancel-task", ServerID: server.ID, Name: "terminal cancel",
		TaskType: storage.TaskTypeCommand, Schedule: storage.ScheduleTypeInterval,
		Status: storage.TaskStatusEnabled,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))
	now := time.Now()
	execution := &storage.TaskExecution{
		ID: uuid.NewString(), TaskID: task.ID, ServerID: server.ID,
		Status: storage.ExecutionStatusRunning, StartedAt: now,
	}
	require.NoError(t, store.CreateTaskExecution(ctx, execution))
	require.NoError(t, store.DB().Model(&storage.TaskExecution{}).Where("id = ?", execution.ID).
		Updates(map[string]any{"status": storage.ExecutionStatusCancelled, "error": "execution cancelled", "ended_at": &now}).Error)

	s := NewScheduler(store, nil, nil, nil, nil, logger.New())
	execution.Status = storage.ExecutionStatusCompleted
	execution.Error = ""
	s.persistTerminalExecution(ctx, execution)
	stored, err := store.GetTaskExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusCancelled, stored.Status)
	require.Equal(t, "execution cancelled", stored.Error)
}

// A refused slot claim still owes a terminal write. If storage fails once, the
// bounded retry must release the claim after recovery instead of leaving the
// row running and blocking later runs.
func TestFailClaimedExecutionRetriesAfterStorageRecovery(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{ID: "claim-write-recovery", Name: "claim-write-recovery", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	task := &storage.ScheduledTask{
		ID: "claim-write-recovery-task", ServerID: server.ID, Name: "claim write recovery",
		TaskType: storage.TaskTypeCommand, Schedule: storage.ScheduleTypeInterval,
		Status: storage.TaskStatusEnabled,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))
	execution := newScheduledExecution(task, time.Now())
	require.NoError(t, store.CreateTaskExecution(ctx, execution))

	failedOnce := false
	const callbackName = "fixture:fail-first-terminal-write"
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Schema == nil || tx.Statement.Schema.Table != "task_executions" {
			return
		}
		if failedOnce {
			return
		}
		failedOnce = true
		_ = tx.AddError(errors.New("storage unavailable"))
	}))
	t.Cleanup(func() { _ = store.DB().Callback().Update().Remove(callbackName) })

	s := NewScheduler(store, nil, nil, nil, nil, logger.New())
	s.failClaimedExecution(ctx, execution, "refused before dispatch")
	require.True(t, failedOnce, "the fixture must fail the first terminal write")
	stored, err := store.GetTaskExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusFailed, stored.Status)
	require.Equal(t, "refused before dispatch", stored.Error)
	require.NotNil(t, stored.EndedAt)
	running, err := store.HasRunningExecution(ctx, task.ID)
	require.NoError(t, err)
	require.False(t, running, "a recovered terminal write must release the claim")
}
