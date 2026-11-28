// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/robfig/cron/v3"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/command"
	appconfig "github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/events"
	"github.com/athNdev/carbon-panel/internal/metrics"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	"github.com/athNdev/carbon-panel/internal/snapshot"
	"github.com/athNdev/carbon-panel/internal/staging"
	"github.com/athNdev/carbon-panel/internal/webhook"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// ErrTaskAlreadyRunning is returned when a manual trigger races an
// in-flight execution of the same task (MINE-146, surfaced as 409).
var ErrTaskAlreadyRunning = errors.New("task is already running")

// errInvalidTaskSchedule distinguishes an invalid schedule definition from a
// transient failure while persisting a valid schedule. Only the former may
// disable a task.
var errInvalidTaskSchedule = errors.New("invalid task schedule")

// ModUpdatePlanner stages reviewed compatible releases. The concrete owner
// lives in the RPC service layer; the scheduler keeps only this interface so
// manual and scheduled updates use one operation without an import cycle or a
// process-global provider. A nil planner fails closed.
type ModUpdatePlanner interface {
	StageAvailableUpdates(ctx context.Context, serverID string, cfg storage.ModUpdateTaskConfig) (staged int, refused int, reasons []string, err error)
}

// maxAutomaticModUpdatesPerRun bounds a single policy run. The value is also
// used as an allocation capacity in the planner, so an oversized persisted
// config must never reach it unchecked.
const maxAutomaticModUpdatesPerRun = 50

// DefaultModUpdatesPerRun is the budget used when a policy omits its limit.
const DefaultModUpdatesPerRun = 10

type modUpdateRunBudget struct {
	staged    int
	exhausted bool
}

// Scheduler manages scheduled tasks for all servers
type Scheduler struct {
	store         *storage.Store
	docker        *docker.Client
	sender        snapshot.CommandSender
	appConfig     *appconfig.Config
	metrics       *metrics.Collector
	log           *logger.Logger
	checkInterval time.Duration

	// State management
	running  bool
	mu       sync.RWMutex
	stopChan chan struct{}
	wg       sync.WaitGroup

	// Execution tracking
	runningExecutions map[string]context.CancelFunc // executionID -> cancel func
	executionMu       sync.RWMutex
	// A claimed execution exists in storage before its worker registers a
	// cancel function. pendingExecutions closes that handoff window;
	// pendingCancellations records a cancel that arrived first so the worker
	// can refuse to start instead of overwriting a cancelled outcome.
	pendingExecutions    map[string]bool
	pendingCancellations map[string]bool

	// In-flight task tracking to prevent double-dispatching
	runningTasks   map[string]bool // taskID -> bool
	runningTasksMu sync.Mutex

	// Cron parser
	cronParser cron.Parser

	// now supplies the scheduler clock. Nil means time.Now; tests replace it so
	// repeated cron advances are deterministic.
	now func() time.Time

	// Stats
	lastCheck time.Time
	nextCheck time.Time

	// Audit-trail retention (MINE-141): last activity-log prune run.
	lastActivityPrune time.Time

	// Snapshot engine for pre-update snapshots and rollback (MINE-23)
	snapshotEngine *snapshot.Engine

	// Instance-owned node storage resolver (W16), shared with backups and
	// staged content. No process-global transport is consulted.
	storageResolver *nodestorage.Resolver

	// Multi-node Docker client pool (optional; falls back to s.docker)
	clientPool *docker.ClientPool

	// playerCount is the same online-player source freeze and deep sleep use.
	// Nil means the provider is not installed, so an idle window does not fire.
	playerCount func(serverID string) int

	// recreateModule replaces a module container after a staged spec change.
	// Nil means that part of a batch cannot be published.
	recreateModule func(ctx context.Context, moduleID string) error

	// admission is the shared in-process start gate, injected at composition
	// root so scheduled and RPC starts contend on the same node key. Nil fails
	// closed at the acquire helper.
	admission *serverruntime.AdmissionCoordinator

	// modUpdatePlanner is injected at composition root. It is the same planner
	// the manual review-and-apply RPC uses, so a schedule cannot fork policy.
	modUpdatePlanner ModUpdatePlanner
}

// SetModUpdatePlanner supplies the shared manual/scheduled mod update planner.
func (s *Scheduler) SetModUpdatePlanner(planner ModUpdatePlanner) {
	s.modUpdatePlanner = planner
}

// Config holds scheduler configuration
type Config struct {
	CheckInterval time.Duration // How often to check for due tasks

	// Admission is the shared in-process start gate. Nil fails closed.
	Admission *serverruntime.AdmissionCoordinator
	// StorageResolver is the instance-owned node storage resolver shared with
	// the snapshot engine and backup archive operations.
	StorageResolver *nodestorage.Resolver
}

// DefaultConfig returns default scheduler configuration
func DefaultConfig() Config {
	return Config{
		CheckInterval: 10 * time.Second,
	}
}

// NewScheduler creates a new task scheduler
func NewScheduler(store *storage.Store, docker *docker.Client, sender *command.Sender, appCfg *appconfig.Config, metricsCollector *metrics.Collector, log *logger.Logger, config ...Config) *Scheduler {
	cfg := DefaultConfig()
	if len(config) > 0 {
		cfg = config[0]
	}

	admission := cfg.Admission
	if admission == nil {
		// A scheduler always owns a gate; the composition root replaces it with
		// the one shared with RPC starts.
		admission = serverruntime.NewAdmissionCoordinator()
	}

	var snapEngine *snapshot.Engine
	if appCfg != nil {
		snapEngine = snapshot.NewEngine(store, docker, sender, log, snapshot.Config{
			BackupDir:       appCfg.Storage.BackupDir,
			MaxSnapshots:    appCfg.Storage.MaxSnapshots,
			RetentionDays:   appCfg.Storage.SnapshotRetentionDays,
			StorageResolver: cfg.StorageResolver,
			Admission:       admission,
		})
	}

	return &Scheduler{
		store:                store,
		docker:               docker,
		sender:               sender,
		appConfig:            appCfg,
		metrics:              metricsCollector,
		log:                  log,
		checkInterval:        cfg.CheckInterval,
		stopChan:             make(chan struct{}),
		runningExecutions:    make(map[string]context.CancelFunc),
		pendingExecutions:    make(map[string]bool),
		pendingCancellations: make(map[string]bool),
		runningTasks:         make(map[string]bool),
		cronParser:           cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow),
		now:                  time.Now,
		snapshotEngine:       snapEngine,
		admission:            admission,
		storageResolver:      cfg.StorageResolver,
	}
}

// SetAdmissionCoordinator supplies the shared in-process start gate after
// construction. The composition root normally passes it through Config.
func (s *Scheduler) SetAdmissionCoordinator(c *serverruntime.AdmissionCoordinator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.admission = c
}

// SetSnapshotEngine configures custom snapshot engine
func (s *Scheduler) SetSnapshotEngine(e *snapshot.Engine) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshotEngine = e
}

// SetClientPool configures the multi-node Docker client pool so scheduled and
// deep-sleep wake tasks operate on the server's assigned node daemon.
func (s *Scheduler) SetClientPool(pool *docker.ClientPool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clientPool = pool
}

// SetPlayerCountProvider installs the online-player source used for idle
// apply windows. It is the same function freeze and deep sleep use.
func (s *Scheduler) SetPlayerCountProvider(provider func(serverID string) int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playerCount = provider
}

// SetModuleRecreator installs the module container replace used when a staged
// batch includes a module spec change.
func (s *Scheduler) SetModuleRecreator(recreate func(ctx context.Context, moduleID string) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recreateModule = recreate
}

func (s *Scheduler) dockerForNode(nodeID string) (*docker.Client, error) {
	s.mu.RLock()
	pool := s.clientPool
	defaultCli := s.docker
	store := s.store
	s.mu.RUnlock()
	return docker.NewLocator(pool, defaultCli, store).For(context.Background(), nodeID)
}

// Start begins the scheduler loop
func (s *Scheduler) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("scheduler already running")
	}

	// Recover orphaned executions left in running state by a prior abnormal process exit.
	if s.store != nil {
		ctx := context.Background()
		if count, err := s.store.FailOrphanedTaskExecutions(ctx, "interrupted by panel restart; run the task again after the server is idle"); err != nil {
			s.log.Error("Failed to recover orphaned task executions: %v", err)
		} else if count > 0 {
			s.log.Warn("Recovered %d orphaned task execution(s) from prior session", count)
		}
	}

	s.running = true
	s.stopChan = make(chan struct{})

	s.wg.Add(1)
	go s.runLoop()

	s.log.Info("Task scheduler started (check interval: %v)", s.checkInterval)
	return nil
}

// Stop gracefully stops the scheduler
func (s *Scheduler) Stop() error {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	close(s.stopChan)
	s.mu.Unlock()

	// Wait for scheduler loop to finish
	s.wg.Wait()

	// Cancel all running executions
	s.executionMu.Lock()
	for _, cancel := range s.runningExecutions {
		cancel()
	}
	s.runningExecutions = make(map[string]context.CancelFunc)
	s.executionMu.Unlock()

	s.log.Info("Task scheduler stopped")
	return nil
}

// IsRunning returns whether the scheduler is running
func (s *Scheduler) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// GetStatus returns current scheduler status
func (s *Scheduler) GetStatus() SchedulerStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	s.executionMu.RLock()
	runningCount := len(s.runningExecutions)
	s.executionMu.RUnlock()

	// Count active tasks
	ctx := context.Background()
	tasks, _ := s.store.ListAllScheduledTasks(ctx)
	activeCount := 0
	for _, task := range tasks {
		if task.Status == storage.TaskStatusEnabled {
			activeCount++
		}
	}

	return SchedulerStatus{
		Running:           s.running,
		ActiveTasks:       activeCount,
		RunningExecutions: runningCount,
		LastCheck:         s.lastCheck,
		NextCheck:         s.nextCheck,
	}
}

// SchedulerStatus represents the current state of the scheduler
type SchedulerStatus struct {
	Running           bool
	ActiveTasks       int
	RunningExecutions int
	LastCheck         time.Time
	NextCheck         time.Time
}

// runLoop is the main scheduler loop
func (s *Scheduler) runLoop() {
	defer s.wg.Done()

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()

	// Run initial check
	s.checkAndRunDueTasks()

	for {
		select {
		case <-ticker.C:
			s.checkAndRunDueTasks()
		case <-s.stopChan:
			return
		}
	}
}

