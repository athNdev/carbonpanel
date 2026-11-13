// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"context"
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
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
	version, err := m.modrinthClient(token, ua).GetVersion(ctx, versionID)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(version.ProjectID)
}

func (m *ModOnlineManager) modrinthClient(token, ua string) *modrinth.Client {
	return modrinth.NewClientWithOptions(nil, modrinth.Options{BaseURL: m.modrinthBaseURL, Token: token, UserAgent: ua, HTTPClient: m.httpClient})
}

func (m *ModOnlineManager) fetchModrinthProjectTitles(ctx context.Context, token, ua string, ids []string) map[string]string {
	out := map[string]string{}
	if len(ids) == 0 || ctx.Err() != nil {
		return out
	}
	provider := m.modrinthClient(token, ua)
	projects, err := provider.GetProjects(ctx, ids)
	if err == nil {
		for _, project := range projects {
			if project.ID != "" && project.Title != "" {
				out[project.ID] = project.Title
			}
		}
	}
	if len(out) > 0 || len(ids) > 10 || ctx.Err() != nil {
		return out
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		project, err := provider.GetModpack(ctx, id)
		if err == nil && project.Title != "" {
			key := project.ID
			if key == "" {
				key = id
			}
			out[key] = project.Title
		}
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
	if ctx.Err() != nil {
		return out
	}
	provider := m.curseForgeClient(apiKey)
	mods, err := provider.GetMods(ctx, modIDs)
	if err == nil {
		for _, mod := range mods {
			if mod.Name != "" {
				out[strconv.Itoa(mod.ID)] = mod.Name
			}
		}
	}
	if len(out) > 0 || len(ids) > 10 || ctx.Err() != nil {
		return out
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		n, err := strconv.Atoi(strings.TrimSpace(id))
		if err != nil {
			continue
		}
		mod, err := provider.GetModpack(ctx, n)
		if err == nil && strings.TrimSpace(mod.Name) != "" {
			out[id] = strings.TrimSpace(mod.Name)
		}
	}
	return out
}
