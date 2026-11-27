// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package reconciler implements the level-triggered, self-healing reconciliation
// engine for Carbon Panel (MINE-107, Layer 2.2 of the Resilient Container
// Orchestrator epic, MINE-102).
//
// The reconciler converges the desired state recorded in the database with the
// observed state of the Docker containers backing each server. It is
// level-triggered rather than edge-triggered: lifecycle events from the MINE-106
// Watcher/Supervisor only schedule a per-server convergence pass (coalesced into
// a quiet window), while a periodic sweep guarantees that a dropped event can
// never leave a server permanently divergent.
//
// Identity comes from MINE-103's deterministic ResolveContainer, so a container
// recreated by Watchtower, docker-compose, Portainer, or force-recreate is
// re-adopted rather than mistaken for a loss.
package reconciler

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/events"
	"github.com/athNdev/carbon-panel/internal/serverruntime"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// Default tuning for the reconciler.
const (
	// DefaultDebounceWindow coalesces bursts of Docker events for the same
	// server into a single convergence pass. It is the dominant contributor to
	// the "external docker stop/kill is reflected within 500ms" budget.
	DefaultDebounceWindow = 300 * time.Millisecond
	// DefaultWorkers is the number of concurrent convergence goroutines.
	DefaultWorkers = 4
	// DefaultSweepInterval is the safety-net period at which every server is
	// re-checked even in the absence of events.
	DefaultSweepInterval = 30 * time.Second
	// workQueueDepth bounds the number of distinct servers awaiting a pass. The
	// queue only ever holds de-duplicated server IDs, so it cannot grow with
	// event volume.
	workQueueDepth = 256
)

// Container exit codes we give special meaning to.
const (
	// exitCodeSIGTERM is 128 + SIGTERM, reported for a graceful `docker stop`.
	exitCodeSIGTERM = 143
)

// Restart-loop detection tuning. A container that dies abnormally three
// times inside the window is a crash loop (Docker's restart policy masks
// each individual death behind "restarting"), not a one-off crash. The
// tally is forgiven once a container boots and stays up, or exits cleanly.
const (
	// restartLoopThreshold is how many consecutive abnormal terminations
	// mark a crash loop.
	restartLoopThreshold = 3
	// restartLoopWindow bounds how far back abnormal terminations are
	// counted; exits older than this no longer count as consecutive.
	restartLoopWindow = 10 * time.Minute
	// restartLoopWindowLabel renders restartLoopWindow in the persisted
	// reason. Kept by hand in sync with restartLoopWindow.
	restartLoopWindowLabel = "10m"
	// restartLoopStableRuntime is how long a container must stay up before
	// its crash-loop tally is forgiven: a server that boots and keeps
	// running is no longer looping.
	restartLoopStableRuntime = 5 * time.Minute
)

// transitionWaitTimeout bounds how long a blocked convergence pass watches
// for the in-flight lifecycle operation to end. If the transition outlives
// the wait, the next Docker event or the periodic sweep covers it.
const transitionWaitTimeout = 2 * time.Minute

// activeReconciler is the reconciler instance NotifyTransitionEnded routes
// to. Start registers it and Stop releases it, so the pointer never
// outlives a running event loop. The last Start wins: a panel process runs
// one reconciler.
var activeReconciler atomic.Pointer[Reconciler]

// Store is the narrow database surface the Reconciler needs. *db.Store
// satisfies it.
//
// Every write it uses is a targeted column update (MINE-110): the reconciler
// never calls the full-row UpdateServer, which would clobber a ContainerID
// written concurrently by another subsystem.
type Store interface {
	GetServer(ctx context.Context, id string) (*db.Server, error)
	ListServers(ctx context.Context) ([]*db.Server, error)
	GetServerConfig(ctx context.Context, serverID string) (*db.ServerConfig, error)
	UpdateServerStatus(ctx context.Context, id string, status db.ServerStatus) error
	UpdateServerStatusReason(ctx context.Context, id string, reason string) error
	UpdateServerContainerID(ctx context.Context, id string, containerID string) error
}

// OperationJournal is the optional lifecycle-operation surface of the store;
// *db.Store implements it. When available, the reconciler consults the
// journal before suppressing a stopping→stopped transition, so a stop whose
// RPC died before announcing is not swallowed.
type OperationJournal interface {
	// AssertNoServerOperation returns nil when no lifecycle operation still
	// owns the server, and an error when one does.
	AssertNoServerOperation(ctx context.Context, serverID string) error
}

// Client is the slice of *docker.Client the Reconciler drives.
type Client interface {
	ResolveContainer(ctx context.Context, serverID string) (*container.Summary, error)
	ObserveContainer(ctx context.Context, containerID string) (*docker.ContainerState, error)
	TailContainerLogs(ctx context.Context, containerID string, tailLines int) ([]string, error)
	StartContainer(ctx context.Context, containerID string) error
	RecreateContainer(ctx context.Context, oldContainerID string, server *db.Server, serverConfig *db.ServerConfig) (*docker.RecreateContainerResult, error)
	StopContainerWithin(ctx context.Context, containerID string, window time.Duration) (bool, error)
}

// ClientResolver resolves a node's Docker client without falling back to the
// local daemon (MINE-109's GetClientStrict). Returning an explicit error lets
// the reconciler distinguish "node unreachable" (leave the DB alone) from
// "container missing" (converge).
type ClientResolver interface {
	GetClientStrict(nodeID string) (Client, error)
}

// ModuleReconciler converges one module container with its record. The module
// manager implements it; the reconciler only schedules the pass, so a module
// keeps its own templates, hooks and dependency rules.
type ModuleReconciler interface {
	ReconcileModule(ctx context.Context, moduleID string) (string, error)
}

// ModuleStore is the module listing the sweep needs. It is optional: a store
// that does not implement it simply skips the module sweep.
type ModuleStore interface {
	ListModules(ctx context.Context) ([]*db.Module, error)
}

// ProxyRouter re-points a server's proxy route at its current backend.
type ProxyRouter interface {
	UpdateServerRoute(server *db.Server) error
}

// EventEmitter publishes lifecycle events on the central bus.
type EventEmitter interface {
	Emit(ctx context.Context, event events.Event)
}

// LogMigrator keeps a server's live console subscriptions attached to its
// container across a recreation (MINE-108): it moves the log streamer's
// subscriber registry and re-points the WebSocket hub's bookkeeping. It is
// optional — a nil LogMigrator simply disables the migration.
type LogMigrator interface {
	MigrateServerLogSubscriptions(serverID, oldContainerID, newContainerID string)
}

// PoolResolver adapts a *docker.ClientPool to the ClientResolver interface used
// by the Reconciler. Go method signatures are invariant, so the pool's concrete
// (*docker.Client, error) cannot satisfy (Client, error) on its own.
type PoolResolver struct {
	Pool *docker.ClientPool
}

// GetClientStrict implements ClientResolver.
func (p PoolResolver) GetClientStrict(nodeID string) (Client, error) {
	if p.Pool == nil {
		return nil, fmt.Errorf("client pool is nil")
	}
	return p.Pool.GetClientStrict(nodeID)
}

