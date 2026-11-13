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
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers"
	"github.com/athNdev/carbon-panel/internal/indexers/fuego"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

type SearchModResult struct {
	ID          string   `json:"id"`
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	IconURL     string   `json:"icon_url"`
	Author      string   `json:"author"`
	Downloads   int64    `json:"downloads"`
	Categories  []string `json:"categories"`
	Platform    string   `json:"platform"`
	ClientSide  string   `json:"client_side,omitempty"`
	ServerSide  string   `json:"server_side,omitempty"`
	Installed   bool     `json:"installed"`
}

type ModDependencyItem struct {
	ProjectID      string `json:"project_id"`
	VersionID      string `json:"version_id,omitempty"`
	DependencyType string `json:"dependency_type"` // "required", "optional"
	Title          string `json:"title,omitempty"`
}

type ModVersionItem struct {
	ID            string              `json:"id"`
	VersionNumber string              `json:"version_number"`
	Name          string              `json:"name"`
	VersionType   string              `json:"version_type"`
	FileName      string              `json:"file_name"`
	DownloadURL   string              `json:"download_url"`
	FileSize      int64               `json:"file_size"`
	Dependencies  []ModDependencyItem `json:"dependencies"`
}

type InstallModItem struct {
	Name        string `json:"name"`
	FileName    string `json:"file_name"`
	DownloadURL string `json:"download_url"`
}

// UnmarshalJSON accepts the canonical snake_case keys as well as the
// camelCase / no-underscore aliases older console builds sent. The console
// once posted `filename` instead of `file_name`, which decoded to an empty
// FileName and made every install fail with "missing url or filename".
func (i *InstallModItem) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	var s string
	if v, ok := raw["name"]; ok {
		if err := json.Unmarshal(v, &s); err == nil {
			i.Name = s
		}
	}
	for _, k := range []string{"file_name", "filename", "fileName"} {
		if v, ok := raw[k]; ok {
			var f string
			if err := json.Unmarshal(v, &f); err == nil && strings.TrimSpace(f) != "" {
				i.FileName = strings.TrimSpace(f)
				break
			}
		}
	}
	for _, k := range []string{"download_url", "downloadUrl", "url"} {
		if v, ok := raw[k]; ok {
			var u string
			if err := json.Unmarshal(v, &u); err == nil && strings.TrimSpace(u) != "" {
				i.DownloadURL = strings.TrimSpace(u)
				break
			}
		}
	}
	return nil
}

type InstallModsRequest struct {
	Items []InstallModItem `json:"items"`
}

type InstallModsResponse struct {
	Success   bool     `json:"success"`
	Installed []string `json:"installed"`
	Errors    []string `json:"errors,omitempty"`
	Message   string   `json:"message"`
}

// ModOnlineManager handles searching, fetching versions and 1-click installing mods
type ModOnlineManager struct {
	store             *storage.Store
	log               *logger.Logger
	authManager       *auth.Manager
	enforcer          *rbac.Enforcer
	httpClient        *http.Client
	modrinthBaseURL   string
	curseForgeBaseURL string
}

type ModOnlineOptions struct {
	HTTPClient                         *http.Client
	ModrinthBaseURL, CurseForgeBaseURL string
}

func NewModOnlineManager(store *storage.Store, log *logger.Logger, authManager *auth.Manager, enforcer *rbac.Enforcer, options ...ModOnlineOptions) *ModOnlineManager {
	var option ModOnlineOptions
	if len(options) > 0 {
		option = options[0]
	}
	client := option.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	return &ModOnlineManager{
		store:             store,
		log:               log,
		authManager:       authManager,
		enforcer:          enforcer,
		httpClient:        indexers.ProtectCredentialsOnRedirect(client),
		modrinthBaseURL:   option.ModrinthBaseURL,
		curseForgeBaseURL: option.CurseForgeBaseURL,
	}
}

