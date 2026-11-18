// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package providers

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const curseforgeAPI = "https://api.curseforge.com"

// CurseForge gameId for Minecraft. This is a stable platform constant, not a
// world class id: world categories are discovered from GET /v1/categories at
// runtime and are never hard-coded here.
const curseforgeGameID = 432

type curseforgeCategoriesResponse struct {
	Data []curseforgeCategory `json:"data"`
}

type curseforgeCategory struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type curseforgeSearchResponse struct {
	Data []curseforgeMod `json:"data"`
}

type curseforgeMod struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Slug  string `json:"slug"`
	Links struct {
		WebsiteURL string `json:"websiteUrl"`
	} `json:"links"`
}

type curseforgeFilesResponse struct {
	Data []curseforgeFile `json:"data"`
}

type curseforgeFile struct {
	ID           int              `json:"id"`
	ModID        int              `json:"modId"`
	DisplayName  string           `json:"displayName"`
	FileName     string           `json:"fileName"`
	IsAvailable  bool             `json:"isAvailable"`
	ReleaseType  int              `json:"releaseType"`
	Hashes       []curseforgeHash `json:"hashes"`
	GameVersions []string         `json:"gameVersions"`
}

type curseforgeHash struct {
	Value string `json:"value"`
	Algo  int    `json:"algo"`
}

type curseforgeDownloadURLResponse struct {
	Data string `json:"data"`
}

// CurseForge implements Searcher against the documented CurseForge v1 API.
// The x-api-key credential is bound to the configured API origin only.
type CurseForge struct {
	base      string
	client    *http.Client
	userAgent string
	apiKey    string
}

var _ Searcher = (*CurseForge)(nil)

// NewCurseForge builds a CurseForge client. An empty baseURL uses the official
// endpoint. Every client gets both redirect stripping and an origin-scoped
// credential transport so x-api-key can never reach a CDN or arbitrary URL.
func NewCurseForge(apiKey, userAgent, baseURL string) (*CurseForge, error) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil, fmt.Errorf("curseforge api key is required: %w", ErrCredentialBoundary)
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = curseforgeAPI
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("invalid curseforge base URL %q: %w", baseURL, err)
	}
	ua := normalizeUserAgent(userAgent)
	return &CurseForge{
		base:      strings.TrimRight(baseURL, "/"),
		client:    newScopedClient(base, apiKey, ua),
		userAgent: ua,
		apiKey:    apiKey,
	}, nil
}

// Search discovers categories at runtime, searches the authorized API origin,
// and returns file metadata plus the temporary CDN URL as an untrusted string.
func (c *CurseForge) Search(ctx context.Context, req SearchRequest) ([]Artifact, string, bool, error) {
	limit := clampLimit(req.Limit)
	offset := clampOffset(req.Cursor)

	var categories curseforgeCategoriesResponse
	rateLimited, err := getJSONWithRetry(ctx, c.client, c.userAgent, ProviderCurseForge,
		c.base+"/v1/categories", &categories)
	if err != nil {
		return nil, "", rateLimited, err
	}

	categoryID, err := resolveCategory(categories.Data, req.ProjectType)
	if err != nil {
		return nil, "", false, err
	}

	q := url.Values{}
	q.Set("gameId", strconv.Itoa(curseforgeGameID))
	q.Set("pageSize", strconv.Itoa(limit))
	q.Set("index", strconv.Itoa(offset))
	if query := strings.TrimSpace(req.Query); query != "" {
		q.Set("searchFilter", query)
	}
	if categoryID != 0 {
		q.Set("categoryId", strconv.Itoa(categoryID))
	}

	var search curseforgeSearchResponse
	rateLimited, err = getJSONWithRetry(ctx, c.client, c.userAgent, ProviderCurseForge,
		c.base+"/v1/mods/search?"+q.Encode(), &search)
	if err != nil {
		return nil, "", rateLimited, err
	}

	artifacts := make([]Artifact, 0, len(search.Data))
	for _, mod := range search.Data {
		if len(artifacts) >= limit {
			break
		}
		artifact, err := c.artifactFor(ctx, mod, req, categoryID)
		if err != nil {
			return nil, "", errors.Is(err, ErrRateLimit), err
		}
		artifacts = append(artifacts, artifact)
	}

	next := ""
	if len(search.Data) >= limit {
		next = strconv.Itoa(offset + len(search.Data))
	}
	return artifacts, next, false, nil
}

