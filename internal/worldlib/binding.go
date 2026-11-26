// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/internal/snapshot"
	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// CommandSender is the narrow RCON/exec seam used for quiesce.
type CommandSender interface {
	SendCommand(ctx context.Context, serverID, command string) (string, error)
}

// Lifecycle is the narrow start/stop seam adapted from ServerService. Stopping
// and restarting require the servers:stop/start grant in the RPC layer; this
// package never broadens that authorization.
type Lifecycle interface {
	BeginTransition(string) (release func(), err error)
	StopServer(ctx context.Context, serverID string) error
	StartServer(ctx context.Context, serverID string) error
	IsRunning(ctx context.Context, serverID string) (bool, error)
}

// KeyedAdmission is a process-local, cancellable admission gate. It is NOT a
// cross-process reservation: a second controller is outside its guarantee.
type KeyedAdmission struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

// NewKeyedAdmission creates an empty gate.
func NewKeyedAdmission() *KeyedAdmission {
	return &KeyedAdmission{locks: map[string]chan struct{}{}}
}

// Acquire takes all keys in sorted order so concurrent callers cannot deadlock.
func (a *KeyedAdmission) Acquire(ctx context.Context, keys ...string) (func(), error) {
	if a == nil {
		return nil, errors.New("admission gate is not configured")
	}
	if len(keys) == 0 {
		return func() {}, nil
	}
	unique := map[string]bool{}
	for _, k := range keys {
		unique[k] = true
	}
	sorted := make([]string, 0, len(unique))
	for k := range unique {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)

	a.mu.Lock()
	channels := make([]chan struct{}, 0, len(sorted))
	for _, k := range sorted {
		ch, ok := a.locks[k]
		if !ok {
			ch = make(chan struct{}, 1)
			ch <- struct{}{}
			a.locks[k] = ch
		}
		channels = append(channels, ch)
	}
	a.mu.Unlock()

	acquired := make([]chan struct{}, 0, len(channels))
	for _, ch := range channels {
		if err := ctx.Err(); err != nil {
			for i := len(acquired) - 1; i >= 0; i-- {
				acquired[i] <- struct{}{}
			}
			return nil, err
		}
		select {
		case <-ctx.Done():
			for i := len(acquired) - 1; i >= 0; i-- {
				acquired[i] <- struct{}{}
			}
			return nil, ctx.Err()
		case <-ch:
			acquired = append(acquired, ch)
		}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			for i := len(acquired) - 1; i >= 0; i-- {
				acquired[i] <- struct{}{}
			}
		})
	}, nil
}

// ErrBindRequestConflict identifies a request ID that cannot prove the requested binding.
var ErrBindRequestConflict = errors.New("world bind request ID conflicts with the requested binding")

// ErrBindLevelUnknown identifies an invalid configured world level.
var ErrBindLevelUnknown = errors.New("server actual level name is unknown")

// BindRequest is the pure input to a bind or swap.
type BindRequest struct {
	ServerID      string
	LevelName     string
	RevisionID    string
	Window        string
	RequestID     string
	NodeID        string
	Generation    int64
	Operator      string
	AcceptUnknown bool
	// Previous is the binding captured under the admission lock before a swap.
	// It is set by Binder, never by RPC callers.
	Previous *storage.WorldBinding
}

// BindResult reports the persisted binding and the journal outcome.
type BindResult struct {
	Binding     *storage.WorldBinding
	OperationID string
	RequestID   string
	Outcome     string
	Consistency string
}

// Binder owns the journalled stop → stage → publish → restart transaction.
type Binder struct {
	store       *storage.Store
	log         *logger.Logger
	resolver    *nodestorage.Resolver
	sender      CommandSender
	lifecycle   Lifecycle
	admission   *KeyedAdmission
	libraryRoot string
	compat      *Engine
	// FreeBytes reports node free bytes. It is required for remote transport
	// nodes, whose filesystem cannot be measured from the controller. Local and
	// shared-storage nodes fall back to files.GetFreeDiskSpace(dataRoot).
	FreeBytes func(ctx context.Context, nodeID, dataRoot string) (int64, error)
	// accessorDecorator is a test-only seam for injecting I/O faults at the
	// real node-accessor boundary. Production leaves it nil.
	accessorDecorator   func(nodestorage.Accessor) nodestorage.Accessor
	rollbackBindingHook func() error
}

// SetAccessorDecorator installs a test-only accessor decorator. Production
// never calls it; regression tests use it to inject promotion/restore faults
// through the real Bind path without touching host data.
func (b *Binder) SetAccessorDecorator(fn func(nodestorage.Accessor) nodestorage.Accessor) {
	b.accessorDecorator = fn
}

// NewBinder constructs a binder. A nil resolver is legal for local-only nodes
// but remote nodes fail closed with nodestorage.ErrUnavailable.
func NewBinder(store *storage.Store, log *logger.Logger, resolver *nodestorage.Resolver, sender CommandSender, lifecycle Lifecycle, libraryRoot string) *Binder {
	return &Binder{
		store: store, log: log, resolver: resolver, sender: sender,
		lifecycle: lifecycle, admission: NewKeyedAdmission(), libraryRoot: libraryRoot,
		compat: NewEngine(),
	}
}

