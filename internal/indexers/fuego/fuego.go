// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package fuego

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/indexers"
)

const (
	OfficialBaseURL = "https://api.curseforge.com/v1"
	KeylessBaseURL  = "https://api.curse.tools/v1/cf"
	MinecraftGameID = 432
	ModpackClassID  = 4471
)

type Client struct {
	apiKey  string
	baseURL string
	http    *indexers.HTTPClient
	keyless *indexers.HTTPClient
	client  *http.Client
}

func NewClient(apiKey string, cfg *config.Config, opts ...Option) *Client {
	baseURL := KeylessBaseURL
	headers := map[string]string{}
	if apiKey != "" {
		baseURL = OfficialBaseURL
		headers["x-api-key"] = apiKey
	}
	c := &Client{
		apiKey:  apiKey,
		baseURL: baseURL,
	}
	for _, opt := range opts {
		opt(c)
	}
	c.http = indexers.NewHTTPClientWithClient("fuego", cfg.Server.UserAgent, headers, c.client)
	c.keyless = indexers.NewHTTPClientWithClient("fuego", cfg.Server.UserAgent, nil, c.client)
	return c
}

// Option customizes a Client (e.g. pointing tests at an httptest server).
type Option func(*Client)

// WithBaseURL overrides the API endpoint (custom mirrors, tests).
func WithBaseURL(url string) Option {
	return func(c *Client) { c.baseURL = url }
}

// WithHTTPClient supplies an instance-owned HTTP client, for example for a
// caller-specific timeout or a transport that targets local test fixtures.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) { c.client = client }
}

// getJSON owns fallback policy; official credentials never reach the keyless
// provider, and a canceled operation does not start another provider request.
func (c *Client) getJSON(ctx context.Context, path string, result any) error {
	primary := c.http
	keyless := isKeylessEndpoint(c.baseURL)
	if keyless {
		primary = c.keyless
	}
	err := primary.DoJSON(ctx, c.baseURL+path, result)
	if err == nil || ctx.Err() != nil || keyless {
		return err
	}
	if fallbackErr := c.keyless.DoJSON(ctx, KeylessBaseURL+path, result); fallbackErr == nil {
		return nil
	}
	return err
}

// Credential policy follows the provider host, independent of URL spelling,
// scheme, port or endpoint path. Custom official mirrors retain their policy.
func isKeylessEndpoint(rawURL string) bool {
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	keyless, err := url.Parse(KeylessBaseURL)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSuffix(endpoint.Hostname(), "."), keyless.Hostname())
}

type SearchModsResponse struct {
	Data       []Modpack  `json:"data"`
	Pagination Pagination `json:"pagination"`
}

type Pagination struct {
	Index       int `json:"index"`
	PageSize    int `json:"pageSize"`
	ResultCount int `json:"resultCount"`
	TotalCount  int `json:"totalCount"`
}

type Modpack struct {
	ID                 int          `json:"id"`
	GameID             int          `json:"gameId"`
	Name               string       `json:"name"`
	Slug               string       `json:"slug"`
	Links              Links        `json:"links"`
	Summary            string       `json:"summary"`
	Status             int          `json:"status"`
	DownloadCount      float64      `json:"downloadCount"`
	IsFeatured         bool         `json:"isFeatured"`
	PrimaryCategoryID  int          `json:"primaryCategoryId"`
	Categories         []Category   `json:"categories"`
	Authors            []Author     `json:"authors"`
	Logo               Logo         `json:"logo"`
	Screenshots        []Screenshot `json:"screenshots"`
	MainFileID         int          `json:"mainFileId"`
	LatestFiles        []File       `json:"latestFiles"`
	LatestFilesIndexes []FileIndex  `json:"latestFilesIndexes"`
	DateCreated        time.Time    `json:"dateCreated"`
	DateModified       time.Time    `json:"dateModified"`
	DateReleased       time.Time    `json:"dateReleased"`
}

type Links struct {
	WebsiteURL string `json:"websiteUrl"`
	WikiURL    string `json:"wikiUrl"`
	IssuesURL  string `json:"issuesUrl"`
	SourceURL  string `json:"sourceUrl"`
}

type Category struct {
	ID      int    `json:"id"`
	GameID  int    `json:"gameId"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	URL     string `json:"url"`
	IconURL string `json:"iconUrl"`
}

type Author struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

type Logo struct {
	ID           int    `json:"id"`
	ModID        int    `json:"modId"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnailUrl"`
	URL          string `json:"url"`
}

