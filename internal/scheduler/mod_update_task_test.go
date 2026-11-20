// SPDX-License-Identifier: MPL-2.0
package scheduler

import (
	"context"
	"errors"
	"strings"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/require"
)

type fakeModPlanner struct {
	calls   int
	cfg     storage.ModUpdateTaskConfig
	staged  int
	refused int
	err     error
}

func (f *fakeModPlanner) StageAvailableUpdates(_ context.Context, _ string, cfg storage.ModUpdateTaskConfig) (int, int, error) {
	f.calls++
	f.cfg = cfg
	return f.staged, f.refused, f.err
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
	planner := &fakeModPlanner{staged: 2, refused: 1}
	s := &Scheduler{log: logger.New(), modUpdatePlanner: planner}
	task := &storage.ScheduledTask{
		ServerID: server.ID,
		Config:   `{"enabled":true,"excluded_projects":["excluded"],"max_updates_per_run":2,"skip_when_running":true}`,
	}

	out, err := s.executeModUpdateTask(ctx, server, task)
	require.NoError(t, err)
	require.Contains(t, out, "staged 2")
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
