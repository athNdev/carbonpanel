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
	"github.com/athNdev/carbon-panel/internal/indexers"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// ValidateKeyRequest represents incoming request payload
type ValidateKeyRequest struct {
	Provider  string `json:"provider"`   // "curseforge" or "modrinth"
	APIKey    string `json:"api_key"`    // Token or API Key
	UserAgent string `json:"user_agent"` // Custom User-Agent
}

// ValidateKeyResponse represents validation outcome
type ValidateKeyResponse struct {
	Valid   bool   `json:"valid"`
	Message string `json:"message"`
}

// NewKeyValidatorHandler is the legacy REST adapter for provider credential
// validation during the product-transport deprecation window (ADR-0012). It
// delegates to the shared indexers.ValidateProviderKey operation so the
// generated ConfigService.ValidateProviderKey RPC and this endpoint keep
// identical behavior, and authorization goes through the shared global
// settings/read policy (fail closed on missing dependencies).
func NewKeyValidatorHandler(authManager *auth.Manager, enforcer *rbac.Enforcer, log *logger.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if err := requireGlobalObject(r, authManager, enforcer, rbac.ResourceSettings, rbac.ActionRead); err != nil {
			if errors.Is(err, errForbidden) {
				http.Error(w, "forbidden", http.StatusForbidden)
			} else {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
			}
			return
		}

		var req ValidateKeyRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}

		result := indexers.ValidateProviderKey(r.Context(), req.Provider, req.APIKey, req.UserAgent)
		writeJSON(w, http.StatusOK, ValidateKeyResponse{Valid: result.Valid, Message: result.Message})
	})
}
