// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Capabilities / SSRF helper
// ---------------------------------------------------------------------------

func TestCapabilitiesNoPrebuiltWorldAutomation(t *testing.T) {
	caps := Capabilities()
	byProvider := make(map[string]Capability, len(caps))
	for _, cap := range caps {
		if _, dup := byProvider[cap.Provider]; dup {
			t.Errorf("duplicate capability for %q", cap.Provider)
		}
		byProvider[cap.Provider] = cap
		if cap.PrebuiltWorldCatalog {
			t.Errorf("provider %q claims prebuilt-world catalog automation (axis b)", cap.Provider)
		}
		if cap.DiscoveryGate != DiscoveryGateD11 {
			t.Errorf("provider %q discovery gate = %q, want %q", cap.Provider, cap.DiscoveryGate, DiscoveryGateD11)
		}
	}

	for _, name := range []string{ProviderModrinth, ProviderCurseForge} {
		cap, ok := byProvider[name]
		if !ok {
			t.Fatalf("missing capability for %q", name)
		}
		if !cap.APIBackedArtifacts {
			t.Errorf("%q should report API-backed artifacts", name)
		}
		if !strings.Contains(strings.ToLower(cap.Note), "not a prebuilt world") {
			t.Errorf("%q note should state that a modpack/datapack is not a prebuilt world: %q", name, cap.Note)
		}
	}

	for _, name := range []string{ProviderPlanetMinecraft, ProviderMinecraftMaps, ProviderBedrockMarketplace} {
		cap, ok := byProvider[name]
		if !ok {
			t.Fatalf("missing capability for manual platform %q", name)
		}
		if cap.APIBackedArtifacts {
			t.Errorf("manual platform %q must not claim API-backed search", name)
		}
		if !strings.Contains(strings.ToLower(cap.Note), "manual") {
			t.Errorf("manual platform %q note should say manual import: %q", name, cap.Note)
		}
	}
}

func TestDownloadURLIsAllowed(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"public https", "https://cdn.example.com/file.zip", true},
		{"public http", "http://media.example.org/world.zip", true},
		{"ftp refused", "ftp://example.com/file.zip", false},
		{"file refused", "file:///etc/passwd", false},
		{"empty refused", "", false},
		{"garbage refused", "not a url", false},
		{"loopback v4 refused", "http://127.0.0.1:8080/x", false},
		{"loopback v6 refused", "http://[::1]/x", false},
		{"private v4 refused", "http://10.1.2.3/x", false},
		{"private v4 192.168 refused", "http://192.168.1.5/x", false},
		{"link-local refused", "http://169.254.169.254/latest", false},
		{"public literal ip refused", "https://8.8.8.8/file.zip", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DownloadURLIsAllowed(tc.url); got != tc.want {
				t.Errorf("DownloadURLIsAllowed(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
	}
}

func TestDefaultUserAgent(t *testing.T) {
	var seen string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = r.Header.Get("User-Agent")
		mu.Unlock()
		writeJSON(t, w, map[string]any{"hits": []any{}})
	}))
	defer server.Close()

	client, err := NewModrinth("", server.URL)
	if err != nil {
		t.Fatalf("NewModrinth: %v", err)
	}
	if _, _, _, err := client.Search(context.Background(), SearchRequest{Limit: 1}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen != defaultUserAgent {
		t.Errorf("user-agent = %q, want %q", seen, defaultUserAgent)
	}
}

// ---------------------------------------------------------------------------
// Modrinth
// ---------------------------------------------------------------------------

type modrinthObserved struct {
	mu       sync.Mutex
	searches []url.Values
	versions int
	worlds   int
}