// checkAndRunDueTasks checks for due tasks and executes them
func (s *Scheduler) checkAndRunDueTasks() {
	s.mu.Lock()
	s.lastCheck = time.Now()
	s.nextCheck = s.lastCheck.Add(s.checkInterval)
	s.mu.Unlock()

	ctx := context.Background()

	// Audit-trail retention (MINE-141): prune daily, keep 90 days / min 1000 rows.
	if time.Since(s.lastActivityPrune) > 24*time.Hour {
		if n, err := activity.Prune(s.store.DB().WithContext(ctx), time.Now().AddDate(0, 0, -90), 1000); err != nil {
			s.log.Error("Failed to prune activity logs: %v", err)
		} else {
			if n > 0 {
				s.log.Info("Pruned %d activity log rows", n)
			}
			s.lastActivityPrune = time.Now()
		}
		// Metrics retention (MINE-144): evict snapshots stale over 2h.
		if s.metrics != nil {
			if n := s.metrics.PruneStale(2 * time.Hour); n > 0 {
				s.log.Info("Pruned %d stale metric snapshots", n)
			}
		}
		// Snapshot retention (DEP-3): prune expired snapshots across all servers.
		if s.snapshotEngine != nil {
			if n, err := s.snapshotEngine.PruneAllSnapshots(ctx); err != nil {
				s.log.Error("Failed to prune expired snapshots: %v", err)
			} else if n > 0 {
				s.log.Info("Pruned %d expired server snapshots", n)
			}
		}
	}

	// Staged config waits for its apply window. This tick must not merge it.
	s.applyDueStagedConfigs(ctx)

	// Get all due tasks
	tasks, err := s.store.ListDueScheduledTasks(ctx, time.Now())
	if err != nil {
		s.log.Error("Failed to list due tasks: %v", err)
		return
	}

	for _, task := range tasks {
		claimed, err := s.tryClaimTask(context.Background(), task.ID)
		if err != nil {
			s.log.Error("Task %s: running check failed, leaving it due: %v", task.Name, err)
			continue
		}
		if !claimed {
			s.log.Debug("Task %s is already running, skipping duplicate dispatch", task.Name)
			continue
		}

		// Advance the schedule before dispatch. If it cannot be advanced the
		// task must not run: a still-due row would otherwise be claimed again
		// on every tick. Disable it with the reason in the log.
		//
		// Every due mod policy, including recurring cron and interval slots,
		// records its execution in the same durable write as the slot claim. An
		// interruption before the async dispatch then leaves a recoverable
		// running record instead of a silently consumed slot, and a failed
		// insert leaves the slot due. An event-driven policy has no due slot.
		var claim *storage.TaskExecution
		if task.TaskType == storage.TaskTypeModUpdate && task.Schedule != storage.ScheduleTypeEvent {
			claim = newScheduledExecution(task, time.Now())
			// The row becomes running inside updateNextRun. Register the
			// handoff before that write so CancelExecution can find it even
			// before the asynchronous worker starts.
			s.registerPendingExecution(claim.ID)
		}
		if err := s.updateNextRun(task, claim); err != nil {
			switch {
			case errors.Is(err, storage.ErrModUpdateScheduleChanged):
				s.log.Debug("Task %s: schedule changed while claiming its due slot", task.Name)
			case errors.Is(err, errInvalidTaskSchedule):
				s.log.Error("Task %s: invalid schedule, refusing dispatch: %v", task.Name, err)
				var dbErr error
				if task.TaskType == storage.TaskTypeModUpdate {
					dbErr = s.store.DisableInvalidModUpdateSchedule(ctx, task)
				} else {
					dbErr = s.store.DB().WithContext(ctx).Model(&storage.ScheduledTask{}).
						Where("id = ?", task.ID).Update("status", storage.TaskStatusDisabled).Error
				}
				if dbErr != nil {
					s.log.Error("Task %s: failed to disable task after schedule error: %v", task.Name, dbErr)
				}
			default:
				s.log.Error("Task %s: cannot persist schedule, leaving it due: %v", task.Name, err)
			}
			if claim != nil {
				s.forgetPendingExecution(claim.ID)
			}
			s.releaseTask(task.ID)
			continue
		}

		// A policy edit may win immediately after the atomic slot claim. Read
		// the current revision before planning and refuse the stale snapshot.
		if task.TaskType == storage.TaskTypeModUpdate {
			claimedTask, err := s.revalidateModUpdateClaim(ctx, task)
			if err != nil {
				s.log.Error("Task %s: mod update policy changed after claiming its due slot, refusing dispatch: %v", task.Name, err)
				if claim != nil {
					s.failClaimedExecution(ctx, claim, "mod update policy changed after claiming its due slot; the stale policy was not dispatched")
					s.forgetPendingExecution(claim.ID)
				}
				s.releaseTask(task.ID)
				continue
			}
			task = claimedTask
		}

		// Execute task asynchronously
		s.wg.Add(1)
		go func(t *storage.ScheduledTask, claimed *storage.TaskExecution) {
			defer s.wg.Done()
			defer s.releaseTask(t.ID)
			if claimed != nil {
				defer s.forgetPendingExecution(claimed.ID)
			}
			// A scheduled run is owned by the scheduler, not by any caller.
			_, _ = s.executeTask(context.Background(), t, "scheduled", v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED, nil, claimed)
		}(task, claim)
	}
}

// tryClaimTask attempts to atomically register taskID as running in memory and checks
// the database for any active running executions (SCH-5). A database error refuses
// the claim and leaves the task unregistered, so a scheduled run stays due.
func (s *Scheduler) tryClaimTask(ctx context.Context, taskID string) (bool, error) {
	s.runningTasksMu.Lock()
	defer s.runningTasksMu.Unlock()

	if s.runningTasks[taskID] {
		return false, nil
	}
	if s.store != nil {
		hasRunning, err := s.store.HasRunningExecution(ctx, taskID)
		if err != nil {
			return false, err
		}
		if hasRunning {
			return false, nil
		}
	}
	s.runningTasks[taskID] = true
	return true, nil
}

// releaseTask unregisters taskID from in-memory running tasks.
func (s *Scheduler) releaseTask(taskID string) {
	s.runningTasksMu.Lock()
	delete(s.runningTasks, taskID)
	s.runningTasksMu.Unlock()
}

// TriggerTask manually triggers a task execution
func (s *Scheduler) TriggerTask(ctx context.Context, taskID string) (*storage.TaskExecution, error) {
	task, err := s.store.GetScheduledTask(ctx, taskID)
	if err != nil {
		return nil, err
	}

	// Conflicting execution guard (MINE-146, SCH-5): never run the same task twice.
	// A failed running check is not proof that the task is idle.
	claimed, err := s.tryClaimTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if !claimed {
		return nil, ErrTaskAlreadyRunning
	}
	defer s.releaseTask(taskID)

	// Thread the caller context so an RPC cancel (or client disconnect)
	// actually interrupts the task instead of the work outliving the request.
	execution, err := s.executeTask(ctx, task, "manual", v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED, nil, nil)
	return execution, err
}

// IsTaskRunning checks if a task is currently executing
func (s *Scheduler) IsTaskRunning(taskID string) bool {
	s.runningTasksMu.Lock()
	if s.runningTasks[taskID] {
		s.runningTasksMu.Unlock()
		return true
	}
	s.runningTasksMu.Unlock()

	if s.store != nil {
		hasRunning, err := s.store.HasRunningExecution(context.Background(), taskID)
		if err != nil || hasRunning {
			return true
		}
	}
	return false
}

// Schedulers subscription to the central event bus
func (s *Scheduler) HandleServerEvent(ctx context.Context, event events.Event) {
	tasks, err := s.store.ListEventTriggeredTasks(ctx, event.ServerID, event.Type)
	if err != nil {
		s.log.Error("Failed to list event-triggered tasks for %s: %v", event.Type, err)
		return
	}
	for _, task := range tasks {
		claimed, err := s.tryClaimTask(ctx, task.ID)
		if err != nil {
			s.log.Error("Event-triggered task %s: running check failed, not dispatching: %v", task.Name, err)
			continue
		}
		if !claimed {
			s.log.Debug("Event-triggered task %s is already running, skipping duplicate dispatch", task.Name)
			continue
		}

		s.wg.Add(1)
		go func(t *storage.ScheduledTask) {
			defer s.wg.Done()
			defer s.releaseTask(t.ID)
			// WithoutCancel keeps the bus dispatch context's Values (trace ids)
			// but not its cancellation: a task legitimately outlives the dispatch
			// that scheduled it, so inheriting the bus cancel would kill it.
			s.executeTaskForEvent(context.WithoutCancel(ctx), t, event.Type, event.Data)
		}(task)
	}
}

// executeTaskForEvent runs a task as a result of an event firing. The event
// type is threaded through to webhook executors so the rendered payload
// reflects which event triggered the delivery.
func (s *Scheduler) executeTaskForEvent(ctx context.Context, task *storage.ScheduledTask, eventType v1.TriggeredEventType, eventData map[string]any) {
	_, _ = s.executeTask(ctx, task, "event", eventType, eventData, nil)
}

// executeTask runs a single task. eventTrigger names the event that drove an
// event-triggered run (empty for scheduled/manual runs). claimed is the durable
// execution recorded with a scheduled slot claim; nil means this call records
// its own execution.
func (s *Scheduler) executeTask(ctx context.Context, task *storage.ScheduledTask, trigger string, eventType v1.TriggeredEventType, eventData map[string]any, claimed *storage.TaskExecution) (*storage.TaskExecution, error) {
	return s.executeTaskAtDepth(ctx, task, trigger, eventType, eventData, 0, claimed)
}

// maxChainDepth caps reaction-chain nesting (MINE-142).
const maxChainDepth = 4

