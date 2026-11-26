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
	"github.com/athNdev/carbon-panel/pkg/logger"
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

// ModuleDataMounts is the verification-side resolver used before a stopped
// export: it must report the same data-directory mounts container creation
// would use, and refuse an unverifiable configuration instead of returning an
// empty safe-looking set.
func TestModuleDataMounts(t *testing.T) {
	root := t.TempDir()
	client := &Client{log: logger.New()}
	server := &models.Server{DataPath: root}
	cfg := &config.Config{Storage: config.StorageConfig{BackupDir: t.TempDir()}}
	source := filepath.ToSlash(filepath.Join(root, "plugins"))

	tests := []struct {
		name     string
		volumes  string
		want     int
		readOnly bool
	}{
		{
			name:    "read-only server data",
			volumes: `[{"source":"` + source + `","target":"/plugins","read_only":true}]`,
			want:    1, readOnly: true,
		},
		{
			name:    "writable server data",
			volumes: `[{"source":"` + source + `","target":"/plugins","read_only":false}]`,
			want:    1, readOnly: false,
		},
		{
			name:    "mount outside server data",
			volumes: `[{"source":"` + filepath.ToSlash(filepath.Join(cfg.Storage.BackupDir, "exports")) + `","target":"/exports","read_only":false}]`,
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
			got, err := client.ModuleDataMounts(module, server, &models.ServerConfig{}, cfg, nil)
			if err != nil {
				t.Fatalf("ModuleDataMounts: %v", err)
			}
			if len(got) != test.want {
				t.Fatalf("data mounts = %+v, want %d", got, test.want)
			}
			if test.want == 1 && got[0].ReadOnly != test.readOnly {
				t.Fatalf("read-only = %v, want %v", got[0].ReadOnly, test.readOnly)
			}
		})
	}
}

func TestModuleDataMountsFailsClosed(t *testing.T) {
	root := t.TempDir()
	client := &Client{log: logger.New()}
	server := &models.Server{DataPath: root}
	cfg := &config.Config{Storage: config.StorageConfig{BackupDir: t.TempDir()}}

	for _, volumes := range []string{
		`not json`,
		`[{"source":"{{unknown.value}}/plugins","target":"/plugins","read_only":false}]`,
		`[{"source":"/etc","target":"/etc","read_only":false}]`,
	} {
		module := &models.Module{VolumeOverrides: volumes}
		if _, err := client.ModuleDataMounts(module, server, &models.ServerConfig{}, cfg, nil); err == nil {
			t.Fatalf("volume overrides %q were accepted", volumes)
		}
	}
}