// SetCompatibility installs the dry-run engine used by bind preflight. Passing
// nil disables compatibility gating; explicit callers must do so deliberately.
func (b *Binder) SetCompatibility(engine *Engine) { b.compat = engine }

// Bind executes a bind or swap. No live bytes are touched before admission,
// strict node resolution and cancellation checks have all succeeded.
func (b *Binder) Bind(ctx context.Context, req BindRequest) (*BindResult, error) {
	if req.ServerID == "" || req.RevisionID == "" {
		return nil, errors.New("bind requires a server and revision")
	}
	req.LevelName = strings.TrimSpace(req.LevelName)
	if req.Window != "" && req.Window != "now" && req.Window != "next_restart" && req.Window != "idle" {
		return nil, fmt.Errorf("unknown bind window %q", req.Window)
	}
	if req.RequestID == "" {
		req.RequestID = "world-bind-" + uuid.NewString()
	}

	if err := operation.ValidateRequestID(req.RequestID); err != nil {
		return nil, err
	}

	server, err := b.store.GetServer(ctx, req.ServerID)
	if err != nil {
		return nil, err
	}
	if req.NodeID != "" && req.NodeID != server.NodeID {
		return nil, fmt.Errorf("%w: requested node %q does not own server %q", ErrRemoteUnavailable, req.NodeID, server.ID)
	}
	req.NodeID = server.NodeID
	node, generation, err := b.resolveNode(ctx, req.NodeID)
	if err != nil {
		return nil, err
	}
	if req.Generation <= 0 {
		req.Generation = generation
	}
	if req.Generation != generation {
		return nil, fmt.Errorf("%w: request generation %d does not match node generation %d",
			ErrStaleGeneration, req.Generation, generation)
	}
	release, err := b.admission.Acquire(ctx, "server:"+req.ServerID, "revision:"+req.RevisionID)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// A completed operation is proved against the installed binding before
	// checking current world config or opening the revision archive.
	if replay, err := b.replayBind(ctx, server, req); replay != nil || err != nil {
		return replay, err
	}
	if err := b.canonicalLevel(ctx, &req); err != nil {
		return nil, err
	}
	levelAtAdmission := req.LevelName
	revision, err := b.store.GetWorldRevision(ctx, req.RevisionID)
	if err != nil {
		return nil, err
	}
	if revision.NodeID != "" && revision.NodeID != req.NodeID {
		return nil, fmt.Errorf("%w: revision is stored on node %q", ErrRemoteUnavailable, revision.NodeID)
	}
	if !node.IsLocal && b.resolver == nil {
		return nil, fmt.Errorf("%w: no storage resolver for node %q", ErrRemoteUnavailable, req.NodeID)
	}
	archivePath, err := ResolveRevisionArchive(b.libraryRoot, revision.StoragePath)
	if err != nil {
		return nil, err
	}
	layout, err := ValidateRevisionArchive(ctx, archivePath)
	if err != nil {
		return nil, err
	}
	if _, err := layout.SelectFamilies(req.LevelName); err != nil {
		return nil, err
	}
	existing, err := b.store.GetWorldBinding(ctx, req.ServerID, req.LevelName)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	payloadHash := worldBindPayloadHash(server, req)
	if existing != nil {
		if !existing.MatchesOwner(req.NodeID, req.Generation) {
			return nil, fmt.Errorf("%w: existing binding belongs to another node generation", ErrStaleGeneration)
		}
		if existing.RevisionID == req.RevisionID {
			var active storage.ServerOperation
			err := b.store.DB().WithContext(ctx).Where("active_server = ?", req.ServerID).First(&active).Error
			switch {
			case err == nil:
				return &BindResult{Outcome: string(operation.OutcomeUnknown), OperationID: active.ID, RequestID: req.RequestID},
					fmt.Errorf("%w: operation %s owns the server", ErrUncertainOutcome, active.ID)
			case !errors.Is(err, gorm.ErrRecordNotFound):
				return nil, err
			}
			return &BindResult{Binding: existing, RequestID: req.RequestID, Outcome: string(operation.OutcomeSuccess), Consistency: existing.Consistency}, nil
		}
		req.Previous = existing
	}

	if b.lifecycle != nil {
		releaseTransition, err := b.lifecycle.BeginTransition(server.ID)
		if err != nil {
			return nil, err
		}
		defer releaseTransition()
	}
	// Completed replays returned above never reach the transition. For a fresh
	// operation the shared server transition is now held, so config can no
	// longer change underneath this bind. Re-read and require the SAME level
	// chosen under admission; a change is refused before stop, journal or any
	// live-world mutation.
	currentLevel, err := b.configuredLevel(ctx, req.ServerID)
	if err != nil {
		return nil, err
	}
	if currentLevel != levelAtAdmission {
		return nil, fmt.Errorf("%w: server level changed before world bind acquired the shared transition",
			ErrUnsupportedWorldLayout)
	}
	current, err := b.store.GetServer(ctx, server.ID)
	if err != nil {
		return nil, err
	}
	if server.NodeID != current.NodeID || server.DataPath != current.DataPath ||
		server.ContainerID != current.ContainerID {
		return nil, errors.New("server identity changed before world bind acquired admission")
	}
	server = current

	node, generation, err = b.resolveNode(ctx, server.NodeID)
	if err != nil {
		return nil, err
	}
	if req.Generation != generation {
		return nil, fmt.Errorf("%w: node generation changed before world bind acquired admission", ErrStaleGeneration)
	}
	if !node.IsLocal && b.resolver == nil {
		return nil, fmt.Errorf("%w: no storage resolver for node %q", ErrRemoteUnavailable, req.NodeID)
	}
	acc, err := b.resolver.ForNode(ctx, node, server.DataPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRemoteUnavailable, err)
	}
	if b.accessorDecorator != nil {
		acc = b.accessorDecorator(acc)
	}
	defer func() { _ = acc.Close() }()

	if b.lifecycle != nil {
		running, err := b.lifecycle.IsRunning(ctx, req.ServerID)
		if err != nil {
			return nil, err
		}
		if running && req.Window == "next_restart" {
			return nil, fmt.Errorf("server is running; next_restart window requires an operator-chosen stop")
		}
	}

	// Compatibility and disk-budget preflight happen before the journal and
	// before any byte is written. Dry-run never mutates a world.
	if err := b.preflight(ctx, server, revision, req, node.IsLocal, archivePath); err != nil {
		return nil, err
	}

	var result *BindResult
	var cleanup []string
	journal, err := operation.RunJournaledOutcome(ctx, b.store, b.log, server, "world-bind", req.RequestID, payloadHash,
		func(ownedCtx context.Context) (bool, error) {
			// The journal now owns the server. A scheduler ApplyBatch can finish
			// under its own journal between preflight and this callback, so
			// recheck the target before Stop or any file effect. Replays never
			// reach this point.
			ownedLevel, err := b.configuredLevel(ownedCtx, req.ServerID)
			if err != nil {
				return false, err
			}
			if ownedLevel != levelAtAdmission {
				return false, fmt.Errorf("%w: server level changed before world bind owned the operation",
					ErrUnsupportedWorldLayout)
			}
			out, roots, err := b.publish(ownedCtx, acc, server, req, archivePath)
			cleanup = roots
			if out != nil {
				result = out
			}
			if err != nil {
				return errors.Is(err, ErrUncertainOutcome), err
			}
			return false, nil
		})
	// Only retire the recovery trees after the journal records the decision.
	// RunJournaledOutcome can leave the lock active when finalization fails.
	if len(cleanup) > 0 {
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		op, readErr := b.store.GetServerOperation(checkCtx, journal.OperationID)
		cancel()
		wantPhase := storage.ServerOperationComplete
		if err != nil {
			wantPhase = storage.ServerOperationFailed
		}
		if readErr != nil || op.Phase != wantPhase {
			return &BindResult{Outcome: string(operation.OutcomeUnknown), OperationID: journal.OperationID, RequestID: req.RequestID},
				fmt.Errorf("%w: world swap decision was not confirmed in the journal; recovery trees preserved", ErrUncertainOutcome)
		}
		for _, root := range cleanup {
			if cleanupErr := acc.RemoveAll(root); cleanupErr != nil && b.log != nil {
				b.log.Warn("world bind: could not retire %s: %v", root, cleanupErr)
			}
		}
	}
	if err != nil {
		if errors.Is(err, operation.ErrInProgress) {
			return nil, fmt.Errorf("%w: another world operation is active", ErrUncertainOutcome)
		}
		if errors.Is(err, ErrUncertainOutcome) {
			// Report the unknown outcome rather than a retryable failure. The
			// journal row keeps its lock and carries the resolution note.
			outcome := &BindResult{Outcome: journal.Outcome, OperationID: journal.OperationID, RequestID: req.RequestID}
			if result != nil {
				outcome.Binding = result.Binding
				outcome.Consistency = result.Consistency
				if result.RequestID != "" {
					outcome.RequestID = result.RequestID
				}
			}
			return outcome, err
		}
		return nil, err
	}
	if result == nil {
		// Another controller may have recorded this operation after our initial read.
		replay, replayErr := b.replayBind(ctx, server, req)
		if replay != nil || replayErr != nil {
			return replay, replayErr
		}
		return nil, ErrBindRequestConflict
	}

	result.OperationID = journal.OperationID
	if result.RequestID == "" {
		result.RequestID = req.RequestID
	}
	return result, nil
}

