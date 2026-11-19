// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// StagedConfigService exposes the generated contract for staged config rollout.
// It delegates to handlers.StagedConfigOperations, the same shared operation
// the legacy /api/v1/staged-config JSON adapter uses. Mutations by staged ID
// re-authorize the record's real owner before touching storage, matching the
// compatibility adapter's dual authorization.
type StagedConfigService struct {
	store     *storage.Store
	ops       *handlers.StagedConfigOperations
	enforcer  *rbac.Enforcer
}

// NewStagedConfigService creates the generated staged-config service.
func NewStagedConfigService(store *storage.Store, ops *handlers.StagedConfigOperations, enforcer *rbac.Enforcer) *StagedConfigService {
	return &StagedConfigService{store: store, ops: ops, enforcer: enforcer}
}

func stagedConnectError(err error) error {
	var opErr *handlers.StagedConfigError
	if !errors.As(err, &opErr) {
		return connect.NewError(connect.CodeInternal, err)
	}
	var code connect.Code
	switch opErr.Status {
	case http.StatusBadRequest:
		code = connect.CodeInvalidArgument
	case http.StatusNotFound:
		code = connect.CodeNotFound
	case http.StatusUnauthorized, http.StatusForbidden:
		code = connect.CodePermissionDenied
	default:
		code = connect.CodeInternal
	}
	return connect.NewError(code, errors.New(opErr.Message))
}

// authorizeOwner re-checks the real owner of a staged record. The interceptor
// authorizes the request's server_id field; this check prevents a URL server ID
// from masking a staged change owned by another server. Fail closed when the
// enforcer is unavailable, mirroring the legacy adapter and the interceptor.
func (s *StagedConfigService) authorizeOwner(ctx context.Context, serverID, action string) error {
	if s.enforcer == nil {
		return connect.NewError(connect.CodePermissionDenied, errors.New("rbac enforcer unavailable"))
	}
	user := auth.GetUserFromContext(ctx)
	if user == nil {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("authentication required"))
	}
	allowed, err := s.enforcer.Enforce(user.Roles, rbac.ResourceServerConfig, action, serverID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("rbac enforcement error: %w", err))
	}
	if !allowed {
		want := rbac.ResourceServerConfig + "." + action
		for _, p := range s.store.SubuserPermissions(ctx, serverID, user.ID) {
			if p == want {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("insufficient permissions for %s/%s on this server", rbac.ResourceServerConfig, action))
	}
	return nil
}

// resolveOwner verifies the staged record exists and returns its real owner.
func (s *StagedConfigService) resolveOwner(ctx context.Context, reqServerID, stagedID, action string) (string, error) {
	if !s.ops.Ready() {
		return "", connect.NewError(connect.CodeInternal, errors.New("store not initialized"))
	}
	ownerID, ok := s.ops.Owner(ctx, stagedID)
	if !ok {
		return "", connect.NewError(connect.CodeNotFound, errors.New("staged change not found"))
	}
	// A matching path server was already authorized by the interceptor; a
	// different real owner must be authorized explicitly.
	if !strings.EqualFold(ownerID, reqServerID) {
		if err := s.authorizeOwner(ctx, ownerID, action); err != nil {
			return "", err
		}
	}
	return ownerID, nil
}

func stagedToProto(c *storage.StagedConfigChange) *v1.StagedConfigChange {
	if c == nil {
		return nil
	}
	out := &v1.StagedConfigChange{
		Id: c.ID, ServerId: c.ServerID, BatchId: c.BatchID,
		Payload: c.Payload, BasePayload: c.BasePayload, PreviousPayload: c.PreviousPayload,
		ApplyMode: string(c.ApplyMode), CronExpr: c.CronExpr,
		IdleMinutes: int32(c.IdleMinutes), Status: string(c.Status),
		CreatedAt: c.CreatedAt.Format(time.RFC3339),
	}
	if c.ApplyAt != nil {
		out.ApplyAt = c.ApplyAt.Format(time.RFC3339)
	}
	if c.AppliedAt != nil {
		out.AppliedAt = c.AppliedAt.Format(time.RFC3339)
	}
	if c.RolledBackAt != nil {
		out.RolledBackAt = c.RolledBackAt.Format(time.RFC3339)
	}
	return out
}

