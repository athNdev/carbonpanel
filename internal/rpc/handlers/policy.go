// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

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

// requireGlobalObject authenticates the caller (even in explicit no-auth mode)
// and enforces resource/action on the global object ("*"). Missing policy
// dependencies fail closed rather than bypassing the check. Legacy REST
// adapters and their generated RPC counterparts must agree on this mapping,
// so every adapter delegates here instead of re-deriving the check.
func requireGlobalObject(r *http.Request, authManager *auth.Manager, enforcer *rbac.Enforcer, resource, action string) error {
	if authManager == nil {
		return errUnauthorized
	}
	user, err := authManager.AuthenticateFromHeader(r.Context(), r.Header.Get("Authorization"))
	if err != nil {
		return errUnauthorized
	}
	if enforcer == nil {
		return errForbidden
	}
	allowed, rbacErr := enforcer.Enforce(user.Roles, resource, action, "*")
	if rbacErr != nil || !allowed {
		return errForbidden
	}
	return nil
}
