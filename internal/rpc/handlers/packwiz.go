// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/minecraft"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/athNdev/carbon-panel/pkg/upload"
)

type PackwizHandler struct {
	uploadManager *upload.Manager
	manager       *packwiz.Manager
	// ops is the shared pack-control operation used by the generated
	// PackwizService as well. Keep the manager for the binary/content routes
	// that stay on HTTP (ADR 0012).
	ops             *PackwizOperations
	store           *storage.Store
	log             *logger.Logger
	authManager     *auth.Manager
	enforcer        *rbac.Enforcer
	config          *config.Config
	storageResolver *nodestorage.Resolver
}

// SetStorageResolver supplies the instance-owned node storage resolver so the
// bake path keeps its mapped-root behaviour when a transport is configured.
func (h *PackwizHandler) SetStorageResolver(resolver *nodestorage.Resolver) {
	h.storageResolver = resolver
}

func NewPackwizHandler(manager *packwiz.Manager, store *storage.Store, uploadManager *upload.Manager, log *logger.Logger, authManager *auth.Manager, enforcer *rbac.Enforcer, cfg *config.Config) *PackwizHandler {
	return NewPackwizHandlerWithOperations(NewPackwizOperations(manager), store, uploadManager, log, authManager, enforcer, cfg)
}

// NewPackwizHandlerWithOperations builds the compatibility adapter over the
// same shared operation instance given to the generated service.
func NewPackwizHandlerWithOperations(ops *PackwizOperations, store *storage.Store, uploadManager *upload.Manager, log *logger.Logger, authManager *auth.Manager, enforcer *rbac.Enforcer, cfg *config.Config) *PackwizHandler {
	return &PackwizHandler{
		manager:       packwizManagerFromOperations(ops),
		ops:           ops,
		store:         store,
		uploadManager: uploadManager,
		log:           log,
		authManager:   authManager,
		enforcer:      enforcer,
		config:        cfg,
	}
}

func packwizManagerFromOperations(ops *PackwizOperations) *packwiz.Manager {
	if ops == nil {
		return nil
	}
	return ops.manager
}

func writePackwizError(w http.ResponseWriter, err error) {
	var opErr *PackwizError
	if errors.As(err, &opErr) {
		http.Error(w, opErr.Message, opErr.Status)
		return
	}
	http.Error(w, fmt.Sprintf("pack operation failed: %v", err), http.StatusInternalServerError)
}

type DeployPackRequest struct {
	ServerID string `json:"server_id"`
	Mode     string `json:"mode"` // "sync" (Mode A) or "bake" (Mode B)
}