func worldBindPayloadHash(server *storage.Server, req BindRequest) string {
	window := req.Window
	if window == "" {
		window = "now"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%q|%q|%q|%q|%d",
		operation.Digest("world-bind", server), req.LevelName, req.RevisionID, window, req.NodeID, req.Generation)))
	return hex.EncodeToString(sum[:])
}

// canonicalLevel resolves the authoritative target level from server config and
// normalises the request to it. Callers must hold the relevant ownership (Binder
// admission and, for a fresh operation, the shared server transition).
func (b *Binder) configuredLevel(ctx context.Context, serverID string) (string, error) {
	cfg, err := b.store.GetServerConfig(ctx, serverID)
	if err != nil {
		return "", fmt.Errorf("server world configuration not found: %w", err)
	}
	if cfg == nil {
		return "", storage.ErrServerConfigNotFound
	}
	level := cfg.ActualLevelName()
	if level == "" {
		return "", ErrBindLevelUnknown
	}
	return level, nil
}

func (b *Binder) canonicalLevel(ctx context.Context, req *BindRequest) error {
	level, err := b.configuredLevel(ctx, req.ServerID)
	if err != nil {
		return err
	}
	if requested := strings.TrimSpace(req.LevelName); requested != "" && requested != level {
		return fmt.Errorf("%w: bind level %q does not match configured level %q",
			ErrUnsupportedWorldLayout, requested, level)
	}
	req.LevelName = level
	return nil
}