func TestModrinthSearchDocumentedProjectTypesOnly(t *testing.T) {
	var seen modrinthObserved
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v2/search":
			seen.mu.Lock()
			seen.searches = append(seen.searches, r.URL.Query())
			seen.mu.Unlock()
			writeJSON(t, w, map[string]any{
				"hits": []map[string]any{{
					"slug": "exploration-kit", "title": "Exploration Kit",
					"project_id": "modpack-123", "project_type": "modpack",
					"license": "MIT", "versions": []string{"1.20.1"},
				}},
			})
		case strings.HasPrefix(r.URL.Path, "/v2/project/"):
			seen.mu.Lock()
			seen.versions++
			seen.mu.Unlock()
			writeJSON(t, w, []map[string]any{{
				"id": "ver-1", "project_id": "modpack-123",
				"game_versions": []string{"1.20.1"}, "loaders": []string{"forge"},
				"files": []map[string]any{{
					"url": "https://cdn.modrinth.com/modpack-123/exploration.jar",
					"filename": "exploration.jar", "primary": true,
					"hashes": map[string]string{"sha1": "abc", "sha512": strings.Repeat("d", 128)},
				}},
			}})
		default:
			if strings.Contains(strings.ToLower(r.URL.Path), "world") {
				seen.mu.Lock()
				seen.worlds++
				seen.mu.Unlock()
			}
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewModrinth(testUserAgent, server.URL)
	if err != nil {
		t.Fatalf("NewModrinth: %v", err)
	}
	artifacts, _, rateLimited, err := client.Search(context.Background(), SearchRequest{
		Query: "exploration", MCVersion: "1.20.1", Loader: "forge",
		ProjectType: "modpack", Limit: 5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if rateLimited {
		t.Error("rateLimited = true on success")
	}
	if len(artifacts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(artifacts))
	}
	a := artifacts[0]
	if a.Provider != ProviderModrinth || a.ProjectID != "modpack-123" || a.FileID != "ver-1" {
		t.Errorf("unexpected identity: %+v", a)
	}
	if a.ProjectType != "modpack" {
		t.Errorf("project type = %q, want modpack", a.ProjectType)
	}
	if a.DownloadURL != "https://cdn.modrinth.com/modpack-123/exploration.jar" {
		t.Errorf("download URL = %q", a.DownloadURL)
	}
	if !strings.Contains(a.SourceURL, "exploration-kit") {
		t.Errorf("source URL = %q", a.SourceURL)
	}
	// Modrinth publishes no sha256; never claim one.
	if a.SHA256 != "" {
		t.Errorf("SHA256 = %q, want empty (Modrinth has no sha256)", a.SHA256)
	}

	seen.mu.Lock()
	defer seen.mu.Unlock()
	if len(seen.searches) != 1 || seen.versions != 1 {
		t.Fatalf("search calls = %d, version calls = %d", len(seen.searches), seen.versions)
	}
	q := seen.searches[0]
	if q.Get("project_type") != "modpack" {
		t.Errorf("project_type param = %q", q.Get("project_type"))
	}
	if q.Get("limit") != "5" {
		t.Errorf("limit param = %q", q.Get("limit"))
	}
	if !strings.Contains(q.Get("filters"), "versions:1.20.1") || !strings.Contains(q.Get("filters"), "categories:forge") {
		t.Errorf("filters = %q, want version and loader constraints", q.Get("filters"))
	}
	if seen.worlds != 0 {
		t.Errorf("unexpected request to a world path: %d", seen.worlds)
	}
	for key, values := range q {
		for _, value := range values {
			if strings.EqualFold(value, "world") || strings.EqualFold(key, "world") {
				t.Errorf("world requested via search param %s=%s", key, value)
			}
		}
	}
	if strings.EqualFold(a.ProjectType, "world") {
		t.Error("artifact claims world project type")
	}
}

func TestModrinthRejectsWorldProjectType(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		writeJSON(t, w, map[string]any{"hits": []any{}})
	}))
	defer server.Close()

	client, err := NewModrinth(testUserAgent, server.URL)
	if err != nil {
		t.Fatalf("NewModrinth: %v", err)
	}
	_, _, rateLimited, err := client.Search(context.Background(), SearchRequest{ProjectType: "world"})
	if !errors.Is(err, ErrUnsupportedProjectType) {
		t.Fatalf("error = %v, want ErrUnsupportedProjectType", err)
	}
	if rateLimited {
		t.Error("rateLimited = true for validation error")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("sent %d HTTP requests for an unsupported project type", got)
	}
}

