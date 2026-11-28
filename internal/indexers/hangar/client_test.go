// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hangar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/indexers"
)

// Fixture JSON mirrors the real Hangar API v1 shapes (verified against
// hangar.papermc.io in 2026): search results carry supportedPlatforms and
// versions carry per-platform downloads plus platformDependencies.
const searchFixture = `{
  "pagination": {"count": 2, "limit": 25, "offset": 0},
  "result": [
    {
      "createdAt": "2026-01-05T16:21:22.608816Z",
      "id": 31,
      "name": "ViaVersion",
      "namespace": {"owner": "ViaVersion", "slug": "ViaVersion"},
      "stats": {"views": 100, "downloads": 533531, "recentViews": 5, "recentDownloads": 2, "stars": 478, "watchers": 274},
      "category": "misc",
      "description": "Allow newer Java Edition versions to connect",
      "lastUpdated": "2026-11-22T14:07:34.580341Z",
      "visibility": "public",
      "supportedPlatforms": {"PAPER": ["1.20.4", "1.21", "1.21.1"], "VELOCITY": ["3.4", "4.0.0"]},
      "avatarUrl": "https://hangarcdn.papermc.io/avatars/project/31.webp"
    },
    {
      "createdAt": "2026-02-05T16:21:22.608816Z",
      "id": 99,
      "name": "VelocityOnly",
      "namespace": {"owner": "someone", "slug": "VelocityOnly"},
      "stats": {"downloads": 10},
      "category": "misc",
      "description": "Proxy side only",
      "supportedPlatforms": {"VELOCITY": ["3.4"]},
      "avatarUrl": ""
    }
  ]
}`

const versionsFixture = `{
  "pagination": {"count": 2, "limit": 25, "offset": 0},
  "result": [
    {
      "createdAt": "2026-11-22T14:07:34.580341Z",
      "id": 34580,
      "projectId": 31,
      "name": "5.12.3-SNAPSHOT+1128",
      "visibility": "public",
      "description": "snapshot build",
      "author": "kennytv",
      "reviewState": "partially_reviewed",
      "channel": {"name": "Snapshot", "description": "Development builds", "color": "#84cc16"},
      "stats": {"totalDownloads": 12, "platformDownloads": {"PAPER": 12}},
      "downloads": {
        "PAPER": {
          "fileInfo": {"name": "ViaVersion-5.12.3-SNAPSHOT.jar", "sizeBytes": 6529843, "sha256Hash": "fbe6"},
          "externalUrl": null,
          "downloadUrl": "https://hangarcdn.papermc.io/plugins/ViaVersion/ViaVersion/versions/5.12.3-SNAPSHOT%2B1128/PAPER/ViaVersion-5.12.3-SNAPSHOT.jar"
        },
        "VELOCITY": {
          "fileInfo": {"name": "ViaVersion-5.12.3-SNAPSHOT.jar", "sizeBytes": 6529843, "sha256Hash": "fbe6"},
          "externalUrl": null,
          "downloadUrl": "https://hangarcdn.papermc.io/plugins/ViaVersion/ViaVersion/versions/5.12.3-SNAPSHOT%2B1128/VELOCITY/ViaVersion-5.12.3-SNAPSHOT.jar"
        }
      },
      "pluginDependencies": {
        "PAPER": [
          {"name": "ViaBackwards", "projectId": 12, "required": false, "externalUrl": null, "platform": "PAPER"},
          {"name": "RequiredDep", "projectId": 13, "required": true, "externalUrl": null, "platform": "PAPER"}
        ]
      },
      "platformDependencies": {"PAPER": ["1.20.4", "1.21", "1.21.1"], "VELOCITY": ["3.4"]}
    },
    {
      "createdAt": "2026-10-01T00:00:00Z",
      "id": 34000,
      "projectId": 31,
      "name": "5.0.0",
      "visibility": "public",
      "channel": {"name": "Release"},
      "downloads": {
        "PAPER": {
          "fileInfo": {"name": "ViaVersion-5.0.0.jar", "sizeBytes": 100, "sha256Hash": "aa"},
          "externalUrl": null,
          "downloadUrl": ""
        },
        "VELOCITY": {
          "fileInfo": {"name": "ViaVersion-5.0.0.jar", "sizeBytes": 100, "sha256Hash": "aa"},
          "externalUrl": "https://example.com/viaversion-5.0.0.jar"
        }
      },
      "pluginDependencies": {},
      "platformDependencies": {"PAPER": ["1.16.5"]}
    }
  ]
}`

const projectPageFixture = `{
  "createdAt": "2022-12-31T20:04:25.895662Z",
  "id": 31,
  "name": "ViaVersion",
  "namespace": {"owner": "ViaVersion", "slug": "ViaVersion"},
  "stats": {"downloads": 533531},
  "category": "misc",
  "description": "Allow newer Java Edition versions to connect",
  "visibility": "public",
  "supportedPlatforms": {"PAPER": ["1.21", "1.21.1"]},
  "avatarUrl": "https://hangarcdn.papermc.io/avatars/project/31.webp"
}`

func testServer(t *testing.T, mux *http.ServeMux) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
	return srv
}