// replayBind only returns success when the installed binding is attributable
// to this operation. Before payload hashes were recorded, the binding and its
// publication time are the available evidence.
func (b *Binder) replayBind(ctx context.Context, server *storage.Server, req BindRequest) (*BindResult, error) {
	var op storage.ServerOperation
	err := b.store.DB().WithContext(ctx).
		Where("server_id = ? AND request_key = ?", server.ID, operation.StorageKey(req.RequestID, "world-bind")).
		First(&op).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if op.Kind != "world-bind" {
		return nil, ErrBindRequestConflict
	}
	legacy := op.PayloadHash == operation.Digest(op.Kind, server)
	if op.Phase == storage.ServerOperationComplete {
		bindings, err := b.store.ListWorldBindings(ctx, req.ServerID, req.RevisionID)
		if err != nil {
			return nil, err
		}
		if len(bindings) != 1 {
			return nil, ErrBindRequestConflict
		}
		binding := &bindings[0]
		if (req.LevelName != "" && req.LevelName != binding.LevelName) ||
			!binding.MatchesOwner(req.NodeID, req.Generation) ||
			binding.BoundAt.Before(op.CreatedAt) || binding.BoundAt.After(op.UpdatedAt) {
			return nil, ErrBindRequestConflict
		}
		req.LevelName = binding.LevelName
		if !legacy && op.PayloadHash != worldBindPayloadHash(server, req) {
			return nil, ErrBindRequestConflict
		}
		return &BindResult{Binding: binding, Outcome: string(operation.OutcomeSuccess),
			OperationID: op.ID, RequestID: req.RequestID, Consistency: binding.Consistency}, nil
	}
	if !legacy {
		if req.LevelName == "" {
			req.LevelName, err = b.configuredLevel(ctx, req.ServerID)
			if err != nil {
				return nil, err
			}
		}
		if op.PayloadHash != worldBindPayloadHash(server, req) {
			return nil, ErrBindRequestConflict
		}
	}
	switch op.Phase {
	case storage.ServerOperationPrepared, storage.ServerOperationExecuting:
		return &BindResult{Outcome: string(operation.OutcomeUnknown), OperationID: op.ID, RequestID: req.RequestID},
			fmt.Errorf("%w: operation %s owns the server", ErrUncertainOutcome, op.ID)
	case storage.ServerOperationFailed:
		return nil, fmt.Errorf("%w: %s", operation.ErrAlreadyFailed, op.LastError)
	default:
		return nil, ErrBindRequestConflict
	}
}

// resolveNode reads locality and endpoint generation from the same node row.
// World binding only uses direct local storage for the controller's own daemon.
// Legacy aliases keep generation 1 when no usable node row exists.
func (b *Binder) resolveNode(ctx context.Context, nodeID string) (nodestorage.NodeInfo, int64, error) {
	info := nodestorage.NodeInfo{ID: nodeID, Name: nodeID, IsLocal: nodestorage.IsLegacyLocalNodeID(nodeID)}
	node, err := b.store.GetNode(ctx, nodeID)
	if err == nil && node.EndpointGeneration > 0 {
		if node.IsLocal {
			info = node.StorageInfo()
		}
		return info, node.EndpointGeneration, nil
	}
	if info.IsLocal {
		return info, 1, nil
	}
	if err != nil {
		return info, 0, fmt.Errorf("%w: node %q: %v", ErrStaleGeneration, nodeID, err)
	}
	return info, 0, fmt.Errorf("%w: node %q has no endpoint generation", ErrStaleGeneration, nodeID)
}

