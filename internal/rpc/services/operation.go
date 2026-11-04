// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

var _ carbonpanelv1connect.OperationServiceHandler = (*OperationService)(nil)

// maxResolveNote bounds the operator note stored on a released lock. The
// journal column is short and an unbounded note would let a caller write a
// megabyte into the database through a diagnostic endpoint.
const maxResolveNote = 500

// OperationService reads and releases the lifecycle operation journal
// (OSS-245). Every call is scoped to a server: an operation is only visible
// through the server it belongs to, so a scoped grant cannot be widened by
// guessing an operation id.
type OperationService struct {
	store *storage.Store
	log   *logger.Logger

	// nudge asks the reconciler to converge one server immediately. It is
	// optional: without it a released lock is picked up by the next sweep.
	nudge func(serverID string)
}

// NewOperationService builds the journal read API.
func NewOperationService(store *storage.Store, log *logger.Logger) *OperationService {
	return &OperationService{store: store, log: log}
}

// SetReconcilerNudge installs the hook that converges a server right after its
// lifecycle lock is released, instead of waiting for the safety-net sweep.
func (s *OperationService) SetReconcilerNudge(fn func(serverID string)) {
	s.nudge = fn
}

// ListOperations returns a server's journal newest first.
func (s *OperationService) ListOperations(ctx context.Context, req *connect.Request[v1.ListOperationsRequest]) (*connect.Response[v1.ListOperationsResponse], error) {
	msg := req.Msg
	serverID := strings.TrimSpace(msg.GetServerId())
	outcome := strings.ToLower(strings.TrimSpace(msg.GetOutcome()))
	if outcome != "" && !operation.ValidOutcome(outcome) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown outcome filter %q", msg.GetOutcome()))
	}

	limit := clampLimit(int(msg.GetLimit()))
	// An outcome filter is applied after projection, so read the whole
	// (already bounded) window before trimming to the caller's limit.
	fetch := limit
	if outcome != "" {
		fetch = maxListLimit
	}

	rows, err := s.store.ListRecentServerOperations(ctx, serverID, fetch)
	if err != nil {
		s.log.Error("Failed to list server operations: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list server operations"))
	}

	views := operation.DescribeAll(rows)
	if outcome != "" {
		views = operation.FilterOutcome(views, operation.Outcome(outcome))
	}
	if len(views) > limit {
		views = views[:limit]
	}

	return connect.NewResponse(&v1.ListOperationsResponse{
		Operations:      operationMessages(views),
		CountsByOutcome: outcomeCounts(views),
	}), nil
}

// GetOperation returns one journal row, refusing a server that does not own it.
func (s *OperationService) GetOperation(ctx context.Context, req *connect.Request[v1.GetOperationRequest]) (*connect.Response[v1.GetOperationResponse], error) {
	op, err := s.lookup(ctx, req.Msg.GetServerId(), req.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetOperationResponse{Operation: operationMessage(operation.Describe(op))}), nil
}

// ResolveOperation releases a lifecycle lock without touching Docker. It is
// idempotent: resolving an already released operation returns the row as it is.
func (s *OperationService) ResolveOperation(ctx context.Context, req *connect.Request[v1.ResolveOperationRequest]) (*connect.Response[v1.ResolveOperationResponse], error) {
	op, err := s.lookup(ctx, req.Msg.GetServerId(), req.Msg.GetOperationId())
	if err != nil {
		return nil, err
	}

	note := strings.TrimSpace(req.Msg.GetNote())
	if len(note) > maxResolveNote {
		note = note[:maxResolveNote]
	}

	if op.ActiveServer != nil {
		if err := s.store.ResolveServerOperation(ctx, op.ID, note); err != nil {
			s.log.Error("Failed to resolve server operation %s: %v", op.ID, err)
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to resolve the operation"))
		}
		s.recordResolution(ctx, op, note)
		// Re-read so the caller sees the released row rather than the one it
		// arrived with.
		if refreshed, getErr := s.store.GetServerOperation(ctx, op.ID); getErr == nil {
			op = refreshed
		}
		if s.nudge != nil {
			s.nudge(op.ServerID)
		}
	}

	return connect.NewResponse(&v1.ResolveOperationResponse{Operation: operationMessage(operation.Describe(op))}), nil
}

// lookup loads an operation and enforces that serverID owns it. A mismatch is
// reported as not-found rather than as a permission error so the endpoint does
// not confirm that an operation id exists on another server.
func (s *OperationService) lookup(ctx context.Context, serverID, operationID string) (*storage.ServerOperation, error) {
	serverID = strings.TrimSpace(serverID)
	operationID = strings.TrimSpace(operationID)
	if serverID == "" || operationID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("server_id and operation_id are required"))
	}
	op, err := s.store.GetServerOperation(ctx, operationID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
	}
	if op.ServerID != serverID {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("operation not found"))
	}
	return op, nil
}

// recordResolution writes the audit trail entry. It is best-effort: the lock is
// already released, and losing the audit row must not turn a successful
// recovery into an error the caller retries.
func (s *OperationService) recordResolution(ctx context.Context, op *storage.ServerOperation, note string) {
	props := fmt.Sprintf(`{"operationId":%q,"kind":%q,"note":%q}`, op.ID, op.Kind, note)
	entry := activity.Entry{
		Event:      activity.EventServerOperationResolve,
		SubjectTyp: "server",
		SubjectID:  op.ServerID,
		Properties: props,
	}
	if u := auth.GetUserFromContext(ctx); u != nil {
		entry.ActorID = u.ID
		entry.ActorName = u.Username
	}
	if err := activity.Log(s.store.DB(), entry); err != nil {
		s.log.Error("Audit log failed for %s: %v", activity.EventServerOperationResolve, err)
	}
}

// operationMessages converts the whole projection for a response.
func operationMessages(views []operation.View) []*v1.LifecycleOperation {
	out := make([]*v1.LifecycleOperation, len(views))
	for i, v := range views {
		out[i] = operationMessage(v)
	}
	return out
}

// operationMessage converts one projected row. Timestamps are only set when
// present so a zero time does not render as year 1 in a UI.
func operationMessage(v operation.View) *v1.LifecycleOperation {
	msg := &v1.LifecycleOperation{
		Id:                 v.ID,
		ServerId:           v.ServerID,
		Kind:               v.Kind,
		Phase:              v.Phase,
		Outcome:            string(v.Outcome),
		NextAction:         string(v.NextAction),
		Reason:             v.Reason,
		HoldsLock:          v.HoldsLock,
		HasContainer:       v.HasContainer,
		EndpointGeneration: v.EndpointGeneration,
		LastError:          v.LastError,
	}
	if !v.StartedAt.IsZero() {
		msg.StartedAt = timestamppb.New(v.StartedAt)
	}
	if !v.UpdatedAt.IsZero() {
		msg.UpdatedAt = timestamppb.New(v.UpdatedAt)
	}
	return msg
}

// outcomeCounts re-keys the projection's counts for the wire.
func outcomeCounts(views []operation.View) map[string]int32 {
	counts := operation.CountByOutcome(views)
	out := make(map[string]int32, len(counts))
	for outcome, n := range counts {
		out[string(outcome)] = int32(n)
	}
	return out
}
