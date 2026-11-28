// SPDX-License-Identifier: MPL-2.0
package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type fakeModPlanner struct {
	calls   int
	cfg     storage.ModUpdateTaskConfig
	staged  int
	refused int
	reasons []string
	err     error
}

func (f *fakeModPlanner) StageAvailableUpdates(_ context.Context, _ string, cfg storage.ModUpdateTaskConfig) (int, int, []string, error) {
	f.calls++
	f.cfg = cfg
	return f.staged, f.refused, f.reasons, f.err
}

func TestCalculateNextRunHonorsTaskTimezone(t *testing.T) {
	s := &Scheduler{
		cronParser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		log:        logger.New(),
	}
	utc := &storage.ScheduledTask{Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "UTC"}
	sydney := &storage.ScheduledTask{Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "Australia/Sydney"}
	utcNext, err := s.CalculateNextRun(utc)
	require.NoError(t, err)
	sydneyNext, err := s.CalculateNextRun(sydney)
	require.NoError(t, err)
	require.False(t, utcNext.Equal(*sydneyNext), "the same cron in a different zone must fire at a different instant")
	require.Error(t, func() error {
		_, err := s.CalculateNextRun(&storage.ScheduledTask{
			Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "Not/AZone",
		})
		return err
	}())
}

func TestCalculateNextRunKeepsExplicitCronTimezoneAcrossAdvance(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	newYork, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)

	for _, prefix := range []string{"CRON_TZ=", "TZ="} {
		t.Run(prefix, func(t *testing.T) {
			s := &Scheduler{cronParser: parser, log: logger.New(), now: func() time.Time { return base }}
			task := &storage.ScheduledTask{
				Schedule: storage.ScheduleTypeCron,
				CronExpr: prefix + "America/New_York 0 3 * * *",
				Timezone: "UTC",
			}
			first, err := s.CalculateNextRun(task)
			require.NoError(t, err)
			wantFirst := time.Date(2026, 1, 16, 3, 0, 0, 0, newYork)
			require.True(t, first.Equal(wantFirst), "first = %v, want %v", first, wantFirst)

			s.now = func() time.Time { return *first }
			second, err := s.CalculateNextRun(task)
			require.NoError(t, err)
			require.True(t, second.Equal(wantFirst.Add(24*time.Hour)),
				"the explicit zone must keep the same local fire time, got %v", second)
		})
	}
}

func TestCalculateNextRunAppliesTaskTimezoneWhenUnprefixed(t *testing.T) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	base := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	s := &Scheduler{cronParser: parser, log: logger.New(), now: func() time.Time { return base }}
	task := &storage.ScheduledTask{
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "UTC",
	}

	first, err := s.CalculateNextRun(task)
	require.NoError(t, err)
	require.True(t, first.Equal(time.Date(2026, 1, 16, 3, 0, 0, 0, time.UTC)), "first = %v", first)

	s.now = func() time.Time { return *first }
	second, err := s.CalculateNextRun(task)
	require.NoError(t, err)
	require.True(t, second.Equal(first.Add(24*time.Hour)), "second = %v, want the next daily slot", second)
}

func TestCalculateNextRunRejectsCronWithNoFutureOccurrence(t *testing.T) {
	s := &Scheduler{
		cronParser: cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		log:        logger.New(),
	}
	_, err := s.CalculateNextRun(&storage.ScheduledTask{
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 31 2 *", Timezone: "UTC",
	})
	require.ErrorContains(t, err, "no future occurrence")
}

