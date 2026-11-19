// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/athNdev/carbon-panel/internal/packwiz"
)

// PackwizOperations is the single application operation for the JSON pack
// control family. The legacy /api/v1/packwiz/packs adapter and the generated
// PackwizService both call these methods so pack handling and error semantics
// cannot diverge between transports (ADR 0012).
type PackwizOperations struct {
	manager *packwiz.Manager
}

// NewPackwizOperations creates the shared pack-control operation.
func NewPackwizOperations(manager *packwiz.Manager) *PackwizOperations {
	return &PackwizOperations{manager: manager}
}

// PackwizError carries the legacy HTTP status and message for each transport
// to map independently.
type PackwizError struct {
	Status  int
	Message string
}

func (e *PackwizError) Error() string { return e.Message }

func packwizError(status int, format string, args ...any) *PackwizError {
	return &PackwizError{Status: status, Message: fmt.Sprintf(format, args...)}
}

// Ready reports whether the pack manager dependency is available.
func (o *PackwizOperations) Ready() bool { return o != nil && o.manager != nil }

// List returns all pack summaries.
func (o *PackwizOperations) List(ctx context.Context) ([]packwiz.PackSummary, error) {
	if !o.Ready() {
		return nil, packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	packs, err := o.manager.ListPacks()
	if err != nil {
		return nil, packwizError(http.StatusInternalServerError, "failed to list packs: %v", err)
	}
	return packs, nil
}

// Get returns one pack. Any lookup failure is the legacy "pack not found" 404.
func (o *PackwizOperations) Get(ctx context.Context, packID string) (*packwiz.Pack, error) {
	if !o.Ready() {
		return nil, packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	pack, err := o.manager.GetPack(packID)
	if err != nil {
		return nil, packwizError(http.StatusNotFound, "pack not found")
	}
	return pack, nil
}

// Clone copies a pack under a new name.
func (o *PackwizOperations) Clone(ctx context.Context, packID, name string) (*packwiz.Pack, error) {
	if !o.Ready() {
		return nil, packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	cloned, err := o.manager.ClonePack(packID, name)
	if err != nil {
		return nil, packwizError(http.StatusInternalServerError, "failed to clone pack: %v", err)
	}
	return cloned, nil
}

// Refresh re-indexes a pack's content lists.
func (o *PackwizOperations) Refresh(ctx context.Context, packID string) error {
	if !o.Ready() {
		return packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	if err := o.manager.RefreshPack(packID); err != nil {
		return packwizError(http.StatusInternalServerError, "failed to refresh pack: %v", err)
	}
	return nil
}

// CheckUpdates lists available updates, optionally filtered by content kind.
func (o *PackwizOperations) CheckUpdates(ctx context.Context, packID, kind string) ([]packwiz.ModUpdateInfo, error) {
	if !o.Ready() {
		return nil, packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	updates, err := o.manager.CheckContentUpdatesContext(ctx, packID)
	if err != nil {
		return nil, packwizError(http.StatusInternalServerError, "failed to check updates: %v", err)
	}
	if kind = strings.TrimSpace(kind); kind != "" {
		filtered := updates[:0]
		for _, update := range updates {
			if update.Kind == kind {
				filtered = append(filtered, update)
			}
		}
		updates = filtered
	}
	return updates, nil
}

// Delete removes a pack and its metadata.
func (o *PackwizOperations) Delete(ctx context.Context, packID string) error {
	if !o.Ready() {
		return packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	if err := o.manager.DeletePack(packID); err != nil {
		return packwizError(http.StatusInternalServerError, "failed to delete pack: %v", err)
	}
	return nil
}
