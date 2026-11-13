// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package modrinth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/indexers"
)

// BaseURL is a var (not const) so tests can point the client at an httptest server.
var (
	BaseURL = "https://api.modrinth.com/v2"
)

type Client struct {
	http    *indexers.HTTPClient
	baseURL string
}

type Options struct {
	BaseURL    string
	Token      string
	UserAgent  string
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
	headers := map[string]string{}
	if options.Token != "" {
		headers["Authorization"] = options.Token
	}
	userAgent := options.UserAgent
	if userAgent == "" && cfg != nil {
		userAgent = cfg.Server.UserAgent
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: indexers.NewHTTPClientWithClient("modrinth", userAgent, headers, options.HTTPClient)}
}

// SearchResponse represents the Modrinth search API response
type SearchResponse struct {
	Hits      []Project `json:"hits"`
	Offset    int       `json:"offset"`
	Limit     int       `json:"limit"`
	TotalHits int       `json:"total_hits"`
}

// Project represents a Modrinth project (modpack)
type Project struct {
	Slug              string   `json:"slug"`
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	Categories        []string `json:"categories"`
	ClientSide        string   `json:"client_side"`
	ServerSide        string   `json:"server_side"`
	ProjectType       string   `json:"project_type"`
	Downloads         int64    `json:"downloads"`
	IconURL           string   `json:"icon_url"`
	ProjectID         string   `json:"project_id"`
	Author            string   `json:"author"`
	DisplayCategories []string `json:"display_categories"`
	Versions          []string `json:"versions"`
	Follows           int      `json:"follows"`
	DateCreated       string   `json:"date_created"`
	DateModified      string   `json:"date_modified"`
	LatestVersion     string   `json:"latest_version"`
	License           string   `json:"license"`
	Gallery           []string `json:"gallery"`
	FeaturedGallery   string   `json:"featured_gallery"`
	Color             *int     `json:"color"`
}

// ProjectDetails represents full project details from GET /project/{id}
type ProjectDetails struct {
	ID                   string        `json:"id"`
	Slug                 string        `json:"slug"`
	Title                string        `json:"title"`
	Description          string        `json:"description"`
	Body                 string        `json:"body"`
	ProjectType          string        `json:"project_type"`
	ClientSide           string        `json:"client_side"`
	ServerSide           string        `json:"server_side"`
	GameVersions         []string      `json:"game_versions"`
	Loaders              []string      `json:"loaders"`
	Categories           []string      `json:"categories"`
	AdditionalCategories []string      `json:"additional_categories"`
	Status               string        `json:"status"`
	Published            string        `json:"published"`
	Updated              string        `json:"updated"`
	Downloads            int64         `json:"downloads"`
	Followers            int           `json:"followers"`
	IconURL              string        `json:"icon_url"`
	Color                *int          `json:"color"`
	Gallery              []GalleryItem `json:"gallery"`
	SourceURL            string        `json:"source_url"`
	IssuesURL            string        `json:"issues_url"`
	WikiURL              string        `json:"wiki_url"`
	DiscordURL           string        `json:"discord_url"`
	DonationURLs         []DonationURL `json:"donation_urls"`
	Team                 string        `json:"team"`
	Versions             []string      `json:"versions"`
	License              License       `json:"license"`
}