// preflight runs the compatibility dry run and a fail-closed disk budget check
// before any external mutation. Unknown compatibility findings are surfaced in
// the report; only blocked or unsupported transforms stop the bind.
func (b *Binder) preflight(ctx context.Context, server *storage.Server, revision *storage.WorldRevision, req BindRequest, localStorage bool, archivePath string) error {
	if b.compat != nil {
		report, err := b.compat.Check(ctx, archivePath, Target{
			ServerID: req.ServerID, MCVersion: server.MCVersion, Edition: "java",
			ModLoader: string(server.ModLoader), DataVersion: revision.DataVersion, NodeID: req.NodeID,
		})
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnknownVersion, err)
		}
		transforms, err := b.compat.DryRun(ctx, report)
		if err != nil {
			return fmt.Errorf("compatibility dry run: %w", err)
		}
		// DryRun also lists explicitly unsupported capabilities as informational
		// entries. Only a transform that this target actually proposes and
		// blocks (cross-edition change, in-place downgrade) stops a bind.
		for _, transform := range transforms {
			if transform.Status == "blocked" {
				return fmt.Errorf("%w: %s: %s", ErrUnsupported, transform.Name, transform.Reason)
			}
		}
		if report.Overall == "blocked" {
			return fmt.Errorf("%w: compatibility report for revision %s is blocked", ErrUnsupported, revision.ID)
		}
	}
	const headroom = int64(256 << 20)
	required := revision.SizeBytes*3 + headroom
	var free int64
	var err error
	if b.FreeBytes != nil {
		free, err = b.FreeBytes(ctx, req.NodeID, server.DataPath)
	} else if localStorage {
		free, err = files.GetFreeDiskSpace(server.DataPath)
	} else {
		return fmt.Errorf("%w: node %q free bytes are not measurable from the controller; inject a FreeBytes probe", ErrDiskBudget, req.NodeID)
	}
	if err != nil {
		return fmt.Errorf("%w: node %q free bytes unavailable: %v", ErrDiskBudget, req.NodeID, err)
	}
	if free < required {
		return fmt.Errorf("%w: need %d bytes (archive, clone, undo and headroom), node has %d", ErrDiskBudget, required, free)
	}
	return nil
}