func TestImpossibleDueModUpdateScheduleFollowsGuardedDisable(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	ctx := context.Background()
	server := &storage.Server{ID: "srv-impossible-cron", Name: "impossible", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	overdue := time.Now().Add(-time.Hour)
	task := &storage.ScheduledTask{
		ID: "task-impossible-cron", ServerID: server.ID, Name: "impossible cron",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 31 2 *", Timezone: "UTC",
		NextRun: &overdue, Config: `{"enabled":true}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	s.checkAndRunDueTasks()
	s.wg.Wait()

	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusDisabled, stored.Status,
		"an existing impossible cron row must follow the guarded disable path")
	require.NotNil(t, stored.NextRun, "the guarded disable keeps the observed row identity")
	require.True(t, stored.NextRun.Equal(overdue))
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Empty(t, executions)
}

func TestDueModUpdateTaskExecutesOnceAndRecordsOutcome(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	planner := &fakeModPlanner{staged: 1, refused: 1, reasons: []string{"mod.jar: provider lookup failed"}}
	s.SetModUpdatePlanner(planner)
	ctx := context.Background()

	server := &storage.Server{ID: "srv-mod-due", Name: "mod-due", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	overdue := time.Now().Add(-3 * time.Hour)
	task := &storage.ScheduledTask{
		ID:       "task-mod-due",
		ServerID: server.ID,
		Name:     "automatic mod updates",
		TaskType: storage.TaskTypeModUpdate,
		Status:   storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron,
		CronExpr: "0 3 * * *",
		NextRun:  &overdue,
		Config:   `{"enabled":true,"window":"next_restart"}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	s.checkAndRunDueTasks()
	s.wg.Wait()

	require.Equal(t, 1, planner.calls)
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Len(t, executions, 1, "a missed due slot must run once")
	require.Equal(t, storage.ExecutionStatusCompleted, executions[0].Status)
	require.Contains(t, executions[0].Output, "staged 1")
	require.Contains(t, executions[0].Output, "mod.jar: provider lookup failed")

	updated, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, updated.NextRun)
	require.True(t, updated.NextRun.After(time.Now()), "the next run must be recomputed in the future")

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Equal(t, 1, planner.calls, "a second tick must not re-run the same task")
}

func TestOneShotModUpdateDispatchesExactlyOnce(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	planner := &fakeModPlanner{staged: 1}
	s.SetModUpdatePlanner(planner)
	ctx := context.Background()

	server := &storage.Server{ID: "srv-mod-once", Name: "mod-once", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	past := time.Now().Add(-time.Minute)
	task := &storage.ScheduledTask{
		ID: "task-mod-once", ServerID: server.ID, Name: "one-shot mod policy",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeOnce, RunAt: &past, NextRun: &past,
		Config: `{"enabled":true}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Equal(t, 1, planner.calls, "a due one-shot policy must dispatch exactly once")
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	require.Equal(t, storage.ExecutionStatusCompleted, executions[0].Status)

	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusDisabled, stored.Status, "the claimed one-shot slot persists its terminal state")
	require.Nil(t, stored.NextRun)
	require.NotNil(t, stored.LastRun)

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Equal(t, 1, planner.calls, "a disabled one-shot policy must not run again")
	executions, err = store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Len(t, executions, 1)
}

func TestOneShotModUpdateClaimRejectsConcurrentPolicyEdit(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	planner := &fakeModPlanner{}
	s.SetModUpdatePlanner(planner)
	ctx := context.Background()
	server := &storage.Server{ID: "srv-once-race", Name: "once-race", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	past := time.Now().Add(-time.Minute)
	task := &storage.ScheduledTask{
		ID: "task-once-race", ServerID: server.ID, Name: "one-shot race",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeOnce, RunAt: &past, NextRun: &past,
		Config: `{"enabled":true,"excluded_projects":["stale"]}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	const editedConfig = `{"enabled":true,"excluded_projects":["current"]}`
	policyEdited := false
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register(
		"fixture:policy-edit-before-one-shot-claim",
		func(tx *gorm.DB) {
			if policyEdited || tx.Statement.Schema == nil || tx.Statement.Schema.Table != "scheduled_tasks" {
				return
			}
			updates, ok := tx.Statement.Dest.(map[string]any)
			if !ok {
				return
			}
			if _, claiming := updates["last_run"]; !claiming {
				return
			}
			policyEdited = true
			observed, err := store.GetScheduledTask(ctx, task.ID)
			require.NoError(t, err)
			edited := *observed
			edited.Config = editedConfig
			saved, err := store.UpdateModUpdatePolicy(ctx, observed, &edited, false)
			require.NoError(t, err)
			require.True(t, saved)
		},
	))

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.True(t, policyEdited, "the fixture must race the one-shot claim")
	require.Zero(t, planner.calls, "a stale one-shot policy must not reach the planner")
	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusEnabled, stored.Status, "a rejected claim must not consume the one-shot slot")
	require.NotNil(t, stored.NextRun)
	require.True(t, stored.NextRun.Equal(past))
	require.Nil(t, stored.LastRun)
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Empty(t, executions)

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Equal(t, 1, planner.calls, "the still-due slot retries with the current policy")
	require.Equal(t, []string{"current"}, planner.cfg.ExcludedProjects)
}

func TestOneShotModUpdateClaimRecordsExecutionWithSlot(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	planner := &fakeModPlanner{}
	s.SetModUpdatePlanner(planner)
	ctx := context.Background()

	server := &storage.Server{ID: "srv-mod-crash", Name: "mod-crash", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	past := time.Now().Add(-time.Minute)
	task := &storage.ScheduledTask{
		ID: "task-mod-crash", ServerID: server.ID, Name: "crash window",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeOnce, RunAt: &past, NextRun: &past,
		Config: `{"enabled":true}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	claim := newScheduledExecution(task, time.Now())
	require.NoError(t, s.updateNextRun(task, claim))

	// Simulate the process stopping after the durable claim but before the
	// asynchronous dispatch goroutine writes anything.
	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusDisabled, stored.Status)
	require.Nil(t, stored.NextRun)
	require.NotNil(t, stored.LastRun)
	recorded, err := store.GetTaskExecution(ctx, claim.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusRunning, recorded.Status)
	require.Equal(t, "scheduled", recorded.Trigger)

	// Existing startup recovery closes the orphaned running record with an
	// outcome, so the consumed slot is never silent.
	count, err := store.FailOrphanedTaskExecutions(ctx, "interrupted by panel restart; run the task again after the server is idle")
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
	recovered, err := store.GetTaskExecution(ctx, claim.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusFailed, recovered.Status)
	require.NotNil(t, recovered.EndedAt)

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Zero(t, planner.calls, "an interrupted one-shot attempt must not run a second time")
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Len(t, executions, 1, "the interrupted attempt leaves exactly one recoverable outcome")
}

func TestOneShotModUpdateSlotStaysDueWhenExecutionInsertFails(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	planner := &fakeModPlanner{staged: 1}
	s.SetModUpdatePlanner(planner)
	ctx := context.Background()

	server := &storage.Server{ID: "srv-mod-insert", Name: "mod-insert", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	past := time.Now().Add(-time.Minute)
	task := &storage.ScheduledTask{
		ID: "task-mod-insert", ServerID: server.ID, Name: "insert failure",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeOnce, RunAt: &past, NextRun: &past,
		Config: `{"enabled":true}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	failInsert := true
	require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register(
		"fixture:fail-claimed-execution-insert",
		func(tx *gorm.DB) {
			if failInsert && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "task_executions" {
				_ = tx.AddError(errors.New("fixture execution insert failure"))
			}
		},
	))

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Zero(t, planner.calls, "a failed execution insert must not dispatch")
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Empty(t, executions)
	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusEnabled, stored.Status, "the failed insert must roll the claim back")
	require.NotNil(t, stored.NextRun)
	require.True(t, stored.NextRun.Equal(past))
	require.Nil(t, stored.LastRun)

	failInsert = false
	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Equal(t, 1, planner.calls, "the still-due slot retries after the insert failure clears")
	executions, err = store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	require.Equal(t, storage.ExecutionStatusCompleted, executions[0].Status)
	stored, err = store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusDisabled, stored.Status)
}