func (s *Scheduler) executeTaskAtDepth(ctx context.Context, task *storage.ScheduledTask, trigger string, eventType v1.TriggeredEventType, eventData map[string]any, depth int, claimed *storage.TaskExecution) (*storage.TaskExecution, error) {
	if claimed != nil {
		// Direct callers and tests may reach executeTask without the dispatch
		// registration; make the handoff visible before the first terminal
		// write can be considered.
		s.registerPendingExecution(claimed.ID)
		defer s.forgetPendingExecution(claimed.ID)
	}

	// Check if server exists
	server, err := s.store.GetServer(ctx, task.ServerID)
	if err != nil {
		s.log.Error("Task %s: server not found: %v", task.Name, err)
		if claimed != nil {
			s.failClaimedExecution(ctx, claimed, "task execution could not start: server lookup failed")
		}
		return claimed, err
	}

	// Check if server is online (if required). Webhook tasks always fire â€”
	// they notify, they don't operate on the server, and most useful events
	// (server_stop, server_restart) happen while the server is not running.
	if task.RequireOnline && task.TaskType != storage.TaskTypeWebhook && server.Status != storage.StatusRunning {
		s.log.Debug("Task %s: skipped (server offline)", task.Name)

		// Create skipped execution record
		execution := claimed
		if execution == nil {
			now := time.Now()
			execution = &storage.TaskExecution{
				ID:        uuid.New().String(),
				TaskID:    task.ID,
				ServerID:  task.ServerID,
				Status:    storage.ExecutionStatusSkipped,
				StartedAt: now,
				EndedAt:   &now,
				Trigger:   trigger,
				Error:     "server offline",
			}
			if err := s.store.CreateTaskExecution(ctx, execution); err != nil {
				s.log.Error("Task %s: failed to record skipped execution: %v", task.Name, err)
			}
		} else {
			// The claim is still running. A cancellation may have won the
			// handoff and storage may be transiently unavailable, so use the
			// guarded, bounded terminal write instead of an unconditional save:
			// it keeps a cancellation and never leaves the claim running.
			if cancelled := s.persistClaimedOutcome(ctx, execution, storage.ExecutionStatusSkipped, "server offline"); cancelled {
				// The cancellation won the handoff. Keep the cancelled outcome
				// and do not start child steps the operator cancelled.
				if trigger != "scheduled" {
					if err := s.updateNextRun(task, nil); err != nil {
						s.log.Error("Task %s: failed to advance schedule after cancelled skip: %v", task.Name, err)
					}
				}
				return execution, nil
			}
		}

		// Skipped steps still fan out to children (each child applies its
		// own online gate), so chains degrade to per-step skip records.
		if depth < maxChainDepth {
			stopped, chainErr := s.runChildChain(ctx, task, trigger, eventType, eventData, depth)
			s.recordChainStop(ctx, execution, stopped, chainErr)
		}

		// Update next run time if not already updated at dispatch
		if trigger != "scheduled" {
			if err := s.updateNextRun(task, nil); err != nil {
				s.log.Error("Task %s: failed to advance schedule after skip: %v", task.Name, err)
			}
		}
		return execution, nil
	}

	// Create execution record
	execution := claimed
	if execution == nil {
		execution = &storage.TaskExecution{
			ID:        uuid.New().String(),
			TaskID:    task.ID,
			ServerID:  task.ServerID,
			Status:    storage.ExecutionStatusRunning,
			StartedAt: time.Now(),
			Trigger:   trigger,
		}
		// Register the handoff before the row becomes visible in storage.
		s.registerPendingExecution(execution.ID)
		if err := s.store.CreateTaskExecution(ctx, execution); err != nil {
			s.forgetPendingExecution(execution.ID)
			s.log.Error("Task %s: failed to create execution record: %v", task.Name, err)
			return nil, err
		}
	}

	// Create cancellable context with timeout
	timeout := time.Duration(task.Timeout) * time.Second
	if timeout == 0 {
		timeout = 5 * time.Minute // Default timeout
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)

	// Activate the worker exactly once. A cancellation that arrived while the
	// claimed row was visible but no worker was registered wins here: do not
	// invoke the planner and keep the cancelled outcome.
	if !s.activateExecution(execution.ID, cancel) {
		cancel()
		now := time.Now()
		execution.Status = storage.ExecutionStatusCancelled
		execution.Error = "execution cancelled"
		execution.EndedAt = &now
		execution.Duration = now.Sub(execution.StartedAt).Milliseconds()
		s.persistTerminalExecution(ctx, execution)
		s.log.Info("Task %s: cancelled before execution started", task.Name)
		return execution, context.Canceled
	}

	defer func() {
		cancel()
		s.executionMu.Lock()
		delete(s.runningExecutions, execution.ID)
		s.executionMu.Unlock()
	}()

	s.log.Info("Task %s: executing on server %s (trigger: %s)", task.Name, server.Name, trigger)

	// Execute the task based on type, retrying on failure if configured.
	// A console command is attempted once: a failed send may already have
	// been delivered, and repeating it is not safe.
	var output string
	var execErr error
	retries := task.RetryCount
	if task.TaskType == storage.TaskTypeCommand {
		retries = 0
	}

	var modBudget modUpdateRunBudget
	for attempt := 0; ; attempt++ {
		output, execErr = s.runTaskType(execCtx, server, task, eventType, eventData, &modBudget)
		// A staged-batch conflict is an admission refusal owned by another
		// lifecycle operation, not a transient planner failure; retrying the
		// same slot would only repeat it.
		if errors.Is(execErr, storage.ErrUpdateBatchConflict) {
			break
		}
		if execErr == nil || attempt >= retries || execCtx.Err() != nil || modBudget.exhausted {
			break
		}

		retryDelay := time.Duration(task.RetryDelay) * time.Second
		if retryDelay <= 0 {
			retryDelay = time.Minute
		}
		s.log.Warn("Task %s: attempt %d failed, retrying in %v: %v", task.Name, attempt+1, retryDelay, execErr)

		select {
		case <-execCtx.Done():
		case <-time.After(retryDelay):
		}
		if execCtx.Err() != nil {
			break
		}
		execution.RetryNum = attempt + 1
	}
	if execErr != nil && task.TaskType == storage.TaskTypeCommand && task.RetryCount > 0 {
		if output != "" {
			output += "\n"
		}
		output += "command was not retried because it may already have been delivered"
	}

	// Update execution record
	endTime := time.Now()
	execution.EndedAt = &endTime
	execution.Duration = endTime.Sub(execution.StartedAt).Milliseconds()
	execution.Output = output

	if execErr != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			execution.Status = storage.ExecutionStatusTimeout
			execution.Error = "execution timed out"
		} else if execCtx.Err() == context.Canceled {
			execution.Status = storage.ExecutionStatusCancelled
			execution.Error = "execution cancelled"
		} else {
			execution.Status = storage.ExecutionStatusFailed
			execution.Error = execErr.Error()
		}
		s.log.Error("Task %s: failed: %v", task.Name, execErr)
	} else {
		execution.Status = storage.ExecutionStatusCompleted
		s.log.Info("Task %s: completed successfully", task.Name)
	}

	s.persistTerminalExecution(ctx, execution)

	// Reaction chain (MINE-142): run enabled children in step order when the
	// parent succeeded, or when it failed but allows continuation. A child
	// failure aborts the rest of the chain unless that child allowed it.
	// Each child runs through the normal path, so RequireOnline and retries
	// apply per step.
	if depth < maxChainDepth && (execErr == nil || task.ContinueOnFailure) {
		stopped, chainErr := s.runChildChain(ctx, task, trigger, eventType, eventData, depth)
		s.recordChainStop(ctx, execution, stopped, chainErr)
	}

	// Update next run time if not already updated at dispatch
	if trigger != "scheduled" {
		if err := s.updateNextRun(task, nil); err != nil {
			s.log.Error("Task %s: failed to advance schedule after execution: %v", task.Name, err)
		}
	}

	return execution, execErr
}

// runChildChain executes a task's children sequentially. The returned step is
// the child where the chain stopped; later children were not started. A
// non-nil error means the child list itself could not be read.
func (s *Scheduler) runChildChain(ctx context.Context, parent *storage.ScheduledTask, trigger string, eventType v1.TriggeredEventType, eventData map[string]any, depth int) (string, error) {
	children, err := s.store.ListChildTasks(ctx, parent.ID)
	if err != nil {
		s.log.Error("Task %s: failed to list child tasks: %v", parent.Name, err)
		return "", err
	}
	for _, child := range children {
		if child.TimeOffsetSecs > 0 {
			s.log.Info("Task %s: waiting %ds before child step %s", parent.Name, child.TimeOffsetSecs, child.Name)
			// Cancellable wait: an uninterruptible time.Sleep meant a cancel or
			// shutdown had to sit out the full offset before the chain noticed.
			timer := time.NewTimer(time.Duration(child.TimeOffsetSecs) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				s.log.Info("Task %s: chain cancelled while waiting before step %s", parent.Name, child.Name)
				return child.Name, nil
			case <-timer.C:
			}
		}
		_, childErr := s.executeTaskAtDepth(ctx, child, trigger, eventType, eventData, depth+1, nil)
		if childErr != nil && !child.ContinueOnFailure {
			s.log.Warn("Task %s: chain aborted at step %s: %v", parent.Name, child.Name, childErr)
			return child.Name, nil
		}
	}
	return "", nil
}

func (s *Scheduler) recordChainStop(ctx context.Context, execution *storage.TaskExecution, stopped string, chainErr error) {
	if execution == nil || (chainErr == nil && stopped == "") {
		return
	}
	note := "chain stopped because the next steps could not be listed"
	if chainErr == nil {
		note = fmt.Sprintf("chain stopped at %s; remaining steps were not run", stopped)
	}
	if execution.Output != "" {
		execution.Output += "\n"
	}
	execution.Output += note
	// Chain-stop bookkeeping is not an outcome. Write only the output column so
	// a terminal status, error or end time owned by a cancellation or another
	// writer is never overwritten.
	if updated, err := s.store.UpdateTaskExecutionOutput(ctx, execution.ID, execution.Output); err != nil {
		s.log.Error("Task execution %s: failed to record chain stop: %v", execution.ID, err)
	} else if !updated {
		s.log.Debug("Task execution %s: chain stop was not recorded on a missing row", execution.ID)
	}
}

// runTaskType dispatches a single execution attempt to the type-specific executor
func (s *Scheduler) runTaskType(ctx context.Context, server *storage.Server, task *storage.ScheduledTask, eventType v1.TriggeredEventType, eventData map[string]any, modBudget *modUpdateRunBudget) (string, error) {
	switch task.TaskType {
	case storage.TaskTypeCommand:
		return s.executeCommandTask(ctx, server, task)
	case storage.TaskTypeRestart:
		return s.executeRestartTask(ctx, server, task)
	case storage.TaskTypeStart:
		return s.executeStartTask(ctx, server, task)
	case storage.TaskTypeStop:
		return s.executeStopTask(ctx, server, task)
	case storage.TaskTypeBackup:
		return s.executeBackupTask(ctx, server, task)
	case storage.TaskTypeScript:
		return s.executeScriptTask(ctx, server, task)
	case storage.TaskTypeWebhook:
		return s.executeWebhookTask(ctx, server, task, eventType, eventData)
	case storage.TaskTypeModpackUpdate:
		return s.executeModpackUpdateTask(ctx, server, task)
	case storage.TaskTypeModUpdate:
		return s.executeModUpdateTask(ctx, server, task, modBudget)
	case storage.TaskTypeChunkyPregen:
		return s.executeChunkyPregen(ctx, server, task)
	default:
		return "", fmt.Errorf("unknown task type: %s", task.TaskType)
	}
}

// registerPendingExecution records a claimed execution before its worker owns
// a cancel function. It is idempotent and never clears a cancellation that
// already won the handoff.
func (s *Scheduler) registerPendingExecution(executionID string) {
	if executionID == "" {
		return
	}
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	if s.pendingExecutions == nil {
		s.pendingExecutions = make(map[string]bool)
	}
	if _, active := s.runningExecutions[executionID]; !active {
		s.pendingExecutions[executionID] = true
	}
}

// forgetPendingExecution drops handoff bookkeeping after a worker starts or a
// claim is abandoned. It does not touch an active worker.
func (s *Scheduler) forgetPendingExecution(executionID string) {
	if executionID == "" {
		return
	}
	s.executionMu.Lock()
	delete(s.pendingExecutions, executionID)
	delete(s.pendingCancellations, executionID)
	s.executionMu.Unlock()
}

// activateExecution makes a worker the owner of its execution. It returns
// false when a cancellation won the handoff, in which case the worker must not
// invoke the task planner.
func (s *Scheduler) activateExecution(executionID string, cancel context.CancelFunc) bool {
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	delete(s.pendingExecutions, executionID)
	if s.pendingCancellations[executionID] {
		delete(s.pendingCancellations, executionID)
		return false
	}
	if s.runningExecutions == nil {
		s.runningExecutions = make(map[string]context.CancelFunc)
	}
	s.runningExecutions[executionID] = cancel
	return true
}

// CancelExecution cancels a running execution
func (s *Scheduler) CancelExecution(executionID string) error {
	s.executionMu.Lock()
	if cancel, exists := s.runningExecutions[executionID]; exists {
		s.executionMu.Unlock()
		cancel()
		return nil
	}
	if s.pendingExecutions[executionID] {
		// The worker has not activated yet. Record the intent and let it
		// observe the cancellation at the handoff instead of writing a
		// terminal state the worker could later overwrite.
		if s.pendingCancellations == nil {
			s.pendingCancellations = make(map[string]bool)
		}
		s.pendingCancellations[executionID] = true
		s.executionMu.Unlock()
		return nil
	}
	s.executionMu.Unlock()

	// If not running in active memory, check if it was left orphaned in storage
	if s.store != nil {
		ctx := context.Background()
		exec, err := s.store.GetTaskExecution(ctx, executionID)
		if err == nil && exec != nil && exec.Status == storage.ExecutionStatusRunning {
			now := time.Now()
			exec.Status = storage.ExecutionStatusCancelled
			exec.Error = "execution cancelled"
			exec.EndedAt = &now
			if exec.StartedAt.IsZero() {
				exec.Duration = 0
			} else {
				exec.Duration = now.Sub(exec.StartedAt).Milliseconds()
			}
			// The row was running when it was read, but an active worker may
			// have finished it since. Only cancel while it is still running so
			// this orphan path never overwrites someone else's outcome.
			updated, updateErr := s.store.UpdateTaskExecutionIfRunning(ctx, exec)
			if updateErr != nil {
				return fmt.Errorf("failed to cancel orphaned execution: %w", updateErr)
			}
			if !updated {
				current, getErr := s.store.GetTaskExecution(ctx, executionID)
				if getErr != nil || current == nil {
					s.log.Warn("Task execution %s: cancellation found no running row to update: %v", executionID, getErr)
				} else {
					s.log.Info("Task execution %s: cancellation kept the existing %s outcome", executionID, current.Status)
				}
				return fmt.Errorf("execution not found or already finished")
			}
			return nil
		}
	}

	return fmt.Errorf("execution not found or already finished")
}

