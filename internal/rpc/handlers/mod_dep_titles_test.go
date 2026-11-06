// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/logger"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResp(status int, v any) *http.Response {
	body, _ := json.Marshal(v)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Header:     make(http.Header),
	}
}

func TestEnrichModrinthDependencyTitles(t *testing.T) {
	mgr := NewModOnlineManager(nil, logger.New(), nil, nil)
	mgr.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if strings.Contains(r.URL.Path, "/v2/projects") {
			return jsonResp(200, []map[string]any{
				{"id": "P7dR8mSH", "title": "Cloth Config API", "slug": "cloth-config"},
				{"id": "9s6osm5g", "title": "Architectury API", "slug": "architectury-api"},
			}), nil
		}
		return jsonResp(404, map[string]any{}), nil
	})}

	versions := []ModVersionItem{{
		ID: "v1", Dependencies: []ModDependencyItem{
			{ProjectID: "P7dR8mSH", DependencyType: "required"},
			{ProjectID: "9s6osm5g", DependencyType: "optional"},
		},
	}}
	mgr.enrichModrinthDependencyTitles(context.Background(), "", "CarbonPanel/test", versions)
	if versions[0].Dependencies[0].Title != "Cloth Config API" {
		t.Fatalf("dep0 title = %q", versions[0].Dependencies[0].Title)
	}
	if versions[0].Dependencies[1].Title != "Architectury API" {
		t.Fatalf("dep1 title = %q", versions[0].Dependencies[1].Title)
	}
}

func TestEnrichModrinthLeavesIDsOnLookupFailure(t *testing.T) {
	mgr := NewModOnlineManager(nil, logger.New(), nil, nil)
	mgr.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(500, map[string]any{}), nil
	})}
	versions := []ModVersionItem{{
		ID: "v1", Dependencies: []ModDependencyItem{
			{ProjectID: "P7dR8mSH", DependencyType: "required"},
		},
	}}
	mgr.enrichModrinthDependencyTitles(context.Background(), "", "CarbonPanel/test", versions)
	if versions[0].Dependencies[0].Title != "" {
		t.Fatalf("expected empty title on failure, got %q", versions[0].Dependencies[0].Title)
	}
	if versions[0].Dependencies[0].ProjectID != "P7dR8mSH" {
		t.Fatalf("project ID must be preserved as fallback")
	}
}

func TestEnrichCurseForgeDependencyTitles(t *testing.T) {
	mgr := NewModOnlineManager(nil, logger.New(), nil, nil)
	mgr.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/mods") {
			return jsonResp(200, map[string]any{
				"data": []map[string]any{
					{"id": 238222, "name": "Just Enough Items", "slug": "jei"},
				},
			}), nil
		}
		return jsonResp(404, map[string]any{}), nil
	})}
	versions := []ModVersionItem{{
		ID: "1", Dependencies: []ModDependencyItem{
			{ProjectID: "238222", DependencyType: "required"},
		},
	}}
	mgr.enrichCurseForgeDependencyTitles(context.Background(), "", versions)
	if versions[0].Dependencies[0].Title != "Just Enough Items" {
		t.Fatalf("dep title = %q", versions[0].Dependencies[0].Title)
	}
}

func TestResolveModrinthVersionProject(t *testing.T) {
	mgr := NewModOnlineManager(nil, logger.New(), nil, nil)
	mgr.httpClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonResp(200, map[string]any{"project_id": "P7dR8mSH"}), nil
	})}
	got := mgr.resolveModrinthVersionProject(context.Background(), "", "CarbonPanel/test", "ver123")
	if got != "P7dR8mSH" {
		t.Fatalf("project = %q", got)
	}
}