type GalleryItem struct {
	URL         string  `json:"url"`
	Featured    bool    `json:"featured"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Created     string  `json:"created"`
	Ordering    int     `json:"ordering"`
}

type DonationURL struct {
	ID       string `json:"id"`
	Platform string `json:"platform"`
	URL      string `json:"url"`
}

type License struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// Version represents a project version
type Version struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	VersionNumber string       `json:"version_number"`
	ProjectID     string       `json:"project_id"`
	GameVersions  []string     `json:"game_versions"`
	Loaders       []string     `json:"loaders"`
	VersionType   string       `json:"version_type"`
	Featured      bool         `json:"featured"`
	Status        string       `json:"status"`
	Files         []File       `json:"files"`
	Downloads     int64        `json:"downloads"`
	DatePublished string       `json:"date_published"`
	Changelog     *string      `json:"changelog"`
	Dependencies  []Dependency `json:"dependencies"`
}

type File struct {
	Hashes   Hashes `json:"hashes"`
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
	Size     int64  `json:"size"`
	FileType string `json:"file_type"`
}

type Hashes struct {
	SHA512 string `json:"sha512"`
	SHA1   string `json:"sha1"`
}

type Dependency struct {
	VersionID      *string `json:"version_id"`
	ProjectID      *string `json:"project_id"`
	FileName       *string `json:"file_name"`
	DependencyType string  `json:"dependency_type"`
}

// SearchProjects searches Modrinth for one project type. Datapacks, resource
// packs and shader packs are loader-agnostic; pass loader only when the caller
// wants to narrow projects that advertise it (for example iris for shaders).
func (c *Client) SearchProjects(ctx context.Context, projectType, query, gameVersion, loader string, offset, limit int) (*SearchResponse, error) {
	return c.Search(ctx, SearchOptions{ProjectType: projectType, Query: query, GameVersion: gameVersion, Loader: strings.ToLower(loader), Offset: offset, Limit: limit, Index: "downloads"})
}

type SearchOptions struct {
	ProjectType, Query, GameVersion, Loader, Index string
	Offset, Limit                                  int
}

// Search shares request construction while keeping each caller's sort policy.
func (c *Client) Search(ctx context.Context, options SearchOptions) (*SearchResponse, error) {
	projectType, query, gameVersion, loader, offset, limit := options.ProjectType, options.Query, options.GameVersion, options.Loader, options.Offset, options.Limit
	facets := [][]string{{fmt.Sprintf("project_type:%s", projectType)}}
	if gameVersion != "" {
		facets = append(facets, []string{fmt.Sprintf("versions:%s", gameVersion)})
	}
	if loader != "" {
		facets = append(facets, []string{fmt.Sprintf("categories:%s", loader)})
	}

	facetsJSON, err := json.Marshal(facets)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal facets: %w", err)
	}

	params := url.Values{}
	if query != "" {
		params.Set("query", query)
	}
	params.Set("facets", string(facetsJSON))
	if options.Index != "" {
		params.Set("index", options.Index)
	}
	params.Set("offset", strconv.Itoa(offset))
	params.Set("limit", strconv.Itoa(limit))

	var resp SearchResponse
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/search?%s", c.baseURL, params.Encode()), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// SearchModpacks searches for modpacks on Modrinth
func (c *Client) SearchModpacks(ctx context.Context, query string, gameVersion string, modLoader string, offset, limit int) (*SearchResponse, error) {
	return c.SearchProjects(ctx, "modpack", query, gameVersion, modLoader, offset, limit)
}

// GetModpack retrieves detailed information about a specific modpack
func (c *Client) GetModpack(ctx context.Context, modpackID string) (*ProjectDetails, error) {
	var project ProjectDetails
	if err := c.http.DoJSON(ctx, fmt.Sprintf("%s/project/%s", c.baseURL, url.PathEscape(modpackID)), &project); err != nil {
		return nil, err
	}

	return &project, nil
}

// GetModpackVersions retrieves all versions for a specific modpack
func (c *Client) GetModpackVersions(ctx context.Context, modpackID string) ([]Version, error) {
	return c.GetVersions(ctx, modpackID, "", "")
}

// GetVersions preserves provider order and optional server-side filtering.
func (c *Client) GetVersions(ctx context.Context, projectID, loader, gameVersion string) ([]Version, error) {
	params := url.Values{}
	if loader != "" && loader != "vanilla" {
		encoded, _ := json.Marshal([]string{loader})
		params.Set("loaders", string(encoded))
	}
	if gameVersion != "" {
		encoded, _ := json.Marshal([]string{gameVersion})
		params.Set("game_versions", string(encoded))
	}
	endpoint := fmt.Sprintf("%s/project/%s/version", c.baseURL, url.PathEscape(projectID))
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	var versions []Version
	if err := c.http.DoJSON(ctx, endpoint, &versions); err != nil {
		return nil, err
	}

	return versions, nil
}