// newScheduledExecution builds the execution record that a scheduled dispatch
// records with its due-slot claim. The row is inserted in the same transaction
// as the claim, so it exists before asynchronous dispatch starts.
func newScheduledExecution(task *storage.ScheduledTask, now time.Time) *storage.TaskExecution {
	return &storage.TaskExecution{
		ID:        uuid.New().String(),
		TaskID:    task.ID,
		ServerID:  task.ServerID,
		Status:    storage.ExecutionStatusRunning,
		StartedAt: now,
		Trigger:   "scheduled",
	}
}

// persistTerminalExecution writes an outcome only while the durable row is
// still running. A cancel that won the handoff keeps its cancelled outcome.
// The write has its own bounded context because a manual run's RPC context can
// be cancelled after the client disconnects while the row still needs its
// terminal outcome.
func (s *Scheduler) persistTerminalExecution(ctx context.Context, execution *storage.TaskExecution) {
	if execution == nil {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	updated, err := s.store.UpdateTaskExecutionIfRunning(writeCtx, execution)
	if err != nil {
		s.log.Error("Task execution %s: failed to record outcome: %v", execution.ID, err)
		return
	}
	if !updated {
		s.log.Debug("Task execution %s: kept an outcome written by a cancellation", execution.ID)
	}
}

// failClaimedExecution records a terminal failure for a slot claim that was
// refused or could not start, so the consumed slot is never silent and no
// execution stays stuck in running until the next process start.
func (s *Scheduler) failClaimedExecution(ctx context.Context, execution *storage.TaskExecution, reason string) {
	s.persistClaimedOutcome(ctx, execution, storage.ExecutionStatusFailed, reason)
}

// persistClaimedOutcome writes a terminal outcome for a claimed execution and
// reports whether a pending cancellation won the handoff. A cancellation that
// won is kept instead of the requested status. The write has its own bounded
// context so caller cancellation cannot abandon the claim, and it retries
// briefly because storage may recover during the retry window. It is guarded on
// the row still running, so a late write cannot overwrite an outcome another
// writer already recorded.
func (s *Scheduler) persistClaimedOutcome(ctx context.Context, execution *storage.TaskExecution, status storage.ExecutionStatus, reason string) bool {
	if execution == nil {
		return false
	}
	s.executionMu.Lock()
	cancelled := s.pendingCancellations[execution.ID]
	if cancelled {
		delete(s.pendingCancellations, execution.ID)
	}
	s.executionMu.Unlock()

	now := time.Now()
	if cancelled {
		execution.Status = storage.ExecutionStatusCancelled
		execution.Error = "execution cancelled"
	} else {
		execution.Status = status
		execution.Error = reason
	}
	execution.EndedAt = &now
	execution.Duration = now.Sub(execution.StartedAt).Milliseconds()

	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		updated, err := s.store.UpdateTaskExecutionIfRunning(writeCtx, execution)
		if err == nil {
			if !updated {
				s.log.Debug("Task execution %s: kept an outcome written by a cancellation", execution.ID)
			}
			return cancelled
		}
		lastErr = err
		if writeCtx.Err() != nil {
			break
		}
		s.log.Warn("Task execution %s: terminal write failed (attempt %d), retrying: %v", execution.ID, attempt+1, lastErr)
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-timer.C:
		case <-writeCtx.Done():
			timer.Stop()
		}
	}
	s.log.Error("Task execution %s: failed to record outcome after retries: %v", execution.ID, lastErr)
	return cancelled
}

// updateNextRun calculates and updates the next run time for a task. It uses
// CalculateNextRun so dispatch and explicit recalculation honor the same
// per-task timezone and cannot drift apart. claimed, when non-nil, is recorded
// in the same durable write as a mod-update slot claim.
func (s *Scheduler) updateNextRun(task *storage.ScheduledTask, claimed *storage.TaskExecution) error {
	ctx := context.Background()
	now := time.Now()
	advance := func(nextRun *time.Time, persistStatus storage.TaskStatus) error {
		if task.TaskType == storage.TaskTypeModUpdate {
			return s.store.AdvanceModUpdateSchedule(ctx, task, nextRun, now, persistStatus, claimed)
		}
		return s.store.UpdateTaskNextRun(ctx, task.ID, nextRun, &now)
	}
	if task.Schedule == storage.ScheduleTypeOnce || task.Schedule == storage.ScheduleTypeEvent {
		persistStatus := storage.TaskStatus("")
		if task.Schedule == storage.ScheduleTypeOnce {
			persistStatus = storage.TaskStatusDisabled
		}
		if err := advance(nil, persistStatus); err != nil {
			return fmt.Errorf("clear next run: %w", err)
		}
		if task.Schedule == storage.ScheduleTypeOnce {
			// The one-shot slot and its terminal status are claimed in one
			// guarded write. Keep the in-memory claim equal to the persisted
			// state so the dispatch observes one consistent state.
			task.Status = storage.TaskStatusDisabled
			task.NextRun = nil
		}
		return nil
	}
	nextRun, err := s.CalculateNextRun(task)
	if err != nil {
		return fmt.Errorf("%w: %w", errInvalidTaskSchedule, err)
	}
	if err := advance(nextRun, ""); err != nil {
		return fmt.Errorf("store next run: %w", err)
	}
	return nil
}

// revalidateModUpdateClaim reads the policy revision after the slot claim.
// An edit that commits between the claim and this read fails closed instead
// of reaching the planner with the stale snapshot.
func (s *Scheduler) revalidateModUpdateClaim(ctx context.Context, claimed *storage.ScheduledTask) (*storage.ScheduledTask, error) {
	current, err := s.store.GetScheduledTask(ctx, claimed.ID)
	if err != nil {
		return nil, err
	}
	if current.TaskType != claimed.TaskType || current.Status != claimed.Status || current.Config != claimed.Config || !sameScheduledTaskTiming(current, claimed) {
		return nil, fmt.Errorf("%w: policy revision changed", storage.ErrModUpdateScheduleChanged)
	}
	return current, nil
}

func sameScheduledTaskTiming(a, b *storage.ScheduledTask) bool {
	if a.Schedule != b.Schedule || a.CronExpr != b.CronExpr || a.Timezone != b.Timezone || a.IntervalSecs != b.IntervalSecs {
		return false
	}
	if a.RunAt == nil || b.RunAt == nil {
		return a.RunAt == nil && b.RunAt == nil
	}
	return a.RunAt.Equal(*b.RunAt)
}

// Task type executors

// CommandTaskConfig represents configuration for command tasks
type CommandTaskConfig struct {
	Command string `json:"command"`
}

func (s *Scheduler) executeCommandTask(ctx context.Context, server *storage.Server, task *storage.ScheduledTask) (string, error) {
	var config CommandTaskConfig
	if task.Config != "" {
		if err := json.Unmarshal([]byte(task.Config), &config); err != nil {
			return "", fmt.Errorf("invalid command config: %w", err)
		}
	}

	if config.Command == "" {
		return "", fmt.Errorf("no command specified")
	}

	if server.ContainerID == "" {
		return "", fmt.Errorf("server has no container")
	}

	output, err := s.sender.SendCommand(ctx, server.ID, config.Command)
	return output, err
}

func (s *Scheduler) refuseActiveOperation(ctx context.Context, serverID string) error {
	if s.store == nil {
		return nil
	}
	if err := s.store.AssertNoServerOperation(ctx, serverID); err != nil {
		return fmt.Errorf("refused while another operation owns the server: %w", err)
	}
	return nil
}

func (s *Scheduler) executeRestartTask(ctx context.Context, server *storage.Server, _ *storage.ScheduledTask) (string, error) {
	transition, err := docker.TryBeginServerTransition(server.ID)
	if err != nil {
		return "", err
	}
	defer transition.Release()
	if err := s.refuseActiveOperation(ctx, server.ID); err != nil {
		return "", err
	}

	if server.ContainerID == "" {
		return "", fmt.Errorf("server has no container")
	}
	release, err := s.acquireNodeAdmission(ctx, server)
	if err != nil {
		return "", err
	}
	defer release()
	if err := s.checkNodeMemoryCapacity(ctx, server); err != nil {
		return "", err
	}
	dockerCli, err := s.dockerForNode(server.NodeID)
	if err != nil {
		return "", err
	}
	if handled, err := recreateForPendingStagedConfig(ctx, s.store, dockerCli, s.log, server, true, true, s.recreateModule, s.storageResolver); handled {
		if err != nil {
			return "", err
		}
		return "server restarted with staged configuration", nil
	}

	// Stop container
	found, err := dockerCli.StopContainer(ctx, server.ContainerID)
	if err != nil {
		return "", fmt.Errorf("failed to stop: %w", err)
	}
	if !found {
		server.ContainerID = ""
		server.Status = storage.StatusStopped
		s.store.UpdateServer(ctx, server)
		return "container not found, marked as stopped", nil
	}

	// Wait a moment
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(2 * time.Second):
	}

	// Start container
	if err := dockerCli.StartContainer(ctx, server.ContainerID); err != nil {
		return "", fmt.Errorf("failed to start: %w", err)
	}

	// Update server status
	server.Status = storage.StatusStarting
	now := time.Now()
	server.LastStarted = &now
	s.store.UpdateServer(ctx, server)

	return "server restarted successfully", nil
}

func (s *Scheduler) executeStartTask(ctx context.Context, server *storage.Server, _ *storage.ScheduledTask) (string, error) {
	transition, err := docker.TryBeginServerTransition(server.ID)
	if err != nil {
		return "", err
	}
	defer transition.Release()
	if err := s.refuseActiveOperation(ctx, server.ID); err != nil {
		return "", err
	}

	if server.ContainerID == "" {
		return "", fmt.Errorf("server has no container")
	}
	release, err := s.acquireNodeAdmission(ctx, server)
	if err != nil {
		return "", err
	}
	defer release()
	if err := s.checkNodeMemoryCapacity(ctx, server); err != nil {
		return "", err
	}
	dockerCli, err := s.dockerForNode(server.NodeID)
	if err != nil {
		return "", err
	}
	if handled, err := recreateForPendingStagedConfig(ctx, s.store, dockerCli, s.log, server, true, true, s.recreateModule, s.storageResolver); handled {
		if err != nil {
			return "", err
		}
		return "server started with staged configuration", nil
	}

	if err := dockerCli.StartContainer(ctx, server.ContainerID); err != nil {
		return "", fmt.Errorf("failed to start: %w", err)
	}

	server.Status = storage.StatusStarting
	now := time.Now()
	server.LastStarted = &now
	s.store.UpdateServer(ctx, server)

	return "server started successfully", nil
}

