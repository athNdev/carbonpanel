// SPDX-License-Identifier: MPL-2.0
package db

import (
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

func TestEffectiveRuntimeTargetRequiresConcreteDockerInputs(t *testing.T) {
	derive := true
	for _, tc := range []struct {
		name string
		cfg  *ServerConfig
		env  map[string]string
		want ModLoader
		game string
	}{
		{name: "missing config"},
		{name: "Docker omits empty values", cfg: &ServerConfig{Type: stringPtr(""), Version: stringPtr("")}},
		{name: "default version", cfg: &ServerConfig{Type: stringPtr("FABRIC"), Version: stringPtr("LATEST")}},
		{name: "moving snapshot", cfg: &ServerConfig{Type: stringPtr("FABRIC"), Version: stringPtr("SNAPSHOT")}},
		{name: "pack selects runtime", cfg: &ServerConfig{Type: stringPtr("MODRINTH"), Version: stringPtr("1.21.1")}},
		{name: "modrinth dynamically selects version", cfg: &ServerConfig{
			Type: stringPtr("FABRIC"), Version: stringPtr("1.21.1"), ModrinthProjects: stringPtr("fabric-api"),
			VersionFromModrinthProjects: &derive,
		}},
		{name: "override dynamically selects version", cfg: &ServerConfig{
			Type: stringPtr("FABRIC"), Version: stringPtr("1.21.1"), ModrinthProjects: stringPtr("fabric-api"),
		}, env: map[string]string{"VERSION_FROM_MODRINTH_PROJECTS": "ON"}},
		{name: "Docker platform override", cfg: &ServerConfig{Type: stringPtr("FABRIC"), Version: stringPtr("1.21.1")},
			env: map[string]string{"MODPACK_PLATFORM": "MODRINTH"}},
		{name: "Docker environment from file", cfg: &ServerConfig{Type: stringPtr("FABRIC"), Version: stringPtr("1.21.1")},
			env: map[string]string{"LOAD_ENV_FROM_FILE": "/data/server.env"}},
		{name: "Docker launcher override", cfg: &ServerConfig{Type: stringPtr("FABRIC"), Version: stringPtr("1.21.1")},
			env: map[string]string{"FABRIC_LAUNCHER": "/data/launcher.jar"}},
		{name: "custom server command", cfg: &ServerConfig{
			Type: stringPtr("FABRIC"), Version: stringPtr("1.21.1"), CustomJarExec: stringPtr("-jar custom.jar"),
		}},
		{name: "legacy Docker TYPE", cfg: &ServerConfig{Type: stringPtr(" MOD_LOADER_FABRIC "), Version: stringPtr("1.7.9")},
			want: ModLoaderFabric, game: "1.7.9"},
		{name: "pinned snapshot", cfg: &ServerConfig{Type: stringPtr("QUILT"), Version: stringPtr("25w35a")},
			want: ModLoaderQuilt, game: "25w35a"},
		{name: "explicit overrides", cfg: &ServerConfig{Type: stringPtr("FABRIC"), Version: stringPtr("1.21.1")},
			env: map[string]string{"TYPE": "NEOFORGE", "VERSION": "26.1-rc-1"}, want: ModLoaderNeoForge, game: "26.1-rc-1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := &Server{ModLoader: ModLoaderFabric, MCVersion: "1.21.1", ModpackLoader: ModLoaderFabric,
				DockerOverrides: &v1.DockerOverrides{Environment: tc.env}}
			loader, game, err := tc.cfg.EffectiveRuntimeTarget(server)
			if tc.want == "" {
				require.ErrorIs(t, err, ErrReviewedModRuntimeUnknown)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, loader)
			require.Equal(t, tc.game, game)
		})
	}
}