func TestRecurringModUpdateSlotStaysDueWhenExecutionInsertFails(t *testing.T) {
	for _, schedule := range []struct {
		name string
		task func(*storage.ScheduledTask)
	}{
		{
			name: "cron",
			task: func(task *storage.ScheduledTask) {
				task.Schedule = storage.ScheduleTypeCron
				task.CronExpr = "0 3 * * *"
			},
		},
		{
			name: "interval",
			task: func(task *storage.ScheduledTask) {
				task.Schedule = storage.ScheduleTypeInterval
				task.IntervalSecs = 3600
			},
		},
	} {
		t.Run(schedule.name, func(t *testing.T) {
			s, store := newTriggerTestScheduler(t)
			planner := &fakeModPlanner{staged: 1}
			s.SetModUpdatePlanner(planner)
			ctx := context.Background()

			server := &storage.Server{ID: "srv-recurring-insert-" + schedule.name, Name: "recurring-insert", Status: storage.StatusStopped}
			require.NoError(t, store.CreateServer(ctx, server))
			past := time.Now().Add(-time.Minute)
			task := &storage.ScheduledTask{
				ID: "task-recurring-insert-" + schedule.name, ServerID: server.ID, Name: "recurring insert failure",
				TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
				NextRun: &past, Config: `{"enabled":true}`,
			}
			schedule.task(task)
			require.NoError(t, store.CreateScheduledTask(ctx, task))

			failInsert := true
			require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register(
				"fixture:fail-recurring-claimed-execution-insert",
				func(tx *gorm.DB) {
					if failInsert && tx.Statement.Schema != nil && tx.Statement.Schema.Table == "task_executions" {
						_ = tx.AddError(errors.New("fixture recurring execution insert failure"))
					}
				},
			))

			s.checkAndRunDueTasks()
			s.wg.Wait()
			require.Zero(t, planner.calls, "a failed execution insert must not dispatch")
			executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
			require.NoError(t, err)
			require.Empty(t, executions)
			stored, err := store.GetScheduledTask(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, storage.TaskStatusEnabled, stored.Status, "a recurring task stays enabled")
			require.NotNil(t, stored.NextRun)
			require.True(t, stored.NextRun.Equal(past), "the failed insert must roll the due slot back")
			require.Nil(t, stored.LastRun)

			failInsert = false
			s.checkAndRunDueTasks()
			s.wg.Wait()
			require.Equal(t, 1, planner.calls, "the still-due slot retries after the insert failure clears")
			executions, err = store.ListTaskExecutions(ctx, task.ID, 10)
			require.NoError(t, err)
			require.Len(t, executions, 1)
			require.Equal(t, storage.ExecutionStatusCompleted, executions[0].Status)
			stored, err = store.GetScheduledTask(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, storage.TaskStatusEnabled, stored.Status)
			require.NotNil(t, stored.NextRun)
			require.True(t, stored.NextRun.After(time.Now()))
			require.NotNil(t, stored.LastRun)
		})
	}
}

