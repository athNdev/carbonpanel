// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package hangar implements a client for the Hangar API v1
// (https://hangar.papermc.io/api/v1), PaperMC's plugin index. Hangar asks
// automated clients to send an identifying User-Agent, which the shared
// indexer HTTP client supplies. Search and public downloads need no API key.
package hangar

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/indexers"
)

// Platform keys used by Hangar for platformDependencies and downloads maps.
const (
	PlatformPaper     = "PAPER"
	PlatformFolia     = "FOLIA"
	PlatformVelocity  = "VELOCITY"
	PlatformWaterfall = "WATERFALL"
)

// DefaultUserAgent satisfies Hangar's identifying User-Agent policy when no
// configured value is available.
const DefaultUserAgent = "CarbonPanel/1.0 (CarbonPanel-admin)"

// searchLimit is Hangar's maximum page size for project listings.
const searchLimit = 25

// versionPages bounds how many version pages (of pageSize) PluginVersions
// walks while looking for a build matching the server's game version before
// giving up.
const (
	versionPageSize = 25
	versionMaxPages = 4
)

// BaseURL is a var (not const) so tests can point the client at an httptest server.
var BaseURL = "https://hangar.papermc.io/api/v1"

type Client struct {
	http    *indexers.HTTPClient
	baseURL string
}

type Options struct {
	BaseURL    string
	UserAgent  string
	APIKey     string
	HTTPClient *http.Client
}

func NewClient(cfg *config.Config) *Client { return NewClientWithOptions(cfg, Options{}) }

// NewClientWithOptions keeps provider endpoint, credentials and transport
// instance-owned while preserving the default client behavior.
func NewClientWithOptions(cfg *config.Config, options Options) *Client {
	baseURL := options.BaseURL
	if baseURL == "" {
		baseURL = BaseURL
	}
	userAgent := options.UserAgent
	if userAgent == "" && cfg != nil {
		userAgent = cfg.Server.UserAgent
	}
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}
	headers := map[string]string{}
	if options.APIKey != "" {
		headers["Authorization"] = options.APIKey
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    indexers.NewHTTPClientWithClient("hangar", userAgent, headers, options.HTTPClient),
	}
}

func init() {
	indexers.RegisterPluginIndexer("hangar", func(apiKey string, cfg *config.Config) indexers.PluginIndexer {
		return NewClientWithOptions(cfg, Options{APIKey: apiKey})
	})
}

// Compile-time check that the client satisfies the plugin indexer contract.
var _ indexers.PluginIndexer = (*Client)(nil)