// StartServerForBoot applies pending restart-only configuration before the
// startup auto-start path can mark a server as successfully started. Existing
// running containers are left alone; only stopped containers are started.
func (s *Scheduler) StartServerForBoot(ctx context.Context, server *storage.Server) error {
	if server == nil || server.ID == "" {
		return fmt.Errorf("server ID is required")
	}
	transition, err := docker.TryBeginServerTransition(server.ID)
	if err != nil {
		return err
	}
	defer transition.Release()
	if err := s.refuseActiveOperation(ctx, server.ID); err != nil {
		return err
	}
	if server.ContainerID == "" {
		return fmt.Errorf("server has no container")
	}
	release, err := s.acquireNodeAdmission(ctx, server)
	if err != nil {
		return err
	}
	defer release()
	if err := s.checkNodeMemoryCapacity(ctx, server); err != nil {
		return err
	}
	dockerCli, err := s.dockerForNode(server.NodeID)
	if err != nil {
		return err
	}
	if handled, err := recreateForPendingStagedConfig(ctx, s.store, dockerCli, s.log, server, true, true, s.recreateModule, s.storageResolver); handled {
		return err
	}

	status, err := dockerCli.GetContainerStatus(ctx, server.ContainerID)
	if err != nil {
		return fmt.Errorf("failed to inspect server container: %w", err)
	}
	if status == storage.StatusPaused {
		if err := dockerCli.UnpauseContainer(ctx, server.ContainerID); err != nil {
			return fmt.Errorf("failed to unpause server container: %w", err)
		}
		status = storage.StatusStarting
	} else if status != storage.StatusRunning && status != storage.StatusUnhealthy {
		if err := dockerCli.StartContainer(ctx, server.ContainerID); err != nil {
			server.Status = storage.StatusError
			_ = s.store.UpdateServer(ctx, server)
			return fmt.Errorf("failed to start server container: %w", err)
		}
		status = storage.StatusStarting
		now := time.Now()
		server.LastStarted = &now
	}
	server.Status = status
	if err := s.store.UpdateServer(ctx, server); err != nil {
		return fmt.Errorf("container started but server state could not be saved: %w", err)
	}
	return nil
}

// acquireNodeAdmission claims the shared per-node gate before a scheduler start
// capacity check. The caller defers the release at function scope so the gate
// covers container mutation and status persistence. Cancellation and refusal
// are plain errors here; RPC callers map their own transport codes.
func (s *Scheduler) acquireNodeAdmission(ctx context.Context, server *storage.Server) (func(), error) {
	s.mu.RLock()
	admission := s.admission
	s.mu.RUnlock()
	release, err := admission.Acquire(ctx, server.NodeID)
	if err != nil {
		return nil, fmt.Errorf("node %s start admission: %w", server.NodeID, err)
	}
	return release, nil
}

// checkNodeMemoryCapacity protects scheduler-owned starts and restarts (including
// boot and deep-sleep wake) with the same persisted reservation and host limits as RPC
// lifecycle operations.
func (s *Scheduler) checkNodeMemoryCapacity(ctx context.Context, server *storage.Server) error {
	if server == nil {
		return fmt.Errorf("server is nil")
	}
	dockerCli, err := s.dockerForNode(server.NodeID)
	if err != nil {
		return err
	}
	if dockerCli == nil {
		return fmt.Errorf("cannot verify node memory capacity without a store and Docker client")
	}
	return serverruntime.CheckStartCapacity(ctx, s.store, server, func(ctx context.Context, _ string) (int64, error) {
		return dockerCli.HostMemoryMB(ctx)
	})
}

type stagedLifecycleDocker interface {
	RecreateContainer(ctx context.Context, oldContainerID string, server *storage.Server, serverConfig *storage.ServerConfig) (*docker.RecreateContainerResult, error)
	StartContainer(ctx context.Context, containerID string) error
	ResolveContainer(ctx context.Context, serverID string) (*container.Summary, error)
}

// recreateForPendingStagedConfig ensures scheduled lifecycle operations cannot
// start an old container and advance LastStarted past an applied on-restart
// change. A replacement ID is persisted even when Docker reports a partial
// recreate failure, allowing the next attempt to recover from the right object.
func recreateForPendingStagedConfig(ctx context.Context, store *storage.Store, dockerClient stagedLifecycleDocker, log *logger.Logger, server *storage.Server, startReplacement, markStarted bool, recreateModule func(context.Context, string) error, resolvers ...*nodestorage.Resolver) (bool, error) {
	return prepareStagedLifecycle(ctx, store, dockerClient, log, server, startReplacement, markStarted, recreateModule, nil, resolvers...)
}

func prepareStagedLifecycle(ctx context.Context, store *storage.Store, dockerClient stagedLifecycleDocker, log *logger.Logger, server *storage.Server, startReplacement, markStarted bool, recreateModule func(context.Context, string) error, transfer func(*staging.RestartPublication), resolvers ...*nodestorage.Resolver) (handled bool, retErr error) {
	var resolver *nodestorage.Resolver
	if len(resolvers) > 0 {
		resolver = resolvers[0]
	}
	publication := &staging.RestartPublication{}
	commit, err := staging.CommitNextRestartFiles(ctx, store, server, recreateModule, resolver)
	if err != nil {
		return true, fmt.Errorf("failed to apply staged content: %w", err)
	}
	publication.Commit = commit
	defer func() {
		if retErr == nil && handled && transfer != nil {
			transfer(publication)
			return
		}
		if finishErr := publication.Resolve(ctx, store, retErr); finishErr != nil {
			handled = true
			retErr = errors.Join(retErr, finishErr)
		}
	}()
	appliedRecords, err := publication.Commit.ApplyConfigs(ctx, store)
	publication.Configs = appliedRecords
	if err != nil {
		return true, fmt.Errorf("failed to apply staged server configuration: %w", err)
	}
	appliedNow := publication.Commit.AppliedConfigsThisAttempt()
	pending, err := HasPendingRestartStagedConfigRecreate(ctx, store, server.ID, server.LastStarted)
	if err != nil {
		return true, fmt.Errorf("failed to check staged configuration recovery: %w", err)
	}
	// An acknowledged publication only owes cleanup. It must not recreate the
	// container again; the caller's normal start/wake proceeds on the current
	// replacement and the deferred Resolve finishes the batch.
	needsPublication := publication.Commit.NeedsRuntimePublication()
	// An unverified dispatch is pending work even when it owes no runtime
	// publication: a file-only batch records the dispatch without needing a new
	// container. Returning "no work" here would skip the identity check, and the
	// deferred Resolve would then reject the dispatch and block the wake.
	needsIdentityVerification := publication.Commit.NeedsIdentityVerification()
	if appliedNow == 0 && !pending && !needsPublication && !needsIdentityVerification {
		return false, nil
	}
	if needsIdentityVerification {
		// A dispatch intent is durable but its outcome was not verified. Repair
		// the tracked identity if the canonical container differs, then accept
		// only the exact replacement identity captured at creation. Discovery of
		// any other container is identity repair, never publication evidence.
		if _, reconcileErr := serverruntime.ReconcileContainerIdentity(ctx, store, dockerClient, server); reconcileErr != nil {
			return true, errors.Join(publication.Commit.Retain(ctx), reconcileErr)
		}
		verified, verifyErr := publication.Commit.VerifyExpectedReplacement(ctx, server.ContainerID)
		if verifyErr != nil {
			return true, verifyErr
		}
		if !verified {
			return true, errors.Join(publication.Commit.Retain(ctx),
				fmt.Errorf("canonical container %q is not the recorded replacement %q; refusing to acknowledge the publication", server.ContainerID, publication.Commit.ExpectedReplacementID()))
		}
		// The runtime is verified. Let the caller's normal lifecycle operate on
		// that container; this attempt owes cleanup, not another replacement.
		publication.RuntimeAccepted = true
		return false, nil
	}

	serverConfig, err := store.GetServerConfig(ctx, server.ID)
	if err != nil {
		return true, fmt.Errorf("failed to get server configuration: %w", err)
	}
	if serverConfig == nil {
		return true, fmt.Errorf("server configuration is empty")
	}
	serverruntime.PrepareConfig(ctx, store, server, serverConfig)

	if err := publication.BeginDispatch(ctx); err != nil {
		return true, fmt.Errorf("failed to record staged publication dispatch: %w", err)
	}
	publication.RuntimeAttempted = true
	result, recreateErr := serverruntime.Recreate(ctx, store, dockerClient, server, serverConfig)
	if recreateErr != nil {
		var outcomeErr *serverruntime.RecreationError
		if errors.As(recreateErr, &outcomeErr) {
			if outcomeErr.Operation == nil {
				publication.RuntimeIdentityUnresolved = true
			}
			// A typed preflight ended before any container mutation; the old
			// container may still be running and that is not evidence about the
			// failed replacement. Record the flag before any identity write.
			publication.RuntimePreflight = outcomeErr.Preflight
		}
	}
	if result != nil && result.NewContainerID != "" && !publication.RuntimePreflight {
		if recordErr := publication.RecordReplacement(ctx, result.NewContainerID); recordErr != nil {
			return true, errors.Join(recordErr, fmt.Errorf("failed to record the replacement identity: %w", recordErr))
		}
	}
	if recreateErr != nil {
		if publication.RuntimeIdentityUnresolved {
			// Docker already runs the applied spec. Preserve the pending marker
			// for retry/reconciliation rather than roll back only the database.
			return true, fmt.Errorf("replacement container created but ID could not be saved: %w", recreateErr)
		}
		return true, fmt.Errorf("failed to recreate server with staged configuration: %w", recreateErr)
	}
	if startReplacement && !result.WasRunning {
		if err := serverruntime.Start(ctx, store, dockerClient, server); err != nil {
			return true, fmt.Errorf("failed to start recreated server: %w", err)
		}
	}
	// A reached-and-running replacement was positively observed. A stopped
	// replacement created for a deep-sleep wake is acknowledged by the wake
	// completion once its start is observed; a caller with no completion owner
	// accepts the created replacement directly.
	publication.RuntimeAccepted = startReplacement || result.WasRunning || transfer == nil
	if err := store.ClearEphemeralConfigFields(ctx, server.ID); err != nil {
		return true, fmt.Errorf("server restarted but temporary config could not be cleared: %w", err)
	}
	if !markStarted {
		return true, nil
	}
	server.Status = storage.StatusStarting
	now := time.Now()
	server.LastStarted = &now
	if err := serverruntime.PersistStarted(ctx, store, server); err != nil {
		return true, fmt.Errorf("server restarted but status could not be saved: %w", err)
	}
	return true, nil
}

// stagedWakeCompletion is the real DeepSleep wake completion callback. A
// successful recreate already mutated the runtime, so a later start failure
// must not erase that fact or the stopped replacement can outlive a config
// rollback.
func stagedWakeCompletion(ctx context.Context, store *storage.Store, p *staging.RestartPublication) docker.WakeCompletion {
	return func(startAttempted bool, wakeErr error) error {
		if startAttempted {
			p.RuntimeAttempted = true
		}
		if wakeErr == nil {
			// A nil wake error is a positive runtime observation: the
			// replacement contains the batch and was started (or was already
			// running). It may acknowledge the publication.
			p.RuntimeAccepted = true
		}
		return p.Resolve(ctx, store, wakeErr)
	}
}

// PrepareDeepSleepWake applies restart-only staged changes and replaces the
// stopped container before DeepSleepManager starts it. The manager records
// LastStarted only after Docker reports the replacement running.
func (s *Scheduler) PrepareDeepSleepWake(ctx context.Context, server *storage.Server) (docker.WakeCompletion, error) {
	if server == nil || server.ID == "" {
		return nil, fmt.Errorf("server ID is required")
	}
	if err := s.checkNodeMemoryCapacity(ctx, server); err != nil {
		return nil, err
	}
	dockerCli, err := s.dockerForNode(server.NodeID)
	if err != nil {
		return nil, err
	}
	var complete docker.WakeCompletion
	handled, err := prepareStagedLifecycle(ctx, s.store, dockerCli, s.log, server, false, false, s.recreateModule, func(p *staging.RestartPublication) {
		complete = stagedWakeCompletion(ctx, s.store, p)
	}, s.storageResolver)
	if err != nil {
		return nil, err
	}
	if !handled && dockerCli != nil {
		needsCreate := server.ContainerID == ""
		if !needsCreate {
			if _, statusErr := dockerCli.GetContainerStatus(ctx, server.ContainerID); statusErr != nil {
				needsCreate = true
			}
		}
		if needsCreate {
			if resolved, resolveErr := dockerCli.ResolveContainer(ctx, server.ID); resolveErr == nil && resolved != nil && resolved.ID != "" {
				server.ContainerID = resolved.ID
				if err := s.store.UpdateServerContainerID(ctx, server.ID, server.ContainerID); err != nil {
					return nil, fmt.Errorf("resolved container for deep-sleep wake but ID could not be saved: %w", err)
				}
			} else {
				serverConfig, cfgErr := s.store.GetServerConfig(ctx, server.ID)
				if cfgErr != nil || serverConfig == nil {
					return nil, fmt.Errorf("failed to get server configuration for deep-sleep wake container creation: %w", cfgErr)
				}
				containerID, createErr := dockerCli.CreateContainer(ctx, server, serverConfig)
				if createErr != nil {
					return nil, fmt.Errorf("failed to create missing container during deep-sleep wake: %w", createErr)
				}
				server.ContainerID = containerID
				if err := s.store.UpdateServerContainerID(ctx, server.ID, server.ContainerID); err != nil {
					return nil, fmt.Errorf("created container for deep-sleep wake but ID could not be saved: %w", err)
				}
			}
		}
	}
	return complete, nil
}

