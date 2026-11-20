// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/rbac"
)

// writeJSON is the shared legacy REST JSON encoder.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// errUnauthorized and errForbidden mark which status a failed global policy
// check must present. Handlers map them instead of guessing per endpoint.
var (
	errUnauthorized = errors.New("unauthorized")
	errForbidden    = errors.New("forbidden")
)

// writePolicyError maps a policy refusal to the legacy HTTP status. All legacy
// adapters use it so a missing dependency and a denied permission are not
// confused with a bad request.
func writePolicyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errUnauthorized) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	http.Error(w, "forbidden", http.StatusForbidden)
}

// requireRequestObject authenticates the caller from the Authorization header
// or the cp_session cookie and enforces resource/action on a concrete object.
// Unlike the older per-handler checks, missing auth manager, missing enforcer
// and enforcement errors all fail closed, including when every provider is
// disabled and the caller relies on the explicit allow_no_auth opt-in.
//
// The object ID must come from the request (path, session owner or the global
// "*" for genuinely global operations), never from a client-supplied scope
// claim. Legacy adapters and their generated RPC counterparts must agree on
// the permission/resource mapping (ADR 0012).
func requireRequestObject(r *http.Request, authManager *auth.Manager, enforcer *rbac.Enforcer, resource, action, objectID string) error {
	_, err := requireRequestObjectUser(r, authManager, enforcer, resource, action, objectID)
	return err
}

// requireRequestObjectUser is the same fail-closed policy check but also
// returns the authenticated principal. Handlers that bind an operation to a
// resource owner (for example an upload session) need that identity; the
// object ID still comes from the request, never from a client-supplied scope.
func requireRequestObjectUser(r *http.Request, authManager *auth.Manager, enforcer *rbac.Enforcer, resource, action, objectID string) (*auth.AuthenticatedUser, error) {
	if authManager == nil {
		return nil, errUnauthorized
	}
	authHeader := r.Header.Get("Authorization")
	if strings.TrimSpace(authHeader) == "" {
		if token := auth.SessionTokenFromRequest(r); token != "" {
			authHeader = "Bearer " + token
		}
	}
	user, err := authManager.AuthenticateFromHeader(r.Context(), authHeader)
	if err != nil {
		return nil, errUnauthorized
	}
	if enforcer == nil {
		return nil, errForbidden
	}
	allowed, rbacErr := enforcer.Enforce(user.Roles, resource, action, objectID)
	if rbacErr != nil || !allowed {
		return nil, errForbidden
	}
	return user, nil
}

// requireGlobalObject authenticates the caller (even in explicit no-auth mode)
// and enforces resource/action on the global object ("*"). It accepts the
// Authorization header or the cp_session cookie so browser-session legacy
// adapters behave consistently with other protected handlers. Missing policy
// dependencies fail closed rather than bypassing the check. Legacy REST
// adapters and their generated RPC counterparts must agree on this mapping,
// so every adapter delegates here instead of re-deriving the check.
func requireGlobalObject(r *http.Request, authManager *auth.Manager, enforcer *rbac.Enforcer, resource, action string) error {
	return requireRequestObject(r, authManager, enforcer, resource, action, "*")
}
