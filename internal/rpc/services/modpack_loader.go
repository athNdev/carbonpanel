// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/minecraft"
)

// isModpackPlatformLoader reports whether the loader is a modpack
// provisioning platform rather than a game loader. The container TYPE must
// stay the platform so the image fetches the pack; displays must not present
// the platform as the mod loader players run.
func isModpackPlatformLoader(l storage.ModLoader) bool {
	switch l {
	case storage.ModLoaderAutoCurseForge,
		storage.ModLoaderCurseForge,
		storage.ModLoaderFTBA,
		storage.ModLoaderModrinth:
		return true
	}
	return false
}

// resolveModpackGameLoader returns the real game loader for a server created
// from an indexed modpack: the pinned version's file loader first, then the
// pack-level loader list. Unknown or platform values are skipped, and ""
// means the loader could not be determined (callers keep the previous value).
func resolveModpackGameLoader(modpack *storage.IndexedModpack, files []*storage.IndexedModpackFile, versionID string) storage.ModLoader {
	if modpack == nil {
		return ""
	}
	if versionID != "" {
		for _, f := range files {
			if f == nil || f.ID != versionID {
				continue
			}
			if l, ok := minecraft.MatchModLoader(f.ModLoader); ok && !isModpackPlatformLoader(l) {
				return l
			}
			break
		}
	}
	var loaders []string
	if err := json.Unmarshal([]byte(modpack.ModLoaders), &loaders); err == nil {
		for _, raw := range loaders {
			if l, ok := minecraft.MatchModLoader(raw); ok && !isModpackPlatformLoader(l) {
				return l
			}
		}
	}
	return ""
}

// backfillModpackLoader resolves and persists the display loader for a
// platform-loader server created before the display loader existed.
// Best-effort: lookup failures leave the row alone and the console keeps
// showing the platform, exactly as before.
func (s *ServerService) backfillModpackLoader(ctx context.Context, server *storage.Server) {
	if server == nil || !isModpackPlatformLoader(server.ModLoader) || server.ModpackLoader != "" {
		return
	}
	cfg, err := s.store.GetServerConfig(ctx, server.ID)
	if err != nil || cfg == nil {
		return
	}
	var pack *storage.IndexedModpack
	var versionID string
	if server.ModLoader == storage.ModLoaderModrinth {
		if cfg.ModrinthModpack == nil {
			return
		}
		pack = s.findModrinthPack(ctx, strings.TrimSpace(*cfg.ModrinthModpack))
		if cfg.ModrinthVersion != nil {
			versionID = strings.TrimSpace(*cfg.ModrinthVersion)
		}
	} else {
		pack = s.findCurseForgePack(ctx, cfg)
		if cfg.CFFileID != nil {
			versionID = strings.TrimSpace(*cfg.CFFileID)
		}
	}
	if pack == nil {
		return
	}
	var files []*storage.IndexedModpackFile
	if all, err := s.store.GetIndexedModpackFiles(ctx, pack.ID); err == nil {
		files = all
	}
	loader := resolveModpackGameLoader(pack, files, versionID)
	if loader == "" {
		return
	}
	server.ModpackLoader = loader
	if err := s.store.UpdateServer(ctx, server); err != nil {
		s.log.Warn("Failed to backfill modpack loader for server %s: %v", server.ID, err)
	}
}

// findModrinthPack locates the indexed pack behind a MODRINTH_MODPACK value,
// which may be a project ID, slug, URL, or local mrpack path.
func (s *ServerService) findModrinthPack(ctx context.Context, ref string) *storage.IndexedModpack {
	if strings.HasPrefix(ref, "http") {
		if pack, err := s.store.GetModpackByWebsiteURL(ctx, ref); err == nil && pack != nil {
			return pack
		}
		return nil
	}
	if ref == "" || strings.HasSuffix(strings.ToLower(ref), ".mrpack") || strings.Contains(ref, "/") {
		// Local file path: no index entry to consult.
		return nil
	}
	if pack, err := s.store.GetIndexedModpack(ctx, "modrinth-"+ref); err == nil && pack != nil {
		return pack
	}
	if pack, err := s.store.GetModpackBySlug(ctx, ref); err == nil && pack != nil {
		return pack
	}
	return nil
}

// findCurseForgePack locates the indexed pack behind the stored CurseForge
// page URL or slug.
func (s *ServerService) findCurseForgePack(ctx context.Context, cfg *storage.ServerConfig) *storage.IndexedModpack {
	if cfg == nil {
		return nil
	}
	if cfg.CFPageURL != nil && strings.TrimSpace(*cfg.CFPageURL) != "" {
		if pack, err := s.store.GetModpackByWebsiteURL(ctx, strings.TrimSpace(*cfg.CFPageURL)); err == nil && pack != nil {
			return pack
		}
	}
	if cfg.CFSlug != nil && strings.TrimSpace(*cfg.CFSlug) != "" {
		if pack, err := s.store.GetModpackBySlug(ctx, strings.TrimSpace(*cfg.CFSlug)); err == nil && pack != nil {
			return pack
		}
	}
	return nil
}
