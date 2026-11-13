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
	"errors"
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
	apiKey    string
	baseURL   string
	http      *indexers.HTTPClient
	keyless   *indexers.HTTPClient
	client    *http.Client
	userAgent string
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
	userAgent := ""
	if cfg != nil {
		userAgent = cfg.Server.UserAgent
	}
	if c.userAgent != "" {
		userAgent = c.userAgent
	}
	c.http = indexers.NewHTTPClientWithClient("fuego", userAgent, headers, c.client)
	c.keyless = indexers.NewHTTPClientWithClient("fuego", userAgent, nil, c.client)
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

// WithUserAgent overrides caller identification without a full service config.
func WithUserAgent(value string) Option { return func(c *Client) { c.userAgent = value } }

// getJSON owns fallback policy; official credentials never reach the keyless
// provider, and a canceled operation does not start another provider request.
func (c *Client) getJSON(ctx context.Context, path string, result any) error {
	return c.requestJSON(ctx, path, nil, result)
}

func (c *Client) requestJSON(ctx context.Context, path string, payload any, result any) error {
	do := func(client *indexers.HTTPClient, endpoint string) error {
		if payload == nil {
			return client.DoJSON(ctx, endpoint, result)
		}
		return client.PostJSON(ctx, endpoint, payload, result)
	}
	primary := c.http
	keyless := isKeylessEndpoint(c.baseURL)
	if keyless {
		primary = c.keyless
	}
	err := do(primary, c.baseURL+path)
	if err == nil || ctx.Err() != nil || keyless {
		return err
	}
	if fallbackErr := do(c.keyless, KeylessBaseURL+path); fallbackErr == nil {
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
	return c.Search(ctx, SearchOptions{ClassID: ModpackClassID, Query: query, GameVersion: gameVersion, ModLoader: modLoader, Index: index, PageSize: pageSize, SortField: 2, SortOrder: "desc"})
}

type SearchOptions struct {
	ClassID                    int
	Query, Slug, GameVersion   string
	ModLoader                  ModLoaderType
	Index, PageSize, SortField int
	SortOrder                  string
}

func (c *Client) Search(ctx context.Context, options SearchOptions) (*SearchModsResponse, error) {
	params := url.Values{}
	params.Set("gameId", strconv.Itoa(MinecraftGameID))
	if options.ClassID > 0 {
		params.Set("classId", strconv.Itoa(options.ClassID))
	}
	if options.PageSize > 0 {
		params.Set("pageSize", strconv.Itoa(options.PageSize))
		params.Set("index", strconv.Itoa(options.Index))
	}
	if options.SortField > 0 {
		params.Set("sortField", strconv.Itoa(options.SortField))
	}
	if options.SortOrder != "" {
		params.Set("sortOrder", options.SortOrder)
	}
	if options.Query != "" {
		params.Set("searchFilter", options.Query)
	}
	if options.Slug != "" {
		params.Set("slug", options.Slug)
	}
	if options.GameVersion != "" {
		params.Set("gameVersion", options.GameVersion)
	}
	if options.ModLoader != ModLoaderAny {
		params.Set("modLoaderType", strconv.Itoa(int(options.ModLoader)))
	}

	var result SearchModsResponse
	if err := c.getJSON(ctx, "/mods/search?"+params.Encode(), &result); err != nil {
		return nil, err
	}

	return &result, nil
}

type FilesOptions struct {
	GameVersion string
	ModLoader   ModLoaderType
	PageSize    int
}

func (c *Client) GetModpackFiles(ctx context.Context, modID int) ([]File, error) {
	return c.GetFiles(ctx, modID, FilesOptions{})
}

func (c *Client) GetFiles(ctx context.Context, modID int, options FilesOptions) ([]File, error) {
	params := url.Values{}
	if options.GameVersion != "" {
		params.Set("gameVersion", options.GameVersion)
	}
	if options.ModLoader != ModLoaderAny {
		params.Set("modLoaderType", strconv.Itoa(int(options.ModLoader)))
	}
	if options.PageSize > 0 {
		params.Set("pageSize", strconv.Itoa(options.PageSize))
	}
	path := fmt.Sprintf("/mods/%d/files", modID)
	if len(params) > 0 {
		path += "?" + params.Encode()
	}
	var result struct {
		Data []File `json:"data"`
	}
	if err := c.getJSON(ctx, path, &result); err != nil {
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

// ResolveSlug preserves community-first resolution without ever sending its host a key.
func (c *Client) ResolveSlug(ctx context.Context, slug string) (int, error) {
	params := url.Values{"gameId": {strconv.Itoa(MinecraftGameID)}, "slug": {strings.TrimSpace(slug)}}
	path := "/mods/search?" + params.Encode()
	var result SearchModsResponse
	err := c.keyless.DoJSON(ctx, KeylessBaseURL+path, &result)
	var providerErr *indexers.IndexerError
	if err != nil && ctx.Err() == nil && c.apiKey != "" && (!errors.As(err, &providerErr) || providerErr.Kind != indexers.ErrDecode) {
		err = c.http.DoJSON(ctx, OfficialBaseURL+path, &result)
	}
	if err != nil {
		return 0, err
	}
	if len(result.Data) == 0 {
		return 0, nil
	}
	return result.Data[0].ID, nil
}

func (c *Client) GetMods(ctx context.Context, ids []int) ([]Modpack, error) {
	var result struct {
		Data []Modpack `json:"data"`
	}
	if err := c.requestJSON(ctx, "/mods", map[string]any{"modIds": ids, "filterPcOnly": false}, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}