func (s *Scheduler) executeStopTask(ctx context.Context, server *storage.Server, _ *storage.ScheduledTask) (string, error) {
	if server.ContainerID == "" {
		return "", fmt.Errorf("server has no container")
	}
	transition, err := docker.TryBeginServerTransition(server.ID)
	if err != nil {
		return "", fmt.Errorf("stop refused while another lifecycle operation owns the server: %w", err)
	}
	defer transition.Release()
	if err := s.refuseActiveOperation(ctx, server.ID); err != nil {
		return "", err
	}

	dockerCli, err := s.dockerForNode(server.NodeID)
	if err != nil {
		return "", err
	}
	server.Status = storage.StatusStopping
	if err := s.store.UpdateServer(ctx, server); err != nil {
		return "", fmt.Errorf("failed to record stop: %w", err)
	}
	window := docker.DefaultStopWindow
	if cfg, cfgErr := s.store.GetServerConfig(ctx, server.ID); cfgErr == nil {
		window = docker.StopWindow(cfg)
	}
	found, err := dockerCli.StopContainerWithin(ctx, server.ContainerID, window)
	if err != nil {
		return "", fmt.Errorf("failed to stop: %w", err)
	}
	if !found {
		server.ContainerID = ""
	}
	server.Status = storage.StatusStopped
	if err := s.store.UpdateServer(ctx, server); err != nil {
		return "", fmt.Errorf("server stopped but status could not be saved: %w", err)
	}
	if !found {
		return "container not found, marked as stopped", nil
	}

	return "server stopped successfully", nil
}

// ScriptTaskConfig represents configuration for script tasks
type ScriptTaskConfig struct {
	ScriptPath string   `json:"script_path"`
	Args       []string `json:"args"`
}

func (s *Scheduler) executeScriptTask(ctx context.Context, server *storage.Server, task *storage.ScheduledTask) (string, error) {
	// Script tasks execute inside the container
	var config ScriptTaskConfig
	if task.Config != "" {
		if err := json.Unmarshal([]byte(task.Config), &config); err != nil {
			return "", fmt.Errorf("invalid config: %w", err)
		}
	}

	if config.ScriptPath == "" {
		return "", fmt.Errorf("no script/executable specified")
	}

	execCmd := []string{config.ScriptPath}
	dockerCli, err := s.dockerForNode(server.NodeID)
	if err != nil {
		return "", err
	}
	if dockerCli == nil {
		return "", fmt.Errorf("docker client unavailable for node %q", server.NodeID)
	}
	return dockerCli.Exec(ctx, server.ContainerID, append(execCmd, config.Args...))
}

func (s *Scheduler) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// cronExprHasExplicitTimezone mirrors the shared cron parser: an expression
// beginning with TZ= or CRON_TZ= carries its own location.
func cronExprHasExplicitTimezone(expr string) bool {
	return strings.HasPrefix(expr, "TZ=") || strings.HasPrefix(expr, "CRON_TZ=")
}

// CalculateNextRun calculates the next run time for a task based on its schedule
func (s *Scheduler) CalculateNextRun(task *storage.ScheduledTask) (*time.Time, error) {
	now := s.currentTime()

	switch task.Schedule {
	case storage.ScheduleTypeCron:
		if task.CronExpr == "" {
			return nil, fmt.Errorf("cron expression required")
		}
		// Timezone applies only to cron slots. An interval or one-shot task
		// must keep its absolute deadline even if a stale zone is configured.
		// An explicit TZ=/CRON_TZ= prefix belongs to the expression and must not
		// be overwritten by the task row's timezone.
		var location *time.Location
		if zone := strings.TrimSpace(task.Timezone); zone != "" && !cronExprHasExplicitTimezone(task.CronExpr) {
			location = time.UTC
			if !strings.EqualFold(zone, "UTC") {
				loaded, err := time.LoadLocation(zone)
				if err != nil {
					return nil, fmt.Errorf("invalid timezone %q: %w", task.Timezone, err)
				}
				location = loaded
			}
		}
		schedule, err := s.cronParser.Parse(task.CronExpr)
		if err != nil {
			return nil, fmt.Errorf("invalid cron expression: %w", err)
		}
		// The shared parser builds a spec schedule in the host zone. Rebind its
		// location so five-field slots are interpreted in the operator's zone.
		if location != nil {
			if spec, ok := schedule.(*cron.SpecSchedule); ok {
				zoneSpec := *spec
				zoneSpec.Location = location
				schedule = &zoneSpec
			}
		}
		next := schedule.Next(now)
		if next.IsZero() {
			// A parseable but impossible expression (for example 31 February)
			// has no future occurrence. Storing the zero value would make the
			// task look overdue forever, so treat it as a calculation failure.
			return nil, fmt.Errorf("cron expression has no future occurrence")
		}
		return &next, nil

	case storage.ScheduleTypeInterval:
		if task.IntervalSecs <= 0 {
			return nil, fmt.Errorf("interval must be positive")
		}
		next := now.Add(time.Duration(task.IntervalSecs) * time.Second)
		return &next, nil

	case storage.ScheduleTypeOnce:
		if task.RunAt == nil {
			return nil, fmt.Errorf("run_at time required for once schedule")
		}
		if task.RunAt.Before(now) {
			return nil, nil // Already passed
		}
		return task.RunAt, nil

	case storage.ScheduleTypeEvent:
		// No scheduled time; execution is triggered via OnEvent.
		return nil, nil

	default:
		return nil, fmt.Errorf("unknown schedule type: %s", task.Schedule)
	}
}

// ValidateCronExpr validates a cron expression
func (s *Scheduler) ValidateCronExpr(expr string) error {
	_, err := s.cronParser.Parse(expr)
	return err
}

func (s *Scheduler) executeWebhookTask(ctx context.Context, server *storage.Server, task *storage.ScheduledTask, eventType v1.TriggeredEventType, eventData map[string]any) (string, error) {
	var cfg webhook.Config
	if task.Config != "" {
		if err := json.Unmarshal([]byte(task.Config), &cfg); err != nil {
			return "", fmt.Errorf("invalid webhook config: %w", err)
		}
	}
	if cfg.URL == "" {
		return "", fmt.Errorf("webhook URL is required")
	}

	// Determine which event drove this run. Event-triggered runs pass the
	// firing event directly - otherwise fall back to the first subscribed event or "manual"
	var event string
	switch {
	case eventType != v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_UNSPECIFIED:
		event = webhookEventName(eventType)
	case len(task.EventTriggers) > 0:
		event = webhookEventName(task.EventTriggers[0])
	default:
		event = "manual"
	}

	// Pull live count from metrics so payloads report players accurately
	if s.metrics != nil {
		if m := s.metrics.GetMetrics(server.ID); m != nil {
			server.PlayersOnline = m.PlayersOnline
		}
	}

	payload := webhook.BuildPayload(event, server, eventData)

	result := webhook.Deliver(ctx, cfg, payload)
	output := fmt.Sprintf("HTTP %d in %dms (attempt %d)", result.ResponseCode, result.DurationMs, result.Attempts)
	if result.ResponseBody != "" {
		output += "\n" + result.ResponseBody
	}
	if result.Success {
		return output, nil
	}
	return output, fmt.Errorf("%s", result.ErrorMessage)
}

// Maps a server event type to the lowercase event name used in webhook payloads
func webhookEventName(t v1.TriggeredEventType) string {
	switch t {
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START:
		return "server_start"
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP:
		return "server_stop"
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_RESTART:
		return "server_restart"
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_HEALTHY:
		return "server_healthy"
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_JOIN:
		return "player_join"
	case v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_PLAYER_LEAVE:
		return "player_leave"
	default:
		return "manual"
	}
}

// ValidateModUpdateConfigJSON rejects a malformed policy before it is stored,
// so a misconfigured schedule fails at create/update time rather than hours
// later when the schedule first fires.
func ValidateModUpdateConfigJSON(raw string) error {
	_, err := ParseModUpdateConfigJSON(raw)
	return err
}

// ParseModUpdateConfigJSON returns the validated policy used to create a task.
func ParseModUpdateConfigJSON(raw string) (storage.ModUpdateTaskConfig, error) {
	var cfg storage.ModUpdateTaskConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg, fmt.Errorf("invalid mod update policy: %w", err)
	}
	return cfg, ValidateModUpdateConfig(cfg)
}

// ValidateModUpdateConfig checks a decoded policy at the execution boundary.
func ValidateModUpdateConfig(cfg storage.ModUpdateTaskConfig) error {
	if cfg.MaxUpdatesPerRun < 0 || cfg.MaxUpdatesPerRun > maxAutomaticModUpdatesPerRun {
		return fmt.Errorf("max_updates_per_run must be between 0 and %d", maxAutomaticModUpdatesPerRun)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Window)) {
	case "", "next_restart":
	case "idle":
		if cfg.IdleMinutes <= 0 || cfg.IdleMinutes > 10080 {
			return errors.New("idle_minutes must be between 1 and 10080 for the idle window")
		}
	default:
		return fmt.Errorf("unknown apply window %q", cfg.Window)
	}
	return nil
}

// executeModUpdateTask runs an opt-in mod update policy through the shared
// planner. It never publishes bytes itself: the existing journaled batch apply
// remains the only path that writes the live mods directory.
func (s *Scheduler) executeModUpdateTask(ctx context.Context, server *storage.Server, task *storage.ScheduledTask, budget *modUpdateRunBudget) (string, error) {
	var cfg storage.ModUpdateTaskConfig
	if task.Config != "" {
		if err := json.Unmarshal([]byte(task.Config), &cfg); err != nil {
			return "", fmt.Errorf("invalid mod update policy: %w", err)
		}
	}
	if !cfg.Enabled {
		return "mod update policy is disabled; nothing staged", nil
	}
	if err := ValidateModUpdateConfig(cfg); err != nil {
		return "", fmt.Errorf("invalid mod update policy: %w", err)
	}
	if cfg.SkipWhenRunning && server.Status == storage.StatusRunning {
		return "server is running; mod update policy skipped by running-server policy", nil
	}
	if s.modUpdatePlanner == nil {
		return "", errors.New("mod update planner is not installed")
	}
	limit := cfg.MaxUpdatesPerRun
	if limit == 0 {
		limit = DefaultModUpdatesPerRun
	}
	cfg.MaxUpdatesPerRun = limit - budget.staged
	staged, refused, reasons, err := s.modUpdatePlanner.StageAvailableUpdates(ctx, server.ID, cfg)
	budget.staged += staged
	budget.exhausted = budget.staged >= limit
	output := fmt.Sprintf("staged %d compatible update(s); %d refused and left untouched", budget.staged, refused)
	if len(reasons) > 0 {
		shown := reasons
		if len(shown) > 3 {
			shown = shown[:3]
		}
		output += "; reasons: " + strings.Join(shown, " | ")
	}
	return output, err
}