func TestRecurringModUpdateClaimSurvivesAbruptInterruption(t *testing.T) {
	for _, schedule := range []struct {
		name string
		task func(*storage.ScheduledTask)
	}{
		{
			name: "cron",
			task: func(task *storage.ScheduledTask) {
				task.Schedule = storage.ScheduleTypeCron
				task.CronExpr = "0 3 * * *"
			},
		},
		{
			name: "interval",
			task: func(task *storage.ScheduledTask) {
				task.Schedule = storage.ScheduleTypeInterval
				task.IntervalSecs = 3600
			},
		},
	} {
		t.Run(schedule.name, func(t *testing.T) {
			s, store := newTriggerTestScheduler(t)
			planner := &fakeModPlanner{}
			s.SetModUpdatePlanner(planner)
			ctx := context.Background()

			server := &storage.Server{ID: "srv-recurring-crash-" + schedule.name, Name: "recurring-crash", Status: storage.StatusStopped}
			require.NoError(t, store.CreateServer(ctx, server))
			past := time.Now().Add(-time.Minute)
			task := &storage.ScheduledTask{
				ID: "task-recurring-crash-" + schedule.name, ServerID: server.ID, Name: "recurring crash window",
				TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
				NextRun: &past, Config: `{"enabled":true}`,
			}
			schedule.task(task)
			require.NoError(t, store.CreateScheduledTask(ctx, task))

			claim := newScheduledExecution(task, time.Now())
			require.NoError(t, s.updateNextRun(task, claim))
			stored, err := store.GetScheduledTask(ctx, task.ID)
			require.NoError(t, err)
			require.Equal(t, storage.TaskStatusEnabled, stored.Status)
			require.NotNil(t, stored.NextRun)
			require.True(t, stored.NextRun.After(past), "the claimed slot advances before dispatch")
			require.NotNil(t, stored.LastRun)
			recorded, err := store.GetTaskExecution(ctx, claim.ID)
			require.NoError(t, err)
			require.Equal(t, storage.ExecutionStatusRunning, recorded.Status)
			require.Equal(t, "scheduled", recorded.Trigger)

			// Simulate a process stop after the durable claim but before the
			// asynchronous dispatch goroutine writes anything.
			count, err := store.FailOrphanedTaskExecutions(ctx, "interrupted by panel restart; run the task again after the server is idle")
			require.NoError(t, err)
			require.EqualValues(t, 1, count)
			recovered, err := store.GetTaskExecution(ctx, claim.ID)
			require.NoError(t, err)
			require.Equal(t, storage.ExecutionStatusFailed, recovered.Status)
			require.NotNil(t, recovered.EndedAt)

			s.checkAndRunDueTasks()
			s.wg.Wait()
			require.Zero(t, planner.calls, "an interrupted recurring attempt must not run the same slot again")
			executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
			require.NoError(t, err)
			require.Len(t, executions, 1, "the interrupted attempt leaves exactly one recoverable outcome")
		})
	}
}