func TestModrinthRateLimitBackoff(t *testing.T) {
	t.Run("retry then success", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			n := atomic.AddInt32(&calls, 1)
			if n == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":"slow down"}`))
				return
			}
			writeJSON(t, w, map[string]any{"hits": []any{}})
		}))
		defer server.Close()
		client, _ := NewModrinth(testUserAgent, server.URL)
		artifacts, _, rateLimited, err := client.Search(context.Background(), SearchRequest{Limit: 1})
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if rateLimited || len(artifacts) != 0 {
			t.Errorf("rateLimited=%v artifacts=%d after successful retry", rateLimited, len(artifacts))
		}
		if got := atomic.LoadInt32(&calls); got != 2 {
			t.Errorf("calls = %d, want 2 (one bounded retry)", got)
		}
	})

	t.Run("persistent 429 is typed", func(t *testing.T) {
		var calls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		client, _ := NewModrinth(testUserAgent, server.URL)
		_, _, rateLimited, err := client.Search(context.Background(), SearchRequest{Limit: 1})
		if !errors.Is(err, ErrRateLimit) {
			t.Fatalf("error = %v, want ErrRateLimit", err)
		}
		if !rateLimited {
			t.Error("rateLimited = false on persistent 429")
		}
		var apiErr *APIError
		if !errors.As(err, &apiErr) || !apiErr.HasRetryAfter {
			t.Errorf("error = %#v, want APIError with parsed Retry-After", err)
		}
		if got := atomic.LoadInt32(&calls); got != 2 {
			t.Errorf("calls = %d, want exactly 2 (single retry)", got)
		}
	})
}

func TestModrinthMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits": [`))
	}))
	defer server.Close()
	client, _ := NewModrinth(testUserAgent, server.URL)
	_, _, rateLimited, err := client.Search(context.Background(), SearchRequest{Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "malformed provider response") {
		t.Fatalf("error = %v, want malformed provider response", err)
	}
	if rateLimited {
		t.Error("rateLimited = true for malformed JSON")
	}
}

func TestModrinthBackoffIsContextCancellable(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(first) })
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, _ := NewModrinth(testUserAgent, server.URL)

	type result struct {
		rateLimited bool
		err         error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		_, _, rl, err := client.Search(ctx, SearchRequest{Limit: 1})
		done <- result{rl, err}
	}()

	select {
	case <-first:
		cancel()
	case <-time.After(3 * time.Second):
		t.Fatal("first request never reached the test server")
	}
	select {
	case res := <-done:
		if !errors.Is(res.err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", res.err)
		}
		if elapsed := time.Since(start); elapsed >= 2*time.Second {
			t.Errorf("cancellation took %s; backoff was not bounded/cancellable", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Search did not return after context cancellation")
	}
}

func TestModrinthContextCancelledBeforeRequest(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
	}))
	defer server.Close()
	client, _ := NewModrinth(testUserAgent, server.URL)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, rateLimited, err := client.Search(ctx, SearchRequest{Limit: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if rateLimited {
		t.Error("rateLimited = true for context cancellation")
	}
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Errorf("sent %d requests with an already-cancelled context", got)
	}
}

// ---------------------------------------------------------------------------
// CurseForge
// ---------------------------------------------------------------------------

