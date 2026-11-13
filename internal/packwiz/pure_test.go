// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package packwiz

import (
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"os"
	"path/filepath"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/logger"
)

func TestPrimaryVersionFile(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []modrinth.File
		want  modrinth.File
	}{
		{name: "empty"},
		{name: "primary", files: []modrinth.File{{URL: "u1", Filename: "a.jar", Size: 1}, {URL: "u2", Filename: "b.jar", Primary: true, Size: 2}}, want: modrinth.File{URL: "u2", Filename: "b.jar", Primary: true, Size: 2}},
		{name: "first", files: []modrinth.File{{URL: "u1", Filename: "a.jar", Size: 1}}, want: modrinth.File{URL: "u1", Filename: "a.jar", Size: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := primaryVersionFile(tc.files); (got == nil && tc.want != (modrinth.File{})) || (got != nil && *got != tc.want) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
		})
	}
}

func TestListPacks_EmptyAndMissing(t *testing.T) {
	m := NewManager(t.TempDir(), logger.New())

	listed, err := m.ListPacks()
	if err != nil || len(listed) != 0 {
		t.Errorf("empty dir = %v, %v", listed, err)
	}

	missing := NewManager(filepath.Join(t.TempDir(), "nope"), logger.New())
	listed, err = missing.ListPacks()
	if err != nil || len(listed) != 0 {
		t.Errorf("missing dir = %v, %v", listed, err)
	}

	// Plain files are skipped; unreadable pack dirs are skipped, not fatal.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "brokenpack"), 0o755); err != nil {
		t.Fatal(err)
	}
	m2 := NewManager(dir, logger.New())
	listed, err = m2.ListPacks()
	if err != nil || len(listed) != 0 {
		t.Errorf("junk dir = %v, %v", listed, err)
	}
}
