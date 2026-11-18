// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package providers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const modrinthAPI = "https://api.modrinth.com"

// Modrinth documents project_type values mod, modpack, resourcepack, shader
// (and all_project_types additionally datapack/plugin). There is NO "world"
// category: axis (b) prebuilt-world automation is explicitly not claimed.
var modrinthProjectTypes = map[string]bool{
	"mod":          true,
	"modpack":      true,
	"resourcepack": true,
	"shader":       true,
	"datapack":     true,
	"plugin":       true,
}

type modrinthSearchResponse struct {
	Hits []modrinthHit `json:"hits"`
}

type modrinthHit struct {
	Slug        string   `json:"slug"`
	Title       string   `json:"title"`
	ProjectID   string   `json:"project_id"`
	ProjectType string   `json:"project_type"`
	License     string   `json:"license"`
	Versions    []string `json:"versions"`
}

type modrinthVersion struct {
	ID           string         `json:"id"`
	ProjectID    string         `json:"project_id"`
	GameVersions []string       `json:"game_versions"`
	Loaders      []string       `json:"loaders"`
	Files        []modrinthFile `json:"files"`
}

type modrinthFile struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
	Hashes   struct {
		SHA1   string `json:"sha1"`
		SHA512 string `json:"sha512"`
	} `json:"hashes"`
}

// Modrinth implements Searcher against the documented Modrinth v2 API.
type Modrinth struct {
	base      string
	client    *http.Client
	userAgent string
}

var _ Searcher = (*Modrinth)(nil)

// NewModrinth builds a Modrinth client. An empty baseURL uses the official
// endpoint. Every client is built with redirect stripping and origin-scoped
// credential transport (no credential is used, but the guard is uniform).
func NewModrinth(userAgent, baseURL string) (*Modrinth, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = modrinthAPI
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid modrinth base URL %q: %w", baseURL, err)
	}
	ua := normalizeUserAgent(userAgent)
	return &Modrinth{
		base:      strings.TrimRight(baseURL, "/"),
		client:    newScopedClient(base, "", ua),
		userAgent: ua,
	}, nil
}

func validateModrinthProjectType(projectType string) error {
	key := strings.ToLower(strings.TrimSpace(projectType))
	if key == "" {
		return nil
	}
	if key == "world" {
		return fmt.Errorf("%w: modrinth documents no world project type; prebuilt-world discovery is not automated (D11)", ErrUnsupportedProjectType)
	}
	if !modrinthProjectTypes[key] {
		return fmt.Errorf("%w: %q is not a documented modrinth project type", ErrUnsupportedProjectType, projectType)
	}
	return nil
}

// Search resolves content artifacts only. It never requests or returns a
// "world" category and never claims to automate prebuilt-world catalogs.
func (m *Modrinth) Search(ctx context.Context, req SearchRequest) ([]Artifact, string, bool, error) {
	if err := validateModrinthProjectType(req.ProjectType); err != nil {
		return nil, "", false, err
	}

	limit := clampLimit(req.Limit)
	offset := clampOffset(req.Cursor)

	q := url.Values{}
	if strings.TrimSpace(req.Query) != "" {
		q.Set("query", strings.TrimSpace(req.Query))
	}
	q.Set("limit", strconv.Itoa(limit))
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if pt := strings.ToLower(strings.TrimSpace(req.ProjectType)); pt != "" {
		q.Set("project_type", pt)
	}
	if filters := modrinthFilters(req.MCVersion, req.Loader); filters != "" {
		q.Set("filters", filters)
	}

	var search modrinthSearchResponse
	rateLimited, err := getJSONWithRetry(ctx, m.client, m.userAgent, ProviderModrinth, m.base+"/v2/search?"+q.Encode(), &search)
	if err != nil {
		return nil, "", rateLimited, err
	}

	artifacts := make([]Artifact, 0, len(search.Hits))
	for _, hit := range search.Hits {
		if len(artifacts) >= limit {
			break
		}
		artifact, err := m.artifactFor(ctx, hit, req)
		if err != nil {
			return nil, "", true, err
		}
		artifacts = append(artifacts, artifact)
	}

	next := ""
	if len(search.Hits) >= limit {
		next = strconv.Itoa(offset + len(search.Hits))
	}
	return artifacts, next, false, nil
}