// ModpackUpdateTaskConfig matches the configuration for periodic modpack updates
type ModpackUpdateTaskConfig struct {
	GitURL                   string `json:"git_url"`
	Branch                   string `json:"branch"`
	TargetSubfolder          string `json:"target_subfolder"`
	RestartImmediately       bool   `json:"restart_immediately"`
	GracefulRestart          bool   `json:"graceful_restart"`
	GracefulCountdownSeconds int    `json:"graceful_countdown_seconds"`
	MaintenanceWindowCron    string `json:"maintenance_window_cron"`
	StageConfigUpdates       bool   `json:"stage_config_updates"`
	StageIntoBatch           bool   `json:"stage_into_batch"`
	PreUpdateSnapshot        bool   `json:"pre_update_snapshot"`      // Create volume snapshot before applying updates (MINE-23)
	AutoRollbackOnFailure    bool   `json:"auto_rollback_on_failure"` // Automatically rollback snapshot if restart/update fails
	BootGateTimeoutSecs      int    `json:"boot_gate_timeout_secs"`   // SLP boot verification after restart; 0 disables (MINE-145)
	AuthToken                string `json:"auth_token"`
}

// modpackPendingCommitFile records a fetched commit whose stage or sync has
// not completed. The clone cache is reset to FETCH_HEAD before the tree is
// staged, so HEAD already names the fetched commit when a later run starts.
// Without this marker that run would compare HEAD with FETCH_HEAD, report "no
// updates", and lose an update a refused stage never published.
//
// The live marker is a sibling of the clone cache, never inside it: a staging
// pass or legacy sync may read the whole cache as pack content.
const modpackPendingCommitFile = ".carbon-panel_pending_update"

// removeModpackPendingCommit is replaceable in tests to prove a clear failure
// is retried without staging a duplicate modpack row.
var removeModpackPendingCommit = os.Remove

func modpackPendingCommitPath(cacheDir string) string {
	return cacheDir + modpackPendingCommitFile
}

func legacyModpackPendingCommitPath(cacheDir string) string {
	return filepath.Join(cacheDir, modpackPendingCommitFile)
}

func readModpackPendingCommit(cacheDir string) string {
	for _, name := range []string{modpackPendingCommitPath(cacheDir), legacyModpackPendingCommitPath(cacheDir)} {
		body, err := os.ReadFile(name)
		if err == nil {
			return strings.TrimSpace(string(body))
		}
	}
	return ""
}

// writeModpackPendingCommit records the fetched commit before the cache is
// reset or staged. A write failure must stop the update rather than let the
// fetched commit become invisible on the next run. The path is outside the
// clone cache, so it can also be written before an initial clone starts.
func writeModpackPendingCommit(cacheDir, commit string) error {
	if commit == "" {
		return nil
	}
	return os.WriteFile(modpackPendingCommitPath(cacheDir), []byte(commit+"\n"), 0o644)
}

// clearModpackPendingCommit removes both the current marker and a marker left
// inside the clone cache by an older version. A failure is returned, not
// ignored, so the next run can retry clearing and staging idempotently.
func clearModpackPendingCommit(cacheDir string) error {
	var failed error
	for _, name := range []string{modpackPendingCommitPath(cacheDir), legacyModpackPendingCommitPath(cacheDir)} {
		if err := removeModpackPendingCommit(name); err != nil && !os.IsNotExist(err) {
			failed = errors.Join(failed, err)
		}
	}
	return failed
}

// modpackStagedInOpenBatch reports whether this exact fetched commit already
// has a staged modpack row. It makes a failed marker clear safe to retry: the
// next run finishes the clear instead of staging the same commit twice.
func (s *Scheduler) modpackStagedInOpenBatch(ctx context.Context, serverID, label string) (bool, error) {
	batch, err := s.store.OpenUpdateBatch(ctx, serverID)
	if err != nil {
		return false, err
	}
	if batch == nil {
		return false, nil
	}
	rows, err := s.store.ListBatchContent(ctx, batch.ID)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.Kind == storage.StagedKindModpack && row.FileName == label {
			return true, nil
		}
	}
	return false, nil
}

// executeModpackUpdateTask handles cloning or fetching updates from Git/source, syncing into the server directory, and restarting if updates are applied
func (s *Scheduler) executeModpackUpdateTask(ctx context.Context, server *storage.Server, task *storage.ScheduledTask) (string, error) {
	var cfg ModpackUpdateTaskConfig
	if task.Config != "" {
		if err := json.Unmarshal([]byte(task.Config), &cfg); err != nil {
			return "", fmt.Errorf("invalid modpack update config: %w", err)
		}
	}

	if cfg.GitURL == "" {
		return "", fmt.Errorf("git_url is required for modpack update task")
	}

	branch := cfg.Branch
	if branch == "" {
		branch = "main"
	}

	// Re-validate defensively at execution time, not only at task
	// create/update time: a task row written before this fix, or by any other
	// writer, must not be able to smuggle a hostile branch or URL into exec.
	if err := validateGitBranch(branch); err != nil {
		return "", err
	}
	if err := validateGitURL(cfg.GitURL); err != nil {
		return "", err
	}

	// The clone cache and the synced world live on the node's storage. A remote
	// node with no reachable mount is refused before a controller directory is
	// created or copied into.
	worldRoot, err := s.serverWorldRoot(ctx, server)
	if err != nil {
		return "", err
	}

	cacheDir := filepath.Join(worldRoot, ".carbon-panel_modpack_git")
	gitURL := cfg.GitURL
	if cfg.AuthToken != "" && strings.HasPrefix(gitURL, "https://") {
		// Embed auth token into clone URL
		gitURL = strings.Replace(gitURL, "https://", fmt.Sprintf("https://oauth2:%s@", cfg.AuthToken), 1)
	}

	var hasUpdates bool
	var updateSummary string
	var currentHash string
	var remoteHash string
	var changedFiles []string

	if _, err := os.Stat(filepath.Join(cacheDir, ".git")); os.IsNotExist(err) {
		// Initial clone
		_ = os.MkdirAll(cacheDir, 0755)
		// Record that a clone is pending before it starts. A crash after the
		// clone but before its commit is read must still be retried instead of
		// leaving HEAD at the fetched commit with no marker.
		if err := writeModpackPendingCommit(cacheDir, "initial-clone"); err != nil {
			return "", fmt.Errorf("record the pending initial modpack clone for retry: %w", err)
		}
		s.log.Info("ModpackTask %s: Performing initial clone from %s (branch: %s)", task.Name, cfg.GitURL, branch)
		if out, err := cloneFn(ctx, gitURL, branch, cacheDir); err != nil {
			return string(out), fmt.Errorf("initial git clone failed: %w: %s", err, string(out))
		}
		cmdHead := exec.CommandContext(ctx, "git", "-C", cacheDir, "rev-parse", "HEAD")
		if headOut, err := cmdHead.Output(); err == nil {
			remoteHash = strings.TrimSpace(string(headOut))
		}
		hasUpdates = true
		updateSummary = fmt.Sprintf("Initial modpack clone successful from %s on branch %s", cfg.GitURL, branch)
		if err := writeModpackPendingCommit(cacheDir, remoteHash); err != nil {
			return "", fmt.Errorf("record the fetched modpack commit for retry: %w", err)
		}
	} else {
		// Fetch and check if updates are available
		if out, err := fetchFn(ctx, cacheDir, branch); err != nil {
			return string(out), fmt.Errorf("git fetch failed: %w: %s", err, string(out))
		}

		pendingHash := readModpackPendingCommit(cacheDir)
		cmdHead := exec.CommandContext(ctx, "git", "-C", cacheDir, "rev-parse", "HEAD")
		headOut, _ := cmdHead.Output()
		currentHash = strings.TrimSpace(string(headOut))

		cmdRemote := exec.CommandContext(ctx, "git", "-C", cacheDir, "rev-parse", "FETCH_HEAD")
		remoteOut, _ := cmdRemote.Output()
		remoteHash = strings.TrimSpace(string(remoteOut))

		// A pending marker means an earlier run fetched an update but its stage
		// or sync did not complete. Treat it as an update even though the
		// earlier reset already moved HEAD to the fetched commit.
		retryingFetched := pendingHash != ""
		if currentHash == remoteHash && currentHash != "" && !retryingFetched {
			return fmt.Sprintf("Modpack is up to date at commit %s (no updates)", currentHash[:min(8, len(currentHash))]), nil
		}

		// Inspect changed files between current commit and new FETCH_HEAD
		if currentHash != "" && remoteHash != "" && currentHash != remoteHash {
			cmdDiff := exec.CommandContext(ctx, "git", "-C", cacheDir, "diff", "--name-only", currentHash, remoteHash)
			if diffOut, err := cmdDiff.Output(); err == nil {
				for _, f := range strings.Split(string(diffOut), "\n") {
					f = strings.TrimSpace(f)
					if f != "" {
						changedFiles = append(changedFiles, f)
					}
				}
			}
		}

		// Persist the fetched commit before resetting HEAD. If the stage below
		// is refused, the next run must still see this commit as unpublished.
		if err := writeModpackPendingCommit(cacheDir, remoteHash); err != nil {
			return "", fmt.Errorf("record the fetched modpack commit for retry: %w", err)
		}

		// Pull latest changes
		cmdReset := exec.CommandContext(ctx, "git", "-C", cacheDir, "reset", "--hard", "FETCH_HEAD")
		if out, err := cmdReset.CombinedOutput(); err != nil {
			return string(out), fmt.Errorf("git reset to FETCH_HEAD failed: %w: %s", err, string(out))
		}

		hasUpdates = true
		if retryingFetched && currentHash == remoteHash {
			updateSummary = fmt.Sprintf("Retrying previously fetched modpack commit %s", remoteHash[:min(8, len(remoteHash))])
		} else {
			updateSummary = fmt.Sprintf("Updated modpack from commit %s to %s",
				currentHash[:min(8, len(currentHash))],
				remoteHash[:min(8, len(remoteHash))])
		}
	}

	if hasUpdates {
		if cfg.StageIntoBatch {
			srcDir, err := resolveModpackSourceDir(cacheDir, cfg.TargetSubfolder)
			if err != nil {
				return updateSummary, err
			}
			label := "modpack"
			if remoteHash != "" {
				label = "modpack " + remoteHash[:min(8, len(remoteHash))]
			}
			// A previous attempt may have staged this exact fetched commit but
			// failed to clear its marker. Do not append a second modpack row for
			// one commit; finish the clear and report the existing stage.
			alreadyStaged, err := s.modpackStagedInOpenBatch(ctx, server.ID, label)
			if err != nil {
				return updateSummary, err
			}
			if !alreadyStaged {
				if _, err := staging.StageModpackTree(ctx, s.store, server, srcDir, label, s.storageResolver); err != nil {
					return updateSummary, fmt.Errorf("stage modpack into batch: %w", err)
				}
			}
			if err := clearModpackPendingCommit(cacheDir); err != nil {
				return updateSummary + "; staged into the open batch", fmt.Errorf("staged modpack but could not clear the pending update marker; retry to clear it: %w", err)
			}
			return updateSummary + "; staged into the open batch", nil
		}

		var createdSnapshot *storage.ServerSnapshot

		// One-click pre-update volume snapshotting (MINE-23)
		if s.snapshotEngine != nil {
			snap, snapErr := s.snapshotEngine.CreatePreUpdateSnapshot(ctx, server, fmt.Sprintf("pre-update: %s", updateSummary))
			if snapErr != nil {
				s.log.Warn("ModpackTask %s: Failed to create pre-update volume snapshot: %v", task.Name, snapErr)
			} else {
				createdSnapshot = snap
				s.log.Info("ModpackTask %s: Created pre-update snapshot %s (%s)", task.Name, snap.ID, snap.Name)
			}
		}

		// Stage configuration updates if requested
		if cfg.StageConfigUpdates {
			stagedDir := filepath.Join(worldRoot, ".carbon-panel_modpack_staged")
			_ = os.MkdirAll(stagedDir, 0755)
			manifest := map[string]any{
				"from_commit":   currentHash,
				"to_commit":     remoteHash,
				"changed_files": changedFiles,
				"staged_at":     time.Now().Format(time.RFC3339),
				"status":        "staged",
			}
			if manifestData, err := json.MarshalIndent(manifest, "", "  "); err == nil {
				_ = os.WriteFile(filepath.Join(stagedDir, "staged_manifest.json"), manifestData, 0644)
			}
			s.log.Info("ModpackTask %s: Staged %d changed config/pack files in %s", task.Name, len(changedFiles), stagedDir)
		}

		// Source directory to copy from, confined to the clone cache so a
		// subfolder like "../../etc" cannot make the sync below read host
		// files into the server directory or overwrite server data.
		srcDir, err := resolveModpackSourceDir(cacheDir, cfg.TargetSubfolder)
		if err != nil {
			return updateSummary, err
		}

		// Sync files into the world on the node's storage (excluding internal
		// directories). The copy holds the lifecycle transition and releases it
		// before a restart, which claims that transition itself.
		if out, syncErr := s.syncModpackFiles(ctx, server, srcDir, worldRoot); syncErr != nil {
			if cfg.AutoRollbackOnFailure && createdSnapshot != nil && s.snapshotEngine != nil {
				_ = s.snapshotEngine.Rollback(ctx, server, createdSnapshot.ID)
			}
			return out, syncErr
		}
		if err := clearModpackPendingCommit(cacheDir); err != nil {
			return fmt.Sprintf("%s\nModpack synced successfully, but the pending update marker could not be cleared; retry to clear it: %v", updateSummary, err), err
		}

		// Check if maintenance window or immediate restart is triggered
		shouldRestartNow := cfg.RestartImmediately
		if !shouldRestartNow && cfg.MaintenanceWindowCron != "" {
			if s.isWithinMaintenanceWindow(cfg.MaintenanceWindowCron, time.Now()) {
				s.log.Info("ModpackTask %s: Current time is within maintenance window (%s), proceeding with restart",
					task.Name, cfg.MaintenanceWindowCron)
				shouldRestartNow = true
			} else {
				s.log.Info("ModpackTask %s: Outside maintenance window (%s), restart deferred",
					task.Name, cfg.MaintenanceWindowCron)
			}
		}

		if shouldRestartNow {
			countdown := cfg.GracefulCountdownSeconds
			if countdown <= 0 && cfg.GracefulRestart {
				countdown = 10
			}

			if cfg.GracefulRestart || countdown > 0 {
				s.log.Info("ModpackTask %s: Executing graceful restart (countdown: %ds) for server %s", task.Name, countdown, server.Name)
				restartOut, err := s.executeGracefulRestart(ctx, server, task, countdown)
				if err != nil {
					if cfg.AutoRollbackOnFailure && createdSnapshot != nil && s.snapshotEngine != nil {
						s.log.Warn("ModpackTask %s: Restart failed, rolling back to pre-update snapshot %s", task.Name, createdSnapshot.ID)
						_ = s.snapshotEngine.Rollback(ctx, server, createdSnapshot.ID)
					}
					return fmt.Sprintf("%s\nModpack synced, but graceful restart failed: %v", updateSummary, err), err
				}
				if bootErr := s.verifyBootAfterUpdate(ctx, server, task, cfg, createdSnapshot); bootErr != nil {
					return fmt.Sprintf("%s\n%s", updateSummary, restartOut), bootErr
				}
				return fmt.Sprintf("%s\n%s", updateSummary, restartOut), nil
			}

			s.log.Info("ModpackTask %s: Restarting server %s immediately", task.Name, server.Name)
			restartOut, err := s.executeRestartTask(ctx, server, task)
			if err != nil {
				if cfg.AutoRollbackOnFailure && createdSnapshot != nil && s.snapshotEngine != nil {
					s.log.Warn("ModpackTask %s: Restart failed, rolling back to pre-update snapshot %s", task.Name, createdSnapshot.ID)
					_ = s.snapshotEngine.Rollback(ctx, server, createdSnapshot.ID)
				}
				return fmt.Sprintf("%s\nModpack synced, but server restart failed: %v", updateSummary, err), err
			}
			if bootErr := s.verifyBootAfterUpdate(ctx, server, task, cfg, createdSnapshot); bootErr != nil {
				return fmt.Sprintf("%s\n%s", updateSummary, restartOut), bootErr
			}
			return fmt.Sprintf("%s\n%s", updateSummary, restartOut), nil
		} else {
			return fmt.Sprintf("%s\nModpack synced successfully (server restart deferred to scheduled maintenance)", updateSummary), nil
		}
	}

	return "No updates found", nil
}