// Config tunes the reconciler. Zero-valued fields fall back to the defaults.
type Config struct {
	// DebounceWindow is the per-server quiet window that coalesces bursts of
	// Docker events into a single convergence pass.
	DebounceWindow time.Duration
	// Workers is the number of concurrent convergence goroutines.
	Workers int
	// SweepInterval is the safety-net period at which every server is
	// re-checked even without an event.
	SweepInterval time.Duration
	// DisableSelfHeal turns off automatic recreation of a missing container for
	// a server the database still believes is running. Self-heal is ON by
	// default; when disabled the divergence is only reported as an error.
	DisableSelfHeal bool
}

// Reconciler converges desired DB state with observed Docker state.
type Reconciler struct {
	store       Store
	clients     ClientResolver
	proxy       ProxyRouter
	bus         EventEmitter
	logMigrator LogMigrator
	log         *logger.Logger

	cfg Config
	deb *debouncer

	// admission is the shared in-process start gate. Self-heal starts a
	// recreated container, so it contends with ordinary starts on that node.
	// A default instance keeps standalone tests serialised; the composition
	// root replaces it with the shared coordinator.
	admission *serverruntime.AdmissionCoordinator

	// modules converges module containers. It is nil when the panel runs
	// without the module system, which disables the module pass entirely.
	modules ModuleReconciler

	work        chan string
	queued      map[string]bool
	moduleWork  chan string
	moduleQueue map[string]bool
	// backends remembers the last container ID + IP pushed to the proxy per
	// server, so route updates are driven by real backend moves rather than
	// being issued on every pass.
	backends map[string]backendRef
	mu       sync.Mutex

	// loops tracks consecutive abnormal container terminations per server
	// (restart-loop detection). Fed by Docker die events and by observed
	// container state; consumed and reset by reconciliation passes.
	loops *restartTracker
	// transitionWaits de-duplicates the bounded waiters that converge a
	// server as soon as its lifecycle transition ends (one per server).
	transitionWaits sync.Map
	// pendingStops remembers suppressed stopping→stopped transitions whose
	// SERVER_STOP announcement is re-emitted if the initiating RPC never
	// finalizes (see suppressStopAnnouncement / flushPendingStops).
	pendingStops map[string]pendingStop
	// retries marks servers whose convergence pass failed since their last
	// successful pass. The first failure of an episode releases the lifecycle
	// transition normally so the transition-end hook retries immediately;
	// later failures release quietly and fall back to the periodic sweep, so a
	// persistent fault cannot spin the reconciler.
	retries map[string]bool

	ctx      context.Context
	wg       sync.WaitGroup
	stop     chan struct{}
	stopOnce sync.Once
	started  bool

	// lastSweep is the Unix-nano time the last full sweep finished observing
	// every server. It is the only honest answer to "how old are the stored
	// statuses?", because a row is not rewritten when its status is unchanged.
	lastSweep atomic.Int64
}

// New constructs a Reconciler. It does not start any goroutines until Start is
// called. logMigrator may be nil, which disables console-subscription migration.
func New(store Store, clients ClientResolver, proxy ProxyRouter, bus EventEmitter, logMigrator LogMigrator, log *logger.Logger, cfg Config) *Reconciler {
	if cfg.DebounceWindow <= 0 {
		cfg.DebounceWindow = DefaultDebounceWindow
	}
	if cfg.Workers <= 0 {
		cfg.Workers = DefaultWorkers
	}
	if cfg.SweepInterval <= 0 {
		cfg.SweepInterval = DefaultSweepInterval
	}

	return &Reconciler{
		store:        store,
		clients:      clients,
		proxy:        proxy,
		bus:          bus,
		logMigrator:  logMigrator,
		log:          log,
		cfg:          cfg,
		deb:          newDebouncer(cfg.DebounceWindow),
		admission:    serverruntime.NewAdmissionCoordinator(),
		work:         make(chan string, workQueueDepth),
		queued:       make(map[string]bool),
		moduleWork:   make(chan string, workQueueDepth),
		moduleQueue:  make(map[string]bool),
		backends:     make(map[string]backendRef),
		loops:        newRestartTracker(),
		pendingStops: make(map[string]pendingStop),
		retries:      make(map[string]bool),
		stop:         make(chan struct{}),
	}
}

// SetAdmissionCoordinator supplies the shared in-process start gate. The
// composition root calls this before Start so self-heal contends with RPC and
// scheduler starts on the same node.
func (r *Reconciler) SetAdmissionCoordinator(c *serverruntime.AdmissionCoordinator) {
	if c != nil {
		r.admission = c
	}
}

// SetModuleReconciler enables convergence of module containers. It must be
// called before Start.
func (r *Reconciler) SetModuleReconciler(modules ModuleReconciler) {
	if r == nil {
		return
	}
	r.modules = modules
}

// Start launches the worker pool, consumes the supervisor's merged event stream
// (which may be nil), and begins the periodic sweep. It returns immediately;
// cancel ctx or call Stop to shut everything down.
//
// Start also registers this instance as the target of the package-level
// NotifyTransitionEnded hook, which lifecycle code uses to request an
// immediate convergence pass when a server's transition ends.
func (r *Reconciler) Start(ctx context.Context, eventStream <-chan ContainerEvent) {
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return
	}
	r.started = true
	r.ctx = ctx
	activeReconciler.Store(r)
	r.mu.Unlock()

	for i := 0; i < r.cfg.Workers; i++ {
		r.wg.Add(1)
		go r.worker()
	}

	if eventStream != nil {
		r.wg.Add(1)
		go r.consume(eventStream)
	}

	if r.modules != nil {
		r.wg.Add(1)
		go r.moduleWorker()
	}

	r.wg.Add(1)
	go r.sweepLoop()

	// Converge once at startup so drift accumulated while the panel was down is
	// repaired immediately rather than at the first event or sweep.
	r.enqueueAll(ctx)
}

// Stop halts the reconciler. It is safe to call more than once.
func (r *Reconciler) Stop() {
	r.stopOnce.Do(func() {
		activeReconciler.CompareAndSwap(r, nil)
		r.deb.stop()
		close(r.stop)
	})
}

// NotifyTransitionEnded is the hook lifecycle code calls when a server's
// lifecycle transition (start, stop, freeze, deep sleep, recreate) is
// released. It schedules an immediate convergence pass for the server so the
// transition's outcome is reconciled right away instead of at the next
// Docker event or the 30s sweep.
//
// internal/docker cannot import this package (import cycle), so the
// composition root bridges the two sides: hand this function to the docker
// package's transition-end notifier. A convergence pass that hits a held
// transition also arms its own bounded waiter
// (convergeWhenTransitionEnds), which converges without any wiring.
func NotifyTransitionEnded(serverID string) {
	if serverID == "" {
		return
	}
	if r := activeReconciler.Load(); r != nil {
		r.Nudge(serverID)
	}
}

// Wait blocks until every reconciler goroutine has exited.
func (r *Reconciler) Wait() {
	r.wg.Wait()
}

// LastSweepAt reports when the last full sweep queued every server for an
// observation. A zero value means no sweep has completed since start.
func (r *Reconciler) LastSweepAt() time.Time {
	nanos := r.lastSweep.Load()
	if nanos == 0 {
		return time.Time{}
	}
	return time.Unix(0, nanos)
}

// Nudge schedules a convergence pass for one server, as if its container had
// emitted an event. An operator action that changes a server's recorded state
// without touching Docker - releasing a stale lifecycle lock, for instance -
// uses this so the server converges immediately instead of waiting for the
// safety-net sweep.
func (r *Reconciler) Nudge(serverID string) {
	if serverID == "" {
		return
	}
	r.request(serverID)
}

