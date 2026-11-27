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

func newOfflineSkipFixture(t *testing.T, id string) (*Scheduler, *storage.Store, context.Context, *storage.ScheduledTask) {
	t.Helper()
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{ID: id, Name: id, Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	task := &storage.ScheduledTask{
		ID: id + "-task", ServerID: server.ID, Name: id,
		TaskType: storage.TaskTypeCommand, Schedule: storage.ScheduleTypeInterval,
		Status: storage.TaskStatusEnabled, RequireOnline: true,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))
	return NewScheduler(store, nil, nil, nil, nil, logger.New()), store, ctx, task
}

// A cancellation that won the handoff while a claimed run was waiting must not
// be overwritten by the offline RequireOnline skip outcome.
func TestOfflineSkipKeepsCancellationThatWonHandoff(t *testing.T) {
	s, store, ctx, task := newOfflineSkipFixture(t, "offline-skip-cancel")
	claim := newScheduledExecution(task, time.Now())
	require.NoError(t, store.CreateTaskExecution(ctx, claim))
	s.registerPendingExecution(claim.ID)
	require.NoError(t, s.CancelExecution(claim.ID))

	execution, err := s.executeTask(ctx, task, "scheduled", 0, nil, claim)
	require.NoError(t, err)
	require.NotNil(t, execution)
	stored, err := store.GetTaskExecution(ctx, claim.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusCancelled, stored.Status)
	require.Equal(t, "execution cancelled", stored.Error)
	require.NotNil(t, stored.EndedAt)
	running, err := store.HasRunningExecution(ctx, task.ID)
	require.NoError(t, err)
	require.False(t, running)

	s.executionMu.Lock()
	_, pendingCancel := s.pendingCancellations[claim.ID]
	_, pendingExec := s.pendingExecutions[claim.ID]
	s.executionMu.Unlock()
	require.False(t, pendingCancel, "a consumed cancellation must be cleared")
	require.False(t, pendingExec, "the handoff must be forgotten")
}

// A manual run's RPC context can be cancelled when the client disconnects. The
// terminal write must still land so the row does not stay running.
func TestTerminalExecutionWriteSurvivesCancelledCaller(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{ID: "terminal-cancelled-caller", Name: "terminal cancelled caller", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	task := &storage.ScheduledTask{
		ID: "terminal-cancelled-caller-task", ServerID: server.ID, Name: "terminal cancelled caller",
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

	s := NewScheduler(store, nil, nil, nil, nil, logger.New())
	caller, cancel := context.WithCancel(ctx)
	cancel()
	ended := time.Now()
	execution.Status = storage.ExecutionStatusCompleted
	execution.EndedAt = &ended
	execution.Duration = ended.Sub(execution.StartedAt).Milliseconds()
	s.persistTerminalExecution(caller, execution)

	stored, err := store.GetTaskExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusCompleted, stored.Status)
	require.NotNil(t, stored.EndedAt)
	running, err := store.HasRunningExecution(ctx, task.ID)
	require.NoError(t, err)
	require.False(t, running, "a cancelled caller must not leave the claim running")
}

// A cancellation that won the handoff during an offline RequireOnline skip must
// not start the child chain: the operator cancelled the run.
func TestOfflineSkipCancellationDoesNotDispatchChildren(t *testing.T) {
	s, store, ctx, task := newOfflineSkipFixture(t, "offline-skip-child-cancel")
	task.IntervalSecs = 3600
	child := &storage.ScheduledTask{
		ID: "offline-skip-child-cancel-child", ServerID: task.ServerID, Name: "cancelled child",
		TaskType: storage.TaskTypeCommand, Schedule: storage.ScheduleTypeInterval,
		Status: storage.TaskStatusEnabled, ParentTaskID: &task.ID, StepOrder: 0,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, child))

	claim := newScheduledExecution(task, time.Now())
	require.NoError(t, store.CreateTaskExecution(ctx, claim))
	s.registerPendingExecution(claim.ID)
	require.NoError(t, s.CancelExecution(claim.ID))

	execution, err := s.executeTask(ctx, task, "manual", 0, nil, claim)
	require.NoError(t, err)
	require.NotNil(t, execution)
	require.Equal(t, storage.ExecutionStatusCancelled, execution.Status)
	require.Zero(t, countExecutions(t, store, ctx, child.ID),
		"a cancelled offline skip must not dispatch the child chain")

	stored, err := store.GetTaskExecution(ctx, claim.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusCancelled, stored.Status)
	require.Equal(t, "execution cancelled", stored.Error)
	require.NotNil(t, stored.EndedAt)

	// The skip path advances the non-scheduled trigger's next run even when the
	// cancellation wins.
	reloaded, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, reloaded.NextRun, "a cancelled skip must still advance the schedule")
}