func (m *ModOnlineManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Auth check if enabled
	if m.authManager != nil && m.authManager.IsAnyAuthEnabled() {
		authHeader := r.Header.Get("Authorization")
		user, err := m.authManager.AuthenticateFromHeader(r.Context(), authHeader)
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if m.enforcer != nil {
			action := rbac.ActionRead
			if r.Method == http.MethodPost {
				action = rbac.ActionCreate
			}
			allowed, rbacErr := m.enforcer.Enforce(user.Roles, rbac.ResourceMods, action, "*")
			if rbacErr != nil || !allowed {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
		}
	}

	// Path parsing: /api/v1/servers/{id}/mods/...
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[1] != "mods" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	serverID := parts[0]
	action := parts[2]

	if m.store == nil {
		http.Error(w, "store not initialized", http.StatusInternalServerError)
		return
	}

	var server *storage.Server
	if serverID != "none" && serverID != "" {
		s, err := m.store.GetServer(r.Context(), serverID)
		if err != nil {
			http.Error(w, "server not found", http.StatusNotFound)
			return
		}
		server = s
	}

	switch {
	case action == "search" && r.Method == http.MethodGet:
		m.handleSearch(w, r, server)
	case action == "install" && r.Method == http.MethodPost:
		if server == nil {
			http.Error(w, "cannot install mods without a target server", http.StatusBadRequest)
			return
		}
		m.handleInstall(w, r, server)
	case len(parts) >= 4 && parts[3] == "versions" && r.Method == http.MethodGet:
		slug := parts[2]
		m.handleVersions(w, r, server, slug)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func normalizeLoaderName(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	s = strings.TrimPrefix(s, "mod_loader_")
	switch {
	case strings.Contains(s, "neoforge"):
		return "neoforge"
	case strings.Contains(s, "forge") || strings.Contains(s, "curseforge"):
		return "forge"
	case strings.Contains(s, "quilt"):
		return "quilt"
	case strings.Contains(s, "fabric"):
		return "fabric"
	default:
		return s
	}
}

func (m *ModOnlineManager) handleSearch(w http.ResponseWriter, r *http.Request, server *storage.Server) {
	query := r.URL.Query().Get("query")
	platform := strings.ToLower(r.URL.Query().Get("platform"))
	if platform == "" {
		platform = "modrinth"
	}

	loader := normalizeLoaderName(r.URL.Query().Get("loader"))
	if loader == "" && server != nil {
		loader = normalizeLoaderName(string(server.ModLoader))
	}
	mcVersion := strings.TrimSpace(r.URL.Query().Get("mc_version"))
	if mcVersion == "" && server != nil {
		mcVersion = strings.TrimSpace(server.MCVersion)
	}
	side := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("side")))
	if side == "" && server != nil && server.ID != "" && server.ID != "none" {
		side = "server"
	}

	installedMods := installedModJars(r.Context(), m.store, server)

	globalSettings, _, _ := m.store.GetGlobalSettings(r.Context())

	var results []SearchModResult

	if platform == "curseforge" {
		apiKey := ""
		if server != nil && server.ID != "" && server.ID != "none" {
			if sCfg, err := m.store.GetServerConfig(r.Context(), server.ID); err == nil && sCfg != nil && sCfg.CFAPIKey != nil && *sCfg.CFAPIKey != "" {
				apiKey = *sCfg.CFAPIKey
			}
		}
		if apiKey == "" && globalSettings != nil && globalSettings.CFAPIKey != nil {
			apiKey = *globalSettings.CFAPIKey
		}
		results = m.searchCurseForge(r.Context(), apiKey, query, loader, mcVersion, installedMods)
	} else {
		// Modrinth
		token := ""
		ua := "CarbonPanel/1.0 (CarbonPanel-admin)"
		if globalSettings != nil {
			if globalSettings.ModrinthToken != nil && *globalSettings.ModrinthToken != "" {
				token = *globalSettings.ModrinthToken
			}
			if globalSettings.ModrinthUserAgent != nil && *globalSettings.ModrinthUserAgent != "" {
				ua = *globalSettings.ModrinthUserAgent
			}
		}
		results = m.searchModrinth(r.Context(), token, ua, query, loader, mcVersion, installedMods)
	}

	// Apply side filter if requested (e.g. side=server)
	if side == "server" {
		var serverResults []SearchModResult
		for _, res := range results {
			if res.ServerSide == "unsupported" {
				continue
			}
			serverResults = append(serverResults, res)
		}
		results = serverResults
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"count":   len(results),
	})
}

