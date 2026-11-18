// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// NewAuthInterceptor builds the shared, fail-closed authentication and
// authorization interceptor used by every generated Connect handler.
//
// It is exported so disposable fixtures (for example the MCP tool tests) can
// mount generated handlers behind the exact production authorization path
// instead of a local approximation. The arguments are the same dependencies
// the Server holds; a nil enforcer or store keeps the fail-closed behavior.
func NewAuthInterceptor(authManager *auth.Manager, enforcer *rbac.Enforcer, store *storage.Store, log *logger.Logger) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			procedure := req.Spec().Procedure

			if rbac.PublicProcedures[procedure] {
				return next(ctx, req)
			}

			user, err := authManager.AuthenticateFromHeader(ctx, req.Header().Get("Authorization"))
			if err != nil {
				log.Debug("Auth: Token validation failed for %s: %v", procedure, err)
				return nil, connect.NewError(connect.CodeUnauthenticated, err)
			}

			ctx = auth.WithUser(ctx, user)

			if rbac.AuthenticatedOnlyProcedures[procedure] {
				return next(ctx, req)
			}

			// Unmapped procedures are denied by default, including when the
			// enforcer failed to initialize.
			perm, ok := rbac.ProcedurePermissions[procedure]
			if !ok {
				log.Warn("RBAC: denying request for unmapped procedure %s (no public/authenticated-only/permission entry)", procedure)
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("procedure %s has no permission mapping and is denied by default", procedure))
			}

			if enforcer == nil {
				log.Error("RBAC: denying request for %s because the RBAC enforcer is unavailable", procedure)
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("rbac enforcer unavailable"))
			}

			objectID := "*"
			if perm.ObjectIDField != "" {
				objectID = extractObjectID(req, perm.ObjectIDField)
			}
			allowed, err := enforcer.Enforce(user.Roles, perm.Resource, perm.Action, objectID)
			if err != nil {
				log.Error("RBAC enforcement error: %v", err)
				return nil, connect.NewError(connect.CodeInternal, err)
			}
			if !allowed {
				// A per-server grant can allow server-scoped procedures the
				// caller's roles deny, except for management procedures.
				if perm.Resource != rbac.ResourceSubusers && perm.ObjectIDField == "server_id" && store != nil {
					want := perm.Resource + "." + perm.Action
					for _, p := range store.SubuserPermissions(ctx, objectID, user.ID) {
						if p == want {
							allowed = true
							break
						}
					}
				}
			}
			if !allowed {
				return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("insufficient permissions for %s/%s", perm.Resource, perm.Action))
			}

			return next(ctx, req)
		}
	}
}
