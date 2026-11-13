// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package packwiz

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/indexers/fuego"
)

// DependencyRef describes a single mod dependency discovered during resolution.
type DependencyRef struct {
	Platform       string `json:"platform"`   // "modrinth" | "curseforge"
	ProjectID      string `json:"project_id"` // Modrinth project id/slug or CurseForge numeric mod id
	VersionID      string `json:"version_id,omitempty"`
	Title          string `json:"title,omitempty"`
	DependencyType string `json:"dependency_type"` // "required" | "optional"
}

// DependencyInstallReport summarises a dependency-aware mod install.
type DependencyInstallReport struct {
	Root       ModItem         `json:"root"`
	Added      []ModItem       `json:"added"`      // transitive deps newly installed
	Skipped    []string        `json:"skipped"`    // slugs already present in the pack
	Unresolved []DependencyRef `json:"unresolved"` // required deps with no compatible version
}

// VersionDependencyFetcher resolves the latest compatible version metadata for
// a project plus that version's own required dependencies. The default
// implementation queries the Modrinth / CurseForge APIs over HTTP; tests inject
// a stub.
type VersionDependencyFetcher interface {
	// LatestCompatibleMod returns install metadata for the newest version of
	// projectID compatible with loader/mcVersion, plus its required deps.
	LatestCompatibleMod(platform, projectID, loader, mcVersion string) (ModItem, []DependencyRef, error)
}

// MaxDependencyDepth bounds transitive resolution to avoid runaway API fan-out.
const MaxDependencyDepth = 5

// depKey uniquely identifies a project across platforms for cycle detection.
func depKey(platform, projectID string) string {
	return strings.ToLower(platform) + ":" + strings.ToLower(projectID)
}

// ResolveTransitiveDependencies walks required dependencies breadth-first,
// cycle-safe and depth-bounded. It returns every distinct required dependency
// (excluding the root project itself).
func (m *Manager) ResolveTransitiveDependencies(fetcher VersionDependencyFetcher, platform, projectID, loader, mcVersion string, maxDepth int) ([]DependencyRef, error) {
	if fetcher == nil {
		return nil, fmt.Errorf("dependency fetcher is required")
	}
	if maxDepth <= 0 {
		maxDepth = MaxDependencyDepth
	}

	visited := map[string]bool{depKey(platform, projectID): true}
	var resolved []DependencyRef

	type queueItem struct {
		platform  string
		projectID string
		depth     int
	}
	queue := []queueItem{{platform: platform, projectID: projectID, depth: 0}}

	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.depth >= maxDepth {
			continue
		}

		_, deps, err := fetcher.LatestCompatibleMod(cur.platform, cur.projectID, loader, mcVersion)
		if err != nil {
			// A single unresolvable node must not abort the whole walk;
			// the caller records it via AddModWithDependencies instead.
			continue
		}
		for _, d := range deps {
			if !strings.EqualFold(d.DependencyType, "required") {
				continue
			}
			key := depKey(d.Platform, d.ProjectID)
			if d.ProjectID == "" || visited[key] {
				continue
			}
			visited[key] = true
			resolved = append(resolved, d)
			queue = append(queue, queueItem{platform: d.Platform, projectID: d.ProjectID, depth: cur.depth + 1})
		}
	}

	if resolved == nil {
		resolved = []DependencyRef{}
	}
	return resolved, nil
}

// AddModWithDependencies installs mod plus all of its transitive required
// dependencies (for loader/mcVersion), skipping anything already in the pack.
// Unresolvable required deps are reported, never fatal.
func (m *Manager) AddModWithDependencies(packID string, mod ModItem, loader, mcVersion string, fetcher VersionDependencyFetcher) (*DependencyInstallReport, error) {
	if fetcher == nil {
		return nil, fmt.Errorf("dependency fetcher is required")
	}

	if err := m.AddMod(packID, mod); err != nil {
		return nil, err
	}

	report := &DependencyInstallReport{Root: mod, Added: []ModItem{}, Skipped: []string{}, Unresolved: []DependencyRef{}}

	pack, err := m.GetPack(packID)
	if err != nil {
		return nil, err
	}
	presentByProject := map[string]bool{}
	presentBySlug := map[string]bool{}
	for _, item := range pack.Mods {
		if item.ProjectID != "" {
			presentByProject[depKey(item.Platform, item.ProjectID)] = true
		}
		if item.Slug != "" {
			presentBySlug[strings.ToLower(item.Slug)] = true
		}
	}

	// Resolve the full transitive closure first (cycle-safe; the root is
	// treated as visited so a dependency cycle can never reinstall it),
	// then install every missing node.
	closure, err := m.ResolveTransitiveDependencies(fetcher, mod.Platform, mod.ProjectID, loader, mcVersion, MaxDependencyDepth)
	if err != nil {
		return report, nil
	}

	for _, d := range closure {
		key := depKey(d.Platform, d.ProjectID)
		if presentByProject[key] {
			report.Skipped = append(report.Skipped, d.ProjectID)
			continue
		}
		compat, _, err := fetcher.LatestCompatibleMod(d.Platform, d.ProjectID, loader, mcVersion)
		if err != nil {
			report.Unresolved = append(report.Unresolved, d)
			continue
		}
		if compat.Slug != "" && presentBySlug[strings.ToLower(compat.Slug)] {
			report.Skipped = append(report.Skipped, compat.Slug)
			continue
		}
		if compat.Side == "" {
			compat.Side = "both"
		}
		if err := m.AddMod(packID, compat); err != nil {
			report.Unresolved = append(report.Unresolved, d)
			continue
		}
		report.Added = append(report.Added, compat)
		presentByProject[key] = true
		if compat.Slug != "" {
			presentBySlug[strings.ToLower(compat.Slug)] = true
		}
	}

	return report, nil
}