// modrinthFilters builds the documented filters string for version/category
// constraints. World is never a valid token; the loader is a category and the
// MC version is a version constraint.
func modrinthFilters(mcVersion, loader string) string {
	parts := make([]string, 0, 2)
	if v := strings.TrimSpace(mcVersion); v != "" {
		parts = append(parts, "versions:"+filterValue(v))
	}
	if l := strings.ToLower(strings.TrimSpace(loader)); l != "" {
		parts = append(parts, "categories:"+filterValue(l))
	}
	return strings.Join(parts, " ")
}

func filterValue(value string) string {
	if strings.ContainsAny(value, " \t:") {
		return `"` + value + `"`
	}
	return value
}

func (m *Modrinth) artifactFor(ctx context.Context, hit modrinthHit, req SearchRequest) (Artifact, error) {
	projectType := strings.ToLower(strings.TrimSpace(hit.ProjectType))
	if projectType == "" {
		projectType = strings.ToLower(strings.TrimSpace(req.ProjectType))
	}
	// Never echo a claimed "world" type even if a malformed response has one.
	if strings.EqualFold(projectType, "world") || !modrinthProjectTypes[projectType] {
		projectType = ""
	}

	a := Artifact{
		Provider:    ProviderModrinth,
		ProjectID:   hit.ProjectID,
		Name:        hit.Title,
		ProjectType: projectType,
		MCVersion:   strings.TrimSpace(req.MCVersion),
		Loader:      strings.TrimSpace(req.Loader),
		License:     hit.License,
		SourceURL:   "https://modrinth.com/" + projectType + "/" + hit.Slug,
	}

	if hit.ProjectID == "" {
		return a, nil
	}
	var versions []modrinthVersion
	if _, err := getJSONWithRetry(ctx, m.client, m.userAgent, ProviderModrinth,
		m.base+"/v2/project/"+url.PathEscape(hit.ProjectID)+"/version", &versions); err != nil {
		if errors.Is(err, ErrRateLimit) {
			return a, err
		}
		return a, nil
	}

	version, file, ok := selectVersionFile(versions, a.MCVersion, a.Loader)
	if !ok {
		return a, nil
	}
	a.FileID = version.ID
	a.DownloadURL = file.URL
	if a.MCVersion == "" && len(version.GameVersions) > 0 {
		a.MCVersion = version.GameVersions[0]
	}
	if a.Loader == "" && len(version.Loaders) > 0 {
		a.Loader = strings.ToLower(version.Loaders[0])
	}
	// Modrinth publishes sha1/sha512, not sha256. We parse the hashes above
	// (see modrinthFile.Hashes) but leave SHA256 empty rather than mislabel a
	// different digest as a SHA-256 checksum.
	return a, nil
}

// selectVersionFile chooses the first version/file matching the requested MC
// version and loader, preferring the file flagged primary. The parsed hash
// fields are available on modrinthFile for verification by download code.
func selectVersionFile(versions []modrinthVersion, mcVersion, loader string) (modrinthVersion, modrinthFile, bool) {
	for _, version := range versions {
		if !versionMatches(version, mcVersion, loader) {
			continue
		}
		if file, ok := primaryFile(version.Files); ok {
			return version, file, true
		}
	}
	return modrinthVersion{}, modrinthFile{}, false
}

func versionMatches(version modrinthVersion, mcVersion, loader string) bool {
	if mcVersion != "" && !containsFold(version.GameVersions, mcVersion) {
		return false
	}
	return loader == "" || containsFold(version.Loaders, loader)
}

func primaryFile(files []modrinthFile) (modrinthFile, bool) {
	for _, file := range files {
		if file.Primary && file.URL != "" {
			return file, true
		}
	}
	for _, file := range files {
		if file.URL != "" {
			return file, true
		}
	}
	return modrinthFile{}, false
}

func containsFold(values []string, want string) bool {
	want = strings.ToLower(strings.TrimSpace(want))
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), want) {
			return true
		}
	}
	return false
}