// request schedules a convergence pass for serverID once the debounce window's
// quiet period elapses. Repeated calls within the window collapse into one.
func (r *Reconciler) request(serverID string) {
	if serverID == "" {
		return
	}
	r.deb.enqueue(serverID, func() { r.push(serverID) })
}

// push hands a server ID to the worker pool, dropping duplicates that are
// already queued so a burst can never enqueue the same server twice.
func (r *Reconciler) push(serverID string) {
	r.mu.Lock()
	ctx := r.ctx
	if r.queued[serverID] {
		r.mu.Unlock()
		return
	}
	r.queued[serverID] = true
	r.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case r.work <- serverID:
	case <-ctx.Done():
		r.clearQueued(serverID)
	case <-r.stop:
		r.clearQueued(serverID)
	}
}

// requestModule schedules a convergence pass for one module.
func (r *Reconciler) requestModule(moduleID string) {
	if moduleID == "" || r.modules == nil {
		return
	}
	r.deb.enqueue("module:"+moduleID, func() { r.pushModule(moduleID) })
}

// pushModule hands a module ID to the module worker, dropping duplicates that
// are already queued.
func (r *Reconciler) pushModule(moduleID string) {
	r.mu.Lock()
	ctx := r.ctx
	if r.moduleQueue[moduleID] {
		r.mu.Unlock()
		return
	}
	r.moduleQueue[moduleID] = true
	r.mu.Unlock()

	if ctx == nil {
		ctx = context.Background()
	}

	select {
	case r.moduleWork <- moduleID:
	case <-ctx.Done():
	case <-r.stop:
	}
}

// moduleWorker converges queued modules one at a time. Module passes are cheap
// relative to server passes and serialising them keeps a burst of sidecar
// events from racing the same parent's hooks.
func (r *Reconciler) moduleWorker() {
	defer r.wg.Done()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.stop:
			return
		case moduleID := <-r.moduleWork:
			r.mu.Lock()
			delete(r.moduleQueue, moduleID)
			r.mu.Unlock()
			r.safeReconcileModule(moduleID)
		}
	}
}

// safeReconcileModule isolates a module pass so a panic cannot take down the
// worker.
func (r *Reconciler) safeReconcileModule(moduleID string) {
	defer func() {
		if rec := recover(); rec != nil && r.log != nil {
			r.log.Error("reconciler: panic while reconciling module %s: %v", moduleID, rec)
		}
	}()

	outcome, err := r.modules.ReconcileModule(r.ctx, moduleID)
	if err != nil && r.log != nil {
		r.log.Error("reconciler: module %s convergence failed: %v", moduleID, err)
		return
	}
	if outcome != "" && outcome != "in_sync" && r.log != nil {
		r.log.Info("reconciler: module %s converged: %s", moduleID, outcome)
	}
	if err == nil {
		r.scheduleModuleDependents(moduleID, outcome)
	}
}

// scheduleModuleDependents queues a pass for modules that declared a dependency
// on moduleID. A dependency becoming ready or stopping is what unblocks a
// waiting sidecar; without this, dependents relied on the periodic sweep alone.
func (r *Reconciler) scheduleModuleDependents(moduleID, outcome string) {
	switch outcome {
	case "adopted", "recreated", "stopped":
	default:
		return
	}
	if moduleID == "" || r.modules == nil {
		return
	}
	moduleStore, ok := r.store.(ModuleStore)
	if !ok {
		return
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	modules, err := moduleStore.ListModules(ctx)
	if err != nil {
		if r.log != nil {
			r.log.Error("reconciler: list modules for dependent propagation: %v", err)
		}
		return
	}
	for _, m := range modules {
		if m == nil || m.ID == moduleID {
			continue
		}
		if moduleDependsOn(m, moduleID) {
			r.requestModule(m.ID)
		}
	}
}

func moduleDependsOn(mod *db.Module, moduleID string) bool {
	for _, dep := range mod.Dependencies {
		if dep != nil && dep.ModuleId == moduleID {
			return true
		}
	}
	return false
}

func (r *Reconciler) clearQueued(serverID string) {
	r.mu.Lock()
	delete(r.queued, serverID)
	r.mu.Unlock()
}

// consume translates the merged event stream into convergence requests.
func (r *Reconciler) consume(eventStream <-chan ContainerEvent) {
	defer r.wg.Done()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.stop:
			return
		case ev, ok := <-eventStream:
			if !ok {
				return
			}
			// A module container has its own record and its own pass; a server
			// event never schedules a module pass and vice versa.
			if ev.ModuleID != "" {
				r.requestModule(ev.ModuleID)
				continue
			}
			if ev.ServerID != "" {
				// Die events carry the exit evidence the restart-loop detector
				// needs: a nonzero exit or an OOM is one abnormal termination,
				// a clean exit forgives the tally, and an oom event without a
				// die is itself the evidence.
				at := ev.Timestamp
				if at.IsZero() {
					at = time.Now()
				}
				switch ev.Action {
				case "die":
					r.loops.recordDie(ev.ServerID, ev.ExitCode, false, at)
				case "oom":
					r.loops.recordDie(ev.ServerID, nil, true, at)
				}
				r.request(ev.ServerID)
			}
		}
	}
}

// sweepLoop periodically enqueues every known server so that a dropped event
// (or a missed reconnect window) cannot leave a server permanently divergent.
func (r *Reconciler) sweepLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(r.cfg.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.stop:
			return
		case <-ticker.C:
			r.enqueueAll(r.ctx)
		}
	}
}

func (r *Reconciler) enqueueAll(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	servers, err := r.store.ListServers(ctx)
	if err != nil {
		if r.log != nil {
			r.log.Error("reconciler: failed to list servers for sweep: %v", err)
		}
		return
	}
	// Every server in this list is now queued for an observation, so the
	// stored statuses are about to be as fresh as this moment.
	r.lastSweep.Store(time.Now().UnixNano())
	for _, s := range servers {
		if s == nil {
			continue
		}
		r.request(s.ID)
	}

	// The same sweep covers module containers. A module without a recorded
	// container has nothing to converge, and a module system that is disabled
	// leaves r.modules nil, which requestModule ignores.
	if r.modules == nil {
		return
	}
	moduleStore, ok := r.store.(ModuleStore)
	if !ok {
		return
	}
	modules, err := moduleStore.ListModules(ctx)
	if err != nil {
		if r.log != nil {
			r.log.Error("reconciler: failed to list modules for sweep: %v", err)
		}
		return
	}
	for _, m := range modules {
		if m == nil {
			continue
		}
		// A running row with no container id still needs passes (for example
		// while waiting to recreate once a dependency or host port frees up).
		if m.ContainerID == "" && m.Status != db.ModuleStatusRunning {
			continue
		}
		r.requestModule(m.ID)
	}
}

func (r *Reconciler) worker() {
	defer r.wg.Done()

	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.stop:
			return
		case serverID := <-r.work:
			r.clearQueued(serverID)
			r.safeReconcile(serverID)
		}
	}
}

// safeReconcile isolates a single convergence pass so a panic in one server
// cannot take down the worker pool.
func (r *Reconciler) safeReconcile(serverID string) {
	defer func() {
		if rec := recover(); rec != nil && r.log != nil {
			r.log.Error("reconciler: panic while reconciling server %s: %v", serverID, rec)
		}
	}()

	if err := r.ReconcileServer(r.ctx, serverID); err != nil && r.log != nil {
		r.log.Error("reconciler: reconcile of server %s failed: %v", serverID, err)
	}
}