func TestCurseForgeCategoryDiscoveryAndArtifact(t *testing.T) {
	const sha256Hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var cdnHits int32
	var searchQuery url.Values
	var mu sync.Mutex

	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/categories":
			writeJSON(t, w, map[string]any{"data": []map[string]any{
				{"id": 17, "name": "Worlds", "slug": "worlds"},
				{"id": 6, "name": "Modpacks", "slug": "modpacks"},
			}})
		case r.URL.Path == "/v1/mods/search":
			mu.Lock()
			searchQuery = r.URL.Query()
			mu.Unlock()
			writeJSON(t, w, map[string]any{"data": []map[string]any{{
				"id": 42, "gameId": 432, "name": "World Pack", "slug": "world-pack",
				"links": map[string]string{"websiteUrl": "https://www.curseforge.com/minecraft/worlds/world-pack"},
			}}})
		case r.URL.Path == "/v1/mods/42/files":
			writeJSON(t, w, map[string]any{"data": []map[string]any{{
				"id": 99, "modId": 42, "isAvailable": true, "displayName": "World Pack 1.0",
				"fileName": "world-pack.zip", "releaseType": 1,
				"hashes": []map[string]any{
					{"value": sha256Hex, "algo": 3},
					{"value": "sha1digest", "algo": 1},
				},
				"gameVersions": []string{"1.20.1", "Forge"},
			}}})
		case r.URL.Path == "/v1/mods/42/files/99/download-url":
			// Deliberately point at this same server's /cdn path. Search must
			// return the string only and must never request it.
			writeJSON(t, w, map[string]any{"data": server.URL + "/cdn/file.zip"})
		case strings.HasPrefix(r.URL.Path, "/cdn/"):
			atomic.AddInt32(&cdnHits, 1)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewCurseForge(testAPIKey, testUserAgent, server.URL)
	if err != nil {
		t.Fatalf("NewCurseForge: %v", err)
	}
	artifacts, _, rateLimited, err := client.Search(context.Background(), SearchRequest{
		Query: "world", MCVersion: "1.20.1", Loader: "forge",
		ProjectType: "worlds", Limit: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if rateLimited {
		t.Error("rateLimited = true on success")
	}
	if len(artifacts) != 1 {
		t.Fatalf("got %d artifacts, want 1", len(artifacts))
	}
	a := artifacts[0]
	if a.Provider != ProviderCurseForge || a.ProjectID != "42" || a.FileID != "99" {
		t.Errorf("unexpected identity: %+v", a)
	}
	if a.SHA256 != sha256Hex {
		t.Errorf("sha256 = %q, want %q", a.SHA256, sha256Hex)
	}
	if a.DownloadURL != server.URL+"/cdn/file.zip" {
		t.Errorf("download URL = %q", a.DownloadURL)
	}
	if a.SourceURL == "" {
		t.Error("source URL is empty")
	}

	mu.Lock()
	defer mu.Unlock()
	if searchQuery.Get("gameId") != "432" {
		t.Errorf("gameId = %q", searchQuery.Get("gameId"))
	}
	// The category id came from the runtime /v1/categories fixture (17), not
	// from a hard-coded world class constant.
	if searchQuery.Get("categoryId") != "17" {
		t.Errorf("categoryId = %q, want discovered id 17", searchQuery.Get("categoryId"))
	}
	if searchQuery.Get("pageSize") != "10" {
		t.Errorf("pageSize = %q", searchQuery.Get("pageSize"))
	}
	if got := atomic.LoadInt32(&cdnHits); got != 0 {
		t.Errorf("Search fetched the CDN URL %d times; it must be returned as a string only", got)
	}
}

func TestCurseForgeMissingCategory(t *testing.T) {
	var searchCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/categories" {
			writeJSON(t, w, map[string]any{"data": []map[string]any{
				{"id": 6, "name": "Modpacks", "slug": "modpacks"},
			}})
			return
		}
		atomic.AddInt32(&searchCalls, 1)
		writeJSON(t, w, map[string]any{"data": []any{}})
	}))
	defer server.Close()
	client, _ := NewCurseForge(testAPIKey, testUserAgent, server.URL)

	_, _, rateLimited, err := client.Search(context.Background(), SearchRequest{ProjectType: "worlds"})
	if !errors.Is(err, ErrCategoryUnavailable) {
		t.Fatalf("error = %v, want ErrCategoryUnavailable", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "no longer available") {
		t.Errorf("error text should say category is no longer available: %v", err)
	}
	if rateLimited {
		t.Error("rateLimited = true for a missing category")
	}
	if got := atomic.LoadInt32(&searchCalls); got != 0 {
		t.Errorf("searched %d times despite unavailable category", got)
	}
}

func TestCurseForgeStatusErrors(t *testing.T) {
	tests := []struct {
		name            string
		status          int
		retryAfter      string
		wantErr         error
		wantRateLimited bool
		wantCalls       int32
	}{
		{"unauthorized", http.StatusUnauthorized, "", ErrUnauthorized, false, 1},
		{"forbidden", http.StatusForbidden, "", ErrForbidden, false, 1},
		{"not found", http.StatusNotFound, "", ErrNotFound, false, 1},
		{"rate limited", http.StatusTooManyRequests, "0", ErrRateLimit, true, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			client, _ := NewCurseForge(testAPIKey, testUserAgent, server.URL)

			_, _, rateLimited, err := client.Search(context.Background(), SearchRequest{Limit: 1})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			if rateLimited != tc.wantRateLimited {
				t.Errorf("rateLimited = %v, want %v", rateLimited, tc.wantRateLimited)
			}
			if got := atomic.LoadInt32(&calls); got != tc.wantCalls {
				t.Errorf("calls = %d, want %d", got, tc.wantCalls)
			}
		})
	}
}

func TestCurseForgeMalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":`))
	}))
	defer server.Close()
	client, _ := NewCurseForge(testAPIKey, testUserAgent, server.URL)
	_, _, _, err := client.Search(context.Background(), SearchRequest{Limit: 1})
	if err == nil || !strings.Contains(err.Error(), "malformed provider response") {
		t.Fatalf("error = %v, want malformed provider response", err)
	}
}