// resolveCategory maps the requested type onto a category discovered from the
// API at runtime. classId=17 for worlds is deliberately NOT assumed. A missing
// category is a clear typed failure, never a silent fallback.
func resolveCategory(categories []curseforgeCategory, requested string) (int, error) {
	want := strings.ToLower(strings.TrimSpace(requested))
	if want == "" {
		return 0, nil
	}
	for _, category := range categories {
		slug := strings.ToLower(strings.TrimSpace(category.Slug))
		name := strings.ToLower(strings.TrimSpace(category.Name))
		if slug == want || name == want || strings.TrimSuffix(slug, "s") == want ||
			strconv.Itoa(category.ID) == want {
			return category.ID, nil
		}
	}
	return 0, fmt.Errorf("%w: %q is not available in the runtime category list (class ids are not assumed)",
		ErrCategoryUnavailable, requested)
}

func (c *CurseForge) artifactFor(ctx context.Context, mod curseforgeMod, req SearchRequest, categoryID int) (Artifact, error) {
	a := Artifact{
		Provider:    ProviderCurseForge,
		ProjectID:   strconv.Itoa(mod.ID),
		Name:        mod.Name,
		ProjectType: strings.TrimSpace(req.ProjectType),
		MCVersion:   strings.TrimSpace(req.MCVersion),
		Loader:      strings.TrimSpace(req.Loader),
		SourceURL:   mod.Links.WebsiteURL,
	}
	if categoryID != 0 && a.ProjectType == "" {
		a.ProjectType = strconv.Itoa(categoryID)
	}
	if mod.ID == 0 {
		return a, nil
	}

	var files curseforgeFilesResponse
	if _, err := getJSONWithRetry(ctx, c.client, c.userAgent, ProviderCurseForge,
		fmt.Sprintf("%s/v1/mods/%d/files", c.base, mod.ID), &files); err != nil {
		if errors.Is(err, ErrRateLimit) {
			return a, err
		}
		return a, nil
	}
	file, ok := selectCurseforgeFile(files.Data, a.MCVersion, a.Loader)
	if !ok {
		return a, nil
	}
	a.FileID = strconv.Itoa(file.ID)
	a.SHA256 = curseforgeSHA256(file)
	if a.MCVersion == "" && len(file.GameVersions) > 0 {
		a.MCVersion = file.GameVersions[0]
	}

	// The download-url endpoint is called on the authorized API origin, so the
	// key is scoped correctly. The returned CDN URL is an untrusted string: it
	// is never fetched by Search and never receives a credential.
	var download curseforgeDownloadURLResponse
	if _, err := getJSONWithRetry(ctx, c.client, c.userAgent, ProviderCurseForge,
		fmt.Sprintf("%s/v1/mods/%d/files/%d/download-url", c.base, mod.ID, file.ID), &download); err != nil {
		if errors.Is(err, ErrRateLimit) {
			return a, err
		}
		return a, nil
	}
	a.DownloadURL = download.Data
	return a, nil
}

func selectCurseforgeFile(files []curseforgeFile, mcVersion, loader string) (curseforgeFile, bool) {
	for _, file := range files {
		if !file.IsAvailable {
			continue
		}
		if mcVersion != "" && !containsFold(file.GameVersions, mcVersion) {
			continue
		}
		if loader != "" && !containsFold(file.GameVersions, loader) {
			continue
		}
		return file, true
	}
	for _, file := range files {
		if file.IsAvailable {
			return file, true
		}
	}
	return curseforgeFile{}, false
}

// curseforgeSHA256 only accepts a genuine 64-hex SHA-256 value from file
// metadata; sha1/md5 entries are never mislabelled as sha256.
func curseforgeSHA256(file curseforgeFile) string {
	for _, hash := range file.Hashes {
		if isHexSHA256(hash.Value) {
			return hash.Value
		}
	}
	return ""
}

// FetchDownloadURL issues a GET to an arbitrary operator-supplied URL with the
// SAME credential-scoped client used for API calls. Because the key is only
// attached to the configured API origin, this proves that CDN and arbitrary
// destinations never observe x-api-key (or any other credential header). It
// returns the final resolved URL string; SSRF policy is applied by the calling
// import pipeline, not by this low-level credential-isolation helper.
func (c *CurseForge) FetchDownloadURL(ctx context.Context, rawURL string) (string, error) {
	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", statusError(ProviderCurseForge, resp)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.Request != nil && resp.Request.URL != nil {
		return resp.Request.URL.String(), nil
	}
	return rawURL, nil
}
