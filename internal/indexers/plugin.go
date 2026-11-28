// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package indexers

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/athNdev/carbon-panel/internal/config"
)

// PluginIndexer defines the interface for Bukkit/Paper plugin discovery
// services. Plugins are a separate content type from mods: they run inside
// Paper-family servers through the Bukkit plugin model, so they get their own
// indexer seam instead of being forced through the modpack/mod interfaces.
type PluginIndexer interface {
	// SearchPlugins searches for plugins matching the query, optionally
	// narrowed to a Minecraft version. Results carry the indexer's own label
	// so callers can render and route per source.
	SearchPlugins(ctx context.Context, query, mcVersion string, limit int) ([]PluginResult, error)

	// PluginVersions lists versions of a plugin. author may be empty for
	// indexers that can resolve it from the slug; mcVersion optionally
	// filters to compatible builds.
	PluginVersions(ctx context.Context, author, slug, mcVersion string) ([]PluginVersion, error)

	// Name returns the name of this indexer (e.g. "hangar").
	Name() string
}

// PluginResult is a plugin as reported by a plugin indexer.
type PluginResult struct {
	ID            string `json:"id"`
	Indexer       string `json:"indexer"`
	Name          string `json:"name"`
	Author        string `json:"author"`
	Slug          string `json:"slug"`
	Summary       string `json:"summary"`
	IconURL       string `json:"icon_url"`
	DownloadCount int64  `json:"download_count"`
	// Platforms are the server platforms the plugin targets (e.g. "PAPER").
	Platforms []string `json:"platforms"`
	// GameVersions are the Minecraft versions the plugin supports.
	GameVersions []string `json:"game_versions"`
}

// PluginVersion is a downloadable plugin build.
type PluginVersion struct {
	ID            string                 `json:"id"`
	VersionNumber string                 `json:"version_number"`
	VersionType   string                 `json:"version_type"` // "release", "beta", "alpha", "snapshot", ...
	FileName      string                 `json:"file_name"`
	DownloadURL   string                 `json:"download_url"`
	Size          int64                  `json:"size"`
	SHA256        string                 `json:"sha256"`
	Platforms     []string               `json:"platforms"`
	GameVersions  []string               `json:"game_versions"`
	Dependencies  []PluginDependencyItem `json:"dependencies"`
}

// PluginDependencyItem is a plugin dependency as reported by an indexer.
type PluginDependencyItem struct {
	ProjectID      string `json:"project_id"`
	Name           string `json:"name"`
	DependencyType string `json:"dependency_type"` // "required", "optional"
}

// PluginIndexerFactory creates a PluginIndexer from an API key and config.
type PluginIndexerFactory func(apiKey string, cfg *config.Config) PluginIndexer

var (
	pluginRegistryMu sync.RWMutex
	pluginRegistry   = make(map[string]PluginIndexerFactory)
)

// RegisterPluginIndexer registers a PluginIndexerFactory under the given name.
// Typically called from an indexer package's init() function.
func RegisterPluginIndexer(name string, factory PluginIndexerFactory) {
	pluginRegistryMu.Lock()
	defer pluginRegistryMu.Unlock()
	pluginRegistry[name] = factory
}

// NewPluginIndexer creates a PluginIndexer by name using the factory registry.
func NewPluginIndexer(name string, apiKey string, cfg *config.Config) (PluginIndexer, error) {
	pluginRegistryMu.RLock()
	factory, ok := pluginRegistry[name]
	pluginRegistryMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown plugin indexer: %s", name)
	}
	return factory(apiKey, cfg), nil
}

