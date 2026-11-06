// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/internal/packwiz"
)

// handleSearchContent searches Modrinth for one content kind. The pack supplies
// the default game version so the Studio does not have to repeat it.
func (h *PackwizHandler) handleSearchContent(w http.ResponseWriter, r *http.Request, packID string) {
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = packwiz.ContentKindDatapack
	}
	if !packwiz.ValidContentKind(kind) {
		http.Error(w, "invalid content kind", http.StatusBadRequest)
		return
	}

	gameVersion := strings.TrimSpace(r.URL.Query().Get("game_version"))
	if gameVersion == "" {
		if pack, err := h.manager.GetPack(packID); err == nil {
			gameVersion = pack.MCVersion
		}
	}
	loader := strings.TrimSpace(r.URL.Query().Get("loader"))
	offset := parseNonNegativeInt(r.URL.Query().Get("offset"), 0)
	limit := parseNonNegativeInt(r.URL.Query().Get("limit"), 20)
	if limit > 50 {
		limit = 50
	}

	client := modrinth.NewClient(h.modrinthConfig())
	resp, err := client.SearchProjects(r.Context(), modrinthProjectType(kind), r.URL.Query().Get("query"), gameVersion, loader, offset, limit)
	if err != nil {
		h.log.Error("Content search failed for %s: %v", kind, err)
		http.Error(w, "search failed", http.StatusBadGateway)
		return
	}

	results := make([]map[string]any, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		results = append(results, map[string]any{
			"id":            hit.ProjectID,
			"slug":          hit.Slug,
			"title":         hit.Title,
			"description":   hit.Description,
			"icon_url":      hit.IconURL,
			"author":        hit.Author,
			"downloads":     hit.Downloads,
			"project_type":  hit.ProjectType,
			"game_versions": hit.Versions,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":         kind,
		"game_version": gameVersion,
		"total_hits":   resp.TotalHits,
		"results":      results,
	})
}

