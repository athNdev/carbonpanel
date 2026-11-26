// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	models "github.com/athNdev/carbon-panel/internal/db"
)

func TestWritableModuleDataSources(t *testing.T) {
	root := t.TempDir()
	backup := t.TempDir()
	server := &models.Server{DataPath: root}
	cfg := &config.Config{Storage: config.StorageConfig{BackupDir: backup}}
	jsonPath := func(elem ...string) string {
		return filepath.ToSlash(filepath.Join(append([]string{root}, elem...)...))
	}

	tests := []struct {
		name    string
		volumes string
		want    int
	}{
		{
			name:    "writable server data",
			volumes: `[{"source":"` + jsonPath("plugins") + `","target":"/plugins","read_only":false}]`,
			want:    1,
		},
		{
			name:    "writable by default",
			volumes: `[{"source":"` + jsonPath() + `","target":"/data"}]`,
			want:    1,
		},
		{
			name:    "read-only server data",
			volumes: `[{"source":"` + jsonPath("world") + `","target":"/app/world","read_only":true}]`,
			want:    0,
		},
		{
			name:    "writable outside server data",
			volumes: `[{"source":"` + filepath.ToSlash(filepath.Join(backup, "exports")) + `","target":"/exports","read_only":false}]`,
			want:    0,
		},
		{
			name:    "named volume",
			volumes: `[{"source":"cache","target":"/cache","type":"volume","read_only":false}]`,
			want:    0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			module := &models.Module{VolumeOverrides: test.volumes}
			got, err := WritableModuleDataSources(module, server, &models.ServerConfig{}, cfg)
			if err != nil {
				t.Fatalf("WritableModuleDataSources: %v", err)
			}
			if len(got) != test.want {
				t.Fatalf("writable sources = %v, want %d", got, test.want)
			}
		})
	}
}

func TestWritableModuleDataSourcesFailsClosed(t *testing.T) {
	root := t.TempDir()
	server := &models.Server{DataPath: root}
	cfg := &config.Config{Storage: config.StorageConfig{BackupDir: t.TempDir()}}

	for _, volumes := range []string{
		`not json`,
		`[{"source":"/etc","target":"/etc","read_only":false}]`,
	} {
		module := &models.Module{VolumeOverrides: volumes}
		if _, err := WritableModuleDataSources(module, server, &models.ServerConfig{}, cfg); err == nil {
			t.Fatalf("volume overrides %q were accepted", volumes)
		}
	}
}