func (h *PackwizHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Raw pack file serving for container bootstrapping (e.g. /api/v1/packwiz/{id}/pack.toml)
	// Does not require auth so container runtime on host/docker network can fetch pack.toml
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/packwiz/")
	parts := strings.Split(path, "/")

	// Route /api/v1/packwiz/loaders/{loader}/versions
	if len(parts) >= 2 && parts[0] == "loaders" {
		loader := parts[1]
		mcVer := r.URL.Query().Get("game_version")
		versions := GetLoaderVersions(r.Context(), loader, mcVer)
		writeJSON(w, http.StatusOK, map[string]any{
			"loader":       loader,
			"game_version": mcVer,
			"versions":     versions,
		})
		return
	}

	if len(parts) >= 2 && parts[0] != "packs" && parts[0] != "loaders" {
		packID := parts[0]
		relPath := strings.Join(parts[1:], "/")
		data, contentType, err := h.manager.ServePackFile(packID, relPath)
		if err != nil {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
		return
	}

	// Auth check for studio management routes. Pack IDs are global objects in
	// the generated RPC mapping, so keep the global object here for parity
	// (ADR 0012), but fail closed on missing dependencies.
	action := rbac.ActionRead
	if r.Method != http.MethodGet {
		action = rbac.ActionCreate
	}
	user, err := requireRequestObjectUser(r, h.authManager, h.enforcer, rbac.ResourceModpacks, action, "*")
	if err != nil {
		writePolicyError(w, err)
		return
	}
	// Upload-consuming routes need the same identity to enforce session
	// ownership; the request context is the only path to those subhandlers.
	r = r.WithContext(auth.WithUser(r.Context(), user))

	// Route /api/v1/packwiz/packs...
	if len(parts) == 1 && parts[0] == "packs" {
		switch r.Method {
		case http.MethodGet:
			h.handleListPacks(w, r)
			return
		case http.MethodPost:
			h.handleCreatePack(w, r)
			return
		}
	}

	if len(parts) >= 2 && parts[0] == "packs" {
		// Sub-route /api/v1/packwiz/packs/import
		if parts[1] == "import" && r.Method == http.MethodPost {
			h.handleImportPack(w, r)
			return
		}

		packID := parts[1]

		if len(parts) == 2 {
			switch r.Method {
			case http.MethodGet:
				h.handleGetPack(w, r, packID)
			case http.MethodPut:
				h.handleUpdatePack(w, r, packID)
			case http.MethodDelete:
				h.handleDeletePack(w, r, packID)
			default:
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			}
			return
		}

		subRoute := parts[2]
		switch {
		case subRoute == "clone" && r.Method == http.MethodPost:
			h.handleClonePack(w, r, packID)
		case subRoute == "refresh" && r.Method == http.MethodPost:
			h.handleRefreshPack(w, r, packID)
		case subRoute == "updates" && r.Method == http.MethodGet:
			h.handleCheckUpdates(w, r, packID)
		case subRoute == "updates" && r.Method == http.MethodPost:
			h.handleApplyUpdates(w, r, packID)
		case subRoute == "migrate" && r.Method == http.MethodPost:
			h.handleMigrate(w, r, packID)
		case subRoute == "mc-version" && r.Method == http.MethodPost:
			h.handleSetMCVersion(w, r, packID)
		case subRoute == "files" && r.Method == http.MethodGet:
			h.handleListFiles(w, r, packID)
		case subRoute == "files" && r.Method == http.MethodPost:
			h.handleSaveFile(w, r, packID)
		case subRoute == "files" && r.Method == http.MethodDelete:
			h.handleDeleteFile(w, r, packID)
		case subRoute == "datapack-folder" && r.Method == http.MethodPut:
			h.handleSetDatapackFolder(w, r, packID)
		case subRoute == "content" && len(parts) >= 4 && parts[3] == "search" && r.Method == http.MethodGet:
			h.handleSearchContent(w, r, packID)
		case subRoute == "content" && len(parts) >= 4 && parts[3] == "versions" && r.Method == http.MethodGet:
			h.handleListContentVersions(w, r, packID)
		case subRoute == "content" && len(parts) >= 4 && parts[3] == "url" && r.Method == http.MethodPost:
			h.handleAddContentURL(w, r, packID)
		case subRoute == "content" && len(parts) >= 4 && parts[3] == "upload" && r.Method == http.MethodPost:
			h.handleUploadContent(w, r, packID)
		case subRoute == "content" && len(parts) == 3 && r.Method == http.MethodPost:
			h.handleAddContent(w, r, packID)
		case subRoute == "content" && len(parts) >= 5 && r.Method == http.MethodPatch:
			h.handleUpdateContent(w, r, packID, parts[3], parts[4])
		case subRoute == "content" && len(parts) >= 5 && r.Method == http.MethodDelete:
			h.handleDeleteContent(w, r, packID, parts[3], parts[4])
		case subRoute == "datapacks" && len(parts) >= 4 && parts[3] == "upload" && r.Method == http.MethodPost:
			h.handleUploadDatapack(w, r, packID)
		case subRoute == "datapacks" && r.Method == http.MethodPost:
			h.handleAddDatapack(w, r, packID)
		case subRoute == "mods" && len(parts) >= 4 && parts[3] == "url" && r.Method == http.MethodPost:
			h.handleAddModURL(w, r, packID)
		case subRoute == "mods" && len(parts) >= 4 && parts[3] == "batch" && r.Method == http.MethodPost:
			h.handleBatchMods(w, r, packID)
		case subRoute == "mods" && len(parts) == 3 && r.Method == http.MethodPost:
			h.handleAddMod(w, r, packID)
		case subRoute == "mods" && len(parts) >= 4 && r.Method == http.MethodPatch:
			modSlug := parts[3]
			h.handleUpdateMod(w, r, packID, modSlug)
		case subRoute == "mods" && len(parts) >= 4 && r.Method == http.MethodDelete:
			modSlug := parts[3]
			h.handleDeleteMod(w, r, packID, modSlug)
		case subRoute == "export" && len(parts) >= 4:
			exportFormat := parts[3]
			h.handleExport(w, r, packID, exportFormat)
		case subRoute == "deploy" && r.Method == http.MethodPost:
			h.handleDeploy(w, r, packID)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
		return
	}

	http.Error(w, "not found", http.StatusNotFound)
}

func (h *PackwizHandler) handleListPacks(w http.ResponseWriter, r *http.Request) {
	packs, err := h.ops.List(r.Context())
	if err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"packs": packs})
}