// observedState is the reconciler's view of a server's container.
type observedState struct {
	// Exists is false when no container matching the server could be resolved.
	Exists      bool
	ContainerID string
	// IP is the container's address on its attached network, when known. It is
	// empty for a container resolved by name fallback on some daemons.
	IP     string
	Status db.ServerStatus
	// ExitCode and OOMKilled describe how a stopped container's process ended.
	ExitCode  int
	OOMKilled bool
	// StartedAt is when the container process last (re)started. Log evidence
	// older than it belongs to a previous boot, not this one.
	StartedAt time.Time
	// AbnormalExits is the number of consecutive abnormal container
	// terminations currently on record for this server (restart-loop
	// detection). Zero when the server is not crash-looping.
	AbnormalExits int
}

// observe resolves the server's container and inspects its runtime state. A
// missing container yields Exists=false and a nil error; a transient Docker or
// node failure is returned as an error so the caller leaves the DB alone.
func (r *Reconciler) observe(ctx context.Context, cli Client, serverID string) (observedState, error) {
	summary, err := cli.ResolveContainer(ctx, serverID)
	if err != nil {
		if errors.Is(err, docker.ErrContainerNotResolved) {
			return observedState{Exists: false}, nil
		}
		return observedState{}, fmt.Errorf("resolve container: %w", err)
	}
	if summary == nil || summary.ID == "" {
		return observedState{Exists: false}, nil
	}

	st, err := cli.ObserveContainer(ctx, summary.ID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return observedState{Exists: false}, nil
		}
		return observedState{}, fmt.Errorf("observe container: %w", err)
	}

	return observedState{
		Exists:      true,
		ContainerID: st.ContainerID,
		IP:          containerIP(summary),
		Status:      st.Status,
		ExitCode:    st.ExitCode,
		OOMKilled:   st.OOMKilled,
		StartedAt:   st.StartedAt,
	}, nil
}

// containerIP picks a deterministic primary address for a container. With more
// than one attached network a random pick would make the route-drift check flap,
// so the network names are sorted and the first non-empty address is used.
func containerIP(summary *container.Summary) string {
	if summary == nil || summary.NetworkSettings == nil || len(summary.NetworkSettings.Networks) == 0 {
		return ""
	}

	names := make([]string, 0, len(summary.NetworkSettings.Networks))
	for name := range summary.NetworkSettings.Networks {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		ep := summary.NetworkSettings.Networks[name]
		if ep != nil && ep.IPAddress.IsValid() {
			return ep.IPAddress.String()
		}
	}
	return ""
}

// ReconcileServer converges a single server's DB status with its container's
// observed state, adopting a drifted container ID, self-healing a missing
// container, and emitting lifecycle events on real transitions.
func (r *Reconciler) ReconcileServer(ctx context.Context, serverID string) error {
	// RPC lifecycle operations, deep sleep, freezing, and reconciliation all
	// mutate the same server/container relationship. A pass that overlaps one
	// of those transitions can observe a half-created or half-stopped container
	// and persist a false state. Leave the current pass to the next Docker event
	// or periodic sweep when another operation owns the server — but watch for
	// the transition to end so the server converges immediately afterwards
	// instead of waiting out the sweep.
	transition, err := docker.TryBeginServerTransition(serverID)
	if err != nil {
		r.convergeWhenTransitionEnds(serverID)
		return nil
	}
	// A pass that converged, or deliberately did nothing, is itself the
	// convergence the transition-end hook would request: release it quietly so
	// it cannot queue the next one. A transient failure is different - the hook
	// is the prompt retry - so the first failure of an episode releases
	// normally. Later failures release quietly and fall back to the periodic
	// sweep, keeping a persistent fault from becoming an unbounded self-nudge
	// loop.
	retry := false
	defer func() {
		if retry && r.beginRetry(serverID) {
			transition.Release()
			return
		}
		if !retry {
			r.clearRetry(serverID)
		}
		transition.ReleaseQuiet()
	}()

	server, err := r.store.GetServer(ctx, serverID)
	if err != nil {
		// A deleted server is a deliberate no-op; a read failure is transient
		// and must retry promptly instead of waiting for the sweep.
		r.loops.reset(serverID)
		r.clearPendingStop(serverID)
		if errors.Is(err, db.ErrServerNotFound) {
			return nil
		}
		retry = true
		return fmt.Errorf("get server: %w", err)
	}
	if server == nil {
		// Deleted between listing and this pass; nothing safe to converge.
		r.loops.reset(serverID)
		r.clearPendingStop(serverID)
		return nil
	}

	cli, err := r.clients.GetClientStrict(server.NodeID)
	if err != nil || cli == nil {
		if r.log != nil {
			r.log.Warn("reconciler: node %q unavailable for server %s: %v", server.NodeID, server.Name, err)
		}
		retry = true
		return nil
	}

	obs, err := r.observe(ctx, cli, server.ID)
	if err != nil {
		retry = true
		return err
	}

	// Restart-loop detection: merge this observation into the per-server
	// abnormal-termination tally. A threshold-exceeding tally is consumed
	// (returned once, then reset) so the loop report below is raised once per
	// loop episode instead of on every pass.
	obs.AbnormalExits = r.loops.observe(server.ID, obs, time.Now())

	dec := decide(server.Status, obs, server.Detached, !r.cfg.DisableSelfHeal)

	// Crash-loop reporting: a container that keeps dying abnormally is an
	// error with a loop-specific reason, not a flapping start/stop cycle.
	// Skipped while an operator is stopping the server or while it is owned
	// by deep sleep, and skipped while the container is up (a boot in
	// progress after past crashes is not itself an error).
	if obs.AbnormalExits >= restartLoopThreshold && dec.action == actNone && !obsAlive(obs) &&
		server.Status != db.StatusStopping && server.Status != db.StatusDeepSleep {
		dec.status = db.StatusError
		dec.reason = restartLoopReason(obs.AbnormalExits)
		dec.replaceReason = true
	}

	// Game-process failure detection. The container can outlive a crashed JVM
	// (the image retries inside a live entrypoint), so Docker-level status
	// alone never turns red for a hard boot failure like a missing hard
	// dependency. Consult the current boot's log tail for fatal markers even
	// when the entrypoint has already exited.
	if dec.action != actStop && dec.action != actRecreate && shouldScanBootFailure(server.Status, obs) {
		if reason, ok := r.detectBootFailure(ctx, cli, obs); ok {
			dec.status = db.StatusError
			dec.reason = reason
		}
	}

	oldContainerID := server.ContainerID
	oldBackend := r.backendFor(server.ID)

	// Adopt a container whose ID drifted (or was never recorded) before acting,
	// so the self-heal step below targets the container that actually exists.
	// This is the MINE-103 identity contract in action: the container is cattle,
	// its deterministic name/labels are the identity, and a recreated container
	// is re-adopted rather than mistaken for a loss.
	adopted := false
	if obs.Exists && obs.ContainerID != "" && obs.ContainerID != server.ContainerID {
		if err := r.store.UpdateServerContainerID(ctx, server.ID, obs.ContainerID); err != nil {
			retry = true
			return fmt.Errorf("adopt container id: %w", err)
		}
		if r.log != nil {
			r.log.Info("reconciler: adopted container %s for server %s (was %q)", shortContainerID(obs.ContainerID), server.Name, server.ContainerID)
		}
		server.ContainerID = obs.ContainerID
		adopted = true
	}

	selfHealed := false
	if dec.action == actStop && server.ContainerID != "" {
		window := docker.DefaultStopWindow
		if cfg, cfgErr := r.store.GetServerConfig(ctx, server.ID); cfgErr == nil {
			window = docker.StopWindow(cfg)
		}
		if _, err := cli.StopContainerWithin(ctx, server.ContainerID, window); err != nil && r.log != nil {
			r.log.Error("reconciler: resume stop failed for server %s: %v", server.Name, err)
		}
	}
	if dec.action == actRecreate {
		if err := r.selfHealRecreate(ctx, cli, server); err != nil {
			if r.log != nil {
				r.log.Error("reconciler: self-heal recreate failed for server %s: %v", server.Name, err)
			}
			// Surface the failure instead of silently flapping.
			dec.status = db.StatusError
			dec.reason = "self-heal recreate failed: " + err.Error()
		} else {
			selfHealed = true
		}
	}

	// Re-point live console subscriptions when the backing container changed, so
	// a WebSocket log console survives a Force Recreate uninterrupted and a
	// later unsubscribe still finds its channel (MINE-108).
	//
	// The old ID comes from the previously observed backend rather than the DB:
	// an RPC-initiated recreate updates the server row itself, so the adoption
	// branch above sees no change — but the backend cache still remembers the
	// container the subscribers were registered against.
	migrateFrom := oldContainerID
	if oldBackend.containerID != "" {
		migrateFrom = oldBackend.containerID
	}
	if r.logMigrator != nil && migrateFrom != "" && server.ContainerID != "" && migrateFrom != server.ContainerID {
		r.logMigrator.MigrateServerLogSubscriptions(server.ID, migrateFrom, server.ContainerID)
	}

	prev := server.Status
	statusChanged := dec.status != "" && dec.status != prev
	if statusChanged {
		if err := r.store.UpdateServerStatus(ctx, server.ID, dec.status); err != nil {
			retry = true
			return fmt.Errorf("update status: %w", err)
		}
		server.Status = dec.status
		r.emitTransition(ctx, server, prev, dec.status, dec.reason)
	}

	// The failure reason is display metadata, not convergence state: record it
	// when an error is adopted or backfilled onto a sticky error, and clear
	// it as soon as any other status is adopted so a recovered server never
	// shows a stale crash.
	wantReason := server.StatusReason
	if statusChanged {
		wantReason = ""
		if dec.status == db.StatusError {
			wantReason = dec.reason
		}
	} else if dec.status == db.StatusError && dec.reason != "" && dec.replaceReason {
		// The decision carries a reason that must stay visible even on a
		// sticky error status (the restart-loop report).
		wantReason = dec.reason
	} else if dec.status == db.StatusError && dec.reason != "" && wantReason == "" {
		wantReason = dec.reason
	}
	if wantReason != server.StatusReason {
		if err := r.store.UpdateServerStatusReason(ctx, server.ID, wantReason); err != nil {
			if r.log != nil {
				r.log.Warn("reconciler: failed to record status reason for server %s: %v", server.Name, err)
			}
		} else {
			server.StatusReason = wantReason
		}
	}

	// Detect backend drift independently of status: a container can be
	// re-allocated a different IP (or be an entirely new container) while the DB
	// status never changes, which previously left the proxy pinned to a dead
	// address. Comparing ID+IP keeps route churn to real moves only.
	backendMoved := false
	if obs.Exists {
		newBackend := backendRef{containerID: obs.ContainerID, ip: obs.IP}
		if newBackend != oldBackend {
			backendMoved = true
			r.rememberBackend(server.ID, newBackend)
		}
	}

	// Refresh the proxy route whenever the backend may have moved: a status
	// transition changes whether the route should exist at all, and adopting,
	// recreating, or re-addressing a container all change the target backend.
	if statusChanged || adopted || selfHealed || backendMoved || dec.reconcileRoute {
		r.updateRoute(ctx, server)
	}

	r.scheduleServerChildModules(server.ID, adopted, selfHealed, statusChanged, server.Status)

	// A stop whose RPC never finalized must still be announced: check the
	// deferred-announcement backlog on every pass.
	r.flushPendingStops(ctx, server, obs)

	return nil
}

