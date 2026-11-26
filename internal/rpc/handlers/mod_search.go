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
	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers"
	"github.com/athNdev/carbon-panel/internal/indexers/fuego"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/storageworker"
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
	DependencyType string `json:"dependency_type"` // "required", "optional", "incompatible"
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

// ModBrowseError carries the HTTP status and message a provider-backed browse
// operation wants to report. It is the shared failure contract between the
// legacy JSON adapter and the generated ModBrowseService: both reproduce the
// same status (or Connect code) and message instead of inventing policy twice.
type ModBrowseError struct {
	Status  int
	Message string
}

func (e *ModBrowseError) Error() string { return e.Message }

// ModSearchParams are the raw browse filters; empty loader/mc_version/side are
// resolved against the target server by search so both transports share the
// same defaulting policy.
type ModSearchParams struct {
	Query, Platform, Loader, MCVersion, Side string
}

// ModVersionParams are the raw version-lookup filters.
type ModVersionParams struct {
	Platform, Loader, MCVersion string
}

// ModInstallActor is the audit identity for an install. Both transports fill
// the same fields so activity entries are identical.
type ModInstallActor struct {
	ID, Name, IP string
}

// ModOnlineManager handles searching, fetching versions and 1-click installing mods
type ModOnlineManager struct {
	store             *storage.Store
	log               *logger.Logger
	authManager       *auth.Manager
	enforcer          *rbac.Enforcer
	storageResolver   *nodestorage.Resolver
	httpClient        *http.Client
	modrinthBaseURL   string
	curseForgeBaseURL string
}

type ModOnlineOptions struct {
	HTTPClient                         *http.Client
	ModrinthBaseURL, CurseForgeBaseURL string
	StorageResolver                    *nodestorage.Resolver
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
		storageResolver:   option.StorageResolver,
		httpClient:        indexers.ProtectCredentialsOnRedirect(client),
		modrinthBaseURL:   option.ModrinthBaseURL,
		curseForgeBaseURL: option.CurseForgeBaseURL,
	}
}

func (m *ModOnlineManager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Path parsing: /api/v1/servers/{id}/mods/...
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/servers/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[1] != "mods" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	serverID := parts[0]
	action := parts[2]

	// Authorize on the server in the path. The global catalog uses the
	// sentinel "none" and is checked against the global object. A missing
	// enforcer or auth manager now denies instead of defaulting to allow.
	objectID := serverID
	if serverID == "none" || serverID == "" {
		objectID = "*"
	}
	rbacAction := rbac.ActionRead
	if r.Method == http.MethodPost {
		rbacAction = rbac.ActionCreate
	}
	if err := requireRequestObject(r, m.authManager, m.enforcer, rbac.ResourceMods, rbacAction, objectID); err != nil {
		writePolicyError(w, err)
		return
	}

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
	results, err := m.Search(r.Context(), server, ModSearchParams{
		Query:     r.URL.Query().Get("query"),
		Platform:  r.URL.Query().Get("platform"),
		Loader:    r.URL.Query().Get("loader"),
		MCVersion: r.URL.Query().Get("mc_version"),
		Side:      r.URL.Query().Get("side"),
	})
	if err != nil {
		writeModBrowseError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"count":   len(results),
	})
}

