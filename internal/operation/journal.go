// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package operation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrInProgress is returned when the same request id is already running.
var ErrInProgress = errors.New("matching lifecycle operation is already in progress")

// ErrAlreadyFailed is returned when the same request id already failed.
// A new request id is required to try again.
var ErrAlreadyFailed = errors.New("matching lifecycle operation already failed")

// ValidateRequestID rejects client request ids that do not fit the journal.
func ValidateRequestID(id string) error {
	if len(strings.TrimSpace(id)) > 128 {
		return errors.New("request_id must be at most 128 characters")
	}
	return nil
}

// StorageKey is the journal key for a client request id and mutation kind.
// An empty request id is unique to this call, so it is not idempotent.
func StorageKey(requestID, kind string) string {
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return uuid.NewString()
	}
	return requestID + ":" + kind
}

// Journal is the operation journal subset that RunJournaled needs.
// *db.Store satisfies it.
type Journal interface {
	Store
	BeginServerOperation(ctx context.Context, serverID, nodeID, containerID, key, kind, digest string) (*db.ServerOperation, bool, error)
}

// RunJournaled persists the intent of a server mutation before it is
// attempted, advances the journal through the attempt, and finalizes it.
// requestID is a client idempotency key: a repeat of a completed key does
// not run mutate again, and a repeat of a key that is still running returns
// ErrInProgress. An empty requestID is unique to this call.
//
// If the process dies between the external effect and finalization, the entry
// stays active and startup recovery observes the container before releasing
// the per-server lock (see RecoverInterrupted). The active lock is the shared
// coordinator: no two lifecycle mutations of one world can interleave. A
// mutation error finalizes as failed so the user can retry without restarting
// the panel.
func RunJournaled(ctx context.Context, store Journal, log *logger.Logger, server *db.Server, kind, requestID string, mutate func() error) error {
	if err := ValidateRequestID(requestID); err != nil {
		return err
	}
	digest := Digest(kind, server)
	op, reused, err := store.BeginServerOperation(ctx, server.ID, server.NodeID, server.ContainerID, StorageKey(requestID, kind), kind, digest)
	if err != nil {
		// Another active operation on the server is the same "in progress"
		// condition as a reused running request, so callers can use one
		// retryable check instead of knowing which uniqueness path won.
		if errors.Is(err, db.ErrActiveOperation) {
			return fmt.Errorf("%w: %w", ErrInProgress, err)
		}
		// Failing to persist intent means we must not perform the mutation.
		return fmt.Errorf("record %s intent: %w", kind, err)
	}
	if reused {
		switch op.Phase {
		case db.ServerOperationComplete:
			return nil
		case db.ServerOperationFailed:
			if op.LastError != "" {
				return fmt.Errorf("%w: %s", ErrAlreadyFailed, op.LastError)
			}
			return ErrAlreadyFailed
		case db.ServerOperationPrepared, db.ServerOperationExecuting:
			return fmt.Errorf("%w: %s", ErrInProgress, op.ID)
		default:
			return fmt.Errorf("lifecycle operation %s is in phase %s; retry with a new request_id", op.ID, op.Phase)
		}
	}

	if err := store.AdvanceServerOperation(ctx, op.ID, op.Phase, db.ServerOperationExecuting, ""); err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = store.AdvanceServerOperation(releaseCtx, op.ID, op.Phase, db.ServerOperationFailed, "could not enter executing phase: "+err.Error())
		return fmt.Errorf("record %s attempt: %w", kind, err)
	}
	return finishMutation(ctx, store, log, op.ID, kind, mutate)
}

// JournalOutcome reports the durable outcome of a journaled mutation. Outcome
// is "success" or "unknown"; an unknown effect keeps the server lock and is
// never presented as a retryable failure.
type JournalOutcome struct {
	OperationID string
	Outcome     string
}