func TestDueModUpdateTaskDoesNotDispatchAfterDisableEnable(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	planner := &fakeModPlanner{}
	s.SetModUpdatePlanner(planner)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server := &storage.Server{ID: "srv-mod-retoggled", Name: "retoggled", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	past := time.Now().UTC().Add(-time.Hour)
	task := &storage.ScheduledTask{
		ID: "task-mod-retoggled", ServerID: server.ID, Name: "retoggled mod policy",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", NextRun: &past,
		Config: `{"enabled":true}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))
	var retoggled bool
	var future *time.Time
	require.NoError(t, store.DB().Callback().Update().Before("gorm:update").Register("fixture:retoggle-before-deadline-advance", func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]any)
		if retoggled || !ok || updates["last_run"] == nil {
			return
		}
		// Advance already calculated from the due snapshot, but has yet to
		// write. Exercise the same transition writes as the operator's RPC.
		retoggled = true
		observed, err := store.GetScheduledTask(ctx, task.ID)
		require.NoError(t, err)
		disabled := *observed
		disabled.Status, disabled.NextRun = storage.TaskStatusDisabled, nil
		saved, err := store.ToggleModUpdateSchedule(ctx, observed, &disabled)
		require.NoError(t, err)
		require.True(t, saved)
		enabled := *observed
		future, err = s.CalculateNextRun(&enabled)
		require.NoError(t, err)
		enabled.NextRun = future
		saved, err = store.ToggleModUpdateSchedule(ctx, &disabled, &enabled)
		require.NoError(t, err)
		require.True(t, saved)
	}))

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.True(t, retoggled)
	require.Zero(t, planner.calls)
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Empty(t, executions)
	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusEnabled, stored.Status)
	require.True(t, future.Equal(*stored.NextRun))
	require.Nil(t, stored.LastRun)
}

type signalingModPlanner struct {
	calls chan struct{}
}

func (f *signalingModPlanner) StageAvailableUpdates(context.Context, string, storage.ModUpdateTaskConfig) (int, int, []string, error) {
	select {
	case f.calls <- struct{}{}:
	default:
	}
	return 0, 0, nil, nil
}

func TestColdBootDispatchesPersistedPolicyOnlyAfterPlannerInstalled(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	ctx := context.Background()
	require.NoError(t, store.CreateServer(ctx, &storage.Server{ID: "srv-cold", Name: "cold", Status: storage.StatusStopped}))
	past := time.Now().Add(-time.Minute)
	task := &storage.ScheduledTask{
		ID: "task-cold", ServerID: "srv-cold", Name: "persisted policy",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "UTC",
		NextRun: &past, Config: `{"enabled":true,"window":"idle","idle_minutes":15}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))
	planner := &signalingModPlanner{calls: make(chan struct{}, 1)}
	s.SetModUpdatePlanner(planner)
	require.NoError(t, s.Start())
	defer func() { require.NoError(t, s.Stop()) }()
	select {
	case <-planner.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("cold boot did not dispatch the persisted mod update policy")
	}
}