// serverWorldRoot is the controller-side directory for a server's files.
// Local nodes keep DataPath. A remote node uses its shared-storage mount, and
// a remote node with no reachable storage is refused.
func (s *Scheduler) serverWorldRoot(ctx context.Context, server *storage.Server) (string, error) {
	if server == nil || server.DataPath == "" {
		return "", fmt.Errorf("server has no data directory")
	}
	if nodestorage.IsLegacyLocalNodeID(server.NodeID) {
		return server.DataPath, nil
	}
	if s.store == nil {
		return "", fmt.Errorf("store is required to resolve node storage")
	}
	node, err := s.store.GetNode(ctx, server.NodeID)
	if err != nil {
		return "", fmt.Errorf("resolve storage node %q: %w", server.NodeID, err)
	}
	return nodestorage.EnsureDataRoot(node.StorageInfo(), server.DataPath, s.storageResolver)
}

// syncModpackFiles copies a modpack tree into the world root on the node's
// storage. It holds the lifecycle transition for the copy and releases it
// before returning, so a following restart can claim that transition itself.
func (s *Scheduler) syncModpackFiles(ctx context.Context, server *storage.Server, srcDir, destRoot string) (string, error) {
	transition, err := docker.TryBeginServerTransition(server.ID)
	if err != nil {
		return "", fmt.Errorf("modpack sync refused while another lifecycle operation owns the server: %w", err)
	}
	defer transition.Release()

	write, err := datawrite.TryShared(server.ID)
	if err != nil {
		return "", fmt.Errorf("modpack sync refused while staged publication owns the data path: %w", err)
	}
	defer write.Release()
	if s.log != nil {
		s.log.Info("Syncing updated modpack files from %s to %s", srcDir, destRoot)
	}
	excludes := []string{".git", ".carbon-panel_modpack_git", ".carbon-panel_modpack_staged", ".carbon-panel_snapshots", modpackPendingCommitFile}
	args := []string{"-avc"}
	for _, exclude := range excludes {
		args = append(args, "--exclude="+exclude)
	}
	args = append(args, srcDir+"/", destRoot+"/")
	cmdRsync := exec.CommandContext(ctx, "rsync", args...)
	if out, err := cmdRsync.CombinedOutput(); err != nil {
		// rsync is not installed on every supported host. The Go fallback
		// applies the same exclusions, including a legacy marker inside the
		// clone cache, so controller state never becomes live pack content.
		if goErr := copyDirExcluding(srcDir, destRoot, excludes); goErr != nil {
			return string(out), fmt.Errorf("failed to copy modpack files: %w", goErr)
		}
	}
	return "", nil
}

// isWithinMaintenanceWindow checks if the given time falls within a 30-minute window of the cron schedule
func (s *Scheduler) isWithinMaintenanceWindow(cronExpr string, now time.Time) bool {
	sched, err := s.cronParser.Parse(cronExpr)
	if err != nil {
		s.log.Warn("Invalid maintenance window cron expression: %s (%v)", cronExpr, err)
		return false
	}
	prev := sched.Next(now.Add(-30 * time.Minute))
	return !prev.After(now)
}

// executeGracefulRestart notifies in-game players, saves world data, and restarts the server
func (s *Scheduler) executeGracefulRestart(ctx context.Context, server *storage.Server, task *storage.ScheduledTask, countdownSeconds int) (string, error) {
	if countdownSeconds < 1 {
		countdownSeconds = 1
	}

	if server.Status == storage.StatusRunning && server.ContainerID != "" && s.sender != nil {
		_, _ = s.sender.SendCommand(ctx, server.ID, fmt.Sprintf("say [Auto-Update] Modpack update installed. Server restarting in %d seconds for maintenance...", countdownSeconds))

		remaining := countdownSeconds
		for remaining > 0 {
			if remaining == 10 || remaining == 5 || remaining == 3 || remaining == 1 {
				_, _ = s.sender.SendCommand(ctx, server.ID, fmt.Sprintf("say [Auto-Update] Restarting in %d seconds...", remaining))
			}
			if remaining == 5 {
				_, _ = s.sender.SendCommand(ctx, server.ID, "save-all")
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(1 * time.Second):
				remaining--
			}
		}
	}

	return s.executeRestartTask(ctx, server, task)
}

// ChunkyPregenTaskConfig represents configuration for Chunky radius pre-generation tasks
type ChunkyPregenTaskConfig struct {
	World   string `json:"world,omitempty"`
	Radius  int    `json:"radius,omitempty"`
	Shape   string `json:"shape,omitempty"`
	CenterX int    `json:"center_x,omitempty"`
	CenterZ int    `json:"center_z,omitempty"`
}

// executeChunkyPregen runs an automated background Chunky radius pregeneration sequence via RCON
func (s *Scheduler) executeChunkyPregen(ctx context.Context, server *storage.Server, task *storage.ScheduledTask) (string, error) {
	if server.ContainerID == "" {
		return "", fmt.Errorf("server has no container")
	}

	var cfg ChunkyPregenTaskConfig
	if task.Config != "" {
		_ = json.Unmarshal([]byte(task.Config), &cfg)
	}

	world := cfg.World
	if world == "" {
		world = "world"
	}
	radius := cfg.Radius
	if radius <= 0 {
		radius = 2500
	}
	shape := cfg.Shape
	if shape == "" {
		shape = "circle"
	}

	commands := []string{
		fmt.Sprintf("chunky world %s", world),
		fmt.Sprintf("chunky shape %s", shape),
		fmt.Sprintf("chunky center %d %d", cfg.CenterX, cfg.CenterZ),
		fmt.Sprintf("chunky radius %d", radius),
		"chunky start",
	}

	var results []string
	for _, cmd := range commands {
		out, err := s.sender.SendCommand(ctx, server.ID, cmd)
		if err != nil {
			return strings.Join(results, "\n"), fmt.Errorf("chunky command '%s' failed: %w", cmd, err)
		}
		results = append(results, fmt.Sprintf("> %s: %s", cmd, strings.TrimSpace(out)))
	}

	return strings.Join(results, "\n"), nil
}

func copyDirExcluding(src, dst string, excludes []string) error {
	excludeMap := make(map[string]bool, len(excludes))
	for _, e := range excludes {
		excludeMap[e] = true
	}
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		parts := strings.Split(rel, string(filepath.Separator))
		if len(parts) > 0 && excludeMap[parts[0]] {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		targetPath := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		return copyFileHelper(path, targetPath, info.Mode())
	})
}

func copyFileHelper(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	sf, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sf.Close()

	df, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer df.Close()

	_, err = io.Copy(df, sf)
	return err
}