func (m *ModOnlineManager) searchModrinth(ctx context.Context, token, ua, query, loader, mcVersion string, installed map[string]bool) []SearchModResult {
	if loader == "vanilla" {
		loader = ""
	}
	provider := m.modrinthClient(token, ua)
	mrResp, err := provider.Search(ctx, modrinth.SearchOptions{ProjectType: "mod", Query: query, Loader: loader, GameVersion: mcVersion, Limit: 25})
	if err != nil {
		return []SearchModResult{}
	}
	var results []SearchModResult
	for _, hit := range mrResp.Hits {
		isInstalled := false
		lowerSlug := strings.ToLower(hit.Slug)
		for k := range installed {
			if strings.Contains(k, lowerSlug) {
				isInstalled = true
				break
			}
		}

		results = append(results, SearchModResult{
			ID:          hit.ProjectID,
			Slug:        hit.Slug,
			Title:       hit.Title,
			Description: hit.Description,
			IconURL:     hit.IconURL,
			Author:      hit.Author,
			Downloads:   hit.Downloads,
			Categories:  hit.Categories,
			Platform:    "modrinth",
			ClientSide:  hit.ClientSide,
			ServerSide:  hit.ServerSide,
			Installed:   isInstalled,
		})
	}

	return results
}

func (m *ModOnlineManager) searchCurseForge(ctx context.Context, apiKey, query, loader, mcVersion string, installed map[string]bool) []SearchModResult {
	provider := m.curseForgeClient(apiKey)
	options := fuego.SearchOptions{ClassID: 6, Query: query, GameVersion: mcVersion, ModLoader: curseForgeLoader(loader), PageSize: 25}
	response, err := provider.Search(ctx, options)
	if (err != nil || len(response.Data) == 0) && mcVersion != "" && ctx.Err() == nil {
		options.GameVersion = ""
		response, err = provider.Search(ctx, options)
	}
	if err != nil {
		return []SearchModResult{}
	}

	var results []SearchModResult
	for _, mod := range response.Data {
		author := ""
		if len(mod.Authors) > 0 {
			author = mod.Authors[0].Name
		}
		var cats []string
		for _, c := range mod.Categories {
			cats = append(cats, c.Name)
		}

		isInstalled := false
		lowerSlug := strings.ToLower(mod.Slug)
		for k := range installed {
			if strings.Contains(k, lowerSlug) {
				isInstalled = true
				break
			}
		}

		results = append(results, SearchModResult{
			ID:          fmt.Sprintf("%d", mod.ID),
			Slug:        mod.Slug,
			Title:       mod.Name,
			Description: mod.Summary,
			IconURL:     mod.Logo.ThumbnailURL,
			Author:      author,
			Downloads:   int64(mod.DownloadCount),
			Categories:  cats,
			Platform:    "curseforge",
			Installed:   isInstalled,
		})
	}

	return results
}

func (m *ModOnlineManager) handleVersions(w http.ResponseWriter, r *http.Request, server *storage.Server, slug string) {
	platform := strings.ToLower(r.URL.Query().Get("platform"))
	if platform == "" {
		platform = "modrinth"
	}

	loader := normalizeLoaderName(r.URL.Query().Get("loader"))
	if loader == "" && server != nil {
		loader = normalizeLoaderName(string(server.ModLoader))
	}
	mcVersion := strings.TrimSpace(r.URL.Query().Get("mc_version"))
	if mcVersion == "" && server != nil {
		mcVersion = strings.TrimSpace(server.MCVersion)
	}

	globalSettings, _, _ := m.store.GetGlobalSettings(r.Context())

	if platform == "curseforge" {
		apiKey := ""
		if server != nil && server.ID != "" && server.ID != "none" {
			if sCfg, err := m.store.GetServerConfig(r.Context(), server.ID); err == nil && sCfg != nil && sCfg.CFAPIKey != nil && *sCfg.CFAPIKey != "" {
				apiKey = *sCfg.CFAPIKey
			}
		}
		if apiKey == "" && globalSettings != nil && globalSettings.CFAPIKey != nil {
			apiKey = *globalSettings.CFAPIKey
		}
		m.handleCurseForgeVersions(w, r, apiKey, slug, loader, mcVersion)
	} else {
		token := ""
		ua := "CarbonPanel/1.0 (CarbonPanel-admin)"
		if globalSettings != nil {
			if globalSettings.ModrinthToken != nil && *globalSettings.ModrinthToken != "" {
				token = *globalSettings.ModrinthToken
			}
			if globalSettings.ModrinthUserAgent != nil && *globalSettings.ModrinthUserAgent != "" {
				ua = *globalSettings.ModrinthUserAgent
			}
		}
		m.handleModrinthVersions(w, r, token, ua, slug, loader, mcVersion)
	}
}

