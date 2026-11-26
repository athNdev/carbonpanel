// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import "strings"

// EffectiveRuntimeTarget returns the loader and game version the Docker
// container will actually launch. The persisted ServerConfig owns the TYPE and
// VERSION environment values, so a config edit can make it differ from the
// panel's Server.MCVersion/ModLoader projection. Callers that gate content
// compatibility must use this target rather than the projection.
//
// A missing config (nil receiver) falls back to the server projection. An
// explicit empty value also falls back, while an explicit "LATEST" stays
// authoritative so compatibility checks fail closed instead of guessing.
func (c *ServerConfig) EffectiveRuntimeTarget(server *Server) (ModLoader, string) {
	loader := ModLoader("")
	game := ""
	if server != nil {
		loader = serverRuntimeLoader(server)
		game = strings.TrimSpace(server.MCVersion)
	}
	if c == nil {
		return loader, game
	}
	if c.Type != nil {
		if value := strings.TrimSpace(*c.Type); value != "" {
			loader = runtimeLoaderForType(value, server)
		}
	}
	if c.Version != nil {
		if value := strings.TrimSpace(*c.Version); value != "" {
			game = value
		}
	}
	return loader, game
}

func runtimeLoaderForType(value string, server *Server) ModLoader {
	clean := strings.ToLower(strings.TrimSpace(value))
	clean = strings.TrimPrefix(clean, "mod_loader_")
	switch ModLoader(clean) {
	case ModLoaderAutoCurseForge, ModLoaderCurseForge, ModLoaderFTBA, ModLoaderModrinth:
		// Pack-platform containers choose the real loader from the pack. The
		// server row is the panel's record of that resolved loader.
		if server != nil && server.ModpackLoader != "" {
			return server.ModpackLoader
		}
	case "sponge_vanilla":
		return ModLoaderSpongeVanilla
	}
	return ModLoader(clean)
}

// serverRuntimeLoader returns the real game loader for a server row. A modpack
// platform container type keeps the platform value in the session's TYPE, so
// the resolved loader lives in ModpackLoader.
func serverRuntimeLoader(server *Server) ModLoader {
	if server == nil {
		return ""
	}
	switch server.ModLoader {
	case ModLoaderAutoCurseForge, ModLoaderCurseForge, ModLoaderFTBA, ModLoaderModrinth:
		if server.ModpackLoader != "" {
			return server.ModpackLoader
		}
	}
	return server.ModLoader
}
