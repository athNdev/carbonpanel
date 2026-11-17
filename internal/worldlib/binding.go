// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"context"
	"errors"
	"fmt"
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
}

// BindResult reports the persisted binding and the journal outcome.
type BindResult struct {
	Binding     *storage.WorldBinding
	OperationID string
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
}

// NewBinder constructs a binder. A nil resolver is legal for local-only nodes
// but remote nodes fail closed with nodestorage.ErrUnavailable.
func NewBinder(store *storage.Store, log *logger.Logger, resolver *nodestorage.Resolver, sender CommandSender, lifecycle Lifecycle, libraryRoot string) *Binder {
	return &Binder{
		store: store, log: log, resolver: resolver, sender: sender,
		lifecycle: lifecycle, admission: NewKeyedAdmission(), libraryRoot: libraryRoot,
	}
}

// Bind executes a bind or swap. No live bytes are touched before admission,
// strict node resolution and cancellation checks have all succeeded.
func (b *Binder) Bind(ctx context.Context, req BindRequest) (*BindResult, error) {
	if req.ServerID == "" || req.LevelName == "" || req.RevisionID == "" {
		return nil, errors.New("bind requires a server, level and revision")
	}
	if strings.ContainsAny(req.LevelName, `/\:`) || req.LevelName == "." || req.LevelName == ".." {
		return nil, fmt.Errorf("%w: invalid level name", ErrArchiveRejected)
	}
	if req.Window != "" && req.Window != "now" && req.Window != "next_restart" && req.Window != "idle" {
		return nil, fmt.Errorf("unknown bind window %q", req.Window)
	}
	if req.RequestID == "" {
		req.RequestID = "world-bind-" + uuid.NewString()
	}

	server, err := b.store.GetServer(ctx, req.ServerID)
	if err != nil {
		return nil, err
	}
	revision, err := b.store.GetWorldRevision(ctx, req.RevisionID)
	if err != nil {
		return nil, err
	}
	if revision.NodeID != "" && revision.NodeID != req.NodeID {
		return nil, fmt.Errorf("%w: revision is stored on node %q", ErrRemoteUnavailable, revision.NodeID)
	}
	if existing, err := b.store.GetWorldBinding(ctx, req.ServerID, req.LevelName); err == nil {
		if existing.RevisionID == req.RevisionID {
			return &BindResult{Binding: existing, Outcome: "success", Consistency: existing.Consistency}, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	release, err := b.admission.Acquire(ctx, "server:"+req.ServerID, "revision:"+req.RevisionID)
	if err != nil {
		return nil, err
	}
	defer release()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	node := nodestorage.NodeInfo{
		ID: req.NodeID, Name: req.NodeID,
		IsLocal: nodestorage.IsLegacyLocalNodeID(req.NodeID),
	}
	if !node.IsLocal && b.resolver == nil {
		return nil, fmt.Errorf("%w: no storage resolver for node %q", ErrRemoteUnavailable, req.NodeID)
	}
	acc, err := b.resolver.ForNode(ctx, node, server.DataPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRemoteUnavailable, err)
	}
	defer acc.Close()

	if b.lifecycle != nil {
		running, err := b.lifecycle.IsRunning(ctx, req.ServerID)
		if err != nil {
			return nil, err
		}
		if running && req.Window == "next_restart" {
			return nil, fmt.Errorf("server is running; next_restart window requires an operator-chosen stop")
		}
	}

	var result *BindResult
	err = operation.RunJournaled(ctx, b.store, b.log, server, "world-bind", req.RequestID, func() error {
		out, err := b.publish(ctx, acc, server, req, revision)
		if err != nil {
			return err
		}
		result = out
		return nil
	})
	if err != nil {
		if errors.Is(err, operation.ErrInProgress) {
			return nil, fmt.Errorf("%w: another world operation is active", ErrUncertainOutcome)
		}
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("%w: journal replay did not return a binding", ErrUncertainOutcome)
	}
	return result, nil
}

func (b *Binder) publish(ctx context.Context, acc nodestorage.Accessor, server *storage.Server, req BindRequest, revision *storage.WorldRevision) (*BindResult, error) {
	wasRunning := false
	var err error
	if b.lifecycle != nil {
		wasRunning, err = b.lifecycle.IsRunning(ctx, req.ServerID)
		if err != nil {
			return nil, err
		}
		if wasRunning {
			if err := b.lifecycle.StopServer(ctx, req.ServerID); err != nil {
				return nil, fmt.Errorf("stop before world swap: %w", err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	consistency := snapshot.ConsistencyVerifiedStopped
	resume := func() error { return nil }
	if wasRunning && b.sender != nil {
		resume, consistency = snapshot.QuiesceWorld(ctx, b.sender, b.log, server)
		defer func() {
			if err := resume(); err != nil {
				if b.log != nil {
					b.log.Warn("world bind: save-on failed: %v", err)
				}
			}
		}()
	}

	// Stage on the owning node through its accessor. For remote nodes this is
	// the bounded W17 transport; there is no controller-local fallback.
	stageID := uuid.NewString()
	stageRoot := path.Join(".world-library", "stage-"+stageID)
	oldRoot := path.Join(".world-library", "old-"+stageID)
	if err := acc.MkdirAll(stageRoot); err != nil {
		return nil, err
	}
	archiveRel := path.Join(stageRoot, "revision.zip")
	if err := copyLocalArchiveToAccessor(ctx, acc, revision.StoragePath, archiveRel); err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, err
	}
	if _, err := nodestorage.ExtractZip(ctx, acc, archiveRel, stageRoot, nil); err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, fmt.Errorf("%w: %v", ErrArchiveRejected, err)
	}
	if err := ctx.Err(); err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, err
	}

	families, err := stageFamilies(acc, stageRoot, req.LevelName)
	if err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, err
	}
	if len(families) == 0 {
		_ = acc.RemoveAll(stageRoot)
		return nil, fmt.Errorf("%w: archive has no level.dat family", ErrArchiveRejected)
	}

	bindingID := "wlb_" + uuid.NewString()
	binding := &storage.WorldBinding{
		ID: bindingID, ServerID: req.ServerID, LevelName: req.LevelName,
		RevisionID: req.RevisionID, NodeID: req.NodeID, Generation: req.Generation,
		Consistency: string(consistency), BoundAt: time.Now().UTC(),
	}
	if req.Generation <= 0 || req.NodeID == "" {
		return nil, fmt.Errorf("%w: node generation must be known before publish", ErrStaleGeneration)
	}
	if err := b.store.CreateWorldBinding(ctx, binding); err != nil {
		_ = acc.RemoveAll(stageRoot)
		return nil, err
	}

	// Atomic family publish: move each live folder aside, promote the staged
	// folder, and roll back if any promotion fails. The old tree is retained
	// until verification succeeds.
	movedOld := []string{}
	movedNew := []string{}
	rollback := func() {
		for i := len(movedNew) - 1; i >= 0; i-- {
			_ = acc.Rename(path.Join(oldRoot, movedNew[i]), movedNew[i])
		}
		_ = acc.RemoveAll(oldRoot)
		_ = acc.RemoveAll(stageRoot)
	}
	for _, family := range families {
		if _, statErr := acc.Stat(family); statErr == nil {
			if err := acc.MkdirAll(oldRoot); err != nil {
				_ = b.store.DeleteWorldBinding(ctx, bindingID)
				rollback()
				return nil, err
			}
			if err := acc.Rename(family, path.Join(oldRoot, family)); err != nil {
				_ = b.store.DeleteWorldBinding(ctx, bindingID)
				rollback()
				return nil, err
			}
			movedOld = append(movedOld, family)
		}
		if err := acc.Rename(path.Join(stageRoot, family), family); err != nil {
			_ = b.store.DeleteWorldBinding(ctx, bindingID)
			rollback()
			return nil, err
		}
		movedNew = append(movedNew, family)
	}

	if err := verifyFamilies(acc, families); err != nil {
		_ = b.store.DeleteWorldBinding(ctx, bindingID)
		rollback()
		return nil, err
	}

	// An ambiguous outcome after an external mutation must not be auto-retried.
	// Keep the old tree until the restart and verification have been observed.
	if wasRunning && b.lifecycle != nil {
		if err := b.lifecycle.StartServer(ctx, req.ServerID); err != nil {
			return nil, fmt.Errorf("%w: restart after swap failed; recovery tree at %s: %v", ErrUncertainOutcome, oldRoot, err)
		}
	}

	// Cleanup uses the node accessor directly; the operation journal has
	// already finalized in its own bounded independent context.
	_ = acc.RemoveAll(stageRoot)
	if len(movedOld) > 0 {
		_ = acc.RemoveAll(oldRoot)
	}

	return &BindResult{Binding: binding, Outcome: "success", Consistency: string(consistency)}, nil
}

// Unbind removes the binding row but never deletes world bytes.
func (b *Binder) Unbind(ctx context.Context, serverID, level, requestID string) (bool, error) {
	binding, err := b.store.GetWorldBinding(ctx, serverID, level)
	if err != nil {
		return false, err
	}
	release, err := b.admission.Acquire(ctx, "server:"+serverID, "revision:"+binding.RevisionID)
	if err != nil {
		return false, err
	}
	defer release()
	if err := b.store.DeleteWorldBinding(ctx, binding.ID); err != nil {
		return false, err
	}
	return true, nil
}

// ResolveOperation records an operator decision on an uncertain operation. It
// never retries the mutation itself.
func (b *Binder) ResolveOperation(ctx context.Context, serverID, operationID, note string) error {
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
	defer src.Close()
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

func stageFamilies(acc nodestorage.Accessor, stageRoot, levelName string) ([]string, error) {
	entries, err := acc.ReadDir(stageRoot)
	if err != nil {
		return nil, err
	}
	families := []string{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if _, err := acc.Stat(path.Join(stageRoot, entry.Name(), "level.dat")); err == nil {
			families = append(families, entry.Name())
		}
	}
	if len(families) == 0 {
		if _, err := acc.Stat(path.Join(stageRoot, levelName, "level.dat")); err == nil {
			families = append(families, levelName)
		}
	}
	sort.Strings(families)
	return families, nil
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

