// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"errors"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"gorm.io/gorm"
)

var _ carbonpanelv1connect.UserServiceHandler = (*UserService)(nil)

type UserService struct {
	store       *storage.Store
	authManager *auth.Manager
	log         *logger.Logger
	oidcHandler *auth.OIDCHandler
	enforcer    *rbac.Enforcer
}

func NewUserService(store *storage.Store, authManager *auth.Manager, log *logger.Logger) *UserService {
	return &UserService{
		store:       store,
		authManager: authManager,
		log:         log,
	}
}

func (s *UserService) SetEnforcer(enforcer *rbac.Enforcer) {
	s.enforcer = enforcer
}

func (s *UserService) SetOIDCHandler(handler *auth.OIDCHandler) {
	s.oidcHandler = handler
}

// BeginOIDCLink records an authenticated local admin's intent to bind their
// own OIDC identity on the next verified callback. The response token names no
// issuer, subject or email and is never logged.
func (s *UserService) BeginOIDCLink(ctx context.Context, _ *connect.Request[v1.BeginOIDCLinkRequest]) (*connect.Response[v1.BeginOIDCLinkResponse], error) {
	caller := auth.GetUserFromContext(ctx)
	if caller == nil || caller.ID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("local admin sign-in required"))
	}
	if s.oidcHandler == nil || !s.oidcHandler.IsEnabled() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("OIDC linking is unavailable"))
	}
	token, cookie, err := s.oidcHandler.BeginLink(ctx, caller)
	if errors.Is(err, auth.ErrOIDCLinkIneligible) {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	if err != nil {
		s.log.Error("OIDC link intent rejected: %v", err)
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("unable to start OIDC link"))
	}
	resp := connect.NewResponse(&v1.BeginOIDCLinkResponse{LinkToken: token})
	resp.Header().Set("Cache-Control", "no-store")
	resp.Header().Set("Set-Cookie", cookie.String())
	return resp, nil
}

func (s *UserService) ListUsers(ctx context.Context, req *connect.Request[v1.ListUsersRequest]) (*connect.Response[v1.ListUsersResponse], error) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		s.log.Error("Failed to list users: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list users"))
	}

	protoUsers := make([]*v1.User, 0, len(users))
	for _, user := range users {
		item, err := s.userWithRoleSources(ctx, user)
		if err != nil {
			return nil, err
		}
		protoUsers = append(protoUsers, item)
	}

	return connect.NewResponse(&v1.ListUsersResponse{
		Users: protoUsers,
	}), nil
}

func (s *UserService) GetUser(ctx context.Context, req *connect.Request[v1.GetUserRequest]) (*connect.Response[v1.GetUserResponse], error) {
	msg := req.Msg
	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user ID is required"))
	}

	user, err := s.store.GetUser(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
	}

	item, err := s.userWithRoleSources(ctx, user)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetUserResponse{User: item}), nil
}

func (s *UserService) CreateUser(ctx context.Context, req *connect.Request[v1.CreateUserRequest]) (*connect.Response[v1.CreateUserResponse], error) {
	msg := req.Msg

	if msg.Username == "" || msg.Password == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("username and password are required"))
	}

	if slices.ContainsFunc(msg.Roles, func(role string) bool { return strings.EqualFold(role, "admin") }) &&
		!canManageWildcards(ctx, s.enforcer) {
		return nil, connect.NewError(connect.CodePermissionDenied, storage.ErrLocalAdminGrantForbidden)
	}

	user, err := s.authManager.CreateLocalUser(ctx, msg.Username, msg.Email, msg.Password)
	if err != nil {
		if errors.Is(err, auth.ErrPasswordTooWeak) {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		s.log.Error("Failed to create user: %v", err)
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("failed to create user"))
	}

	// Assign roles
	for _, roleName := range msg.Roles {
		if err := s.store.AssignRole(ctx, user.ID, roleName, "local"); err != nil {
			s.log.Error("Failed to assign role %s to user %s: %v", roleName, user.ID, err)
		}
	}

	// If no roles specified, assign default roles
	if len(msg.Roles) == 0 {
		defaultRoles, _ := s.store.GetDefaultRoles(ctx)
		for _, role := range defaultRoles {
			_ = s.store.AssignRole(ctx, user.ID, role.Name, "local")
		}
	}

	writeAudit(ctx, s.store, s.log, auditFields{
		Event:      activity.EventUserCreate,
		SubjectTyp: "user",
		SubjectID:  user.ID,
	}, map[string]any{
		"username":  user.Username,
		"roles":     strings.Join(msg.Roles, ","),
		"email_set": user.Email != nil && *user.Email != "",
		"active":    user.IsActive,
	})

	item, err := s.userWithRoleSources(ctx, user)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.CreateUserResponse{User: item}), nil
}

