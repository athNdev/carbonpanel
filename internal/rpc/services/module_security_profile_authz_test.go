// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

type profileAuthorizer struct {
	allow bool
	err   error
}

func (a profileAuthorizer) Enforce(_ []string, resource, action, _ string) (bool, error) {
	if resource != rbac.ResourceServers || action != rbac.ActionManageDockerPrivileged {
		return false, nil
	}
	return a.allow, a.err
}

// TestNamedModuleSecurityProfileRequiresElevatedPermission proves the named
// exception is gated at the RPC seam and fails closed on a missing identity,
// missing enforcer, denied decision or enforcer error.
func TestNamedModuleSecurityProfileRequiresElevatedPermission(t *testing.T) {
	user := auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: "u-1", Username: "user", Roles: []string{"user"},
	})

	cases := []struct {
		name     string
		ctx      context.Context
		enforcer ModuleAuthorizer
		grant    bool
	}{
		{"no identity", context.Background(), profileAuthorizer{allow: true}, false},
		{"no enforcer", user, nil, false},
		{"denied", user, profileAuthorizer{allow: false}, false},
		{"enforcer error", user, profileAuthorizer{allow: true, err: context.DeadlineExceeded}, false},
		{"granted", user, profileAuthorizer{allow: true}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &ModuleService{enforcer: tc.enforcer, log: logger.New()}
			err := svc.requireModuleSecurityException(tc.ctx)
			if tc.grant {
				if err != nil {
					t.Fatalf("expected grant, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected denial, got nil")
			}
			if got := connect.CodeOf(err); got != connect.CodePermissionDenied {
				t.Fatalf("code = %v, want permission_denied", got)
			}
		})
	}
}