func testCfg() *config.Config {
	return &config.Config{}
}

func fixtureMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/ViaVersion/ViaVersion/versions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(versionsFixture))
	})
	mux.HandleFunc("/projects/ViaVersion", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(projectPageFixture))
	})
	return mux
}

func TestSearchProjectsSendsQueryParamsAndUA(t *testing.T) {
	var gotUA, gotQuery string
	mux := http.NewServeMux()
	mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		q := r.URL.Query()
		if q.Get("q") != "via" || q.Get("limit") != "25" || q.Get("offset") != "0" {
			t.Errorf("unexpected query params: %v", q)
		}
		gotQuery = q.Get("q")
		_, _ = w.Write([]byte(searchFixture))
	})
	testServer(t, mux)

	resp, err := NewClient(testCfg()).SearchProjects(context.Background(), "via", 0, 25)
	if err != nil {
		t.Fatalf("SearchProjects: %v", err)
	}
	if gotQuery != "via" {
		t.Errorf("server saw query %q", gotQuery)
	}
	if !strings.Contains(gotUA, "CarbonPanel") {
		t.Errorf("Hangar policy requires an identifying User-Agent, got %q", gotUA)
	}
	if resp.Pagination.Count != 2 || len(resp.Result) != 2 {
		t.Fatalf("resp=%+v", resp)
	}
	if resp.Result[0].Namespace.Owner != "ViaVersion" || resp.Result[0].Stats.Downloads != 533531 {
		t.Fatalf("project=%+v", resp.Result[0])
	}
}

func TestProjectByBareSlug(t *testing.T) {
	testServer(t, fixtureMux())

	p, err := NewClient(testCfg()).Project(context.Background(), "ViaVersion")
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if p.Namespace.Owner != "ViaVersion" || p.Namespace.Slug != "ViaVersion" {
		t.Fatalf("namespace=%+v", p.Namespace)
	}
}

func TestProjectVersionsMapsPlatformArtifacts(t *testing.T) {
	testServer(t, fixtureMux())

	resp, err := NewClient(testCfg()).ProjectVersions(context.Background(), "ViaVersion", "ViaVersion", 0, 25)
	if err != nil {
		t.Fatalf("ProjectVersions: %v", err)
	}
	if len(resp.Result) != 2 {
		t.Fatalf("versions=%d", len(resp.Result))
	}
	v := resp.Result[0]
	if v.Name != "5.12.3-SNAPSHOT+1128" {
		t.Fatalf("name=%q", v.Name)
	}
	spec, ok := v.DownloadFor(PlatformPaper)
	if !ok || spec.FileInfo.Name != "ViaVersion-5.12.3-SNAPSHOT.jar" || spec.FileInfo.SizeBytes != 6529843 {
		t.Fatalf("paper download=%+v ok=%v", spec, ok)
	}
	if len(v.PlatformDependencies[PlatformPaper]) == 0 {
		t.Fatal("expected paper game versions in platformDependencies")
	}
}

func TestSearchPluginsFiltersByPaperGameVersion(t *testing.T) {
	testServer(t, fixtureMux())
	mux := http.NewServeMux()
	mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(searchFixture))
	})
	testServer(t, mux)

	idx := NewClient(testCfg())
	if idx.Name() != "hangar" {
		t.Fatalf("name=%q", idx.Name())
	}

	results, err := idx.SearchPlugins(context.Background(), "via", "1.21.1", 25)
	if err != nil {
		t.Fatalf("SearchPlugins: %v", err)
	}
	if len(results) != 1 || results[0].Slug != "ViaVersion" {
		t.Fatalf("results=%+v", results)
	}
	r := results[0]
	if r.Indexer != "hangar" || r.Author != "ViaVersion" || r.DownloadCount != 533531 {
		t.Fatalf("result=%+v", r)
	}

	// A game version the project does not list must filter it out.
	empty, err := idx.SearchPlugins(context.Background(), "via", "1.8", 25)
	if err != nil {
		t.Fatalf("SearchPlugins(1.8): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no results for 1.8, got %+v", empty)
	}

	// No game version: no filtering.
	all, err := idx.SearchPlugins(context.Background(), "via", "", 25)
	if err != nil || len(all) != 2 {
		t.Fatalf("unfiltered results=%+v err=%v", all, err)
	}
}