func (b *Binder) publish(ctx context.Context, acc nodestorage.Accessor, server *storage.Server, req BindRequest, archivePath string) (out *BindResult, cleanup []string, retErr error) {
	wasRunning := false
	var err error
	if b.lifecycle != nil {
		wasRunning, err = b.lifecycle.IsRunning(ctx, req.ServerID)
		if err != nil {
			return nil, nil, err
		}
	}

	// Quiesce BEFORE stopping the server: save-off/save-all flush need a live
	// JVM. Resuming saves while it is still running avoids leaving a stopped
	// server with saves off, and the flush is already on disk when we stop.
	consistency := snapshot.ConsistencyVerifiedStopped
	if wasRunning && b.sender != nil {
		resume, level := snapshot.QuiesceWorld(ctx, b.sender, b.log, server)
		consistency = level
		if err := resume(); err != nil && b.log != nil {
			b.log.Warn("world bind: save-on before stop failed: %v", err)
		}
	}
	if wasRunning && b.lifecycle != nil {
		if err := b.lifecycle.StopServer(ctx, req.ServerID); err != nil {
			return nil, nil, fmt.Errorf("%w: cannot confirm stop before world swap: %v", ErrUncertainOutcome, err)
		}
		// Restore the original running intent after success or a proven failure.
		// A disconnected caller cannot cancel this recovery action.
		defer func() {
			if errors.Is(retErr, ErrUncertainOutcome) {
				return
			}
			restartCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
			defer cancel()
			if err := b.lifecycle.StartServer(restartCtx, req.ServerID); err != nil {
				cleanup = nil
				retErr = fmt.Errorf("%w: restart after world swap could not be verified: %w",
					ErrUncertainOutcome, errors.Join(retErr, err))
			}
		}()

	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	// Stage on the owning node through its accessor. For remote nodes this is
	// the bounded W17 transport; there is no controller-local fallback.
	stageID := uuid.NewString()
	stageRoot := path.Join(".world-library", "stage-"+stageID)
	oldRoot := path.Join(".world-library", "old-"+stageID)
	extractRoot := path.Join(stageRoot, "extracted")
	if err := acc.MkdirAll(stageRoot); err != nil {
		return nil, nil, err
	}
	archiveRel := path.Join(stageRoot, "revision.zip")
	if err := copyLocalArchiveToAccessor(ctx, acc, archivePath, archiveRel); err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, nil, err
	}
	if _, err := nodestorage.ExtractZip(ctx, acc, archiveRel, extractRoot, nil); err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, nil, fmt.Errorf("%w: %v", ErrArchiveRejected, err)
	}
	if err := ctx.Err(); err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, nil, err
	}

	families, err := stageFamilies(acc, extractRoot, req.LevelName)
	if err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, nil, err
	}
	if len(families) == 0 {
		_ = acc.RemoveAll(stageRoot)
		return nil, nil, fmt.Errorf("%w: archive has no usable level.dat family", ErrArchiveRejected)
	}

	next := &storage.WorldBinding{
		ID: "wlb_" + uuid.NewString(), ServerID: req.ServerID, LevelName: req.LevelName,
		RevisionID: req.RevisionID, NodeID: req.NodeID, Generation: req.Generation,
		Consistency: string(consistency), BoundAt: time.Now().UTC(),
	}
	if req.Generation <= 0 || req.NodeID == "" {
		return nil, nil, fmt.Errorf("%w: node generation must be known before publish", ErrStaleGeneration)
	}
	// Record which live folders exist before the first rename. Hash an old
	// level.dat if it exists, without reading the file into memory.
	oldDigests := make(map[string]string, len(families))
	for _, family := range families {
		info, err := acc.Stat(family)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() {
			_ = acc.RemoveAll(stageRoot)
			return nil, nil, fmt.Errorf("cannot inspect live world family %s: %v", family, err)
		}
		digest, err := levelFamilyDigest(ctx, acc, family)
		if err != nil {
			_ = acc.RemoveAll(stageRoot)
			return nil, nil, fmt.Errorf("cannot fingerprint live world family %s: %w", family, err)
		}
		oldDigests[family] = digest
	}

	// One transaction swaps the binding row, so there is no window where the
	// (server_id, level_name) unique index blocks the new revision.
	if err := b.store.SwapWorldBinding(ctx, req.Previous, next); err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, nil, err
	}
	binding := next

	// Atomic family publish with data-preserving compensation.
	//
	// Retained old bytes live in oldRoot, promoted new bytes that must be undone
	// are quarantined in quarantineRoot, and NEITHER is removed until every
	// filesystem step and the durable binding restore have been verified. Any
	// ambiguous I/O leaves both trees in place and returns ErrUncertainOutcome
	// so the journal keeps its lock for operator recovery.
	quarantineRoot := path.Join(".world-library", "quarantine-"+stageID)
	movedOld := []string{}
	movedNew := []string{}
	compensate := func(cause error) (*BindResult, []string, error) {
		uncertain := func(detail string, args ...any) (*BindResult, []string, error) {
			reason := fmt.Sprintf(detail, args...)
			return nil, nil, fmt.Errorf("%w: %s; retained old tree %s and quarantined new tree %s are preserved for operator recovery: %v",
				ErrUncertainOutcome, reason, oldRoot, quarantineRoot, cause)
		}

		// 1. Move every already-promoted world out of the live path. Renaming a
		// non-empty directory onto another would silently fail, so new bytes go
		// to their own quarantine instead of being deleted or mixed with old.
		for i := len(movedNew) - 1; i >= 0; i-- {
			family := movedNew[i]
			if err := acc.MkdirAll(quarantineRoot); err != nil {
				return uncertain("cannot create quarantine for %s: %v", family, err)
			}
			if err := acc.Rename(family, path.Join(quarantineRoot, family)); err != nil {
				return uncertain("cannot quarantine promoted family %s: %v", family, err)
			}
		}

		// 2. Restore each retained old family from its immutable copy.
		for i := len(movedOld) - 1; i >= 0; i-- {
			family := movedOld[i]
			if err := acc.Rename(path.Join(oldRoot, family), family); err != nil {
				return uncertain("cannot restore retained family %s: %v", family, err)
			}
		}

		// 3. Verify the restored paths and identity files before changing the binding.
		checkCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		for _, family := range movedOld {
			info, err := acc.Stat(family)
			if err != nil || !info.IsDir() {
				return uncertain("restored family %s is missing", family)
			}
			got, err := levelFamilyDigest(checkCtx, acc, family)
			if err != nil || got != oldDigests[family] {
				return uncertain("restored family %s level.dat differs from the retained original", family)
			}
			if _, err := acc.Stat(path.Join(oldRoot, family)); !errors.Is(err, os.ErrNotExist) {
				return uncertain("cannot confirm that %s moved out of the retained tree", family)
			}
		}
		for _, family := range movedNew {
			if _, err := acc.Stat(path.Join(quarantineRoot, family, "level.dat")); err != nil {
				return uncertain("quarantined family %s has no level.dat", family)
			}
		}

		// 4. Persist the rollback binding decision in a bounded independent
		// context. A refused hook or store error is ambiguous: the binding row
		// stays on the new revision and the old bytes stay retained.
		if b.rollbackBindingHook != nil {
			if err := b.rollbackBindingHook(); err != nil {
				return uncertain("binding restore was refused: %v", err)
			}
		}
		restoreCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if req.Previous != nil {
			if err := b.store.SwapWorldBinding(restoreCtx, next, req.Previous); err != nil {
				return uncertain("cannot restore prior binding: %v", err)
			}
		} else {
			if err := b.store.DeleteWorldBinding(restoreCtx, next.ID); err != nil {
				return uncertain("cannot remove new binding: %v", err)
			}
		}

		// 5. Re-read the durable decision. Never report a rollback before the
		// row is proven to name the prior binding again.
		if req.Previous != nil {
			current, err := b.store.GetWorldBinding(restoreCtx, req.ServerID, req.LevelName)
			if err != nil || current.ID != req.Previous.ID || current.RevisionID != req.Previous.RevisionID {
				return uncertain("binding row does not name the prior binding after restore")
			}
		} else if _, err := b.store.GetWorldBinding(restoreCtx, req.ServerID, req.LevelName); !errors.Is(err, gorm.ErrRecordNotFound) {
			return uncertain("binding row is still present after rollback")
		}

		return nil, []string{quarantineRoot, stageRoot, oldRoot}, cause
	}

	// A lost reply can be reported as a rename error after the owning node
	// applied it. Compensate only when both endpoints still show the prior
	// state. Otherwise retain every known copy for recovery under the lock.
	renameFailed := func(src, dst string, cause error) (*BindResult, []string, error) {
		if _, srcErr := acc.Stat(src); srcErr == nil {
			if _, dstErr := acc.Stat(dst); errors.Is(dstErr, os.ErrNotExist) {
				return compensate(cause)
			}
		}
		return nil, nil, fmt.Errorf("%w: cannot confirm rename %s to %s; recovery trees preserved: %v",
			ErrUncertainOutcome, src, dst, cause)
	}
	for _, family := range families {
		if _, existed := oldDigests[family]; existed {
			if err := acc.MkdirAll(oldRoot); err != nil {
				return compensate(err)
			}
			retained := path.Join(oldRoot, family)
			if err := acc.Rename(family, retained); err != nil {
				return renameFailed(family, retained, err)
			}
			movedOld = append(movedOld, family)
		}
		staged := path.Join(extractRoot, family)
		if err := acc.Rename(staged, family); err != nil {
			return renameFailed(staged, family, err)
		}
		movedNew = append(movedNew, family)
	}

	if err := verifyFamilies(acc, families); err != nil {
		return compensate(err)
	}

	return &BindResult{Binding: binding, RequestID: req.RequestID, Outcome: "success", Consistency: string(consistency)},
		[]string{stageRoot, oldRoot}, nil
}