func TestInvalidTimezoneTaskCannotRepeatEveryTick(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	ctx := context.Background()
	require.NoError(t, store.CreateServer(ctx, &storage.Server{ID: "srv-bad-tz", Name: "bad-tz", Status: storage.StatusStopped}))
	past := time.Now().Add(-time.Minute)
	task := &storage.ScheduledTask{
		ID: "task-bad-tz", ServerID: "srv-bad-tz", Name: "stale zone",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", Timezone: "Bad/Zone",
		NextRun: &past, Config: `{"enabled":true}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))
	planner := &fakeModPlanner{}
	s.SetModUpdatePlanner(planner)

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Zero(t, planner.calls, "a task whose schedule cannot advance must not dispatch")
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Empty(t, executions)
	reloaded, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.TaskStatusDisabled, reloaded.Status)

	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Zero(t, planner.calls, "a disabled task must not repeat on the next tick")
}

func TestPersistedInvalidModUpdatePolicyFailsBeforePlanner(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	planner := &fakeModPlanner{}
	s.SetModUpdatePlanner(planner)
	ctx := context.Background()
	server := &storage.Server{ID: "srv-bad-policy", Name: "bad policy", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	overdue := time.Now().Add(-time.Hour)
	task := &storage.ScheduledTask{
		ID: "task-bad-policy", ServerID: server.ID, Name: "invalid stored policy",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", NextRun: &overdue,
		Config: `{"enabled":true,"window":"unknown"}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))
	s.checkAndRunDueTasks()
	s.wg.Wait()
	require.Zero(t, planner.calls)
	executions, err := store.ListTaskExecutions(ctx, task.ID, 10)
	require.NoError(t, err)
	require.Len(t, executions, 1)
	require.Equal(t, storage.ExecutionStatusFailed, executions[0].Status)
}

type retryBudgetPlanner struct {
	limits []int
	err    error
}

func (p *retryBudgetPlanner) StageAvailableUpdates(_ context.Context, _ string, cfg storage.ModUpdateTaskConfig) (int, int, []string, error) {
	p.limits = append(p.limits, cfg.MaxUpdatesPerRun)
	if len(p.limits) == 1 {
		return 1, 1, nil, p.err
	}
	return 1, 0, nil, nil
}

func TestModUpdateTaskRetriesUseRemainingExecutionBudget(t *testing.T) {
	for _, test := range []struct {
		name       string
		limit      string
		wantLimits []int
		wantError  bool
	}{
		{"explicit cap", "2", []int{2, 1}, false},
		{"default cap", "0", []int{10, 9}, false},
		{"exhausted", "1", []int{1}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, store := newTriggerTestScheduler(t)
			interrupted := errors.New("provider response lost")
			planner := &retryBudgetPlanner{err: interrupted}
			s.SetModUpdatePlanner(planner)
			ctx := context.Background()
			server := &storage.Server{ID: "srv-budget", Name: "budget", Status: storage.StatusStopped}
			require.NoError(t, store.CreateServer(ctx, server))
			task := &storage.ScheduledTask{
				ID: "task-budget", ServerID: server.ID, Name: "bounded update task",
				TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
				Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", RetryCount: 1, RetryDelay: 1,
				Config: `{"enabled":true,"max_updates_per_run":` + test.limit + `}`,
			}
			require.NoError(t, store.CreateScheduledTask(ctx, task))
			execution, err := s.TriggerTask(ctx, task.ID)
			require.Equal(t, test.wantLimits, planner.limits)
			if test.wantError {
				require.ErrorIs(t, err, interrupted)
				require.Equal(t, storage.ExecutionStatusFailed, execution.Status)
				require.Contains(t, execution.Output, "staged 1")
			} else {
				require.NoError(t, err)
				require.Equal(t, storage.ExecutionStatusCompleted, execution.Status)
				require.Equal(t, 1, execution.RetryNum)
				require.Contains(t, execution.Output, "staged 2")
			}
		})
	}
}