// handleListContentVersions returns installable files for one Modrinth project,
// newest first, filtered by the pack's game version and the requested loader.
func (h *PackwizHandler) handleListContentVersions(w http.ResponseWriter, r *http.Request, packID string) {
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	if projectID == "" {
		http.Error(w, "project_id is required", http.StatusBadRequest)
		return
	}
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	if kind == "" {
		kind = packwiz.ContentKindDatapack
	}
	gameVersion := strings.TrimSpace(r.URL.Query().Get("game_version"))
	if gameVersion == "" {
		if pack, err := h.manager.GetPack(packID); err == nil {
			gameVersion = pack.MCVersion
		}
	}
	loader := strings.TrimSpace(r.URL.Query().Get("loader"))

	client := modrinth.NewClient(h.modrinthConfig())
	versions, err := client.GetModpackVersions(r.Context(), projectID)
	if err != nil {
		h.log.Error("Content versions failed for %s: %v", projectID, err)
		http.Error(w, "version lookup failed", http.StatusBadGateway)
		return
	}

	out := make([]map[string]any, 0, len(versions))
	for _, version := range versions {
		if gameVersion != "" && !versionHasString(version.GameVersions, gameVersion) {
			continue
		}
		if loader != "" && !versionHasString(version.Loaders, loader) {
			continue
		}
		file := contentVersionFile(kind, version.Files)
		if file == nil {
			continue
		}
		hashFormat, hash := "sha512", file.Hashes.SHA512
		if hash == "" {
			hashFormat, hash = "sha1", file.Hashes.SHA1
		}
		out = append(out, map[string]any{
			"id":             version.ID,
			"name":           version.Name,
			"version_number": version.VersionNumber,
			"version_type":   version.VersionType,
			"game_versions":  version.GameVersions,
			"loaders":        version.Loaders,
			"file_name":      file.Filename,
			"download_url":   file.URL,
			"file_size":      file.Size,
			"hash_format":    hashFormat,
			"hash":           hash,
			"sha512":         file.Hashes.SHA512,
			"changelog":      version.Changelog,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": out})
}

// handleAddContent adds a tracked Modrinth project, resolving the newest
// compatible file when the caller does not name a version.
func (h *PackwizHandler) handleAddContent(w http.ResponseWriter, r *http.Request, packID string) {
	var item packwiz.ModItem
	if err := json.NewDecoder(r.Body).Decode(&item); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !packwiz.ValidContentKind(item.Kind) {
		http.Error(w, "invalid content kind", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(item.Name) == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	if item.Platform == "" {
		item.Platform = "modrinth"
	}
	if item.Platform == "modrinth" && strings.TrimSpace(item.ProjectID) == "" {
		http.Error(w, "project_id is required for Modrinth content", http.StatusBadRequest)
		return
	}

	// Resolve version details server-side when the client only sent a project.
	if strings.TrimSpace(item.DownloadURL) == "" || strings.TrimSpace(item.FileName) == "" {
		if err := h.resolveModrinthContent(r, packID, &item); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
	}

	if err := h.manager.AddContent(packID, item); err != nil {
		http.Error(w, fmt.Sprintf("failed to add content: %v", err), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "slug": item.Slug})
}

func (h *PackwizHandler) resolveModrinthContent(r *http.Request, packID string, item *packwiz.ModItem) error {
	pack, err := h.manager.GetPack(packID)
	if err != nil {
		return err
	}
	client := modrinth.NewClient(h.modrinthConfig())
	versions, err := client.GetModpackVersions(r.Context(), item.ProjectID)
	if err != nil {
		return fmt.Errorf("version lookup failed: %w", err)
	}
	for _, version := range versions {
		if item.VersionID != "" && version.ID != item.VersionID {
			continue
		}
		if !versionHasString(version.GameVersions, pack.MCVersion) {
			continue
		}
		if item.Kind == packwiz.ContentKindMod && !versionHasString(version.Loaders, pack.ModLoader) {
			continue
		}
		file := contentVersionFile(item.Kind, version.Files)
		if file == nil {
			continue
		}
		item.VersionID = version.ID
		if item.FileName == "" {
			item.FileName = filepath.Base(file.Filename)
		}
		item.DownloadURL = file.URL
		item.FileSize = file.Size
		item.HashFormat, item.Hash = "sha512", file.Hashes.SHA512
		if item.Hash == "" {
			item.HashFormat, item.Hash = "sha1", file.Hashes.SHA1
		}
		item.SHA512 = file.Hashes.SHA512
		return nil
	}
	return fmt.Errorf("no compatible %s version found for Minecraft %s", item.Kind, pack.MCVersion)
}

func (h *PackwizHandler) handleAddContentURL(w http.ResponseWriter, r *http.Request, packID string) {
	var req packwiz.DirectURLContentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	item, err := h.manager.AddContentFromURL(packID, req)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to add content: %v", err), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *PackwizHandler) handleUploadContent(w http.ResponseWriter, r *http.Request, packID string) {
	var req struct {
		Kind              string `json:"kind"`
		UploadSessionID   string `json:"upload_session_id"`
		FileName          string `json:"file_name"`
		Name              string `json:"name"`
		Side              string `json:"side"`
		Pinned            bool   `json:"pinned"`
		Optional          bool   `json:"optional"`
		Default           bool   `json:"default"`
		OptionDescription string `json:"option_description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.UploadSessionID) == "" {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if !packwiz.ValidContentKind(req.Kind) {
		http.Error(w, "invalid content kind", http.StatusBadRequest)
		return
	}

	tempPath, originalFilename, err := h.uploadManager.GetTempPath(req.UploadSessionID)
	if err != nil {
		http.Error(w, "upload session not found or not completed", http.StatusNotFound)
		return
	}
	cleanup := func() {
		_ = os.Remove(tempPath)
		_ = h.uploadManager.CleanupSession(req.UploadSessionID)
	}

	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		fileName = filepath.Base(originalFilename)
	}

	f, err := os.Open(tempPath)
	if err != nil {
		cleanup()
		http.Error(w, "uploaded file unavailable", http.StatusInternalServerError)
		return
	}
	defer func() { _ = f.Close() }()

	folder, err := h.manager.ContentFolder(packID, req.Kind)
	if err != nil {
		cleanup()
		http.Error(w, "pack not found", http.StatusNotFound)
		return
	}
	serveURL := contentServeURL(r, packID, folder, fileName)
	item, installErr := h.manager.AddContentFile(packID, req.Kind, fileName, packwiz.ContentUploadMetadata{
		Name:              req.Name,
		Side:              req.Side,
		Pinned:            req.Pinned,
		Optional:          req.Optional,
		Default:           req.Default,
		OptionDescription: req.OptionDescription,
	}, serveURL, f)
	cleanup()
	if installErr != nil {
		http.Error(w, fmt.Sprintf("failed to install content: %v", installErr), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *PackwizHandler) handleUpdateContent(w http.ResponseWriter, r *http.Request, packID, kind, slug string) {
	var patch packwiz.ContentPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := h.manager.UpdateContent(packID, kind, slug, patch); err != nil {
		http.Error(w, fmt.Sprintf("failed to update content: %v", err), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleDeleteContent(w http.ResponseWriter, r *http.Request, packID, kind, slug string) {
	if err := h.manager.DeleteContent(packID, kind, slug); err != nil {
		http.Error(w, fmt.Sprintf("failed to delete content: %v", err), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

func (h *PackwizHandler) handleSetDatapackFolder(w http.ResponseWriter, r *http.Request, packID string) {
	var req struct {
		Folder string `json:"folder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if err := h.manager.SetDatapackFolder(packID, req.Folder); err != nil {
		http.Error(w, fmt.Sprintf("failed to set datapack folder: %v", err), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "folder": req.Folder})
}

func (h *PackwizHandler) modrinthConfig() *config.Config {
	if h.config != nil {
		return h.config
	}
	return &config.Config{}
}

func modrinthProjectType(kind string) string {
	switch kind {
	case packwiz.ContentKindResourcePack:
		return "resourcepack"
	case packwiz.ContentKindShaderPack:
		return "shader"
	case packwiz.ContentKindDatapack:
		return "datapack"
	default:
		return "mod"
	}
}

func versionHasString(list []string, want string) bool {
	for _, value := range list {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

// contentVersionFile picks the primary artifact of the expected type. Modrinth
// projects sometimes publish a mod jar and a datapack zip as sibling files.
func contentVersionFile(kind string, files []modrinth.File) *modrinth.File {
	wantExt := ".jar"
	if kind != packwiz.ContentKindMod {
		wantExt = ".zip"
	}
	var fallback *modrinth.File
	for i := range files {
		file := &files[i]
		if !strings.EqualFold(filepath.Ext(file.Filename), wantExt) {
			continue
		}
		if file.Primary {
			return file
		}
		if fallback == nil {
			fallback = file
		}
	}
	return fallback
}

func contentServeURL(r *http.Request, packID, folder, fileName string) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = proto
	}
	parts := strings.Split(strings.Trim(folder, "/"), "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return fmt.Sprintf("%s://%s/api/v1/packwiz/%s/%s/%s", scheme, r.Host, url.PathEscape(packID), strings.Join(parts, "/"), url.PathEscape(fileName))
}

func parseNonNegativeInt(raw string, fallback int) int {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return fallback
	}
	return value
}