func (m *ModOnlineManager) handleModrinthVersions(w http.ResponseWriter, r *http.Request, token, ua, slug, loader, mcVersion string) {
	provider := m.modrinthClient(token, ua)
	rawVersions, err := provider.GetVersions(r.Context(), slug, loader, mcVersion)
	if err != nil {
		var providerErr *indexers.IndexerError
		if errors.As(err, &providerErr) && providerErr.StatusCode != 0 {
			http.Error(w, fmt.Sprintf("modrinth returned %d", providerErr.StatusCode), providerErr.StatusCode)
		} else if errors.As(err, &providerErr) && providerErr.Kind == indexers.ErrDecode {
			http.Error(w, "failed to parse versions", http.StatusInternalServerError)
		} else {
			http.Error(w, fmt.Sprintf("failed to fetch versions: %v", err), http.StatusBadGateway)
		}
		return
	}
	var versions []ModVersionItem
	for _, v := range rawVersions {
		if len(v.Files) == 0 {
			continue
		}

		selectedFile := v.Files[0]
		for _, f := range v.Files {
			if f.Primary {
				selectedFile = f
				break
			}
		}

		var deps []ModDependencyItem
		for _, d := range v.Dependencies {
			pID := ""
			if d.ProjectID != nil {
				pID = *d.ProjectID
			}
			vID := ""
			if d.VersionID != nil {
				vID = *d.VersionID
			}
			deps = append(deps, ModDependencyItem{
				ProjectID:      pID,
				VersionID:      vID,
				DependencyType: d.DependencyType,
			})
		}

		versions = append(versions, ModVersionItem{
			ID:            v.ID,
			VersionNumber: v.VersionNumber,
			Name:          v.Name,
			VersionType:   v.VersionType,
			FileName:      selectedFile.Filename,
			DownloadURL:   selectedFile.URL,
			FileSize:      selectedFile.Size,
			Dependencies:  deps,
		})
	}

	m.enrichModrinthDependencyTitles(r.Context(), token, ua, versions)

	writeJSON(w, http.StatusOK, map[string]any{
		"versions": versions,
		"count":    len(versions),
	})
}

func (m *ModOnlineManager) resolveCurseForgeSlug(ctx context.Context, apiKey, slug string) string {
	if strings.TrimSpace(slug) == "" {
		return ""
	}
	id, err := m.curseForgeClient(apiKey).ResolveSlug(ctx, slug)
	if err != nil || id == 0 {
		return ""
	}
	return strconv.Itoa(id)
}

func (m *ModOnlineManager) curseForgeClient(apiKey string) *fuego.Client {
	options := []fuego.Option{fuego.WithHTTPClient(m.httpClient), fuego.WithUserAgent("CarbonPanel/1.0 (CarbonPanel-admin)")}
	if m.curseForgeBaseURL != "" {
		options = append(options, fuego.WithBaseURL(m.curseForgeBaseURL))
	}
	return fuego.NewClient(apiKey, nil, options...)
}

func curseForgeLoader(loader string) fuego.ModLoaderType {
	switch loader {
	case "forge":
		return fuego.ModLoaderForge
	case "fabric":
		return fuego.ModLoaderFabric
	case "quilt":
		return fuego.ModLoaderQuilt
	case "neoforge":
		return fuego.ModLoaderNeoForge
	}
	return fuego.ModLoaderAny
}

