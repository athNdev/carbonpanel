// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// enrichModrinthDependencyTitles fills each dependency Title with the real
// project name so the console lists a name instead of a raw project ID.
// Best-effort: lookup failures leave Title empty and the versions response
// still succeeds with raw IDs as fallback.
func (m *ModOnlineManager) enrichModrinthDependencyTitles(ctx context.Context, token, ua string, versions []ModVersionItem) {
	idSet := map[string]struct{}{}
	for _, v := range versions {
		for _, d := range v.Dependencies {
			if id := strings.TrimSpace(d.ProjectID); id != "" {
				idSet[id] = struct{}{}
			}
		}
	}
	for vi := range versions {
		for di := range versions[vi].Dependencies {
			d := &versions[vi].Dependencies[di]
			if strings.TrimSpace(d.ProjectID) == "" && strings.TrimSpace(d.VersionID) != "" {
				if pid := m.resolveModrinthVersionProject(ctx, token, ua, strings.TrimSpace(d.VersionID)); pid != "" {
					d.ProjectID = pid
					idSet[pid] = struct{}{}
				}
			}
		}
	}
	if len(idSet) == 0 {
		return
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	if len(ids) > 200 {
		ids = ids[:200]
	}
	titles := m.fetchModrinthProjectTitles(ctx, token, ua, ids)
	if len(titles) == 0 {
		return
	}
	for vi := range versions {
		for di := range versions[vi].Dependencies {
			d := &versions[vi].Dependencies[di]
			if d.Title == "" && d.ProjectID != "" {
				if title, ok := titles[d.ProjectID]; ok && title != "" {
					d.Title = title
				}
			}
		}
	}
}

func (m *ModOnlineManager) resolveModrinthVersionProject(ctx context.Context, token, ua, versionID string) string {
	reqURL := "https://api.modrinth.com/v2/version/" + url.PathEscape(versionID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", ua)
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var data struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ""
	}
	return strings.TrimSpace(data.ProjectID)
}

func (m *ModOnlineManager) fetchModrinthProjectTitles(ctx context.Context, token, ua string, ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	idsJSON, _ := json.Marshal(ids)
	reqURL := "https://api.modrinth.com/v2/projects?ids=" + url.QueryEscape(string(idsJSON))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err == nil {
		req.Header.Set("User-Agent", ua)
		if token != "" {
			req.Header.Set("Authorization", token)
		}
		if resp, err := m.httpClient.Do(req); err == nil {
			if resp.StatusCode == http.StatusOK {
				var projects []struct {
					ID    string `json:"id"`
					Title string `json:"title"`
					Slug  string `json:"slug"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&projects); err == nil {
					for _, pr := range projects {
						if pr.ID != "" && pr.Title != "" {
							out[pr.ID] = pr.Title
						}
					}
				}
			}
			resp.Body.Close()
		}
	}
	if len(out) > 0 {
		return out
	}
	if len(ids) > 10 {
		m.log.Warn("Modrinth batch project lookup failed; leaving dependency titles unresolved")
		return out
	}
	for _, id := range ids {
		if _, ok := out[id]; ok {
			continue
		}
		singleURL := "https://api.modrinth.com/v2/project/" + url.PathEscape(id)
		sreq, err := http.NewRequestWithContext(ctx, http.MethodGet, singleURL, nil)
		if err != nil {
			continue
		}
		sreq.Header.Set("User-Agent", ua)
		if token != "" {
			sreq.Header.Set("Authorization", token)
		}
		sresp, err := m.httpClient.Do(sreq)
		if err != nil {
			continue
		}
		if sresp.StatusCode != http.StatusOK {
			sresp.Body.Close()
			continue
		}
		var project struct {
			ID    string `json:"id"`
			Title string `json:"title"`
		}
		if err := json.NewDecoder(sresp.Body).Decode(&project); err == nil && project.Title != "" {
			key := project.ID
			if key == "" {
				key = id
			}
			out[key] = project.Title
		}
		sresp.Body.Close()
	}
	return out
}

// enrichCurseForgeDependencyTitles fills each dependency Title with the real
// mod name so the console lists a name instead of a raw numeric mod ID.
// Best-effort: lookup failures leave Title empty.
func (m *ModOnlineManager) enrichCurseForgeDependencyTitles(ctx context.Context, apiKey string, versions []ModVersionItem) {
	idSet := map[string]struct{}{}
	for _, v := range versions {
		for _, d := range v.Dependencies {
			if id := strings.TrimSpace(d.ProjectID); id != "" {
				if _, err := strconv.Atoi(id); err == nil {
					idSet[id] = struct{}{}
				}
			}
		}
	}
	if len(idSet) == 0 {
		return
	}
	ids := make([]string, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	if len(ids) > 200 {
		ids = ids[:200]
	}
	titles := m.fetchCurseForgeModTitles(ctx, apiKey, ids)
	if len(titles) == 0 {
		return
	}
	for vi := range versions {
		for di := range versions[vi].Dependencies {
			d := &versions[vi].Dependencies[di]
			if d.Title == "" && d.ProjectID != "" {
				if title, ok := titles[d.ProjectID]; ok && title != "" {
					d.Title = title
				}
			}
		}
	}
}

func (m *ModOnlineManager) fetchCurseForgeModTitles(ctx context.Context, apiKey string, ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	modIDs := make([]int, 0, len(ids))
	for _, id := range ids {
		if n, err := strconv.Atoi(strings.TrimSpace(id)); err == nil {
			modIDs = append(modIDs, n)
		}
	}
	if len(modIDs) == 0 {
		return out
	}
	body, _ := json.Marshal(map[string]any{"modIds": modIDs, "filterPcOnly": false})
	tryBatch := func(baseURL string, withKey bool) bool {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/mods", bytes.NewReader(body))
		if err != nil {
			return false
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Content-Type", "application/json")
		if withKey {
			req.Header.Set("x-api-key", apiKey)
		} else {
			req.Header.Set("User-Agent", "CarbonPanel/1.0 (CarbonPanel-admin)")
		}
		resp, err := m.httpClient.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var data struct {
			Data []struct {
				ID   int    `json:"id"`
				Name string `json:"name"`
				Slug string `json:"slug"`
			} `json:"data"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
			return false
		}
		for _, mod := range data.Data {
			if mod.Name != "" {
				out[fmt.Sprintf("%d", mod.ID)] = mod.Name
			}
		}
		return len(out) > 0
	}
	if apiKey != "" && tryBatch("https://api.curseforge.com/v1", true) {
		return out
	}
	if tryBatch("https://api.curse.tools/v1/cf", false) {
		return out
	}
	if len(ids) > 10 {
		m.log.Warn("CurseForge batch mod lookup failed; leaving dependency titles unresolved")
		return out
	}
	for _, id := range ids {
		if _, ok := out[id]; ok {
			continue
		}
		fetched := ""
		if apiKey != "" {
			fetched = m.fetchCurseForgeModTitle(ctx, "https://api.curseforge.com/v1/mods/"+url.PathEscape(id), apiKey, true)
		}
		if fetched == "" {
			fetched = m.fetchCurseForgeModTitle(ctx, "https://api.curse.tools/v1/cf/mods/"+url.PathEscape(id), "", false)
		}
		if fetched != "" {
			out[id] = fetched
		}
	}
	return out
}

func (m *ModOnlineManager) fetchCurseForgeModTitle(ctx context.Context, reqURL, apiKey string, withKey bool) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Accept", "application/json")
	if withKey {
		req.Header.Set("x-api-key", apiKey)
	} else {
		req.Header.Set("User-Agent", "CarbonPanel/1.0 (CarbonPanel-admin)")
	}
	resp, err := m.httpClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var data struct {
		Data struct {
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return ""
	}
	return strings.TrimSpace(data.Data.Name)
}