// beginRetry records that serverID still needs a retry and reports whether this
// is the first failure of the episode. Only that first failure releases the
// transition loudly; a repeated failure stays quiet and leaves the next attempt
// to the periodic sweep.
func (r *Reconciler) beginRetry(serverID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.retries[serverID] {
		return false
	}
	r.retries[serverID] = true
	return true
}

// clearRetry forgets a server's failure episode after a pass that needed no
// retry, whether it converged or deliberately did nothing (for example a
// deleted server).
func (r *Reconciler) clearRetry(serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.retries, serverID)
}

// scheduleServerChildModules queues a module pass for every sidecar on serverID
// when the parent container identity or readiness changed. Without this,
// modules waiting on a recreated or stopped parent relied on the periodic sweep.
func (r *Reconciler) scheduleServerChildModules(serverID string, adopted, selfHealed, statusChanged bool, status db.ServerStatus) {
	if serverID == "" || r.modules == nil {
		return
	}
	propagate := adopted || selfHealed
	if statusChanged && (status == db.StatusRunning || status == db.StatusStopped) {
		propagate = true
	}
	if !propagate {
		return
	}
	moduleStore, ok := r.store.(ModuleStore)
	if !ok {
		return
	}
	ctx := r.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	modules, err := moduleStore.ListModules(ctx)
	if err != nil {
		if r.log != nil {
			r.log.Error("reconciler: list modules for server propagation: %v", err)
		}
		return
	}
	for _, m := range modules {
		if m != nil && m.ServerID == serverID {
			r.requestModule(m.ID)
		}
	}
}

// backendRef identifies the container a proxy route points at: the container ID
// plus its primary IP. A change to either means the route may now be stale.
type backendRef struct {
	containerID string
	ip          string
}

// backendFor returns the last backend the reconciler pushed to the proxy for a
// server, or the zero value if it has not observed one yet.
func (r *Reconciler) backendFor(serverID string) backendRef {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.backends[serverID]
}

func (r *Reconciler) rememberBackend(serverID string, ref backendRef) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[serverID] = ref
}

