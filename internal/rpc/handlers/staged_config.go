// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// StagedConfigHandler serves scheduled/staged config rollout for instances.
//
// Mounted at /api/v1/staged-config/ (see rpc/server.go):
//   - GET    /api/v1/staged-config/{serverID}/staged[?status=staged]  list changes
//   - POST   /api/v1/staged-config/{serverID}/staged                  stage a diff
//   - POST   /api/v1/staged-config/{serverID}/staged/{id}/apply       apply now
//   - POST   /api/v1/staged-config/{serverID}/staged/{id}/rollback    rollback applied change (SCH-1)
//   - DELETE /api/v1/staged-config/{serverID}/staged/{id}             discard
//
// Staging records the change regardless of the instance's operational state.
// "on_restart" changes are picked up by ApplyOnRestartStagedConfigs;
// "scheduled" changes are picked up by the scheduler cron tick.
//
// The HTTP adapter delegates every storage operation to StagedConfigOperations,
// the same shared operation the generated StagedConfigService calls.
type StagedConfigHandler struct {
	ops         *StagedConfigOperations
	log         *logger.Logger
	authManager *auth.Manager
	enforcer    *rbac.Enforcer
}

func NewStagedConfigHandler(store *storage.Store, log *logger.Logger, authManager *auth.Manager, enforcer *rbac.Enforcer) *StagedConfigHandler {
	return NewStagedConfigHandlerWithOperations(NewStagedConfigOperations(store), log, authManager, enforcer)
}

// NewStagedConfigHandlerWithOperations builds the compatibility adapter over a
// shared operation instance so the controller can give the generated service
// the exact same operation.
func NewStagedConfigHandlerWithOperations(ops *StagedConfigOperations, log *logger.Logger, authManager *auth.Manager, enforcer *rbac.Enforcer) *StagedConfigHandler {
	return &StagedConfigHandler{ops: ops, log: log, authManager: authManager, enforcer: enforcer}
}

// stagedServerID resolves the owning server of a staged change. Apply,
// rollback and discard routes carry a staged ID, so authorizing the server ID
// from the path alone would let a caller with access to server A act on a
// staged change belonging to server B.
func (h *StagedConfigHandler) stagedServerID(ctx context.Context, stagedID string) (string, bool) {
	return h.ops.Owner(ctx, stagedID)
}

func (h *StagedConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.ops.Ready() {
		http.Error(w, "store not initialized", http.StatusInternalServerError)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/staged-config/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	// Expect: {serverID}/staged[/{id}[/apply]]
	if len(parts) < 2 || parts[0] == "" || parts[1] != "staged" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	serverID := parts[0]
	action := rbac.ActionRead
	if r.Method != http.MethodGet {
		action = rbac.ActionCreate
	}

	// Authorize against the path server first, before any staged-ID lookup.
	// Otherwise an unauthorized caller could distinguish "missing record"
	// (404) from "forbidden" (403) and enumerate staged IDs.
	if err := requireRequestObject(r, h.authManager, h.enforcer, rbac.ResourceServerConfig, action, serverID); err != nil {
		writePolicyError(w, err)
		return
	}

	// Mutations by staged ID must also be authorized against the record's real
	// owner, not only the server ID supplied in the URL.
	if len(parts) >= 3 {
		ownerID, ok := h.stagedServerID(r.Context(), parts[2])
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		if !strings.EqualFold(ownerID, serverID) {
			if err := requireRequestObject(r, h.authManager, h.enforcer, rbac.ResourceServerConfig, action, ownerID); err != nil {
				writePolicyError(w, err)
				return
			}
		}
		serverID = ownerID
	}

	switch {
	case len(parts) == 2 && r.Method == http.MethodGet:
		h.handleList(w, r, serverID)
	case len(parts) == 2 && r.Method == http.MethodPost:
		h.handleStage(w, r, serverID)
	case len(parts) == 4 && parts[3] == "apply" && r.Method == http.MethodPost:
		h.handleApply(w, r, parts[2])
	case len(parts) == 4 && parts[3] == "rollback" && r.Method == http.MethodPost:
		h.handleRollback(w, r, parts[2])
	case len(parts) == 3 && r.Method == http.MethodDelete:
		h.handleDiscard(w, r, parts[2])
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func writeStagedConfigError(w http.ResponseWriter, err error) {
	var opErr *StagedConfigError
	if errors.As(err, &opErr) {
		http.Error(w, opErr.Message, opErr.Status)
		return
	}
	http.Error(w, fmt.Sprintf("staged config operation failed: %v", err), http.StatusInternalServerError)
}

func (h *StagedConfigHandler) handleList(w http.ResponseWriter, r *http.Request, serverID string) {
	status := storage.StagedStatus(strings.TrimSpace(r.URL.Query().Get("status")))
	changes, err := h.ops.List(r.Context(), serverID, status)
	if err != nil {
		writeStagedConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": changes, "count": len(changes)})
}

func (h *StagedConfigHandler) handleStage(w http.ResponseWriter, r *http.Request, serverID string) {
	var req struct {
		Changes   map[string]any `json:"changes"`
		ApplyMode string         `json:"apply_mode"`
		CronExpr  string         `json:"cron_expr"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	staged, err := h.ops.Stage(r.Context(), serverID, req.Changes, storage.StagedApplyMode(req.ApplyMode), req.CronExpr)
	if err != nil {
		writeStagedConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, staged)
}

func (h *StagedConfigHandler) handleApply(w http.ResponseWriter, r *http.Request, stagedID string) {
	applied, err := h.ops.Apply(r.Context(), stagedID)
	if err != nil {
		writeStagedConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "change": applied})
}

func (h *StagedConfigHandler) handleRollback(w http.ResponseWriter, r *http.Request, stagedID string) {
	rolledBack, err := h.ops.Rollback(r.Context(), stagedID)
	if err != nil {
		writeStagedConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "change": rolledBack})
}

func (h *StagedConfigHandler) handleDiscard(w http.ResponseWriter, r *http.Request, stagedID string) {
	if err := h.ops.Discard(r.Context(), stagedID); err != nil {
		writeStagedConfigError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