// Unbind removes the binding row but never deletes world bytes. Admission is
// taken before the row is read, and the row is deleted only inside a journaled
// operation, so a replayed request_id cannot interleave with a bind. A replay
// of an already completed request is idempotent even when the binding row is no
// longer present.
func (b *Binder) Unbind(ctx context.Context, serverID, level, requestID string) (bool, error) {
	if requestID == "" {
		requestID = "world-unbind-" + uuid.NewString()
	}
	server, err := b.store.GetServer(ctx, serverID)
	if err != nil {
		return false, err
	}
	requestKey := operation.StorageKey(requestID, "world-unbind")
	releaseServer, err := b.admission.Acquire(ctx, "server:"+serverID)
	if err != nil {
		return false, err
	}
	defer releaseServer()
	binding, err := b.store.GetWorldBinding(ctx, serverID, level)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			var op storage.ServerOperation
			if qerr := b.store.DB().WithContext(ctx).
				Where("server_id = ? AND request_key = ?", serverID, requestKey).
				First(&op).Error; qerr == nil && op.Kind == "world-unbind" && op.Phase == storage.ServerOperationComplete {
				return false, nil
			}
		}
		return false, err
	}
	releaseRevision, err := b.admission.Acquire(ctx, "revision:"+binding.RevisionID)
	if err != nil {
		return false, err
	}
	defer releaseRevision()

	removed := false
	_, err = operation.RunJournaledOutcome(ctx, b.store, b.log, server, "world-unbind", requestID, "",
		func(context.Context) (bool, error) {
			current, err := b.store.GetWorldBinding(ctx, serverID, level)
			if err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return false, nil
				}
				return false, err
			}
			if err := b.store.DeleteWorldBinding(ctx, current.ID); err != nil {
				return false, err
			}
			removed = true
			return false, nil
		})
	if err != nil {
		return false, err
	}
	return removed, nil
}