type Screenshot struct {
	ID           int    `json:"id"`
	ModID        int    `json:"modId"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	ThumbnailURL string `json:"thumbnailUrl"`
	URL          string `json:"url"`
}

type File struct {
	ID                   int                   `json:"id"`
	GameID               int                   `json:"gameId"`
	ModID                int                   `json:"modId"`
	IsAvailable          bool                  `json:"isAvailable"`
	DisplayName          string                `json:"displayName"`
	FileName             string                `json:"fileName"`
	ReleaseType          int                   `json:"releaseType"`
	FileStatus           int                   `json:"fileStatus"`
	Hashes               []Hash                `json:"hashes"`
	FileDate             time.Time             `json:"fileDate"`
	FileLength           int64                 `json:"fileLength"`
	DownloadCount        int64                 `json:"downloadCount"`
	DownloadURL          string                `json:"downloadUrl"`
	GameVersions         []string              `json:"gameVersions"`
	SortableGameVersions []SortableGameVersion `json:"sortableGameVersions"`
	Dependencies         []Dependency          `json:"dependencies"`
	AlternateFileID      int                   `json:"alternateFileId"`
	ServerPackFileID     *int                  `json:"serverPackFileId"`
}

type Hash struct {
	Value string `json:"value"`
	Algo  int    `json:"algo"`
}

type SortableGameVersion struct {
	GameVersionName        string    `json:"gameVersionName"`
	GameVersionPadded      string    `json:"gameVersionPadded"`
	GameVersion            string    `json:"gameVersion"`
	GameVersionReleaseDate time.Time `json:"gameVersionReleaseDate"`
}

type Dependency struct {
	ModID        int `json:"modId"`
	RelationType int `json:"relationType"`
}

type FileIndex struct {
	GameVersion       string `json:"gameVersion"`
	FileID            int    `json:"fileId"`
	Filename          string `json:"filename"`
	ReleaseType       int    `json:"releaseType"`
	GameVersionTypeID *int   `json:"gameVersionTypeId"`
	ModLoader         *int   `json:"modLoader"`
}

type ModLoaderType int

const (
	ModLoaderAny        ModLoaderType = 0
	ModLoaderForge      ModLoaderType = 1
	ModLoaderCauldron   ModLoaderType = 2
	ModLoaderLiteLoader ModLoaderType = 3
	ModLoaderFabric     ModLoaderType = 4
	ModLoaderQuilt      ModLoaderType = 5
	ModLoaderNeoForge   ModLoaderType = 6
)

func (c *Client) SearchModpacks(ctx context.Context, query string, gameVersion string, modLoader ModLoaderType, index, pageSize int) (*SearchModsResponse, error) {
	params := url.Values{}
	params.Set("gameId", strconv.Itoa(MinecraftGameID))
	params.Set("classId", strconv.Itoa(ModpackClassID))
	params.Set("index", strconv.Itoa(index))
	params.Set("pageSize", strconv.Itoa(pageSize))
	params.Set("sortField", "2") // Sort by popularity
	params.Set("sortOrder", "desc")

	if query != "" {
		params.Set("searchFilter", query)
	}

	if gameVersion != "" {
		params.Set("gameVersion", gameVersion)
	}

	if modLoader != ModLoaderAny {
		params.Set("modLoaderType", strconv.Itoa(int(modLoader)))
	}

	var result SearchModsResponse
	if err := c.getJSON(ctx, "/mods/search?"+params.Encode(), &result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (c *Client) GetModpackFiles(ctx context.Context, modID int) ([]File, error) {
	var result struct {
		Data []File `json:"data"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/mods/%d/files", modID), &result); err != nil {
		return nil, err
	}

	// Ensure each file has a download URL, falling back to CurseForge edge CDN if missing
	for i := range result.Data {
		if result.Data[i].DownloadURL == "" && result.Data[i].FileName != "" {
			result.Data[i].DownloadURL = fmt.Sprintf("https://edge.forgecdn.net/files/%d/%d/%s",
				result.Data[i].ID/1000, result.Data[i].ID%1000, url.PathEscape(result.Data[i].FileName))
		}
	}

	return result.Data, nil
}

func (c *Client) GetModpack(ctx context.Context, modID int) (*Modpack, error) {
	var result struct {
		Data Modpack `json:"data"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf("/mods/%d", modID), &result); err != nil {
		return nil, err
	}

	return &result.Data, nil
}