// Search is the shared mod browse operation. Both the legacy JSON adapter and
// the generated ModBrowseService call it; provider selection, credential
// resolution and side filtering live here so the two transports cannot drift.
func (m *ModOnlineManager) Search(ctx context.Context, server *storage.Server, p ModSearchParams) ([]SearchModResult, error) {
	platform := strings.ToLower(p.Platform)
	if platform == "" {
		platform = "modrinth"
	}

	loader := normalizeLoaderName(p.Loader)
	if loader == "" && server != nil {
		loader = normalizeLoaderName(string(server.ModLoader))
	}
	mcVersion := strings.TrimSpace(p.MCVersion)
	if mcVersion == "" && server != nil {
		mcVersion = strings.TrimSpace(server.MCVersion)
	}
	side := strings.ToLower(strings.TrimSpace(p.Side))
	if side == "" && server != nil && server.ID != "" && server.ID != "none" {
		side = "server"
	}

	installedMods := installedModJars(ctx, m.store, server, m.storageResolver)

	globalSettings, _, _ := m.store.GetGlobalSettings(ctx)

	var results []SearchModResult

	if platform == "curseforge" {
		apiKey := ""
		if server != nil && server.ID != "" && server.ID != "none" {
			if sCfg, err := m.store.GetServerConfig(ctx, server.ID); err == nil && sCfg != nil && sCfg.CFAPIKey != nil && *sCfg.CFAPIKey != "" {
				apiKey = *sCfg.CFAPIKey
			}
		}
		if apiKey == "" && globalSettings != nil && globalSettings.CFAPIKey != nil {
			apiKey = *globalSettings.CFAPIKey
		}
		results = m.searchCurseForge(ctx, apiKey, p.Query, loader, mcVersion, installedMods)
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
		results = m.searchModrinth(ctx, token, ua, p.Query, loader, mcVersion, installedMods)
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

	return results, nil
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
	versions, browseErr := m.Versions(r.Context(), server, slug, ModVersionParams{
		Platform:  r.URL.Query().Get("platform"),
		Loader:    r.URL.Query().Get("loader"),
		MCVersion: r.URL.Query().Get("mc_version"),
	})
	if browseErr != nil {
		writeModBrowseError(w, browseErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"versions": versions,
		"count":    len(versions),
	})
}

// Versions is the shared version-lookup operation. Provider selection, default
// resolution, file/dependency mapping and provider error mapping are identical
// for the legacy JSON adapter and the generated ModBrowseService.
func (m *ModOnlineManager) Versions(ctx context.Context, server *storage.Server, slug string, p ModVersionParams) ([]ModVersionItem, *ModBrowseError) {
	platform := strings.ToLower(p.Platform)
	if platform == "" {
		platform = "modrinth"
	}

	loader := normalizeLoaderName(p.Loader)
	if loader == "" && server != nil {
		loader = normalizeLoaderName(string(server.ModLoader))
	}
	mcVersion := strings.TrimSpace(p.MCVersion)
	if mcVersion == "" && server != nil {
		mcVersion = strings.TrimSpace(server.MCVersion)
	}

	globalSettings, _, _ := m.store.GetGlobalSettings(ctx)

	if platform == "curseforge" {
		apiKey := ""
		if server != nil && server.ID != "" && server.ID != "none" {
			if sCfg, err := m.store.GetServerConfig(ctx, server.ID); err == nil && sCfg != nil && sCfg.CFAPIKey != nil && *sCfg.CFAPIKey != "" {
				apiKey = *sCfg.CFAPIKey
			}
		}
		if apiKey == "" && globalSettings != nil && globalSettings.CFAPIKey != nil {
			apiKey = *globalSettings.CFAPIKey
		}
		return m.curseForgeVersions(ctx, apiKey, slug, loader, mcVersion)
	}

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
	return m.modrinthVersions(ctx, token, ua, slug, loader, mcVersion)
}

// handleModrinthVersions is the legacy JSON adapter kept for callers that use
// the HTTP form directly; the policy lives in modrinthVersions.
func (m *ModOnlineManager) handleModrinthVersions(w http.ResponseWriter, r *http.Request, token, ua, slug, loader, mcVersion string) {
	versions, browseErr := m.modrinthVersions(r.Context(), token, ua, slug, loader, mcVersion)
	if browseErr != nil {
		writeModBrowseError(w, browseErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"versions": versions,
		"count":    len(versions),
	})
}

func (m *ModOnlineManager) modrinthVersions(ctx context.Context, token, ua, slug, loader, mcVersion string) ([]ModVersionItem, *ModBrowseError) {
	provider := m.modrinthClient(token, ua)
	rawVersions, err := provider.GetVersions(ctx, slug, loader, mcVersion)
	if err != nil {
		var providerErr *indexers.IndexerError
		if errors.As(err, &providerErr) && providerErr.StatusCode != 0 {
			return nil, &ModBrowseError{Status: providerErr.StatusCode, Message: fmt.Sprintf("modrinth returned %d", providerErr.StatusCode)}
		} else if errors.As(err, &providerErr) && providerErr.Kind == indexers.ErrDecode {
			return nil, &ModBrowseError{Status: http.StatusInternalServerError, Message: "failed to parse versions"}
		}
		return nil, &ModBrowseError{Status: http.StatusBadGateway, Message: fmt.Sprintf("failed to fetch versions: %v", err)}
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

	m.enrichModrinthDependencyTitles(ctx, token, ua, versions)

	return versions, nil
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

// handleCurseForgeVersions is the legacy JSON adapter kept for callers that use
// the HTTP form directly; the policy lives in curseForgeVersions.
func (m *ModOnlineManager) handleCurseForgeVersions(w http.ResponseWriter, r *http.Request, apiKey, modID, loader, mcVersion string) {
	versions, browseErr := m.curseForgeVersions(r.Context(), apiKey, modID, loader, mcVersion)
	if browseErr != nil {
		writeModBrowseError(w, browseErr)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"versions": versions,
		"count":    len(versions),
	})
}

func (m *ModOnlineManager) curseForgeVersions(ctx context.Context, apiKey, modID, loader, mcVersion string) ([]ModVersionItem, *ModBrowseError) {
	// If modID is a slug (contains non-digits), resolve to numeric ID
	if _, err := strconv.Atoi(modID); err != nil {
		if resolved := m.resolveCurseForgeSlug(ctx, apiKey, modID); resolved != "" {
			modID = resolved
		}
	}

	id, err := strconv.Atoi(modID)
	if err != nil {
		return nil, &ModBrowseError{Status: http.StatusNotFound, Message: "CurseForge project not found"}
	}
	provider := m.curseForgeClient(apiKey)
	files, err := provider.GetFiles(ctx, id, fuego.FilesOptions{PageSize: 50, GameVersion: mcVersion, ModLoader: curseForgeLoader(loader)})
	var providerErr *indexers.IndexerError
	if err != nil && ctx.Err() == nil && (!errors.As(err, &providerErr) || providerErr.Kind != indexers.ErrDecode) {
		files, err = provider.GetFiles(ctx, id, fuego.FilesOptions{PageSize: 50})
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
		return nil, &ModBrowseError{Status: status, Message: fmt.Sprintf("failed to fetch curseforge files: %v", err)}
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

		downloadURL := f.DownloadURL

		versions = append(versions, ModVersionItem{
			ID:            fmt.Sprintf("%d", f.ID),
			VersionNumber: f.DisplayName,
			Name:          f.DisplayName,
			VersionType:   vType,
			FileName:      f.FileName,
			DownloadURL:   downloadURL,
			FileSize:      f.FileLength,
			Dependencies:  curseForgeModDependencies(f.Dependencies),
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
			downloadURL := f.DownloadURL
			versions = append(versions, ModVersionItem{
				ID:            fmt.Sprintf("%d", f.ID),
				VersionNumber: f.DisplayName,
				Name:          f.DisplayName,
				VersionType:   vType,
				FileName:      f.FileName,
				DownloadURL:   downloadURL,
				FileSize:      f.FileLength,
				Dependencies:  curseForgeModDependencies(f.Dependencies),
			})
		}
	}

	m.enrichCurseForgeDependencyTitles(ctx, apiKey, versions)

	return versions, nil
}

func curseForgeModDependencies(dependencies []fuego.Dependency) []ModDependencyItem {
	var items []ModDependencyItem
	for _, dependency := range dependencies {
		kind := "optional"
		switch dependency.RelationType {
		case 3: // RequiredDependency
			kind = "required"
		case 5: // Incompatible
			kind = "incompatible"
		}
		items = append(items, ModDependencyItem{
			ProjectID:      strconv.Itoa(dependency.ModID),
			DependencyType: kind,
		})
	}
	return items
}

// auditModInstall records an online mod install in the Activity tab, failures
// included. A failed install used to surface only as a toast, leaving no
// trace of what was tried and why it failed.
func (m *ModOnlineManager) auditModInstall(server *storage.Server, success bool, installed, errs []string, actor ModInstallActor) {
	if m.store == nil || server == nil {
		return
	}
	actorID, actorName := actor.ID, actor.Name
	props, _ := json.Marshal(map[string]any{
		"success":   success,
		"installed": installed,
		"errors":    errs,
	})
	if err := activity.Log(m.store.DB(), activity.Entry{
		ActorID:    actorID,
		ActorName:  actorName,
		IP:         actor.IP,
		Event:      activity.EventModInstall,
		SubjectTyp: "server",
		SubjectID:  server.ID,
		Properties: string(props),
	}); err != nil {
		m.log.Error("Audit log failed for %s: %v", activity.EventModInstall, err)
	}
}

func (m *ModOnlineManager) handleInstall(w http.ResponseWriter, r *http.Request, server *storage.Server) {
	var req InstallModsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	actor := ModInstallActor{IP: r.RemoteAddr}
	if m.authManager != nil {
		if user, err := m.authManager.AuthenticateFromHeader(r.Context(), r.Header.Get("Authorization")); err == nil && user != nil {
			actor.ID, actor.Name = user.ID, user.Username
		}
	}

	resp, browseErr := m.Install(r.Context(), server, req.Items, actor)
	if browseErr != nil {
		writeModBrowseError(w, browseErr)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// Install is the shared one-click install operation. Filename confinement,
// per-item error aggregation, audit logging and message formatting are shared
// by the legacy JSON adapter and the generated ModBrowseService.
func (m *ModOnlineManager) Install(ctx context.Context, server *storage.Server, items []InstallModItem, actor ModInstallActor) (InstallModsResponse, *ModBrowseError) {
	if server == nil {
		return InstallModsResponse{}, &ModBrowseError{Status: http.StatusBadRequest, Message: "cannot install mods without a target server"}
	}
	write, err := datawrite.TryShared(server.ID)
	if errors.Is(err, datawrite.ErrExclusiveHeld) {
		return InstallModsResponse{}, &ModBrowseError{Status: http.StatusConflict, Message: err.Error()}
	}
	if err != nil {
		return InstallModsResponse{}, &ModBrowseError{Status: http.StatusInternalServerError, Message: err.Error()}
	}
	defer write.Release()

	modsDir, err := modsDirectory(ctx, m.store, server, true, m.storageResolver)
	if err != nil {
		if nodestorage.IsUnavailable(err) {
			m.log.Error("mod install refused: %v", err)
			return InstallModsResponse{}, &ModBrowseError{Status: http.StatusServiceUnavailable, Message: "node storage is not available"}
		}
		m.log.Error("mod install storage: %v", err)
		return InstallModsResponse{}, &ModBrowseError{Status: http.StatusInternalServerError, Message: "failed to resolve mods directory"}
	}
	if modsDir == "" {
		return InstallModsResponse{}, &ModBrowseError{Status: http.StatusBadRequest, Message: "server does not support mods"}
	}

	if err := os.MkdirAll(modsDir, 0755); err != nil {
		return InstallModsResponse{}, &ModBrowseError{Status: http.StatusInternalServerError, Message: fmt.Sprintf("failed to create mods directory: %v", err)}
	}

	if len(items) == 0 {
		return InstallModsResponse{}, &ModBrowseError{Status: http.StatusBadRequest, Message: "no mods specified to install"}
	}
	storageRoot, err := nodestorage.NewLocal(modsDir)
	if err != nil {
		return InstallModsResponse{}, &ModBrowseError{Status: http.StatusInternalServerError, Message: fmt.Sprintf("failed to open mods directory: %v", err)}
	}
	defer func() {
		if closeErr := storageRoot.Close(); closeErr != nil {
			m.log.Warn("closing mod install directory: %v", closeErr)
		}
	}()

	resp := InstallModsResponse{
		Success:   true,
		Installed: make([]string, 0),
		Errors:    make([]string, 0),
	}

	for _, item := range items {
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

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, item.DownloadURL, nil)
		if err != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("invalid download for %s: %v", item.Name, err))
			continue
		}
		res, err := m.httpClient.Do(req)
		if err != nil {
			resp.Errors = append(resp.Errors, fmt.Sprintf("failed to download %s: %v", item.Name, err))
			continue
		}

		if res.StatusCode != http.StatusOK {
			res.Body.Close()
			resp.Errors = append(resp.Errors, fmt.Sprintf("download %s failed with HTTP %d", item.Name, res.StatusCode))
			continue
		}

		limit := res.ContentLength
		if limit <= 0 || limit > storageworker.MaxStreamBytes {
			limit = storageworker.MaxStreamBytes
		}
		out, err := storageRoot.BeginWrite(ctx, fileName, limit)
		if err != nil {
			res.Body.Close()
			resp.Errors = append(resp.Errors, fmt.Sprintf("failed to save %s: %v", item.Name, err))
			continue
		}

		_, copyErr := io.Copy(out, res.Body)
		res.Body.Close()
		if copyErr != nil {
			_ = out.Close()
			resp.Errors = append(resp.Errors, fmt.Sprintf("incomplete download for %s: %v", item.Name, copyErr))
			continue
		}
		if err := out.Commit(); err != nil {
			_ = out.Close()
			resp.Errors = append(resp.Errors, fmt.Sprintf("failed to publish %s: %v", item.Name, err))
			continue
		}

		resp.Installed = append(resp.Installed, fileName)
	}

	m.auditModInstall(server, resp.Success, resp.Installed, resp.Errors, actor)

	if len(resp.Installed) == 0 && len(resp.Errors) > 0 {
		resp.Success = false
		resp.Message = "Failed to install selected mods: " + strings.Join(resp.Errors, "; ")
	} else if len(resp.Errors) > 0 {
		resp.Message = fmt.Sprintf("Successfully installed %d mod(s) with %d error(s): %s.", len(resp.Installed), len(resp.Errors), strings.Join(resp.Errors, "; "))
	} else {
		resp.Message = fmt.Sprintf("Successfully installed %d mod(s).", len(resp.Installed))
	}

	return resp, nil
}

// writeModBrowseError reproduces the legacy HTTP status and message for a
// shared browse failure. The generated service maps the same type to a Connect
// code without changing the operation's policy.
func writeModBrowseError(w http.ResponseWriter, err error) {
	var browseErr *ModBrowseError
	if errors.As(err, &browseErr) {
		http.Error(w, browseErr.Message, browseErr.Status)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