// Pagination is the shared Hangar page envelope.
type Pagination struct {
	Count  int `json:"count"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// SearchResponse is the GET /projects payload.
type SearchResponse struct {
	Pagination Pagination `json:"pagination"`
	Result     []Project  `json:"result"`
}

// Namespace identifies a project as owner/slug.
type Namespace struct {
	Owner string `json:"owner"`
	Slug  string `json:"slug"`
}

// Stats are the download/star counters of a project.
type Stats struct {
	Views           int64 `json:"views"`
	Downloads       int64 `json:"downloads"`
	RecentViews     int64 `json:"recentViews"`
	RecentDownloads int64 `json:"recentDownloads"`
	Stars           int   `json:"stars"`
	Watchers        int   `json:"watchers"`
}

// Project is a Hangar project as returned by search and the project page.
type Project struct {
	CreatedAt string    `json:"createdAt"`
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Namespace Namespace `json:"namespace"`
	Stats     Stats     `json:"stats"`
	Category  string    `json:"category"`
	// Description is the short summary shown under the project title.
	Description string `json:"description"`
	LastUpdated string `json:"lastUpdated"`
	Visibility  string `json:"visibility"`
	// SupportedPlatforms maps platform (e.g. "PAPER") to the Minecraft
	// versions the project advertises for it.
	SupportedPlatforms map[string][]string `json:"supportedPlatforms"`
	AvatarURL          string              `json:"avatarUrl"`
}

// VersionsResponse is the GET /projects/{author}/{slug}/versions payload.
type VersionsResponse struct {
	Pagination Pagination `json:"pagination"`
	Result     []Version  `json:"result"`
}

// VersionChannel is the release channel of a version ("Release", "Snapshot"…).
type VersionChannel struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
}

// VersionStats are per-version download counters.
type VersionStats struct {
	TotalDownloads    int64            `json:"totalDownloads"`
	PlatformDownloads map[string]int64 `json:"platformDownloads"`
}

// FileInfo describes the downloadable artifact of a platform.
type FileInfo struct {
	Name       string `json:"name"`
	SizeBytes  int64  `json:"sizeBytes"`
	SHA256Hash string `json:"sha256Hash"`
}

// DownloadSpec is a version's per-platform download entry. Hangar serves
// files from hangarcdn.papermc.io; externally hosted versions carry
// externalUrl instead of a CDN link.
type DownloadSpec struct {
	FileInfo    FileInfo `json:"fileInfo"`
	ExternalURL *string  `json:"externalUrl"`
	DownloadURL string   `json:"downloadUrl"`
}

// ResolvedURL returns the working download URL for this spec: the CDN link
// when present, otherwise the external download target.
func (s DownloadSpec) ResolvedURL() string {
	if s.DownloadURL != "" {
		return s.DownloadURL
	}
	if s.ExternalURL != nil {
		return strings.TrimSpace(*s.ExternalURL)
	}
	return ""
}

// PluginDependency is a version's dependency entry for one platform.
type PluginDependency struct {
	Name        string  `json:"name"`
	ProjectID   int64   `json:"projectId"`
	Required    bool    `json:"required"`
	ExternalURL *string `json:"externalUrl"`
	Platform    string  `json:"platform"`
}

// Version is one build of a Hangar project. Despite the field name,
// PlatformDependencies maps platform -> supported Minecraft versions.
type Version struct {
	CreatedAt            string                        `json:"createdAt"`
	ID                   int64                         `json:"id"`
	ProjectID            int64                         `json:"projectId"`
	Name                 string                        `json:"name"`
	Visibility           string                        `json:"visibility"`
	Description          string                        `json:"description"`
	Author               string                        `json:"author"`
	ReviewState          string                        `json:"reviewState"`
	Channel              VersionChannel                `json:"channel"`
	Stats                VersionStats                  `json:"stats"`
	Downloads            map[string]DownloadSpec       `json:"downloads"`
	PluginDependencies   map[string][]PluginDependency `json:"pluginDependencies"`
	PlatformDependencies map[string][]string           `json:"platformDependencies"`
}

// DownloadFor returns the artifact for a platform with a usable URL.
func (v Version) DownloadFor(platform string) (DownloadSpec, bool) {
	spec, ok := v.Downloads[platform]
	if !ok {
		return DownloadSpec{}, false
	}
	if spec.ResolvedURL() == "" {
		return DownloadSpec{}, false
	}
	return spec, true
}

// Platforms lists the platforms a version ships artifacts for, sorted.
func (v Version) Platforms() []string {
	platforms := make([]string, 0, len(v.Downloads))
	for platform := range v.Downloads {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	return platforms
}

// SearchProjects queries GET /projects. Hangar narrows by query and page only;
// game-version compat is decided client-side from supportedPlatforms.
func (c *Client) SearchProjects(ctx context.Context, query string, offset, limit int) (*SearchResponse, error) {
	if limit <= 0 || limit > searchLimit {
		limit = searchLimit
	}
	params := url.Values{}
	if query != "" {
		params.Set("q", query)
	}
	params.Set("offset", strconv.Itoa(offset))
	params.Set("limit", strconv.Itoa(limit))

	var resp SearchResponse
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/projects?%s", c.baseURL, params.Encode()), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Project fetches GET /projects/{slug}. Hangar project slugs are globally
// unique, so a bare slug resolves the full namespace — this is how a search
// result's slug becomes {author}/{slug} for the versions endpoint.
func (c *Client) Project(ctx context.Context, slug string) (*Project, error) {
	var p Project
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/projects/%s", c.baseURL, url.PathEscape(slug)), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ProjectVersions queries GET /projects/{author}/{slug}/versions, newest
// first per Hangar's default ordering.
func (c *Client) ProjectVersions(ctx context.Context, author, slug string, offset, limit int) (*VersionsResponse, error) {
	if limit <= 0 {
		limit = versionPageSize
	}
	params := url.Values{}
	params.Set("offset", strconv.Itoa(offset))
	params.Set("limit", strconv.Itoa(limit))
	endpoint := fmt.Sprintf("%s/projects/%s/%s/versions?%s", c.baseURL, url.PathEscape(author), url.PathEscape(slug), params.Encode())

	var resp VersionsResponse
	if err := c.http.DoJSON(ctx, endpoint, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Name implements indexers.PluginIndexer.
func (c *Client) Name() string { return "hangar" }

// SearchPlugins implements indexers.PluginIndexer: Hangar projects narrowed to
// the Paper platform and, when a game version is supplied, to projects whose
// supportedPlatforms admit it.
func (c *Client) SearchPlugins(ctx context.Context, query, mcVersion string, limit int) ([]indexers.PluginResult, error) {
	resp, err := c.SearchProjects(ctx, query, 0, limit)
	if err != nil {
		return nil, err
	}
	results := make([]indexers.PluginResult, 0, len(resp.Result))
	for _, p := range resp.Result {
		if !indexers.SupportsGameVersion(p.SupportedPlatforms, PlatformPaper, mcVersion) {
			continue
		}
		results = append(results, indexers.PluginResult{
			ID:            strconv.FormatInt(p.ID, 10),
			Indexer:       c.Name(),
			Name:          p.Name,
			Author:        p.Namespace.Owner,
			Slug:          p.Namespace.Slug,
			Summary:       p.Description,
			IconURL:       p.AvatarURL,
			DownloadCount: p.Stats.Downloads,
			Platforms:     platformNames(p.SupportedPlatforms),
			GameVersions:  p.SupportedPlatforms[PlatformPaper],
		})
	}
	return results, nil
}

// PluginVersions implements indexers.PluginIndexer. An empty author is
// resolved through the project page first. Versions are filtered to ones with
// a Paper artifact and — when a game version is supplied — Paper game-version
// compatibility; paging continues (bounded) until a compatible build is found.
func (c *Client) PluginVersions(ctx context.Context, author, slug, mcVersion string) ([]indexers.PluginVersion, error) {
	if strings.TrimSpace(author) == "" {
		page, err := c.Project(ctx, slug)
		if err != nil {
			return nil, err
		}
		author = page.Namespace.Owner
		if author == "" {
			return nil, fmt.Errorf("hangar project %q has no namespace owner", slug)
		}
	}

	var versions []indexers.PluginVersion
	offset := 0
	for page := 0; page < versionMaxPages; page++ {
		resp, err := c.ProjectVersions(ctx, author, slug, offset, versionPageSize)
		if err != nil {
			return nil, err
		}
		for _, v := range resp.Result {
			if v.Visibility != "" && v.Visibility != "public" {
				continue
			}
			spec, ok := v.DownloadFor(PlatformPaper)
			if !ok {
				continue
			}
			if !indexers.SupportsGameVersion(v.PlatformDependencies, PlatformPaper, mcVersion) {
				continue
			}
			versions = append(versions, versionToPluginVersion(v, spec))
		}
		if len(versions) > 0 || len(resp.Result) < versionPageSize || offset+len(resp.Result) >= resp.Pagination.Count {
			break
		}
		offset += len(resp.Result)
	}
	return versions, nil
}

// versionToPluginVersion maps one Hangar version onto the shared plugin
// version shape. The artifact name falls back to the version name so callers
// always have a filename to write.
func versionToPluginVersion(v Version, spec DownloadSpec) indexers.PluginVersion {
	fileName := spec.FileInfo.Name
	if fileName == "" {
		fileName = v.Name + ".jar"
	}
	deps := v.PluginDependencies[PlatformPaper]
	dependencies := make([]indexers.PluginDependencyItem, 0, len(deps))
	for _, dep := range deps {
		dependencyType := "optional"
		if dep.Required {
			dependencyType = "required"
		}
		dependencies = append(dependencies, indexers.PluginDependencyItem{
			ProjectID:      strconv.FormatInt(dep.ProjectID, 10),
			Name:           dep.Name,
			DependencyType: dependencyType,
		})
	}
	return indexers.PluginVersion{
		ID:            strconv.FormatInt(v.ID, 10),
		VersionNumber: v.Name,
		VersionType:   pluginVersionType(v.Channel.Name),
		FileName:      fileName,
		DownloadURL:   spec.ResolvedURL(),
		Size:          spec.FileInfo.SizeBytes,
		SHA256:        spec.FileInfo.SHA256Hash,
		Platforms:     v.Platforms(),
		GameVersions:  v.PlatformDependencies[PlatformPaper],
		Dependencies:  dependencies,
	}
}

// pluginVersionType maps a Hangar release channel onto the version-type
// vocabulary the mod browse surface already renders.
func pluginVersionType(channel string) string {
	switch strings.ToLower(strings.TrimSpace(channel)) {
	case "snapshot", "alpha", "beta":
		return strings.ToLower(strings.TrimSpace(channel))
	case "":
		return "release"
	default:
		return "release"
	}
}

// platformNames extracts the platform keys of a platform -> versions map.
func platformNames(platforms map[string][]string) []string {
	names := make([]string, 0, len(platforms))
	for platform := range platforms {
		names = append(names, platform)
	}
	sort.Strings(names)
	return names
}
