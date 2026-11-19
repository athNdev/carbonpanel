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

// AddModResult is the soft result of a dependency-aware mod add. A resolver
// failure is not an operation failure: the requested mod was already installed,
// so the compatibility adapter and generated service both report success plus
// the resolver detail.
type AddModResult struct {
	DependencyError   string
	AddedDependencies []packwiz.ModItem
	Skipped           []string
	Unresolved        []packwiz.DependencyRef
}

// AddMod installs one mod entry. When resolveDependencies is set it also walks
// required transitive dependencies for the pack's loader/MC version. Both the
// legacy JSON adapter and the generated PackwizService call this method so
// per-item behavior cannot drift between transports.
func (o *PackwizOperations) AddMod(ctx context.Context, packID string, mod packwiz.ModItem, resolveDependencies bool, loader, mcVersion string) (AddModResult, error) {
	if !o.Ready() {
		return AddModResult{}, packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	if err := o.manager.AddMod(packID, mod); err != nil {
		return AddModResult{}, packwizError(http.StatusInternalServerError, "failed to add mod: %v", err)
	}
	if !resolveDependencies {
		return AddModResult{}, nil
	}
	if loader == "" || mcVersion == "" {
		if pack, err := o.manager.GetPack(packID); err == nil {
			if loader == "" {
				loader = pack.ModLoader
			}
			if mcVersion == "" {
				mcVersion = pack.MCVersion
			}
		}
	}
	report, err := o.manager.AddModWithDependencies(packID, mod, loader, mcVersion, o.manager.NewHTTPDependencyFetcherContext(ctx))
	if err != nil {
		return AddModResult{DependencyError: err.Error()}, nil
	}
	return AddModResult{
		AddedDependencies: report.Added,
		Skipped:           report.Skipped,
		Unresolved:        report.Unresolved,
	}, nil
}

// AddModFromURL installs a mod described by a direct download URL.
func (o *PackwizOperations) AddModFromURL(ctx context.Context, packID string, req packwiz.DirectURLModRequest) (*packwiz.ModItem, error) {
	if !o.Ready() {
		return nil, packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	mod, err := o.manager.AddModFromURL(packID, req)
	if err != nil {
		return nil, packwizError(http.StatusInternalServerError, "failed to add mod from URL: %v", err)
	}
	return mod, nil
}

// UpdateMod changes a mod's side and pinned flag.
func (o *PackwizOperations) UpdateMod(ctx context.Context, packID, slug, side string, pinned bool) error {
	if !o.Ready() {
		return packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	if err := o.manager.UpdateMod(packID, slug, side, pinned); err != nil {
		return packwizError(http.StatusInternalServerError, "failed to update mod: %v", err)
	}
	return nil
}

// DeleteMod removes one mod from a pack.
func (o *PackwizOperations) DeleteMod(ctx context.Context, packID, slug string) error {
	if !o.Ready() {
		return packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	if err := o.manager.DeleteMod(packID, slug); err != nil {
		return packwizError(http.StatusInternalServerError, "failed to remove mod: %v", err)
	}
	return nil
}

// BatchMods applies one action to a set of slugs. Per-item failures are
// intentionally ignored, matching the compatibility adapter's best-effort
// behavior. It returns the number of slugs the caller asked to process.
func (o *PackwizOperations) BatchMods(ctx context.Context, packID, action string, slugs []string, side string) (int, error) {
	if !o.Ready() {
		return 0, packwizError(http.StatusInternalServerError, "pack manager not initialized")
	}
	for _, slug := range slugs {
		switch action {
		case "set_side":
			_ = o.manager.UpdateMod(packID, slug, side, false)
		case "pin":
			_ = o.manager.UpdateMod(packID, slug, "", true)
		case "unpin":
			_ = o.manager.UpdateMod(packID, slug, "", false)
		case "remove":
			_ = o.manager.DeleteMod(packID, slug)
		}
	}
	return len(slugs), nil
}