// httpDependencyFetcher is the production VersionDependencyFetcher backed by
// the Modrinth API and the keyless CurseForge community proxy (the same
// fallback chain used by the mod search handler).
type httpDependencyFetcher struct {
	manager *Manager
	ctx     context.Context
}

// NewHTTPDependencyFetcher returns the production fetcher for a Manager.
func (m *Manager) NewHTTPDependencyFetcher() VersionDependencyFetcher {
	return m.NewHTTPDependencyFetcherContext(context.Background())
}

// NewHTTPDependencyFetcherContext binds provider calls to the operation lifetime.
func (m *Manager) NewHTTPDependencyFetcherContext(ctx context.Context) VersionDependencyFetcher {
	return &httpDependencyFetcher{manager: m, ctx: ctx}
}

func (f *httpDependencyFetcher) LatestCompatibleMod(platform, projectID, loader, mcVersion string) (ModItem, []DependencyRef, error) {
	switch strings.ToLower(platform) {
	case "curseforge":
		return f.latestCurseForgeMod(projectID, loader, mcVersion)
	default:
		return f.latestModrinthMod(projectID, loader, mcVersion)
	}
}

func (f *httpDependencyFetcher) latestModrinthMod(projectID, loader, mcVersion string) (ModItem, []DependencyRef, error) {
	if loader == "vanilla" {
		loader = ""
	}
	versions, err := f.manager.modrinthClient().GetVersions(f.ctx, projectID, strings.ToLower(loader), mcVersion)
	if err != nil {
		return ModItem{}, nil, err
	}
	if len(versions) == 0 {
		return ModItem{}, nil, fmt.Errorf("no compatible version of %s for %s %s", projectID, loader, mcVersion)
	}

	latest := versions[0]
	file := primaryVersionFile(latest.Files)
	dlURL, fileName, size := "", "", int64(0)
	if file != nil {
		dlURL, fileName, size = file.URL, file.Filename, file.Size
	}
	title := latest.Name
	if title == "" {
		title = latest.VersionNumber
	}
	if title == "" {
		title = projectID
	}

	mod := ModItem{
		Slug:        sanitizeSlug(projectID),
		Name:        title,
		FileName:    fileName,
		Side:        "both",
		Platform:    "modrinth",
		ProjectID:   projectID,
		VersionID:   latest.ID,
		DownloadURL: dlURL,
		FileSize:    size,
	}

	var deps []DependencyRef
	for _, d := range latest.Dependencies {
		if d.ProjectID == nil || *d.ProjectID == "" {
			continue
		}
		vid := ""
		if d.VersionID != nil {
			vid = *d.VersionID
		}
		deps = append(deps, DependencyRef{
			Platform:       "modrinth",
			ProjectID:      *d.ProjectID,
			VersionID:      vid,
			DependencyType: d.DependencyType,
		})
	}
	return mod, deps, nil
}

func (f *httpDependencyFetcher) latestCurseForgeMod(modID, loader, mcVersion string) (ModItem, []DependencyRef, error) {
	id, err := strconv.Atoi(modID)
	if err != nil {
		return ModItem{}, nil, fmt.Errorf("invalid CurseForge project ID %q: %w", modID, err)
	}
	files, err := f.manager.curseForgeClient().GetFiles(f.ctx, id, fuego.FilesOptions{GameVersion: mcVersion, PageSize: 50})
	if err != nil {
		return ModItem{}, nil, err
	}
	if len(files) == 0 {
		return ModItem{}, nil, fmt.Errorf("no compatible file of %s for %s", modID, mcVersion)
	}

	file := files[0]
	if loader != "" && loader != "vanilla" {
		for _, entry := range files {
			for _, gv := range entry.GameVersions {
				if strings.EqualFold(gv, loader) {
					file = entry
					break
				}
			}
		}
	}

	mod := ModItem{
		Slug:        sanitizeSlug(fmt.Sprintf("cf-%s", modID)),
		Name:        file.DisplayName,
		FileName:    file.FileName,
		Side:        "both",
		Platform:    "curseforge",
		ProjectID:   modID,
		VersionID:   fmt.Sprintf("%d", file.ID),
		DownloadURL: file.DownloadURL,
		FileSize:    file.FileLength,
	}

	var deps []DependencyRef
	for _, d := range file.Dependencies {
		depType := "optional"
		if d.RelationType == 3 {
			depType = "required"
		}
		deps = append(deps, DependencyRef{
			Platform:       "curseforge",
			ProjectID:      fmt.Sprintf("%d", d.ModID),
			DependencyType: depType,
		})
	}
	return mod, deps, nil
}