// PluginIndexerNames returns the registered plugin indexer names, sorted, so
// callers can search every provider for merged coverage.
func PluginIndexerNames() []string {
	pluginRegistryMu.RLock()
	defer pluginRegistryMu.RUnlock()
	names := make([]string, 0, len(pluginRegistry))
	for name := range pluginRegistry {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// PluginIndexersFor builds the plugin indexer set: the named indexer, or every
// registered provider when name is empty so search results from any provider
// remain resolvable (mirrors DatapackIndexersFor).
func PluginIndexersFor(name, apiKey string, cfg *config.Config) ([]PluginIndexer, error) {
	if strings.TrimSpace(name) != "" {
		idx, err := NewPluginIndexer(name, apiKey, cfg)
		if err != nil {
			return nil, err
		}
		return []PluginIndexer{idx}, nil
	}
	var idxs []PluginIndexer
	for _, n := range PluginIndexerNames() {
		if idx, err := NewPluginIndexer(n, apiKey, cfg); err == nil {
			idxs = append(idxs, idx)
		}
	}
	if len(idxs) == 0 {
		return nil, fmt.Errorf("no plugin providers available")
	}
	return idxs, nil
}

// SupportsGameVersion reports whether a platform's game-version list admits
// the requested Minecraft version. A listed "LATEST" covers every requested
// version, and a listed range such as "1.8-1.21.10" covers every version
// ordered inclusively between its endpoints. Otherwise matching is
// segment-aware: a listing of "1.21" covers "1.21.1" and vice versa, but
// "1.21.1" never matches "1.21.10". An empty game version is treated as
// unfiltered; with an empty platform the union of every platform's list is
// consulted.
func SupportsGameVersion(platforms map[string][]string, platform, gameVersion string) bool {
	if strings.TrimSpace(gameVersion) == "" {
		return true
	}
	for listedPlatform, versions := range platforms {
		if platform != "" && !strings.EqualFold(listedPlatform, platform) {
			continue
		}
		for _, listed := range versions {
			if gameVersionSegmentMatch(listed, gameVersion) {
				return true
			}
		}
	}
	return false
}

// gameVersionSegmentMatch compares one listed value against a requested
// Minecraft version. "LATEST" and numeric ranges are coverage markers; every
// other listing is compared by its dot-separated segments.
func gameVersionSegmentMatch(listed, requested string) bool {
	listed = strings.TrimSpace(listed)
	requested = strings.TrimSpace(requested)
	if strings.EqualFold(listed, "LATEST") {
		return true
	}
	if low, high, ok := gameVersionRange(listed); ok {
		return versionCompare(requested, low) >= 0 && versionCompare(requested, high) <= 0
	}
	return gameVersionPrefixMatch(listed, requested)
}

// gameVersionPrefixMatch matches equal versions and a strict dot-segment
// prefix in either direction.
func gameVersionPrefixMatch(a, b string) bool {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	if len(as) > len(bs) {
		as, bs = bs, as
	}
	for i, seg := range as {
		if seg != bs[i] {
			return false
		}
	}
	return true
}

// gameVersionRange recognizes "a-b" listings whose endpoints are numeric
// release versions. A loader's pre-release suffix (for example
// "1.20.1-pre1") is not a range.
func gameVersionRange(listed string) (low, high string, ok bool) {
	if strings.Count(listed, "-") != 1 {
		return "", "", false
	}
	low, high, _ = strings.Cut(listed, "-")
	low, high = strings.TrimSpace(low), strings.TrimSpace(high)
	if !isNumericVersion(low) || !isNumericVersion(high) {
		return "", "", false
	}
	return low, high, true
}

func isNumericVersion(version string) bool {
	if version == "" {
		return false
	}
	for _, segment := range strings.Split(version, ".") {
		if segment == "" {
			return false
		}
		for _, char := range segment {
			if char < '0' || char > '9' {
				return false
			}
		}
	}
	return true
}

// versionCompare orders numeric release versions segment by segment. A
// shorter version sorts before the same prefix with more segments, so
// "1.21" is below "1.21.1" and both are below "1.21.10".
func versionCompare(a, b string) int {
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		if i >= len(as) {
			return -1
		}
		if i >= len(bs) {
			return 1
		}
		aNum, aErr := strconv.Atoi(as[i])
		bNum, bErr := strconv.Atoi(bs[i])
		if aErr == nil && bErr == nil {
			if aNum != bNum {
				if aNum < bNum {
					return -1
				}
				return 1
			}
			continue
		}
		if as[i] != bs[i] {
			if as[i] < bs[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// MergeLabeledResults merges search pages from multiple providers into one
// list while keeping every result's own label. keyOf extracts the dedupe key
// (typically the display name): per case-insensitive key the first provider's
// entry wins, so a duplicate from a second index never shadows the
// plugin-native result, mirroring how the datapack search merges
// multi-provider coverage.
func MergeLabeledResults[T any](keyOf func(T) string, pages ...[]T) []T {
	seen := make(map[string]bool)
	var merged []T
	for _, page := range pages {
		for _, result := range page {
			key := strings.ToLower(strings.TrimSpace(keyOf(result)))
			if key != "" && seen[key] {
				continue
			}
			if key != "" {
				seen[key] = true
			}
			merged = append(merged, result)
		}
	}
	return merged
}