func (h *PackwizHandler) handleGetPack(w http.ResponseWriter, r *http.Request, packID string) {
	pack, err := h.ops.Get(r.Context(), packID)
	if err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, pack)
}

func (h *PackwizHandler) handleCreatePack(w http.ResponseWriter, r *http.Request) {
	var pack packwiz.Pack
	if err := json.NewDecoder(r.Body).Decode(&pack); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	created, err := h.manager.CreatePack(&pack)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create pack: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *PackwizHandler) handleUpdatePack(w http.ResponseWriter, r *http.Request, packID string) {
	var pack packwiz.Pack
	if err := json.NewDecoder(r.Body).Decode(&pack); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	pack.ID = packID

	if err := h.manager.UpdatePack(&pack); err != nil {
		http.Error(w, fmt.Sprintf("failed to update pack: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleDeletePack(w http.ResponseWriter, r *http.Request, packID string) {
	if err := h.ops.Delete(r.Context(), packID); err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleClonePack(w http.ResponseWriter, r *http.Request, packID string) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	cloned, err := h.ops.Clone(r.Context(), packID, req.Name)
	if err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, cloned)
}

func (h *PackwizHandler) handleRefreshPack(w http.ResponseWriter, r *http.Request, packID string) {
	if err := h.ops.Refresh(r.Context(), packID); err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Pack re-indexed and refreshed successfully"})
}

func (h *PackwizHandler) handleCheckUpdates(w http.ResponseWriter, r *http.Request, packID string) {
	updates, err := h.ops.CheckUpdates(r.Context(), packID, r.URL.Query().Get("kind"))
	if err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updates": updates})
}

func (h *PackwizHandler) handleApplyUpdates(w http.ResponseWriter, r *http.Request, packID string) {
	var req struct {
		Slug            string `json:"slug"`
		Kind            string `json:"kind"`
		TargetVersionID string `json:"target_version_id"`
		TargetFileName  string `json:"target_file_name"`
		TargetURL       string `json:"target_url"`
		HashFormat      string `json:"hash_format"`
		Hash            string `json:"hash"`
		SHA512          string `json:"sha512"`
		ApplyAll        bool   `json:"apply_all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Kind == "" {
		req.Kind = packwiz.ContentKindMod
	}

	if req.ApplyAll {
		updates, err := h.manager.CheckContentUpdatesContext(r.Context(), packID)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to check updates: %v", err), http.StatusInternalServerError)
			return
		}
		updatedCount := 0
		for _, u := range updates {
			if u.Kind != req.Kind || !u.UpdateAvailable || u.LatestVersionID == "" {
				continue
			}
			if err := h.manager.ApplyContentUpdate(packID, u.Kind, u.Slug, u.LatestVersionID, u.LatestFileName, u.LatestDownloadURL, u.LatestHashFormat, u.LatestHash, u.LatestSHA512); err == nil {
				updatedCount++
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "updated_count": updatedCount})
		return
	}

	if req.Slug == "" || req.TargetVersionID == "" {
		http.Error(w, "slug and target_version_id are required", http.StatusBadRequest)
		return
	}

	if err := h.manager.ApplyContentUpdate(packID, req.Kind, req.Slug, req.TargetVersionID, req.TargetFileName, req.TargetURL, req.HashFormat, req.Hash, req.SHA512); err != nil {
		http.Error(w, fmt.Sprintf("failed to apply update: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleAddMod(w http.ResponseWriter, r *http.Request, packID string) {
	var mod packwiz.ModItem
	if err := json.NewDecoder(r.Body).Decode(&mod); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	// Compatibility adapter: the shared PackwizOperations owns add and optional
	// transitive dependency resolution so the generated service cannot drift.
	resolveDependencies := r.URL.Query().Get("resolve_dependencies") == "true"
	result, err := h.ops.AddMod(r.Context(), packID, mod, resolveDependencies,
		r.URL.Query().Get("loader"), r.URL.Query().Get("mc_version"))
	if err != nil {
		writePackwizError(w, err)
		return
	}
	if result.DependencyError != "" {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "dependency_error": result.DependencyError})
		return
	}
	if resolveDependencies {
		writeJSON(w, http.StatusOK, map[string]any{
			"success":            true,
			"added_dependencies": result.AddedDependencies,
			"skipped":            result.Skipped,
			"unresolved":         result.Unresolved,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleAddModURL(w http.ResponseWriter, r *http.Request, packID string) {
	var req packwiz.DirectURLModRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	mod, err := h.ops.AddModFromURL(r.Context(), packID, req)
	if err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, mod)
}

func (h *PackwizHandler) handleBatchMods(w http.ResponseWriter, r *http.Request, packID string) {
	var req struct {
		Action string   `json:"action"` // "set_side", "pin", "unpin", "remove"
		Slugs  []string `json:"slugs"`
		Side   string   `json:"side,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	entries, err := h.ops.BatchMods(r.Context(), packID, req.Action, req.Slugs, req.Side)
	if err != nil {
		writePackwizError(w, err)
		return
	}
	results := make([]map[string]any, 0, len(entries))
	var succeeded int
	for _, entry := range entries {
		results = append(results, map[string]any{
			"slug": entry.Slug, "success": entry.Success,
			"message": entry.Message, "error_code": entry.ErrorCode,
		})
		if entry.Success {
			succeeded++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "count": succeeded, "results": results})
}

func (h *PackwizHandler) handleUpdateMod(w http.ResponseWriter, r *http.Request, packID, slug string) {
	var req struct {
		Side   string `json:"side"`
		Pinned bool   `json:"pinned"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if err := h.ops.UpdateMod(r.Context(), packID, slug, req.Side, req.Pinned); err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleDeleteMod(w http.ResponseWriter, r *http.Request, packID, slug string) {
	if err := h.ops.DeleteMod(r.Context(), packID, slug); err != nil {
		writePackwizError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleListFiles(w http.ResponseWriter, r *http.Request, packID string) {
	files, err := h.manager.ListPackFiles(packID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list files: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files})
}

func (h *PackwizHandler) handleSaveFile(w http.ResponseWriter, r *http.Request, packID string) {
	// Multipart file upload or JSON payload
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "failed to parse multipart form", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file field", http.StatusBadRequest)
			return
		}
		defer file.Close()

		relPath := r.FormValue("path")
		if relPath == "" {
			relPath = "config/" + header.Filename
		}

		if err := h.manager.SavePackFile(packID, relPath, file); err != nil {
			http.Error(w, fmt.Sprintf("failed to save file: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "path": relPath})
		return
	}

	var req struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}

	if err := h.manager.SavePackFile(packID, req.Path, strings.NewReader(req.Content)); err != nil {
		http.Error(w, fmt.Sprintf("failed to save file: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "path": req.Path})
}

func (h *PackwizHandler) handleDeleteFile(w http.ResponseWriter, r *http.Request, packID string) {
	path := r.URL.Query().Get("path")
	if path == "" {
		http.Error(w, "path query parameter is required", http.StatusBadRequest)
		return
	}

	if err := h.manager.DeletePackFile(packID, path); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete file: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleMigrate(w http.ResponseWriter, r *http.Request, packID string) {
	var req struct {
		TargetMC     string `json:"target_mc"`
		TargetLoader string `json:"target_loader"`
		Apply        bool   `json:"apply"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.TargetMC == "" {
		http.Error(w, "target_mc is required", http.StatusBadRequest)
		return
	}
	if req.TargetLoader == "" {
		req.TargetLoader = "fabric"
	}

	if req.Apply {
		report, err := h.manager.ApplyMigration(packID, req.TargetMC, req.TargetLoader)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to apply migration: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, report)
		return
	}

	report, err := h.manager.SimulateMigrationContext(r.Context(), packID, req.TargetMC, req.TargetLoader)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to simulate migration: %v", err), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// handleSetMCVersion changes the pack's Minecraft version, marking installed
// mods stale. With apply=false it is a dry run (no writes). With apply=true
// it writes the new version and attaches a migration simulation so the caller
// can offer the update-and-resolve-conflicts flow in one round trip.
func (h *PackwizHandler) handleSetMCVersion(w http.ResponseWriter, r *http.Request, packID string) {
	var req struct {
		TargetMC string `json:"target_mc"`
		Apply    bool   `json:"apply"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.TargetMC == "" {
		http.Error(w, "target_mc is required", http.StatusBadRequest)
		return
	}

	if !req.Apply {
		report, err := h.manager.DryRunMCVersion(packID, req.TargetMC)
		if err != nil {
			http.Error(w, fmt.Sprintf("failed to check MC version change: %v", err), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, report)
		return
	}

	stale, err := h.manager.SetMCVersion(packID, req.TargetMC)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to set MC version: %v", err), http.StatusInternalServerError)
		return
	}

	pack, err := h.manager.GetPack(packID)
	if err != nil {
		http.Error(w, "pack not found after MC version change", http.StatusInternalServerError)
		return
	}
	migration, err := h.manager.SimulateMigrationContext(r.Context(), packID, req.TargetMC, pack.ModLoader)
	if err != nil {
		// Stale marking succeeded; surface the simulation failure alongside.
		writeJSON(w, http.StatusOK, map[string]any{"stale": stale, "migration_error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"stale": stale, "migration": migration})
}

// maxImportUploadBytes is the total multipart body ceiling for a modpack
// import. ParseMultipartForm's argument only sizes the in-memory portion; it
// does not bound the request. MaxBytesReader below is the real cap. A var (not
// a const) so the limit harness can lower it without a gigabyte fixture.
var maxImportUploadBytes int64 = 1 << 30 // 1 GiB

func (h *PackwizHandler) handleImportPack(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxImportUploadBytes)
		if err := r.ParseMultipartForm(64 << 20); err != nil {
			http.Error(w, "failed to parse multipart form", http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file", http.StatusBadRequest)
			return
		}
		defer file.Close()

		// Read at most one byte past the declared ceiling so an oversized
		// part is rejected rather than buffered without bound.
		data, err := io.ReadAll(io.LimitReader(file, maxImportUploadBytes+1))
		if err != nil {
			http.Error(w, "failed to read uploaded file", http.StatusInternalServerError)
			return
		}
		if int64(len(data)) > maxImportUploadBytes {
			http.Error(w, "uploaded archive exceeds the import size limit", http.StatusRequestEntityTooLarge)
			return
		}
		readerAt := bytes.NewReader(data)
		size := int64(len(data))

		format := r.FormValue("format")
		if format == "" {
			if strings.HasSuffix(header.Filename, ".mrpack") {
				format = "mrpack"
			} else {
				format = "curseforge"
			}
		}

		opts := packwiz.ImportOptions{
			Name:          r.FormValue("name"),
			Author:        r.FormValue("author"),
			Version:       r.FormValue("version"),
			MCVersion:     r.FormValue("mc_version"),
			ModLoader:     r.FormValue("mod_loader"),
			LoaderVersion: r.FormValue("loader_version"),
		}

		var pack *packwiz.Pack
		switch format {
		case "mrpack":
			pack, err = h.manager.ImportMrpack(readerAt, size, opts)
		case "curseforge":
			pack, err = h.manager.ImportCurseForge(readerAt, size, opts)
		case "packwiz":
			pack, err = h.manager.ImportPackwizZip(readerAt, size, opts)
		default:
			http.Error(w, "unsupported import format", http.StatusBadRequest)
			return
		}

		if err != nil {
			http.Error(w, fmt.Sprintf("import failed: %v", err), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusCreated, pack)
		return
	}

	http.Error(w, "multipart/form-data required for import", http.StatusBadRequest)
}

func errorAsDropped(err error, target **packwiz.DroppedModsError) bool {
	return errors.As(err, target)
}

// parseExportOptions reads per-launcher export options from the query string:
// ?include_client=false&optional_mode=optional&overrides=configs-only&summary=...
func parseExportOptions(r *http.Request) packwiz.ExportOptions {
	opts := packwiz.DefaultExportOptions()
	q := r.URL.Query()
	if _, ok := q["include_client"]; ok {
		v := strings.ToLower(strings.TrimSpace(q.Get("include_client")))
		opts.IncludeClient = v != "false" && v != "0" && v != "no" && v != "off"
	}
	if v := strings.TrimSpace(q.Get("optional_mode")); v != "" {
		opts.OptionalMode = v
	}
	if v := strings.TrimSpace(q.Get("overrides")); v != "" {
		opts.Overrides = v
	}
	if v := q.Get("summary"); v != "" {
		opts.Summary = v
	}
	return opts
}

func (h *PackwizHandler) handleExport(w http.ResponseWriter, r *http.Request, packID, format string) {
	pack, err := h.manager.GetPack(packID)
	if err != nil {
		http.Error(w, "pack not found", http.StatusNotFound)
		return
	}

	opts := parseExportOptions(r)
	var buf bytes.Buffer
	var filename, contentType string

	switch format {
	case "packwiz":
		filename = fmt.Sprintf("%s-%s.packwiz.zip", pack.Name, pack.Version)
		contentType = "application/zip"
		if err := h.manager.ExportPackwizZip(packID, &buf, opts); err != nil {
			h.log.Error("Failed to export packwiz zip: %v", err)
			http.Error(w, "failed to export packwiz zip", http.StatusInternalServerError)
			return
		}
	case "mrpack":
		filename = fmt.Sprintf("%s-%s.mrpack", pack.Name, pack.Version)
		contentType = "application/x-modrinth-modpack+zip"
		if err := h.manager.ExportMrpack(packID, &buf, opts); err != nil {
			h.log.Error("Failed to export mrpack: %v", err)
			http.Error(w, "failed to export mrpack", http.StatusInternalServerError)
			return
		}
	case "curseforge", "zip":
		filename = fmt.Sprintf("%s-%s.zip", pack.Name, pack.Version)
		contentType = "application/zip"
		if err := h.manager.ExportCurseForge(packID, &buf, opts); err != nil {
			var dropped *packwiz.DroppedModsError
			if ok := errorAsDropped(err, &dropped); ok {
				h.log.Error("CurseForge export blocked (data loss): %v", err)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnprocessableEntity)
				_ = json.NewEncoder(w).Encode(map[string]any{
					"error":   err.Error(),
					"dropped": dropped.Dropped,
				})
				return
			}
			h.log.Error("Failed to export curseforge pack: %v", err)
			http.Error(w, "failed to export curseforge pack", http.StatusInternalServerError)
			return
		}
	case "mmc", "prism", "multimc", "instance":
		filename = fmt.Sprintf("%s-%s-mmc.zip", pack.Name, pack.Version)
		contentType = "application/zip"
		if err := h.manager.ExportMMC(packID, &buf, opts); err != nil {
			h.log.Error("Failed to export MMC instance: %v", err)
			http.Error(w, "failed to export instance zip", http.StatusInternalServerError)
			return
		}
	default:
		http.Error(w, "unsupported export format", http.StatusBadRequest)
		return
	}

	sum := sha256.Sum256(buf.Bytes())
	sha256Hex := hex.EncodeToString(sum[:])
	sha256B64 := base64.StdEncoding.EncodeToString(sum[:])

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", filename))
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.Header().Set("X-Checksum-SHA256", sha256Hex)
	w.Header().Set("Digest", "SHA-256="+sha256B64)
	w.Header().Set("ETag", fmt.Sprintf("\"%s\"", sha256Hex))

	_, _ = w.Write(buf.Bytes())
}

func (h *PackwizHandler) handleDeploy(w http.ResponseWriter, r *http.Request, packID string) {
	var req DeployPackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	pack, err := h.manager.GetPack(packID)
	if err != nil {
		http.Error(w, "pack not found", http.StatusNotFound)
		return
	}

	server, err := h.store.GetServer(r.Context(), req.ServerID)
	if err != nil {
		http.Error(w, "server not found", http.StatusNotFound)
		return
	}

	cfg, err := h.store.GetServerConfig(r.Context(), req.ServerID)
	if err != nil {
		http.Error(w, "failed to get server config", http.StatusInternalServerError)
		return
	}

	if req.Mode != "sync" {
		if err := pack.ValidateServerBake(); err != nil {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
	}

	// Align Minecraft version and mod loader
	oldMCVersion, oldModLoader := server.MCVersion, server.ModLoader
	server.MCVersion = pack.MCVersion
	server.ModLoader = storage.ModLoader(strings.ToLower(pack.ModLoader))
	runtimeChanged := oldMCVersion != server.MCVersion || oldModLoader != server.ModLoader

	if req.Mode == "sync" {
		if err := h.store.UpdateServer(r.Context(), server); err != nil {
			http.Error(w, fmt.Sprintf("failed to update server: %v", err), http.StatusInternalServerError)
			return
		}
		// Mode A: Live Sync URL
		host := r.Host
		if host == "" {
			host = "localhost:8080"
		}
		packURL := fmt.Sprintf("http://%s/api/v1/packwiz/%s/pack.toml", host, packID)
		cfg.PackwizURL = &packURL
		typeStr := strings.ToUpper(pack.ModLoader)
		cfg.Type = &typeStr
		cfg.Version = &pack.MCVersion

		if err := h.store.UpdateServerConfig(r.Context(), cfg); err != nil {
			if runtimeChanged {
				server.MCVersion = oldMCVersion
				server.ModLoader = oldModLoader
				if rbErr := h.store.UpdateServer(r.Context(), server); rbErr != nil {
					h.log.Error("packwiz sync rollback failed after config error: %v (original: %v)", rbErr, err)
				}
			}
			http.Error(w, fmt.Sprintf("failed to update config: %v", err), http.StatusInternalServerError)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"mode":    "sync",
			"url":     packURL,
			"message": fmt.Sprintf("Server configured with PACKWIZ_URL=%s", packURL),
		})
		return
	}

	// Mode B: bake into the server's data directory on the node's mapped storage.
	dataRoot, err := serverDataDirectory(r.Context(), h.store, server, true, h.storageResolver)
	if err != nil {
		if nodestorage.IsUnavailable(err) {
			h.log.Error("packwiz bake refused: %v", err)
			http.Error(w, "node storage is not available", http.StatusServiceUnavailable)
			return
		}
		h.log.Error("packwiz bake storage: %v", err)
		http.Error(w, "failed to resolve server storage", http.StatusInternalServerError)
		return
	}
	modsDir := minecraft.GetModsPath(dataRoot, server.ModLoader)
	if modsDir == "" {
		http.Error(w, "server does not support mods", http.StatusBadRequest)
		return
	}

	levelName := ""
	if props, propErr := minecraft.LoadServerProperties(dataRoot); propErr == nil {
		levelName = props.GetString("level-name", "")
	}

	if runtimeChanged {
		if err := h.store.UpdateServer(r.Context(), server); err != nil {
			http.Error(w, fmt.Sprintf("failed to update server: %v", err), http.StatusInternalServerError)
			return
		}
	}

	result, err := h.manager.BakeToServerContent(packID, modsDir, minecraft.GetDatapacksPath(dataRoot, levelName))
	if err != nil {
		if runtimeChanged {
			server.MCVersion = oldMCVersion
			server.ModLoader = oldModLoader
			if rbErr := h.store.UpdateServer(r.Context(), server); rbErr != nil {
				h.log.Error("packwiz bake rollback failed after bake error: %v (original: %v)", rbErr, err)
			}
		}
		http.Error(w, fmt.Sprintf("failed to bake to server: %v", err), http.StatusInternalServerError)
		return
	}
	installed := []string{fmt.Sprintf("%d mod(s)", result.Mods)}
	if result.Datapacks > 0 {
		installed = append(installed, fmt.Sprintf("%d datapack(s)", result.Datapacks))
	}
	message := fmt.Sprintf("Baked %s into %s", strings.Join(installed, " and "), dataRoot)
	if result.ClientOnly > 0 {
		message += fmt.Sprintf("; %d client-side item(s) ship through packwiz sync or an export", result.ClientOnly)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"mode":        "bake",
		"installed":   result.Mods,
		"datapacks":   result.Datapacks,
		"client_only": result.ClientOnly,
		"message":     message,
	})
}