// RunJournaledOutcome is RunJournaled for mutations whose external effect may
// be uncertain (for example a world swap whose restart could not be observed).
// When mutate reports uncertain=true with an error, the journal row keeps its
// active server lock and records an explicit operator-resolution note. It does
// not advance to failed, so the UI cannot offer a blind retry. A completion or
// a definite failure follows the same path as RunJournaled.
func RunJournaledOutcome(ctx context.Context, store Journal, log *logger.Logger, server *db.Server, kind, requestID, payloadHash string, mutate func(context.Context) (uncertain bool, err error)) (JournalOutcome, error) {
	if err := ValidateRequestID(requestID); err != nil {
		return JournalOutcome{}, err
	}
	if payloadHash == "" {
		payloadHash = Digest(kind, server)
	}
	op, reused, err := store.BeginServerOperation(ctx, server.ID, server.NodeID, server.ContainerID, StorageKey(requestID, kind), kind, payloadHash)
	if err != nil {
		if errors.Is(err, db.ErrActiveOperation) {
			return JournalOutcome{}, fmt.Errorf("%w: %w", ErrInProgress, err)
		}
		return JournalOutcome{}, fmt.Errorf("record %s intent: %w", kind, err)
	}
	if reused {
		switch op.Phase {
		case db.ServerOperationComplete:
			return JournalOutcome{OperationID: op.ID, Outcome: string(OutcomeSuccess)}, nil
		case db.ServerOperationFailed:
			if op.LastError != "" {
				return JournalOutcome{OperationID: op.ID}, fmt.Errorf("%w: %s", ErrAlreadyFailed, op.LastError)
			}
			return JournalOutcome{OperationID: op.ID}, ErrAlreadyFailed
		case db.ServerOperationPrepared, db.ServerOperationExecuting:
			return JournalOutcome{OperationID: op.ID}, fmt.Errorf("%w: %s", ErrInProgress, op.ID)
		default:
			return JournalOutcome{OperationID: op.ID}, fmt.Errorf("lifecycle operation %s is in phase %s; retry with a new request_id", op.ID, op.Phase)
		}
	}
	if err := store.AdvanceServerOperation(ctx, op.ID, op.Phase, db.ServerOperationExecuting, ""); err != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = store.AdvanceServerOperation(releaseCtx, op.ID, op.Phase, db.ServerOperationFailed, "could not enter executing phase: "+err.Error())
		return JournalOutcome{OperationID: op.ID}, fmt.Errorf("record %s attempt: %w", kind, err)
	}

	uncertain, mutateErr := mutate(context.WithValue(ctx, journalOwnerKey{}, op.ID))
	if mutateErr == nil {
		if err := finishMutation(ctx, store, log, op.ID, kind, func() error { return nil }); err != nil {
			return JournalOutcome{OperationID: op.ID, Outcome: string(OutcomeSuccess)}, err
		}
		return JournalOutcome{OperationID: op.ID, Outcome: string(OutcomeSuccess)}, nil
	}
	if uncertain {
		noteCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		detail := UncertainOutcomeMarker + "; confirm the live world on the node before starting the server. " + mutateErr.Error()
		if noteErr := store.NoteActiveServerOperation(noteCtx, op.ID, detail); noteErr != nil && log != nil {
			log.Error("Failed to record uncertain %s operation %s: %v", kind, op.ID, noteErr)
		}
		return JournalOutcome{OperationID: op.ID, Outcome: string(OutcomeUnknown)}, mutateErr
	}
	return JournalOutcome{OperationID: op.ID}, finishMutation(ctx, store, log, op.ID, kind, func() error { return mutateErr })
}

type journalOwnerKey struct{}

// AssertWorldBindOwner validates the live journal capability carried only
// inside a running mutation callback. An RPC request ID confers no ownership.
func AssertWorldBindOwner(ctx context.Context, store *db.Store, server *db.Server) error {
	id, _ := ctx.Value(journalOwnerKey{}).(string)
	if id == "" {
		return errors.New("world lifecycle requires the active bind owner")
	}
	op, err := store.GetServerOperation(ctx, id)
	if err != nil {
		return err
	}
	if op.Kind != "world-bind" || op.Phase != db.ServerOperationExecuting ||
		op.ActiveServer == nil || *op.ActiveServer != server.ID || op.ServerID != server.ID ||
		op.NodeID != server.NodeID || op.ContainerID != server.ContainerID {
		return errors.New("world bind no longer owns this container")
	}
	node, err := store.GetNode(ctx, server.NodeID)
	if errors.Is(err, gorm.ErrRecordNotFound) && op.EndpointGeneration == 0 && op.DaemonID == "" {
		return nil // Legacy local nodes may have no persisted identity.
	}
	if err != nil {
		return err
	}
	if node.EndpointGeneration != op.EndpointGeneration ||
		(op.DaemonID != "" && node.DaemonID != op.DaemonID) {
		return errors.New("world bind no longer owns this node endpoint")
	}
	return nil
}

func finishMutation(ctx context.Context, store Journal, log *logger.Logger, id, kind string, mutate func() error) error {
	if mutateErr := mutate(); mutateErr != nil {
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if advErr := store.AdvanceServerOperation(releaseCtx, id, db.ServerOperationExecuting, db.ServerOperationFailed, mutateErr.Error()); advErr != nil {
			if log != nil {
				log.Error("Failed to finalize %s operation %s after error: %v", kind, id, advErr)
			}
		}
		return mutateErr
	}

	finalizeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := store.AdvanceServerOperation(finalizeCtx, id, db.ServerOperationExecuting, db.ServerOperationComplete, ""); err != nil {
		// The mutation succeeded. The entry stays active and startup recovery
		// observes the container before releasing the lock, rather than a retry
		// acting on an unknown outcome.
		if log != nil {
			log.Error("Failed to record completion of %s operation %s: %v", kind, id, err)
		}
	}
	return nil
}

// Digest is the request payload fingerprint for a journaled mutation.
func Digest(kind string, server *db.Server) string {
	sum := sha256.Sum256([]byte(kind + "|" + server.ID + "|" + server.NodeID + "|" + server.ContainerID))
	return hex.EncodeToString(sum[:])
}