func (s *UserService) UpdateUser(ctx context.Context, req *connect.Request[v1.UpdateUserRequest]) (*connect.Response[v1.UpdateUserResponse], error) {
	msg := req.Msg

	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user ID is required"))
	}

	roles := msg.GetLocalRolesUpdate()
	var roleUpdate *storage.LocalRoleUpdate
	switch {
	case roles != nil && len(msg.Roles) != 0:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provide only one role selection"))
	case roles != nil || len(msg.Roles) != 0:
		desired := msg.Roles
		if roles != nil {
			desired = roles.Roles
		}
		roleUpdate = &storage.LocalRoleUpdate{
			Roles: desired, LegacyEffectiveNames: roles == nil, AllowAdminGrant: canManageWildcards(ctx, s.enforcer),
		}
	}
	err := s.store.UpdateUserProfileAndRoles(ctx, msg.Id, msg.Email, msg.IsActive, roleUpdate)
	if errors.Is(err, storage.ErrLocalAdminGrantForbidden) {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
	}
	if err != nil {
		s.log.Error("Failed to update user: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to update user"))
	}

	roleNames := ""
	rolesChanged := roleUpdate != nil
	if rolesChanged {
		roleNames = strings.Join(roleUpdate.Roles, ",")
	}
	writeAudit(ctx, s.store, s.log, auditFields{
		Event:      activity.EventUserUpdate,
		SubjectTyp: "user",
		SubjectID:  msg.Id,
	}, map[string]any{
		"email_changed":  msg.Email != nil,
		"active_changed": msg.IsActive != nil,
		"roles_changed":  rolesChanged,
		"roles":          roleNames,
	})

	user, err := s.store.GetUser(ctx, msg.Id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
	}
	item, err := s.userWithRoleSources(ctx, user)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.UpdateUserResponse{User: item}), nil
}

func (s *UserService) userWithRoleSources(ctx context.Context, user *storage.User) (*v1.User, error) {
	roles, err := s.store.GetUserRoleNames(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get user roles"))
	}
	item := dbUserToProto(user, roles)
	item.LocalRoles, err = s.store.GetUserLocalRoleNames(ctx, user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get local roles"))
	}
	item.OidcRoles, err = s.store.GetUserRoleNamesBySource(ctx, user.ID, "oidc")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get OIDC roles"))
	}
	return item, nil
}

func (s *UserService) DeleteUser(ctx context.Context, req *connect.Request[v1.DeleteUserRequest]) (*connect.Response[v1.DeleteUserResponse], error) {
	msg := req.Msg

	if msg.Id == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("user ID is required"))
	}

	var properties map[string]any
	if user, err := s.store.GetUser(ctx, msg.Id); err == nil {
		properties = map[string]any{
			"username":      user.Username,
			"auth_provider": user.AuthProvider,
		}
	}

	if err := s.store.DeleteUser(ctx, msg.Id); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("user not found"))
		}
		s.log.Error("Failed to delete user: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete user"))
	}

	writeAudit(ctx, s.store, s.log, auditFields{
		Event:      activity.EventUserDelete,
		SubjectTyp: "user",
		SubjectID:  msg.Id,
	}, properties)

	return connect.NewResponse(&v1.DeleteUserResponse{
		Message: "user deleted",
	}), nil
}

func dbUserToProto(user *storage.User, roles []string) *v1.User {
	protoUser := &v1.User{
		Id:           user.ID,
		Username:     user.Username,
		Email:        user.Email,
		AuthProvider: user.AuthProvider,
		IsActive:     user.IsActive,
		Roles:        roles,
		CreatedAt:    timestamppb.New(user.CreatedAt),
		UpdatedAt:    timestamppb.New(user.UpdatedAt),
	}
	if user.LastLogin != nil {
		protoUser.LastLogin = timestamppb.New(*user.LastLogin)
	}
	return protoUser
}