// Recover observes real node state after an interrupted bind. It restores a
// retained old level family only when the live family is missing, preserves
// staged and quarantined new bytes for operator inspection, and refuses
// ambiguous states for operator resolution. It never deletes a retained old
// tree and never auto-retries the mutation itself.
func (b *Binder) Recover(ctx context.Context, serverID, dataRoot, nodeID string, generation int64) (string, error) {
	if serverID == "" || dataRoot == "" {
		return "", errors.New("recovery requires a server and data root")
	}
	if generation <= 0 {
		return "", fmt.Errorf("%w: node generation is unknown", ErrStaleGeneration)
	}
	node, currentGeneration, err := b.resolveNode(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if generation != currentGeneration {
		return "", fmt.Errorf("%w: node generation changed before world recovery", ErrStaleGeneration)
	}
	if !node.IsLocal && b.resolver == nil {
		return "", fmt.Errorf("%w: no storage resolver for node %q", ErrRemoteUnavailable, nodeID)
	}
	acc, err := b.resolver.ForNode(ctx, node, dataRoot)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrRemoteUnavailable, err)
	}
	defer func() { _ = acc.Close() }()

	entries, err := acc.ReadDir(".world-library")
	if err != nil {
		if os.IsNotExist(err) {
			return "no world recovery state", nil
		}
		return "", err
	}
	if len(entries) > 0 {
		if err := b.store.AssertNoServerOperation(ctx, serverID); err != nil {
			return "", fmt.Errorf("%w: %v; preserve world recovery trees until the operation is resolved", ErrUncertainOutcome, err)
		}
	}
	restored := 0
	preserved := 0
	ambiguous := []string{}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, "old-"):
			oldRoot := path.Join(".world-library", name)
			families, listErr := acc.ReadDir(oldRoot)
			if listErr != nil {
				ambiguous = append(ambiguous, name+": unreadable recovery tree")
				continue
			}
			for _, family := range families {
				if !family.IsDir() {
					continue
				}
				familyName := family.Name()
				_, liveErr := acc.Stat(familyName)
				_, oldErr := acc.Stat(path.Join(oldRoot, familyName))
				switch {
				case errors.Is(liveErr, os.ErrNotExist) && oldErr == nil:
					if renameErr := acc.Rename(path.Join(oldRoot, familyName), familyName); renameErr != nil {
						return "", fmt.Errorf("restore %s from %s: %w", familyName, name, renameErr)
					}
					restored++
				case liveErr == nil && oldErr == nil:
					ambiguous = append(ambiguous, name+"/"+familyName+": both live and recovery trees exist")
				default:
					ambiguous = append(ambiguous, name+"/"+familyName+": cannot verify live and retained paths")
				}
			}
		case strings.HasPrefix(name, "stage-"), strings.HasPrefix(name, "quarantine-"):
			// Staged and quarantined bytes are new-world evidence. Recovery is
			// read-only for them: an operator may need to inspect or copy them
			// before deciding, so never delete them here.
			preserved++
		}
	}
	if len(ambiguous) > 0 {
		return "", fmt.Errorf("%w: %s", ErrUncertainOutcome, strings.Join(ambiguous, "; "))
	}
	return fmt.Sprintf("restored %d level families, preserved %d staged or quarantined trees", restored, preserved), nil
}

// ResolveOperation records an operator decision on an uncertain operation. It
// never retries the mutation itself.
func (b *Binder) ResolveOperation(ctx context.Context, serverID, operationID, note string) error {
	release, err := b.admission.Acquire(ctx, "server:"+serverID)
	if err != nil {
		return err
	}
	defer release()
	op, err := b.store.GetServerOperation(ctx, operationID)
	if err != nil {
		return err
	}
	if op.ServerID != serverID {
		return errors.New("operation does not belong to this server")
	}
	return b.store.ResolveServerOperation(ctx, operationID, note)
}

func copyLocalArchiveToAccessor(ctx context.Context, acc nodestorage.Accessor, localPath, destRel string) error {
	if localPath == "" {
		return errors.New("revision has no storage path")
	}
	src, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	const maxArchive = int64(512 << 20)
	if info.Size() > maxArchive {
		return fmt.Errorf("%w: revision archive exceeds %d bytes", ErrArchiveRejected, maxArchive)
	}
	_, err = nodestorage.WriteFrom(ctx, acc, destRel, src, info.Size())
	return err
}

// stageFamilies uses WorldFamilyFromMarker, the single bounded definition of a
// publishable level family shared with import and preview. It also resolves the
// configured target BEFORE the caller swaps the binding or moves live data.
func stageFamilies(acc nodestorage.Accessor, stageRoot, levelName string) ([]string, error) {
	entries, err := acc.ReadDir(stageRoot)
	if err != nil {
		return nil, err
	}
	families := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || WorldFamilyFromMarker(path.Join(entry.Name(), "level.dat")) == "" {
			continue
		}
		// A directory is a world family only when its actual direct marker is a
		// regular, non-empty file. Ordinary backup companions (config/, mods/)
		// must never be published as worlds.
		marker, err := acc.Stat(path.Join(stageRoot, entry.Name(), "level.dat"))
		if err != nil || !marker.Mode().IsRegular() || marker.Size() == 0 {
			continue
		}
		families = append(families, entry.Name())
	}
	if len(families) == 0 {
		// Preserve the legacy exact-name fallback: a configured level folder
		// with an actual direct level.dat is usable even if the directory
		// listing was unavailable at extraction time.
		if WorldFamilyFromMarker(path.Join(levelName, "level.dat")) != "" {
			if marker, err := acc.Stat(path.Join(stageRoot, levelName, "level.dat")); err == nil &&
				marker.Mode().IsRegular() && marker.Size() > 0 {
				families = append(families, levelName)
			}
		}
	}
	sort.Strings(families)
	return SelectWorldFamilies(families, levelName)
}

// levelFamilyDigest fingerprints the optional level.dat for rollback verification.
func levelFamilyDigest(ctx context.Context, acc nodestorage.Accessor, family string) (string, error) {
	file, err := acc.OpenRead(ctx, path.Join(family, "level.dat"))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifyFamilies(acc nodestorage.Accessor, families []string) error {
	for _, family := range families {
		info, err := acc.Stat(path.Join(family, "level.dat"))
		if err != nil || info.Size() == 0 {
			return fmt.Errorf("%w: published family %q has no level.dat", ErrArchiveRejected, family)
		}
	}
	return nil
}
