// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectWorldLayoutClassifiesAndValidates(t *testing.T) {
	for _, tc := range []struct {
		name     string
		entries  []zipEntry
		wantErr  bool
		wantMsg  string
		wantFams []string
	}{
		{
			name: "named world family with vanilla dimension data stays supported",
			entries: []zipEntry{
				{name: "world/"}, {name: "world/level.dat", data: []byte("nbt")},
				{name: "world/region/r.0.0.mca", data: []byte("region")},
				{name: "world/DIM-1/region/r.0.0.mca", data: []byte("nether")},
				{name: "world/DIM1/region/r.0.0.mca", data: []byte("end")},
			},
			wantFams: []string{"world"},
		},
		{
			name: "paper style companions and root config stay supported",
			entries: []zipEntry{
				{name: "world/level.dat", data: []byte("nbt")},
				{name: "world_nether/level.dat", data: []byte("nbt")},
				{name: "world_the_end/level.dat", data: []byte("nbt")},
				{name: "server.properties", data: []byte("level-name=world")},
				{name: "config/example.conf", data: []byte("x")},
			},
			wantFams: []string{"world", "world_nether", "world_the_end"},
		},
		{
			name: "dot-prefixed marker still normalizes to the named family",
			entries: []zipEntry{
				{name: "./world/level.dat", data: []byte("nbt")},
			},
			wantFams: []string{"world"},
		},
		{
			name:    "root level.dat is refused with actionable message",
			entries: []zipEntry{{name: "level.dat", data: []byte("nbt")}},
			wantErr: true,
			wantMsg: "named world folder",
		},
		{
			name: "mixed root and named family is ambiguous",
			entries: []zipEntry{
				{name: "level.dat", data: []byte("nbt")},
				{name: "world/level.dat", data: []byte("nbt")},
			},
			wantErr:  true,
			wantMsg:  "ambiguous archive",
			wantFams: []string{"world"},
		},
		{
			name:    "no usable family is refused",
			entries: []zipEntry{{name: "mods/example.jar", data: []byte("jar")}},
			wantErr: true,
			wantMsg: "no usable level family",
		},
		{
			name:    "nested marker is not a usable family",
			entries: []zipEntry{{name: "server/world/level.dat", data: []byte("nbt")}},
			wantErr: true,
			wantMsg: "no usable level family",
		},
		{
			name:    "hidden top-level family is not usable",
			entries: []zipEntry{{name: ".world/level.dat", data: []byte("nbt")}},
			wantErr: true,
			wantMsg: "no usable level family",
		},
		{
			name:    "case variant marker is not usable on case-sensitive hosts",
			entries: []zipEntry{{name: "WORLD/LEVEL.DAT", data: []byte("nbt")}},
			wantErr: true,
			wantMsg: "no usable level family",
		},
		{
			name:    "backslash marker does not match the Linux extractor",
			entries: []zipEntry{{name: `world\level.dat`, data: []byte("nbt")}},
			wantErr: true,
			wantMsg: "no usable level family",
		},
		{
			name:    "empty marker is refused before live rename",
			entries: []zipEntry{{name: "world/level.dat", data: []byte{}}},
			wantErr: true,
			wantMsg: "level.dat is empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := writeTestZip(t, filepath.Join(t.TempDir(), "archive.zip"), tc.entries...)
			layout, err := InspectWorldLayout(archive)
			if err != nil {
				t.Fatalf("InspectWorldLayout: %v", err)
			}
			if strings.Join(layout.NamedFamilies, ",") != strings.Join(tc.wantFams, ",") {
				t.Fatalf("NamedFamilies = %v, want %v", layout.NamedFamilies, tc.wantFams)
			}
			err = layout.Validate()
			if tc.wantErr {
				if err == nil || !errors.Is(err, ErrUnsupportedWorldLayout) || !strings.Contains(err.Error(), tc.wantMsg) {
					t.Fatalf("Validate = %v, want unsupported layout with %q", err, tc.wantMsg)
				}
				if !IsUnsupportedWorldLayout(err) {
					t.Fatal("IsUnsupportedWorldLayout should recognise the error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestSelectWorldFamiliesRequiresUnambiguousTarget(t *testing.T) {
	families := []string{"world", "world_nether"}
	if got, err := SelectWorldFamilies(families, "world"); err != nil || len(got) != 2 {
		t.Fatalf("matching target = %v, %v", got, err)
	}
	if got, err := SelectWorldFamilies(families, ""); err != nil || len(got) != 2 {
		t.Fatalf("empty target = %v, %v", got, err)
	}
	if got, err := SelectWorldFamilies([]string{"MySave"}, "MySave"); err != nil || len(got) != 1 {
		t.Fatalf("matching single family = %v, %v", got, err)
	}
	if _, err := SelectWorldFamilies([]string{"MySave"}, "world"); err == nil ||
		!strings.Contains(err.Error(), `family "MySave" does not match target level "world"`) {
		t.Fatalf("mismatched target = %v, want actionable mismatch", err)
	}
	if _, err := SelectWorldFamilies([]string{"world", "other"}, "world"); err == nil ||
		!strings.Contains(err.Error(), "unrelated world families") {
		t.Fatalf("unrelated family = %v, want refusal", err)
	}
	if got, err := SelectWorldFamilies([]string{"world", "world_nether", "other"}, "world"); err == nil ||
		len(got) != 0 {
		t.Fatalf("mixed unrelated families = %v, %v, want refusal", got, err)
	}
}

func writeTestZipFromMap(t *testing.T, path string, entries map[string]string) string {
	t.Helper()
	items := make([]zipEntry, 0, len(entries))
	for name, data := range entries {
		items = append(items, zipEntry{name: name, data: []byte(data)})
	}
	return writeTestZip(t, path, items...)
}

func TestInspectRevisionArchiveRejectsNormalizedDestinationCollisions(t *testing.T) {
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "collision.zip"),
		zipEntry{name: "world/level.dat", data: []byte("nbt")},
		zipEntry{name: "world/./level.dat", data: []byte("nbt")})
	_, _, err := InspectRevisionArchive(context.Background(), archive)
	if err == nil || !strings.Contains(err.Error(), "share destination") {
		t.Fatalf("collision = %v, want shared-destination refusal", err)
	}
}

func TestInspectRevisionArchiveRejectsCaseAndAncestorCollisions(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		entries    map[string]string
	}{
		{"case folded", "share destination", map[string]string{
			"world/level.dat": "nbt", "WORLD/LEVEL.DAT": "nbt",
		}},
		{"file and parent directory", "is a file but also a parent directory", map[string]string{
			"world/level.dat": "nbt", "world/level.dat/extra": "child",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := writeTestZipFromMap(t, filepath.Join(t.TempDir(), "collision.zip"), tc.entries)
			_, _, err := InspectRevisionArchive(context.Background(), archive)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("collision = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestInspectRevisionArchiveCanonicalDirAndAncestorRules(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		entries    []zipEntry
	}{
		{
			name: "sibling between ancestor and descendant cannot hide collision",
			want: "parent directory",
			entries: []zipEntry{
				{name: "dir", data: []byte("file")},
				{name: "dir-", data: []byte("sibling")},
				{name: "dir/child", data: []byte("descendant")},
			},
		},
		{
			name: "explicit directory and regular file at same destination",
			want: "share destination",
			entries: []zipEntry{
				{name: "world/", mode: os.ModeDir},
				{name: "world", data: []byte("file")},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := writeTestZip(t, filepath.Join(t.TempDir(), "case.zip"), tc.entries...)
			_, _, err := InspectRevisionArchive(context.Background(), archive)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("collision = %v, want %q", err, tc.want)
			}
		})
	}

	t.Run("explicit and implicit directories remain supported", func(t *testing.T) {
		archive := writeTestZip(t, filepath.Join(t.TempDir(), "supported.zip"),
			zipEntry{name: "world/", mode: os.ModeDir},
			zipEntry{name: "world/level.dat", data: []byte("nbt")},
			zipEntry{name: "world/region/", mode: os.ModeDir},
			zipEntry{name: "world/region/r.0.0.mca", data: []byte("region")},
		)
		_, layout, err := InspectRevisionArchive(context.Background(), archive)
		if err != nil {
			t.Fatalf("supported layout: %v", err)
		}
		if err := layout.Validate(); err != nil {
			t.Fatalf("supported layout validate: %v", err)
		}
	})
}

func TestInspectRevisionArchiveIsBoundedAndCancelable(t *testing.T) {
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "world.zip"),
		zipEntry{name: "world/level.dat", data: []byte("nbt")})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := InspectRevisionArchive(ctx, archive); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled preflight = %v, want context.Canceled", err)
	}
}