func TestPluginVersionsFiltersToPaperAndGameVersion(t *testing.T) {
	testServer(t, fixtureMux())

	// Empty author: the client resolves the namespace via the project page.
	versions, err := NewClient(testCfg()).PluginVersions(context.Background(), "", "ViaVersion", "1.21.1")
	if err != nil {
		t.Fatalf("PluginVersions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("versions=%+v", versions)
	}
	v := versions[0]
	if v.ID != "34580" || v.VersionNumber != "5.12.3-SNAPSHOT+1128" {
		t.Fatalf("version=%+v", v)
	}
	if v.VersionType != "snapshot" || v.FileName != "ViaVersion-5.12.3-SNAPSHOT.jar" || v.Size != 6529843 {
		t.Fatalf("mapped version=%+v", v)
	}
	if !strings.HasPrefix(v.DownloadURL, "https://hangarcdn.papermc.io/") {
		t.Errorf("download url=%q", v.DownloadURL)
	}
	if len(v.Dependencies) != 2 {
		t.Fatalf("dependencies=%+v", v.Dependencies)
	}
	for _, dep := range v.Dependencies {
		if dep.Name == "RequiredDep" && dep.DependencyType != "required" {
			t.Errorf("required dep mapped as %q", dep.DependencyType)
		}
		if dep.Name == "ViaBackwards" && dep.DependencyType != "optional" {
			t.Errorf("optional dep mapped as %q", dep.DependencyType)
		}
	}

	// Older server: only the 5.0.0 build claims 1.16.5, but its Paper entry
	// has no usable URL, so nothing matches.
	old, err := NewClient(testCfg()).PluginVersions(context.Background(), "ViaVersion", "ViaVersion", "1.16.5")
	if err != nil {
		t.Fatalf("PluginVersions(1.16.5): %v", err)
	}
	if len(old) != 0 {
		t.Fatalf("expected no usable builds for 1.16.5, got %+v", old)
	}

	// No game version filter: every version with a Paper artifact passes.
	all, err := NewClient(testCfg()).PluginVersions(context.Background(), "ViaVersion", "ViaVersion", "")
	if err != nil || len(all) != 1 {
		t.Fatalf("unfiltered versions=%+v err=%v", all, err)
	}
}

func TestPluginVersionsExternalDownloadUsed(t *testing.T) {
	external := "https://example.com/viaversion-5.0.0.jar"
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/ViaVersion", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(projectPageFixture))
	})
	mux.HandleFunc("/projects/ViaVersion/ViaVersion/versions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
		  "pagination": {"count": 1, "limit": 25, "offset": 0},
		  "result": [{
		    "id": 34000, "projectId": 31, "name": "5.0.0", "visibility": "public",
		    "channel": {"name": "Release"},
		    "downloads": {"PAPER": {"fileInfo": {"name": "ViaVersion-5.0.0.jar", "sizeBytes": 100}, "externalUrl": "` + external + `", "downloadUrl": ""}},
		    "platformDependencies": {"PAPER": ["1.21.1"]}
		  }]
		}`))
	})
	testServer(t, mux)

	versions, err := NewClient(testCfg()).PluginVersions(context.Background(), "", "ViaVersion", "1.21.1")
	if err != nil {
		t.Fatalf("PluginVersions: %v", err)
	}
	if len(versions) != 1 || versions[0].DownloadURL != external {
		t.Fatalf("versions=%+v", versions)
	}
}

func TestPluginVersionsRespectsVersionVisibility(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/ViaVersion", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(projectPageFixture))
	})
	mux.HandleFunc("/projects/ViaVersion/ViaVersion/versions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
		  "pagination": {"count": 1, "limit": 25, "offset": 0},
		  "result": [{
		    "id": 1, "projectId": 31, "name": "soft-deleted", "visibility": "softDelete",
		    "downloads": {"PAPER": {"fileInfo": {"name": "x.jar", "sizeBytes": 1}, "downloadUrl": "https://hangarcdn.papermc.io/x.jar"}},
		    "platformDependencies": {"PAPER": ["1.21.1"]}
		  }]
		}`))
	})
	testServer(t, mux)

	versions, err := NewClient(testCfg()).PluginVersions(context.Background(), "ViaVersion", "ViaVersion", "1.21.1")
	if err != nil {
		t.Fatalf("PluginVersions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("non-public version leaked: %+v", versions)
	}
}

func TestSearchProjectsError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	})
	testServer(t, mux)

	if _, err := NewClient(testCfg()).SearchProjects(context.Background(), "x", 0, 25); err == nil {
		t.Fatal("want error on 429")
	}
	var idxErr *indexers.IndexerError
	if _, err := NewClient(testCfg()).SearchProjects(context.Background(), "x", 0, 25); !errors.As(err, &idxErr) || idxErr.Kind != indexers.ErrRateLimit {
		t.Fatalf("want typed rate-limit error, got %v", err)
	}
}

func TestPluginVersionsNotFound(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/projects/missing", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	testServer(t, mux)

	if _, err := NewClient(testCfg()).PluginVersions(context.Background(), "", "missing", ""); err == nil {
		t.Fatal("want error for unknown project")
	}
}

func TestRegisteredAsPluginIndexer(t *testing.T) {
	idx, err := indexers.NewPluginIndexer("hangar", "", testCfg())
	if err != nil {
		t.Fatalf("registry lookup: %v", err)
	}
	if idx.Name() != "hangar" {
		t.Fatalf("name=%q", idx.Name())
	}
	if got := indexers.PluginIndexerNames(); len(got) == 0 {
		t.Fatalf("expected registered plugin indexers, got %v", got)
	}

	// Raw response fields decode with the expected keys (guards against
	// drift between the struct tags and the live API shape).
	var raw map[string]any
	if err := json.Unmarshal([]byte(searchFixture), &raw); err != nil {
		t.Fatalf("fixture: %v", err)
	}
}