func (m *ModOnlineManager) handleCurseForgeVersions(w http.ResponseWriter, r *http.Request, apiKey, modID, loader, mcVersion string) {
	// If modID is a slug (contains non-digits), resolve to numeric ID
	if _, err := strconv.Atoi(modID); err != nil {
		if resolved := m.resolveCurseForgeSlug(r.Context(), apiKey, modID); resolved != "" {
			modID = resolved
		}
	}

	id, err := strconv.Atoi(modID)
	if err != nil {
		http.Error(w, "CurseForge project not found", http.StatusNotFound)
		return
	}
	provider := m.curseForgeClient(apiKey)
	files, err := provider.GetFiles(r.Context(), id, fuego.FilesOptions{PageSize: 50, GameVersion: mcVersion, ModLoader: curseForgeLoader(loader)})
	var providerErr *indexers.IndexerError
	if err != nil && r.Context().Err() == nil && !(errors.As(err, &providerErr) && providerErr.Kind == indexers.ErrDecode) {
		files, err = provider.GetFiles(r.Context(), id, fuego.FilesOptions{PageSize: 50})
	}
	if err != nil {
		status := http.StatusBadGateway
		if errors.As(err, &providerErr) {
			if providerErr.StatusCode > 0 {
				status = providerErr.StatusCode
			}
			if providerErr.Kind == indexers.ErrDecode {
				status = http.StatusInternalServerError
			}
		}
		http.Error(w, fmt.Sprintf("failed to fetch curseforge files: %v", err), status)
		return
	}

	var versions []ModVersionItem
	for _, f := range files {
		// Filter by mcVersion / loader if gameVersions present
		if len(f.GameVersions) > 0 {
			if mcVersion != "" {
				mcMatch := false
				for _, gv := range f.GameVersions {
					if gv == mcVersion || strings.HasPrefix(gv, mcVersion) {
						mcMatch = true
						break
					}
				}
				if !mcMatch {
					continue
				}
			}
			if loader != "" && loader != "vanilla" {
				loaderMatch := false
				for _, gv := range f.GameVersions {
					if strings.EqualFold(gv, loader) {
						loaderMatch = true
						break
					}
				}
				if !loaderMatch {
					continue
				}
			}
		}

		vType := "release"
		switch f.ReleaseType {
		case 2:
			vType = "beta"
		case 3:
			vType = "alpha"
		}

		var deps []ModDependencyItem
		for _, d := range f.Dependencies {
			depType := "optional"
			if d.RelationType == 3 {
				depType = "required"
			}
			deps = append(deps, ModDependencyItem{
				ProjectID:      fmt.Sprintf("%d", d.ModID),
				DependencyType: depType,
			})
		}

		downloadURL := f.DownloadURL

		versions = append(versions, ModVersionItem{
			ID:            fmt.Sprintf("%d", f.ID),
			VersionNumber: f.DisplayName,
			Name:          f.DisplayName,
			VersionType:   vType,
			FileName:      f.FileName,
			DownloadURL:   downloadURL,
			FileSize:      f.FileLength,
			Dependencies:  deps,
		})
	}

	// If strict gameVersion filtering was too restrictive, fallback to returning all files
	if len(versions) == 0 && len(files) > 0 {
		for _, f := range files {
			vType := "release"
			switch f.ReleaseType {
			case 2:
				vType = "beta"
			case 3:
				vType = "alpha"
			}
			var deps []ModDependencyItem
			for _, d := range f.Dependencies {
				depType := "optional"
				if d.RelationType == 3 {
					depType = "required"
				}
				deps = append(deps, ModDependencyItem{
					ProjectID:      fmt.Sprintf("%d", d.ModID),
					DependencyType: depType,
				})
			}
			downloadURL := f.DownloadURL
			versions = append(versions, ModVersionItem{
				ID:            fmt.Sprintf("%d", f.ID),
				VersionNumber: f.DisplayName,
				Name:          f.DisplayName,
				VersionType:   vType,
				FileName:      f.FileName,
				DownloadURL:   downloadURL,
				FileSize:      f.FileLength,
				Dependencies:  deps,
			})
		}
	}

	m.enrichCurseForgeDependencyTitles(r.Context(), apiKey, versions)

	writeJSON(w, http.StatusOK, map[string]any{
		"versions": versions,
		"count":    len(versions),
	})
}

