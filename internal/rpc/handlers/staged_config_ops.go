// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
)

// StagedConfigOperations is the single application operation for staged config
// rollout. The legacy /api/v1/staged-config JSON adapter and the generated
// StagedConfigService both call these methods so storage behavior and error
// semantics cannot diverge between transports (ADR 0012).
type StagedConfigOperations struct {
	store *storage.Store
}

// NewStagedConfigOperations creates the shared staged-config operation.
func NewStagedConfigOperations(store *storage.Store) *StagedConfigOperations {
	return &StagedConfigOperations{store: store}
}

// StagedConfigError carries the legacy HTTP status and message so each
// transport can map the same failure to its own wire error.
type StagedConfigError struct {
	Status  int
	Message string
}

func (e *StagedConfigError) Error() string { return e.Message }

func stagedError(status int, message string) *StagedConfigError {
	return &StagedConfigError{Status: status, Message: message}
}

// Ready reports whether the store dependency is available. Both transports
// fail closed when it is not.
func (o *StagedConfigOperations) Ready() bool { return o != nil && o.store != nil }

// Owner resolves the real owning server of a staged record. Mutations by staged
// ID must re-authorize this owner, not just the server ID from the request.
func (o *StagedConfigOperations) Owner(ctx context.Context, stagedID string) (string, bool) {
	if !o.Ready() || stagedID == "" {
		return "", false
	}
	change, err := o.store.GetStagedConfigChange(ctx, stagedID)
	if err != nil || change == nil || change.ServerID == "" {
		return "", false
	}
	return change.ServerID, true
}

// List returns staged changes for a server, optionally filtered by status.
func (o *StagedConfigOperations) List(ctx context.Context, serverID string, status storage.StagedStatus) ([]*storage.StagedConfigChange, error) {
	if !o.Ready() {
		return nil, stagedError(http.StatusInternalServerError, "store not initialized")
	}
	changes, err := o.store.ListStagedConfigChanges(ctx, serverID, status)
	if err != nil {
		return nil, stagedError(http.StatusInternalServerError, fmt.Sprintf("failed to list staged changes: %v", err))
	}
	if changes == nil {
		changes = []*storage.StagedConfigChange{}
	}
	return changes, nil
}

// Stage records a config diff. An empty apply mode defaults to on_restart.
func (o *StagedConfigOperations) Stage(ctx context.Context, serverID string, changes map[string]any, mode storage.StagedApplyMode, cronExpr string) (*storage.StagedConfigChange, error) {
	if !o.Ready() {
		return nil, stagedError(http.StatusInternalServerError, "store not initialized")
	}
	mode = storage.StagedApplyMode(strings.TrimSpace(string(mode)))
	if mode == "" {
		mode = storage.StagedApplyOnRestart
	}
	staged, err := o.store.StageConfigChange(ctx, serverID, changes, mode, strings.TrimSpace(cronExpr))
	if errors.Is(err, storage.ErrUpdateBatchConflict) {
		return nil, stagedError(http.StatusConflict, err.Error())
	}
	if err != nil {
		return nil, stagedError(http.StatusBadRequest, fmt.Sprintf("failed to stage config change: %v", err))
	}
	return staged, nil
}

// Apply applies a staged change immediately.
func (o *StagedConfigOperations) Apply(ctx context.Context, stagedID string) (*storage.StagedConfigChange, error) {
	if !o.Ready() {
		return nil, stagedError(http.StatusInternalServerError, "store not initialized")
	}
	applied, err := o.store.ApplyStagedConfigChange(ctx, stagedID)
	if errors.Is(err, storage.ErrActiveOperation) {
		return nil, stagedError(http.StatusConflict, err.Error())
	}
	if err != nil {
		return nil, stagedError(http.StatusBadRequest, fmt.Sprintf("failed to apply staged change: %v", err))
	}
	return applied, nil
}

// Rollback restores the values captured before an apply.
func (o *StagedConfigOperations) Rollback(ctx context.Context, stagedID string) (*storage.StagedConfigChange, error) {
	if !o.Ready() {
		return nil, stagedError(http.StatusInternalServerError, "store not initialized")
	}
	rolledBack, err := o.store.RollbackStagedConfigChange(ctx, stagedID)
	if errors.Is(err, storage.ErrActiveOperation) {
		return nil, stagedError(http.StatusConflict, err.Error())
	}
	if err != nil {
		return nil, stagedError(http.StatusBadRequest, fmt.Sprintf("failed to rollback staged change: %v", err))
	}
	return rolledBack, nil
}

// Discard removes a staged change without applying it.
func (o *StagedConfigOperations) Discard(ctx context.Context, stagedID string) error {
	if !o.Ready() {
		return stagedError(http.StatusInternalServerError, "store not initialized")
	}
	if err := o.store.DiscardStagedConfigChange(ctx, stagedID); err != nil {
		return stagedError(http.StatusBadRequest, fmt.Sprintf("failed to discard staged change: %v", err))
	}
	return nil
}