// selfHealRecreate replaces a missing container for a server the DB still
// believes is running. It records the new container ID and starts it, since
// RecreateContainer only auto-starts when the previous container was running —
// which is never the case in this path.
func (r *Reconciler) selfHealRecreate(ctx context.Context, cli Client, server *db.Server) error {
	// The replacement is started below, so self-heal must contend with other
	// starts on this node rather than admit against a stale persisted total.
	release, err := r.admission.Acquire(ctx, server.NodeID)
	if err != nil {
		return fmt.Errorf("node %s start admission: %w", server.NodeID, err)
	}
	defer release()

	cfg, err := r.store.GetServerConfig(ctx, server.ID)
	if err != nil {
		return fmt.Errorf("load server config: %w", err)
	}

	res, err := cli.RecreateContainer(ctx, server.ContainerID, server, cfg)
	if err != nil {
		// RecreateContainer can create a replacement and then fail while
		// starting it. Persist the partial identity (or clear the stale missing
		// ID when no replacement exists) before reporting the failure.
		partialID := ""
		if res != nil {
			partialID = res.NewContainerID
		}
		if persistErr := r.store.UpdateServerContainerID(ctx, server.ID, partialID); persistErr != nil {
			return fmt.Errorf("recreate failed: %w (also failed to persist replacement container id %q: %v)", err, partialID, persistErr)
		}
		server.ContainerID = partialID
		return err
	}
	if res == nil || res.NewContainerID == "" {
		return fmt.Errorf("recreate returned no container id")
	}

	if err := r.store.UpdateServerContainerID(ctx, server.ID, res.NewContainerID); err != nil {
		return fmt.Errorf("record recreated container id: %w", err)
	}
	server.ContainerID = res.NewContainerID

	if err := cli.StartContainer(ctx, res.NewContainerID); err != nil {
		return fmt.Errorf("start recreated container: %w", err)
	}

	if r.log != nil {
		r.log.Warn("reconciler: self-healed server %s by recreating missing container %s", server.Name, shortContainerID(res.NewContainerID))
	}
	return nil
}

// updateRoute re-points the proxy at the server's current backend.
func (r *Reconciler) updateRoute(ctx context.Context, server *db.Server) {
	if r.proxy == nil || (server.ProxyHostname == "" && server.Port <= 0) {
		return
	}
	if err := r.proxy.UpdateServerRoute(server); err != nil && r.log != nil {
		r.log.Error("reconciler: failed to update proxy route for %s: %v", server.Name, err)
	}
}

// emitTransition publishes the lifecycle event corresponding to a status change.
//
// SERVER_START is announced once, on the transition into running, including
// when the previous row was creating or starting. Create, unpause, and
// StartServer share this writer so a panel-initiated start is not announced
// twice (D-005). SERVER_STOP is still skipped when the RPC already recorded
// stopping. SERVER_HEALTHY is only the recovery from unhealthy.
func (r *Reconciler) emitTransition(ctx context.Context, server *db.Server, from, to db.ServerStatus, reason string) {
	if r.log != nil {
		r.log.Info("reconciler: server %s transitioned %s -> %s%s", server.Name, from, to, reasonSuffix(reason))
	}

	if r.bus == nil {
		return
	}

	var eventType v1.TriggeredEventType
	switch to {
	case db.StatusStarting, db.StatusCreating:
		// Start is announced once, when the container is actually running.
		// A row that only reached "starting" has not finished that transition.
		return
	case db.StatusRunning:
		if from == db.StatusUnhealthy {
			eventType = v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_HEALTHY
		} else {
			// Previous status "starting" or "creating" must not swallow the
			// start event. Create, unpause, and StartServer all land here.
			eventType = v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_START
		}
	case db.StatusStopped, db.StatusError:
		if from == db.StatusStopping && r.suppressStopAnnouncement(ctx, server.ID, to, reason) {
			// The initiating RPC (or the lifecycle journal that owns the
			// stop) announces it. See suppressStopAnnouncement: when
			// ownership cannot be checked, the announcement is deferred and
			// re-emitted if the stop evidence persists past one sweep.
			return
		}
		eventType = v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP
	default:
		return
	}

	data := map[string]any{
		"previous_status": string(from),
		"status":          string(to),
	}
	if reason != "" {
		data["reason"] = reason
	}

	r.bus.Emit(ctx, events.Event{Type: eventType, ServerID: server.ID, Data: data})
}

// suppressStopAnnouncement decides whether the SERVER_STOP announcement for
// a stopping→stopped/error transition belongs to the RPC that requested the
// stop. When the lifecycle journal is part of the store, an active operation
// row means the stop is still owned (its RPC or recovery announces it), and
// no active row means that RPC finalized without announcing — the reconciler
// announces instead of swallowing the transition. When the journal is not
// part of the store, ownership cannot be checked: the announcement is
// deferred here and re-emitted by flushPendingStops once the stop evidence
// has persisted past one sweep, so a stop whose RPC crashed mid-way is
// never silently swallowed.
func (r *Reconciler) suppressStopAnnouncement(ctx context.Context, serverID string, to db.ServerStatus, reason string) bool {
	if journal, ok := r.store.(OperationJournal); ok {
		err := journal.AssertNoServerOperation(ctx, serverID)
		if err == nil {
			return false
		}
		if errors.Is(err, db.ErrServerOperationActive) {
			return true
		}
		// A journal read failure means ownership is unknown. Defer the
		// announcement through the pending-stop path instead of suppressing it
		// as if an active operation owned the server.
		r.rememberPendingStop(serverID, to, reason)
		return true
	}
	r.rememberPendingStop(serverID, to, reason)
	return true
}

// pendingStop remembers a suppressed stopping→stopped transition so the
// SERVER_STOP announcement can still be made if the initiating RPC died
// before announcing it.
type pendingStop struct {
	at     time.Time
	to     db.ServerStatus
	reason string
}

func (r *Reconciler) rememberPendingStop(serverID string, to db.ServerStatus, reason string) {
	if serverID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pendingStops == nil {
		r.pendingStops = make(map[string]pendingStop)
	}
	r.pendingStops[serverID] = pendingStop{at: time.Now(), to: to, reason: reason}
}

func (r *Reconciler) clearPendingStop(serverID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pendingStops, serverID)
}

// flushPendingStops re-announces suppressed stop transitions whose evidence
// has persisted past one sweep. The RPC that requested the stop is given one
// sweep to announce it; if the server is still stopped (or errored) with the
// container still down after that, the RPC is presumed dead and the
// reconciler announces the stop so subscribers are not left blind.
func (r *Reconciler) flushPendingStops(ctx context.Context, server *db.Server, obs observedState) {
	if server == nil {
		return
	}
	r.mu.Lock()
	pending, ok := r.pendingStops[server.ID]
	r.mu.Unlock()
	if !ok {
		return
	}

	if server.Status != db.StatusStopped && server.Status != db.StatusError {
		// The server left the stopped state; the stop announcement is moot.
		r.clearPendingStop(server.ID)
		return
	}
	if obs.Exists && obsAlive(obs) {
		// The container is up again; a start announcement covers the story.
		r.clearPendingStop(server.ID)
		return
	}
	if time.Since(pending.at) < r.cfg.SweepInterval {
		return // still inside the RPC's one-sweep grace window
	}
	r.clearPendingStop(server.ID)

	if r.log != nil {
		r.log.Info("reconciler: announcing stop for server %s whose stop RPC never finalized (%s -> %s%s)",
			server.Name, db.StatusStopping, pending.to, reasonSuffix(pending.reason))
	}
	if r.bus == nil {
		return
	}
	data := map[string]any{
		"previous_status": string(db.StatusStopping),
		"status":          string(pending.to),
	}
	if pending.reason != "" {
		data["reason"] = pending.reason
	}
	r.bus.Emit(ctx, events.Event{
		Type:     v1.TriggeredEventType_TRIGGERED_EVENT_TYPE_SERVER_STOP,
		ServerID: server.ID,
		Data:     data,
	})
}

