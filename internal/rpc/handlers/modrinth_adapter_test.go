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
)

type modrinthAdapterTransport func(*http.Request) (*http.Response, error)

func (f modrinthAdapterTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestModrinthBrowseUsesProviderAdapterAndRetainsCallerPolicy(t *testing.T) {
	m := &ModOnlineManager{httpClient: &http.Client{Transport: modrinthAdapterTransport(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "fixture-token" || r.Header.Get("User-Agent") != "fixture-agent" {
			t.Error("caller headers lost")
		}
		q := r.URL.Query()
		if q.Get("query") != "test query" || q.Get("limit") != "25" || q.Get("index") != "" || strings.Contains(q.Get("facets"), "vanilla") {
			t.Errorf("browse policy changed: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"hits":[{"project_id":"id","slug":"fixture-mod","title":"Fixture","downloads":123}]}`)), Header: make(http.Header)}, nil
	})}}
	results := m.searchModrinth(context.Background(), "fixture-token", "fixture-agent", "test query", "vanilla", "1.21.1", map[string]bool{"fixture-mod.jar": true})
	if len(results) != 1 || results[0].ID != "id" || !results[0].Installed {
		t.Fatalf("browse mapping changed: %+v", results)
	}
}

func TestModrinthVersionAdapterPreservesPrimaryFileAndStatus(t *testing.T) {
	for _, status := range []int{200, 401, 403, 404, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m := &ModOnlineManager{httpClient: &http.Client{Transport: modrinthAdapterTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Query().Get("loaders") != `["fabric"]` || r.URL.Query().Get("game_versions") != `["1.21.1"]` {
					t.Errorf("filters lost: %s", r.URL)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`[{"id":"version","name":"Version","version_number":"1","files":[{"filename":"extra.jar"},{"filename":"primary.jar","primary":true,"url":"https://cdn.fixture/file.jar","size":123}]}]`)), Header: make(http.Header)}, nil
			})}}
			w := httptest.NewRecorder()
			m.handleModrinthVersions(w, httptest.NewRequest(http.MethodGet, "/versions", nil), "token", "agent", "project", "fabric", "1.21.1")
			if w.Code != status {
				t.Fatalf("status %d, want %d: %s", w.Code, status, w.Body.String())
			}
			if status == 200 {
				var result struct {
					Versions []ModVersionItem `json:"versions"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if len(result.Versions) != 1 || result.Versions[0].FileName != "primary.jar" || result.Versions[0].FileSize != 123 {
					t.Fatalf("primary file mapping changed: %+v", result)
				}
			}
		})
	}
}
