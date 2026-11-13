// SPDX-License-Identifier: MPL-2.0
package serverruntime

import (
	"context"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
)

func TestRuntimeConfigDefaultsPreserveOverridesAndExplicitSource(t *testing.T) {
	ctx := context.Background()
	store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: ":memory:", MaxConnections: 1}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cf, token, explicit, page, slug := "global-cf", "global-token", "server-value", "https://fixture.example/pack", "explicit-slug"
	if err := store.UpdateGlobalSettings(ctx, &db.ServerConfig{CFAPIKey: &cf, ModrinthToken: &token}); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		name                        string
		cfg                         db.ServerConfig
		server                      *db.Server
		wantCF, wantToken, wantSlug string
	}{
		{"launch defaults", db.ServerConfig{}, &db.Server{ModLoader: db.ModLoaderAutoCurseForge, Name: "  A Pack!  "}, cf, token, "a-pack"},
		{"explicit credentials", db.ServerConfig{CFAPIKey: &explicit, ModrinthToken: &explicit}, nil, explicit, explicit, ""},
		{"explicit page", db.ServerConfig{CFPageURL: &page}, &db.Server{ModLoader: db.ModLoaderAutoCurseForge, Name: "Do Not Infer"}, cf, token, ""},
		{"explicit slug", db.ServerConfig{CFSlug: &slug}, &db.Server{ModLoader: db.ModLoaderAutoCurseForge, Name: "Do Not Infer"}, cf, token, slug},
		{"config-only", db.ServerConfig{}, nil, cf, token, ""},
		{"other loader", db.ServerConfig{}, &db.Server{ModLoader: db.ModLoaderPaper, Name: "Do Not Infer"}, cf, token, ""},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			PrepareConfig(ctx, store, fixture.server, &fixture.cfg)
			value := func(p *string) string {
				if p == nil {
					return ""
				}
				return *p
			}
			if value(fixture.cfg.CFAPIKey) != fixture.wantCF || value(fixture.cfg.ModrinthToken) != fixture.wantToken || value(fixture.cfg.CFSlug) != fixture.wantSlug {
				t.Fatalf("runtime defaults changed explicit values or source")
			}
		})
	}
}
