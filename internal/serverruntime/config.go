// SPDX-License-Identifier: MPL-2.0
package serverruntime

import (
	"context"
	"regexp"
	"strings"

	"github.com/athNdev/carbon-panel/internal/db"
)

type configStore interface {
	GetGlobalSettings(context.Context) (*db.ServerConfig, bool, error)
}

var slugSeparators = regexp.MustCompile(`[^a-z0-9]+`)

// PrepareConfig fills missing runtime provider credentials without persisting
// them. A server also enables the existing launch-time name-to-CurseForge-slug
// default. Config-only recreation passes nil to preserve its explicit source.
// Global lookup remains best effort; explicit per-server values always win.
func PrepareConfig(ctx context.Context, store configStore, server *db.Server, cfg *db.ServerConfig) {
	if cfg == nil {
		return
	}
	missingCF := cfg.CFAPIKey == nil || *cfg.CFAPIKey == ""
	missingModrinth := cfg.ModrinthToken == nil || *cfg.ModrinthToken == ""
	if (missingCF || missingModrinth) && store != nil {
		if global, _, err := store.GetGlobalSettings(ctx); err == nil && global != nil {
			if missingCF && global.CFAPIKey != nil && *global.CFAPIKey != "" {
				cfg.CFAPIKey = global.CFAPIKey
			}
			if missingModrinth && global.ModrinthToken != nil && *global.ModrinthToken != "" {
				cfg.ModrinthToken = global.ModrinthToken
			}
		}
	}
	if server != nil && server.ModLoader == db.ModLoaderAutoCurseForge && (cfg.CFSlug == nil || *cfg.CFSlug == "") && (cfg.CFPageURL == nil || *cfg.CFPageURL == "") {
		slug := strings.Trim(slugSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(server.Name)), "-"), "-")
		if slug != "" {
			cfg.CFSlug = &slug
		}
	}
}
