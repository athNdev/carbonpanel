// SPDX-License-Identifier: MPL-2.0
package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/logger"
)

func TestCurseForgeBrowseSharesFallbackAndCallerFilters(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			m := NewModOnlineManager(nil, logger.New(), nil, nil, ModOnlineOptions{CurseForgeBaseURL: "https://official.fixture/v1", HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				q := r.URL.Query()
				if q.Get("classId") != "6" || q.Get("pageSize") != "25" || q.Get("gameVersion") != "1.21.1" || q.Get("modLoaderType") != "4" || q.Get("searchFilter") != "test & query" {
					t.Errorf("filters: %s", r.URL)
				}
				if r.URL.Host == "official.fixture" {
					if r.Header.Get("X-Api-Key") != "fixture-key" {
						t.Error("official key missing")
					}
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("malformed")), Header: make(http.Header)}, nil
				}
				if r.URL.Host != "api.curse.tools" || r.Header.Get("X-Api-Key") != "" {
					t.Error("fallback credential boundary")
				}
				return jsonResp(200, map[string]any{"data": []map[string]any{{"id": 12, "slug": "test-mod", "name": "Test", "downloadCount": 123, "authors": []map[string]string{{"name": "Author"}}}}}), nil
			})}})
			results := m.searchCurseForge(context.Background(), "fixture-key", "test & query", "fabric", "1.21.1", map[string]bool{"test-mod.jar": true})
			if len(results) != 1 || results[0].ID != "12" || !results[0].Installed || results[0].Author != "Author" || results[0].Downloads != 123 || calls != 2 {
				t.Fatalf("results=%+v calls=%d", results, calls)
			}
		})
	}
}

func TestCurseForgeBrowseRelaxesOnlyVersionAndStopsOnCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "cancelled"}[cancelled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			m := NewModOnlineManager(nil, logger.New(), nil, nil, ModOnlineOptions{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if cancelled {
					cancel()
					return nil, ctx.Err()
				}
				if r.URL.Query().Get("modLoaderType") != "1" {
					t.Error("loader filter removed")
				}
				if calls == 1 {
					if r.URL.Query().Get("gameVersion") != "1.21.1" {
						t.Error("strict version absent")
					}
					return jsonResp(200, map[string]any{"data": []any{}}), nil
				}
				if r.URL.Query().Get("gameVersion") != "" {
					t.Error("version was not relaxed")
				}
				return jsonResp(200, map[string]any{"data": []map[string]any{{"id": 12}}}), nil
			})}})
			results := m.searchCurseForge(ctx, "", "test", "forge", "1.21.1", nil)
			if cancelled {
				if calls != 1 || len(results) != 0 {
					t.Fatalf("cancelled calls=%d results=%+v", calls, results)
				}
			} else if calls != 2 || len(results) != 1 {
				t.Fatalf("calls=%d results=%+v", calls, results)
			}
		})
	}
}

func TestCurseForgeVersionsRetainMetadataAndFilterFallback(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "filtered", true: "relaxed"}[fallback], func(t *testing.T) {
			calls := 0
			m := NewModOnlineManager(nil, logger.New(), nil, nil, ModOnlineOptions{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("X-Api-Key") != "" {
					t.Error("key on keyless")
				}
				body := `{"data":[{"id":1234567,"displayName":"Version","fileName":"file name.jar","fileLength":99,"releaseType":2,"gameVersions":["1.21.1","Fabric"]},{"id":2,"fileName":"wrong.jar","gameVersions":["1.20.1","Forge"]}]}`
				if fallback {
					body = `{"data":[{"id":1234567,"fileName":"file name.jar","gameVersions":["1.20.1","Forge"]}]}`
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})}})
			w := httptest.NewRecorder()
			m.handleCurseForgeVersions(w, httptest.NewRequest(http.MethodGet, "/versions", nil), "", "12", "fabric", "1.21.1")
			var result struct {
				Versions []ModVersionItem `json:"versions"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if w.Code != 200 || calls != 1 || len(result.Versions) != 1 || result.Versions[0].ID != "1234567" || result.Versions[0].DownloadURL != "https://edge.forgecdn.net/files/1234/567/file%20name.jar" {
				t.Fatalf("response=%s calls=%d", w.Body, calls)
			}
			if !fallback && (result.Versions[0].VersionType != "beta" || result.Versions[0].FileSize != 99) {
				t.Fatalf("metadata %+v", result.Versions)
			}
		})
	}
}

func TestCurseForgeVersionFailuresAreBounded(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			m := NewModOnlineManager(nil, logger.New(), nil, nil, ModOnlineOptions{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("malformed")), Header: make(http.Header)}, nil
			})}})
			w := httptest.NewRecorder()
			m.handleCurseForgeVersions(w, httptest.NewRequest(http.MethodGet, "/versions", nil), "", "12", "fabric", "1.21.1")
			wantStatus, wantCalls := status, 2
			if status == 200 {
				wantStatus, wantCalls = 500, 1
			}
			if w.Code != wantStatus || calls != wantCalls {
				t.Fatalf("status=%d calls=%d want %d %d", w.Code, calls, wantStatus, wantCalls)
			}
		})
	}
}

func TestDependencyTitleFallbackUsesSharedBatchPolicy(t *testing.T) {
	for _, platform := range []string{"modrinth", "curseforge"} {
		t.Run(platform, func(t *testing.T) {
			calls := 0
			m := NewModOnlineManager(nil, logger.New(), nil, nil, ModOnlineOptions{HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if platform == "modrinth" {
					if r.Header.Get("Authorization") != "fixture-token" {
						t.Error("token missing")
					}
					if strings.HasSuffix(r.URL.Path, "/projects") {
						return jsonResp(500, map[string]any{}), nil
					}
					return jsonResp(200, map[string]any{"id": "12", "title": "Title"}), nil
				}
				if r.URL.Host == "api.curse.tools" && r.Header.Get("X-Api-Key") != "" {
					t.Error("batch key leaked")
				}
				if r.Method == http.MethodPost {
					var payload struct {
						IDs []int `json:"modIds"`
					}
					if json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload.IDs) != 1 || payload.IDs[0] != 12 || r.Header.Get("Content-Type") != "application/json" {
						t.Error("batch request lost")
					}
					return jsonResp(500, map[string]any{}), nil
				}
				return jsonResp(200, map[string]any{"data": map[string]any{"name": "Title"}}), nil
			})}})
			var titles map[string]string
			if platform == "modrinth" {
				titles = m.fetchModrinthProjectTitles(context.Background(), "fixture-token", "fixture-agent", []string{"12"})
			} else {
				titles = m.fetchCurseForgeModTitles(context.Background(), "fixture-key", []string{"12"})
			}
			wantCalls := 2
			if platform == "curseforge" {
				wantCalls = 3
			}
			if titles["12"] != "Title" || calls != wantCalls {
				t.Fatalf("titles=%+v calls=%d", titles, calls)
			}
		})
	}
}