// auditModInstall records an online mod install in the Activity tab, failures
// included. A failed install used to surface only as a toast, leaving no
// trace of what was tried and why it failed.
func (m *ModOnlineManager) auditModInstall(r *http.Request, server *storage.Server, success bool, installed, errs []string) {
	if m.store == nil || server == nil {
		return
	}
	actorID, actorName := "", ""
	if m.authManager != nil {
		if user, err := m.authManager.AuthenticateFromHeader(r.Context(), r.Header.Get("Authorization")); err == nil && user != nil {
			actorID, actorName = user.ID, user.Username
		}
	}
	props, _ := json.Marshal(map[string]any{
		"success":   success,
		"installed": installed,
		"errors":    errs,
	})
	if err := activity.Log(m.store.DB(), activity.Entry{
		ActorID:    actorID,
		ActorName:  actorName,
		IP:         r.RemoteAddr,
		Event:      activity.EventModInstall,
		SubjectTyp: "server",
		SubjectID:  server.ID,
		Properties: string(props),
	}); err != nil {
		m.log.Error("Audit log failed for %s: %v", activity.EventModInstall, err)
	}
}

func (m *ModOnlineManager) handleInstall(w http.ResponseWriter, r *http.Request, server *storage.Server) {
	modsDir, err := modsDirectory(r.Context(), m.store, server, true)
	if err != nil {
		if nodestorage.IsUnavailable(err) {
			m.log.Error("mod install refused: %v", err)
			http.Error(w, "node storage is not available", http.StatusServiceUnavailable)
			return
		}
		m.log.Error("mod install storage: %v", err)
		http.Error(w, "failed to resolve mods directory", http.StatusInternalServerError)
		return
	}
	if modsDir == "" {
		http.Error(w, "server does not support mods", http.StatusBadRequest)
		return
	}

	if err := os.MkdirAll(modsDir, 0755); err != nil {
		http.Error(w, fmt.Sprintf("failed to create mods directory: %v", err), http.StatusInternalServerError)
		return
	}

	var req InstallModsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if len(req.Items) == 0 {
		http.Error(w, "no mods specified to install", http.StatusBadRequest)
		return
	}

	resp := InstallModsResponse{
		Success:   true,
		Installed: make([]string, 0),
		Errors:    make([]string, 0),
	}

	for _, item := range req.Items {
		if item.DownloadURL == "" || strings.TrimSpace(item.FileName) == "" {
			resp.Errors = append(resp.Errors, fmt.Sprintf("skipped %s: missing url or filename", item.Name))
			continue
		}

		// Confine the write to the mods directory even if a caller sends a
		// path with separators or traversal segments.
		fileName := filepath.Base(strings.TrimSpace(item.FileName))
		if fileName == "" || fileName == "." || fileName == string(filepath.Separator) {
			resp.Errors = append(resp.Errors, fmt.Sprintf("skipped %s: invalid filename", item.Name))
			continue
		}

		targetPath := filepath.Join(modsDir, fileName)

		// Download directly to target file
		res, err := m.httpClient.Get(item.DownloadURL)
		if err != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("failed to download %s: %v", item.Name, err))
			continue
		}

		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			resp.Errors = append(resp.Errors, fmt.Sprintf("download %s failed with HTTP %d", item.Name, res.StatusCode))
			continue
		}

		out, err := os.Create(targetPath)
		if err != nil {
			res.Body.Close()
			resp.Errors = append(resp.Errors, fmt.Sprintf("failed to save %s: %v", item.Name, err))
			continue
		}

		_, copyErr := io.Copy(out, res.Body)
		res.Body.Close()
		out.Close()

		if copyErr != nil {
			_ = os.Remove(targetPath)
			resp.Errors = append(resp.Errors, fmt.Sprintf("incomplete download for %s: %v", item.Name, copyErr))
			continue
		}

		resp.Installed = append(resp.Installed, fileName)
	}

	m.auditModInstall(r, server, resp.Success, resp.Installed, resp.Errors)

	if len(resp.Installed) == 0 && len(resp.Errors) > 0 {
		resp.Success = false
		resp.Message = "Failed to install selected mods: " + strings.Join(resp.Errors, "; ")
	} else if len(resp.Errors) > 0 {
		resp.Message = fmt.Sprintf("Successfully installed %d mod(s) with %d error(s): %s.", len(resp.Installed), len(resp.Errors), strings.Join(resp.Errors, "; "))
	} else {
		resp.Message = fmt.Sprintf("Successfully installed %d mod(s).", len(resp.Installed))
	}

	writeJSON(w, http.StatusOK, resp)
}