// convergeWhenTransitionEnds schedules one convergence pass for serverID to
// run as soon as its current lifecycle transition ends, closing the blind
// window between "a transition holds the lock" and "the next Docker event or
// the 30s sweep". Waiters are de-duplicated per server and bounded, so a
// burst of events during a long transition cannot pile up goroutines.
func (r *Reconciler) convergeWhenTransitionEnds(serverID string) {
	if r.ctx == nil {
		// Not started (direct ReconcileServer calls, e.g. tests): there is no
		// event loop or sweep to serve a follow-up pass, so do not wait.
		return
	}
	if _, loaded := r.transitionWaits.LoadOrStore(serverID, struct{}{}); loaded {
		return // a waiter is already watching this server
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer r.transitionWaits.Delete(serverID)

		waitCtx, cancel := context.WithTimeout(r.ctx, transitionWaitTimeout)
		defer cancel()
		transitionDone := make(chan struct{})
		go func() {
			_ = docker.WaitForServerTransition(waitCtx, serverID)
			close(transitionDone)
		}()
		select {
		case <-transitionDone:
			r.request(serverID)
		case <-r.stop:
			// Shutting down: the bounded watcher goroutine exits on its own
			// when waitCtx is done.
		}
	}()
}

// reconcileAction is the self-heal action a decision calls for.
type reconcileAction int

const (
	// actNone means the observed state needs no corrective action.
	actNone reconcileAction = iota
	// actRecreate means the container is missing and should be recreated.
	actRecreate
	// actStop resumes a stop that was persisted before the panel exited.
	actStop
)

// decision is the outcome of the pure convergence policy.
type decision struct {
	// status is the status to persist, or "" to leave the DB unchanged.
	status db.ServerStatus
	// action is the self-heal action to perform, if any.
	action reconcileAction
	// reason explains a non-obvious status (e.g. an OOM kill) for logs/events.
	reason string
	// replaceReason forces the persisted reason to this decision's reason
	// even when the status itself did not change. Used by the restart-loop
	// report, which must stay visible on a sticky error status.
	replaceReason bool
	// reconcileRoute requests a proxy-route refresh even without a status
	// change (used when reality has converged but the backend may have moved).
	reconcileRoute bool
}

// decide is the pure convergence policy: given the desired status recorded in
// the database and the container state observed from Docker, it returns what (if
// anything) must change. Keeping it pure makes the policy — including the
// OOM/exit-code rules — unit-testable without a live Docker daemon.
//
// Deliberate boundary: a container that exists but has exited is reported as it
// is (stopped, or error for a crash/OOM). Only a container that is entirely
// *missing* is recreated, because an exited container may have been stopped on
// purpose (e.g. `/stop` in-game) and silently restarting it would fight the
// operator and risk a crash loop.
func decide(desired db.ServerStatus, obs observedState, detached, selfHeal bool) decision {
	if desired == db.StatusDeepSleep {
		// Deeply-asleep servers are owned by the DeepSleepManager. An exited
		// or missing container is the expected state. A running container that
		// is not inside a held wake (the transition lock skips this pass) is a
		// Docker restart-policy surprise: stop it, and do not adopt it as running.
		if obs.Exists && (obs.Status == db.StatusRunning || obs.Status == db.StatusUnhealthy || obs.Status == db.StatusStarting) {
			return decision{action: actStop, reconcileRoute: true}
		}
		return decision{reconcileRoute: true}
	}
	if !obs.Exists {
		switch desired {
		case db.StatusRunning, db.StatusStarting, db.StatusCreating:
			// Missing while the panel still wants the server up, including a
			// create or start whose container never appeared. An in-flight
			// lifecycle transition is skipped before decide, so this is not
			// a create that still holds the lock.
			if selfHeal && !detached {
				return decision{
					status: db.StatusStarting,
					action: actRecreate,
					reason: "container missing; recreating",
				}
			}
			if desired == db.StatusRunning {
				return decision{status: db.StatusError, reason: "container missing"}
			}
			return decision{}
		case db.StatusStopping, db.StatusPaused:
			return decision{status: db.StatusStopped, reconcileRoute: true}
		default:
			return decision{}
		}
	}

	switch obs.Status {
	case db.StatusRunning, db.StatusUnhealthy:
		if desired == db.StatusStopping {
			// The operator already asked for stopped. Resume that stop instead
			// of adopting the still-running JVM as the desired state.
			return decision{action: actStop}
		}
		if desired == db.StatusUnhealthy {
			// Preserve the explicit unhealthy signal.
			return decision{}
		}
		if obs.Status == desired {
			return decision{}
		}
		if obs.Status == db.StatusUnhealthy && isBootStatus(desired) {
			// Health checks fail during normal boot: image pulls and modpack
			// downloads take minutes, and the Docker healthcheck does not
			// know that. Stay in the boot state instead of painting a
			// starting server unhealthy for failing probes it was never
			// expected to pass yet.
			return decision{reconcileRoute: true}
		}
		return decision{status: obs.Status, reconcileRoute: true}

	case db.StatusPaused:
		if desired == db.StatusPaused {
			return decision{}
		}
		return decision{status: db.StatusPaused}

	case db.StatusStarting:
		// Docker is still restarting the container; let a single restart
		// settle. But a container that keeps dying is a crash loop: Docker's
		// restart policy would leave the row "starting" forever, so report
		// the loop once the abnormal-exit tally crosses the threshold. The
		// tally also covers a boot whose healthcheck is still starting, but
		// that path self-corrects the moment the boot completes.
		if obs.AbnormalExits >= restartLoopThreshold {
			return decision{
				status:         db.StatusError,
				reason:         restartLoopReason(obs.AbnormalExits),
				replaceReason:  true,
				reconcileRoute: true,
			}
		}
		return decision{}

	case db.StatusStopped, db.StatusError:
		if desired == db.StatusStopping {
			// The stop the operator asked for has completed.
			return decision{status: db.StatusStopped, reconcileRoute: true}
		}
		status, reason := classifyExit(obs)
		return decision{status: status, reason: reason, reconcileRoute: true}
	}

	return decision{}
}

// isBootStatus reports whether the stored status means the server is still
// coming up. Observations from a boot window (notably failing healthchecks)
// are normal boot noise, not evidence about the running server.
func isBootStatus(s db.ServerStatus) bool {
	return s == db.StatusStarting || s == db.StatusCreating
}

// classifyExit maps a non-running container's exit information onto a panel
// status. OOM is recorded only when Docker set OOMKilled. Exit 137 after a
// stop timeout is a real exit code, not an out-of-memory kill. A clean exit
// (or the SIGTERM of a graceful `docker stop`) is a normal stop.
func classifyExit(obs observedState) (db.ServerStatus, string) {
	if obs.OOMKilled {
		return db.StatusError, fmt.Sprintf("container was OOM-killed (exit code %d)", obs.ExitCode)
	}
	switch obs.ExitCode {
	case 0, exitCodeSIGTERM:
		return db.StatusStopped, ""
	default:
		return db.StatusError, fmt.Sprintf("container exited with code %d", obs.ExitCode)
	}
}

func reasonSuffix(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// debouncer coalesces rapid enqueues for the same key into a single callback
// fired once the key has been quiet for the configured window. Each call to
// enqueue resets the pending timer, so only the last call in a burst fires.
type debouncer struct {
	window time.Duration

	mu     sync.Mutex
	timers map[string]*time.Timer
}

func newDebouncer(window time.Duration) *debouncer {
	return &debouncer{
		window: window,
		timers: make(map[string]*time.Timer),
	}
}

func (d *debouncer) enqueue(key string, fire func()) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if t, ok := d.timers[key]; ok {
		t.Stop()
	}
	d.timers[key] = time.AfterFunc(d.window, func() {
		d.mu.Lock()
		delete(d.timers, key)
		d.mu.Unlock()
		fire()
	})
}