func TestModUpdateBatchConflictIsNotRetried(t *testing.T) {
	s, store := newTriggerTestScheduler(t)
	planner := &fakeModPlanner{err: storage.ErrUpdateBatchConflict}
	s.SetModUpdatePlanner(planner)
	ctx := context.Background()
	server := &storage.Server{ID: "srv-batch-conflict", Name: "batch conflict", Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, server))
	task := &storage.ScheduledTask{
		ID: "task-batch-conflict", ServerID: server.ID, Name: "batch conflict",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", RetryCount: 1, RetryDelay: 1,
		Config: `{"enabled":true}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))

	execution, err := s.TriggerTask(ctx, task.ID)
	require.ErrorIs(t, err, storage.ErrUpdateBatchConflict)
	require.Equal(t, 1, planner.calls, "a batch admission conflict must not spend retries")
	require.Zero(t, execution.RetryNum)
	require.Equal(t, storage.ExecutionStatusFailed, execution.Status)
}

func TestValidateModUpdateConfigJSON(t *testing.T) {
	require.NoError(t, ValidateModUpdateConfigJSON(`{"enabled":true}`))
	require.NoError(t, ValidateModUpdateConfigJSON(`{"enabled":true,"window":"idle","idle_minutes":15,"max_updates_per_run":3}`))
	require.Error(t, ValidateModUpdateConfigJSON(`{"enabled":true,"window":"idle"}`), "idle window needs a duration")
	require.Error(t, ValidateModUpdateConfigJSON(`not-json`))
	require.Error(t, ValidateModUpdateConfigJSON(`{"enabled":true,"max_updates_per_run":-1}`))
	require.Error(t, ValidateModUpdateConfigJSON(`{"enabled":true,"window":"at_time"}`), "a policy has no apply timestamp")
}

func TestExecuteModUpdateTaskUsesSharedPlannerAndOptInPolicy(t *testing.T) {
	ctx := context.Background()
	server := &storage.Server{ID: "srv-mod-task", Status: storage.StatusStopped}
	planner := &fakeModPlanner{staged: 2, refused: 1, reasons: []string{"mod.jar: provider lookup failed"}}
	s := &Scheduler{log: logger.New(), modUpdatePlanner: planner}
	task := &storage.ScheduledTask{
		ServerID: server.ID,
		Config:   `{"enabled":true,"excluded_projects":["excluded"],"max_updates_per_run":2,"skip_when_running":true}`,
	}

	out, err := s.executeModUpdateTask(ctx, server, task, &modUpdateRunBudget{})
	require.NoError(t, err)
	require.Contains(t, out, "staged 2")
	require.Contains(t, out, "mod.jar: provider lookup failed", "the operator must see the refusal cause")
	require.Equal(t, 1, planner.calls)
	require.True(t, planner.cfg.Enabled)
	require.Equal(t, []string{"excluded"}, planner.cfg.ExcludedProjects)
	require.Equal(t, 2, planner.cfg.MaxUpdatesPerRun)
	require.True(t, planner.cfg.SkipWhenRunning)
}

func TestExecuteModUpdateTaskDisabledAndRunningPoliciesDoNotPlan(t *testing.T) {
	ctx := context.Background()
	server := &storage.Server{ID: "srv-mod-task", Status: storage.StatusRunning}
	planner := &fakeModPlanner{staged: 3}
	s := &Scheduler{log: logger.New(), modUpdatePlanner: planner}

	disabled := &storage.ScheduledTask{ServerID: server.ID, Config: `{"enabled":false}`}
	out, err := s.executeModUpdateTask(ctx, server, disabled, &modUpdateRunBudget{})
	require.NoError(t, err)
	require.Contains(t, out, "disabled")
	require.Zero(t, planner.calls)

	running := &storage.ScheduledTask{ServerID: server.ID, Config: `{"enabled":true,"skip_when_running":true}`}
	out, err = s.executeModUpdateTask(ctx, server, running, &modUpdateRunBudget{})
	require.NoError(t, err)
	require.True(t, strings.Contains(out, "running"), out)
	require.Zero(t, planner.calls, "a running-server policy must not stage")
}

func TestExecuteModUpdateTaskFailsClosedWithoutPlannerOrWithBadConfig(t *testing.T) {
	ctx := context.Background()
	server := &storage.Server{ID: "srv-mod-task", Status: storage.StatusStopped}
	task := &storage.ScheduledTask{ServerID: server.ID, Config: `{"enabled":true}`}

	noPlanner := &Scheduler{log: logger.New()}
	_, err := noPlanner.executeModUpdateTask(ctx, server, task, &modUpdateRunBudget{})
	require.Error(t, err, "a missing planner must fail closed")

	badConfig := &Scheduler{log: logger.New(), modUpdatePlanner: &fakeModPlanner{}}
	_, err = badConfig.executeModUpdateTask(ctx, server, &storage.ScheduledTask{ServerID: server.ID, Config: `not-json`}, &modUpdateRunBudget{})
	require.Error(t, err)

	failing := &fakeModPlanner{err: errors.New("provider outage")}
	withFailure := &Scheduler{log: logger.New(), modUpdatePlanner: failing}
	_, err = withFailure.executeModUpdateTask(ctx, server, task, &modUpdateRunBudget{})
	require.ErrorContains(t, err, "provider outage")
}