// ListStagedChanges lists staged changes for a server.
func (s *StagedConfigService) ListStagedChanges(ctx context.Context, req *connect.Request[v1.ListStagedChangesRequest]) (*connect.Response[v1.ListStagedChangesResponse], error) {
	changes, err := s.ops.List(ctx, req.Msg.ServerId, storage.StagedStatus(req.Msg.Status))
	if err != nil {
		return nil, stagedConnectError(err)
	}
	out := make([]*v1.StagedConfigChange, 0, len(changes))
	for _, c := range changes {
		out = append(out, stagedToProto(c))
	}
	return connect.NewResponse(&v1.ListStagedChangesResponse{Changes: out, Count: int32(len(out))}), nil
}

// StageConfigChange records a config diff. The changes JSON is re-serialized
// from the wire string so the shared operation receives the same map the legacy
// adapter decodes from its request body.
func (s *StagedConfigService) StageConfigChange(ctx context.Context, req *connect.Request[v1.StageConfigChangeRequest]) (*connect.Response[v1.StageConfigChangeResponse], error) {
	changes := map[string]any{}
	if req.Msg.Changes != "" {
		if err := json.Unmarshal([]byte(req.Msg.Changes), &changes); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid request body"))
		}
	}
	staged, err := s.ops.Stage(ctx, req.Msg.ServerId, changes, storage.StagedApplyMode(req.Msg.ApplyMode), req.Msg.CronExpr)
	if err != nil {
		return nil, stagedConnectError(err)
	}
	return connect.NewResponse(&v1.StageConfigChangeResponse{Change: stagedToProto(staged)}), nil
}

// ApplyStagedChange applies a staged change immediately.
func (s *StagedConfigService) ApplyStagedChange(ctx context.Context, req *connect.Request[v1.ApplyStagedChangeRequest]) (*connect.Response[v1.ApplyStagedChangeResponse], error) {
	_, err := s.resolveOwner(ctx, req.Msg.ServerId, req.Msg.StagedId, rbac.ActionCreate)
	if err != nil {
		return nil, err
	}
	applied, opErr := s.ops.Apply(ctx, req.Msg.StagedId)
	if opErr != nil {
		return nil, stagedConnectError(opErr)
	}
	return connect.NewResponse(&v1.ApplyStagedChangeResponse{Success: true, Change: stagedToProto(applied)}), nil
}

// RollbackStagedChange restores values captured before an apply.
func (s *StagedConfigService) RollbackStagedChange(ctx context.Context, req *connect.Request[v1.RollbackStagedChangeRequest]) (*connect.Response[v1.RollbackStagedChangeResponse], error) {
	_, err := s.resolveOwner(ctx, req.Msg.ServerId, req.Msg.StagedId, rbac.ActionCreate)
	if err != nil {
		return nil, err
	}
	rolledBack, opErr := s.ops.Rollback(ctx, req.Msg.StagedId)
	if opErr != nil {
		return nil, stagedConnectError(opErr)
	}
	return connect.NewResponse(&v1.RollbackStagedChangeResponse{Success: true, Change: stagedToProto(rolledBack)}), nil
}

// DiscardStagedChange removes a staged change without applying it.
func (s *StagedConfigService) DiscardStagedChange(ctx context.Context, req *connect.Request[v1.DiscardStagedChangeRequest]) (*connect.Response[v1.DiscardStagedChangeResponse], error) {
	_, err := s.resolveOwner(ctx, req.Msg.ServerId, req.Msg.StagedId, rbac.ActionCreate)
	if err != nil {
		return nil, err
	}
	if opErr := s.ops.Discard(ctx, req.Msg.StagedId); opErr != nil {
		return nil, stagedConnectError(opErr)
	}
	return connect.NewResponse(&v1.DiscardStagedChangeResponse{Success: true}), nil
}
