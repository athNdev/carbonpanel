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

func TestValidateModUpdateConfigJSON(t *testing.T) {
	require.NoError(t, ValidateModUpdateConfigJSON(`{"enabled":true}`))
	require.NoError(t, ValidateModUpdateConfigJSON(`{"enabled":true,"window":"idle","max_updates_per_run":3}`))
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

	out, err := s.executeModUpdateTask(ctx, server, task)
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
	out, err := s.executeModUpdateTask(ctx, server, disabled)
	require.NoError(t, err)
	require.Contains(t, out, "disabled")
	require.Zero(t, planner.calls)

	running := &storage.ScheduledTask{ServerID: server.ID, Config: `{"enabled":true,"skip_when_running":true}`}
	out, err = s.executeModUpdateTask(ctx, server, running)
	require.NoError(t, err)
	require.True(t, strings.Contains(out, "running"), out)
	require.Zero(t, planner.calls, "a running-server policy must not stage")
}

func TestExecuteModUpdateTaskFailsClosedWithoutPlannerOrWithBadConfig(t *testing.T) {
	ctx := context.Background()
	server := &storage.Server{ID: "srv-mod-task", Status: storage.StatusStopped}
	task := &storage.ScheduledTask{ServerID: server.ID, Config: `{"enabled":true}`}

	noPlanner := &Scheduler{log: logger.New()}
	_, err := noPlanner.executeModUpdateTask(ctx, server, task)
	require.Error(t, err, "a missing planner must fail closed")

	badConfig := &Scheduler{log: logger.New(), modUpdatePlanner: &fakeModPlanner{}}
	_, err = badConfig.executeModUpdateTask(ctx, server, &storage.ScheduledTask{ServerID: server.ID, Config: `not-json`})
	require.Error(t, err)

	failing := &fakeModPlanner{err: errors.New("provider outage")}
	withFailure := &Scheduler{log: logger.New(), modUpdatePlanner: failing}
	_, err = withFailure.executeModUpdateTask(ctx, server, task)
	require.ErrorContains(t, err, "provider outage")
}