func (d *debouncer) stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	for key, t := range d.timers {
		t.Stop()
		delete(d.timers, key)
	}
}

// obsAlive reports whether the observed container is up (booting, serving,
// or frozen while up).
func obsAlive(obs observedState) bool {
	return obs.Status == db.StatusRunning || obs.Status == db.StatusUnhealthy || obs.Status == db.StatusPaused
}

// isAbnormalExit reports whether a stopped container's exit was a crash
// rather than a clean stop or a graceful `docker stop` (SIGTERM).
func isAbnormalExit(obs observedState) bool {
	if obs.OOMKilled {
		return true
	}
	return obs.ExitCode != 0 && obs.ExitCode != exitCodeSIGTERM
}

// restartLoopReason renders the persisted failure reason for a detected
// restart loop, e.g. "restarting repeatedly (3 times in 10m)".
func restartLoopReason(count int) string {
	return fmt.Sprintf("restarting repeatedly (%d times in %s)", count, restartLoopWindowLabel)
}

// obsExitKey identifies one container process lifetime, so repeated
// observations of the same exit count the death only once.
func obsExitKey(obs observedState) string {
	return obs.ContainerID + "|" + obs.StartedAt.UTC().Format(time.RFC3339Nano)
}

// observedExitDedupWindow suppresses counting one death twice when both a
// die event and the observed exited state describe it: they arrive within
// moments of each other, while genuinely distinct deaths in a restart loop
// are separated by the daemon's restart backoff or the sweep interval.
const observedExitDedupWindow = 10 * time.Second

// restartTracker counts consecutive abnormal container terminations per
// server. Deaths are recorded from Docker die events (consume) and from
// observed container state (ReconcileServer, deduplicated by container
// identity so one exit observed by many passes counts once). A tally is
// forgiven by a clean exit or by a container boot that stays up, expires
// outside the window, and is consumed — returned once, then reset — by the
// reconciliation pass that reports it.
type restartTracker struct {
	mu    sync.Mutex
	loops map[string]*restartLoop
}

// restartLoop is one server's abnormal-termination tally inside the window.
type restartLoop struct {
	// deaths are the abnormal terminations inside the window, oldest first.
	deaths []time.Time
	// lastKey is the identity of the last observed exit counted, so the same
	// death seen again on a later pass does not count twice.
	lastKey string
	// lastDieAt is when a die event last counted a death; the observed-exit
	// path defers to it, because a die event and the exited state it
	// produced arrive within moments of each other and describe one death.
	lastDieAt time.Time
}

func newRestartTracker() *restartTracker {
	return &restartTracker{loops: make(map[string]*restartLoop)}
}

// recordDie accounts one Docker die event. An oom flag counts regardless of
// the exit code; a clean 0/143 exit forgives the tally; a missing exit code
// is no evidence either way.
func (t *restartTracker) recordDie(serverID string, exitCode *int, oom bool, now time.Time) {
	if serverID == "" {
		return
	}
	switch {
	case oom:
		t.recordAbnormalExit(serverID, now)
	case exitCode != nil && *exitCode != 0 && *exitCode != exitCodeSIGTERM:
		t.recordAbnormalExit(serverID, now)
	case exitCode != nil:
		// A clean stop forgives the loop.
		t.reset(serverID)
	}
}

// observe merges one reconciliation pass's observation into the tally and
// returns the count the pass should act on.
func (t *restartTracker) observe(serverID string, obs observedState, now time.Time) int {
	if serverID == "" || !obs.Exists {
		return t.tally(serverID, now, false)
	}
	switch obs.Status {
	case db.StatusRunning, db.StatusUnhealthy, db.StatusPaused:
		// A container that stays up is no longer crash-looping.
		if !obs.StartedAt.IsZero() && now.Sub(obs.StartedAt) >= restartLoopStableRuntime {
			t.reset(serverID)
		}
		// Not consumed: the tally should still be available to the next
		// pass that observes a restarting or exited container.
		return t.tally(serverID, now, false)
	case db.StatusStopped:
		if isAbnormalExit(obs) {
			t.recordObservedExit(serverID, obsExitKey(obs), now)
		} else {
			// A clean stop forgives the loop.
			t.reset(serverID)
		}
		return t.tally(serverID, now, true)
	default:
		// Starting (including Docker's "restarting") and unknown states do
		// not change the tally, but a threshold-exceeding tally is consumed
		// so the deciding pass reports the loop exactly once.
		return t.tally(serverID, now, true)
	}
}

// recordAbnormalExit counts one abnormal termination reported by a die event.
func (t *restartTracker) recordAbnormalExit(serverID string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	loop := t.loops[serverID]
	if loop == nil {
		loop = &restartLoop{}
		t.loops[serverID] = loop
	}
	loop.deaths = pruneExpiredDeaths(loop.deaths, now)
	loop.deaths = append(loop.deaths, now)
	loop.lastDieAt = now
}

// recordObservedExit counts one abnormal exit observed from container state,
// deduplicating repeats of the same death and deaths a recent die event
// already counted.
func (t *restartTracker) recordObservedExit(serverID, key string, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	loop := t.loops[serverID]
	if loop != nil && loop.lastKey == key {
		return // the same death, already counted
	}
	if loop != nil && !loop.lastDieAt.IsZero() && now.Sub(loop.lastDieAt) < observedExitDedupWindow {
		// A die event counted a death moments ago; treat this observation as
		// the exited state of that death rather than a second one.
		loop.lastKey = key
		return
	}
	if loop == nil {
		loop = &restartLoop{}
		t.loops[serverID] = loop
	}
	loop.deaths = pruneExpiredDeaths(loop.deaths, now)
	loop.deaths = append(loop.deaths, now)
	loop.lastKey = key
}

// reset forgives a server's tally entirely (clean exit, stable runtime).
func (t *restartTracker) reset(serverID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.loops, serverID)
}

// tally reports the abnormal-termination count inside the window. With
// consume set, a threshold-exceeding tally is returned once and reset, so
// the loop report is not re-raised on every pass.
func (t *restartTracker) tally(serverID string, now time.Time, consume bool) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	loop, ok := t.loops[serverID]
	if !ok {
		return 0
	}
	loop.deaths = pruneExpiredDeaths(loop.deaths, now)
	count := len(loop.deaths)
	if consume && count >= restartLoopThreshold {
		// Keep the entry (its lastKey still deduplicates the death the
		// passes continue to observe) but drop the deaths.
		loop.deaths = nil
		return count
	}
	return count
}

// count reports the current tally without consuming it. Test observability
// and diagnostics use this.
func (t *restartTracker) count(serverID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	loop, ok := t.loops[serverID]
	if !ok {
		return 0
	}
	return len(pruneExpiredDeaths(loop.deaths, time.Now()))
}

// pruneExpiredDeaths drops abnormal terminations older than the window.
func pruneExpiredDeaths(deaths []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-restartLoopWindow)
	keep := deaths[:0]
	for _, at := range deaths {
		if at.After(cutoff) {
			keep = append(keep, at)
		}
	}
	return keep
}