// A transient storage failure on the offline skip write must be retried so the
// claim does not stay running and block later runs.
func TestOfflineSkipRetriesAfterStorageRecovery(t *testing.T) {
	s, store, ctx, task := newOfflineSkipFixture(t, "offline-skip-retry")
	claim := newScheduledExecution(task, time.Now())
	require.NoError(t, store.CreateTaskExecution(ctx, claim))

	failedOnce := false
	const callbackName = "fixture:fail-first-offline-skip-write"
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

	execution, err := s.executeTask(ctx, task, "scheduled", 0, nil, claim)
	require.NoError(t, err)
	require.NotNil(t, execution)
	require.True(t, failedOnce, "the fixture must fail the first skip write")
	stored, err := store.GetTaskExecution(ctx, claim.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusSkipped, stored.Status)
	require.Equal(t, "server offline", stored.Error)
	require.NotNil(t, stored.EndedAt)
	running, err := store.HasRunningExecution(ctx, task.ID)
	require.NoError(t, err)
	require.False(t, running, "a recovered skip write must release the claim")
}

// Chain-stop bookkeeping is output only. It must not rewrite a terminal
// cancellation that another writer already recorded.
func TestChainStopDoesNotOverwriteTerminalCancellation(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &storage.Server{ID: "chain-stop-cancel", Name: "chain-stop-cancel", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	task := &storage.ScheduledTask{
		ID: "chain-stop-cancel-task", ServerID: server.ID, Name: "chain stop cancel",
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
	stale := *execution
	stale.Status = storage.ExecutionStatusCompleted
	stale.Output = "task output"
	s.recordChainStop(ctx, &stale, "step-a", nil)

	stored, err := store.GetTaskExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusCancelled, stored.Status)
	require.Equal(t, "execution cancelled", stored.Error)
	require.NotNil(t, stored.EndedAt)
	require.Contains(t, stored.Output, "chain stopped at step-a")
	require.Contains(t, stored.Output, "task output")
}

// The orphan-cancel path must not overwrite an outcome written after its
// running-state read, for example by a worker that just finished.
func TestOrphanCancelKeepsOutcomeWrittenAfterRead(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	ctx := context.Background()
	server := &storage.Server{ID: "orphan-cancel-race", Name: "orphan-cancel-race", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	task := &storage.ScheduledTask{
		ID: "orphan-cancel-race-task", ServerID: server.ID, Name: "orphan cancel race",
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

	raced := false
	const callbackName = "fixture:finish-before-orphan-cancel"
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if raced || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "task_executions" {
			return
		}
		if _, isStatusUpdate := tx.Statement.Dest.(map[string]any)["status"]; !isStatusUpdate {
			return
		}
		raced = true
		ended := time.Now()
		require.NoError(t, store.DB().Model(&storage.TaskExecution{}).Where("id = ?", execution.ID).
			Updates(map[string]any{"status": storage.ExecutionStatusCompleted, "error": "", "ended_at": &ended}).Error)
	}))
	t.Cleanup(func() { _ = store.DB().Callback().Update().Remove(callbackName) })

	err := s.CancelExecution(execution.ID)
	require.Error(t, err, "the orphan cancel must report that it did not win")
	require.True(t, raced, "the fixture must finish the row after the running-state read")
	stored, err := store.GetTaskExecution(ctx, execution.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusCompleted, stored.Status)
	require.Empty(t, stored.Error)
}
