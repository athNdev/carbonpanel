// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package minecraft

import (
	"os"
	"path/filepath"
	"testing"
)

func writeArchive(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.zip")
	if err := os.WriteFile(path, zipBytes(t, entries), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateContentArchive(t *testing.T) {
	t.Run("resource pack with assets", func(t *testing.T) {
		path := writeArchive(t, map[string]string{
			"pack.mcmeta":          `{"pack":{"pack_format":34}}`,
			"assets/example/a.png": "",
		})
		if err := ValidateContentArchive(ContentResourcePack, path); err != nil {
			t.Fatalf("valid resource pack rejected: %v", err)
		}
	})

	t.Run("datapack is not a resource pack", func(t *testing.T) {
		path := writeArchive(t, map[string]string{
			"pack.mcmeta":      `{"pack":{"pack_format":34}}`,
			"data/minecraft/x": "",
		})
		if err := ValidateContentArchive(ContentResourcePack, path); err == nil {
			t.Fatal("datapack accepted as a resource pack")
		}
	})

	t.Run("resource pack must sit at the root", func(t *testing.T) {
		path := writeArchive(t, map[string]string{
			"wrapped/pack.mcmeta": `{}`,
			"wrapped/assets/x":    "",
		})
		if err := ValidateContentArchive(ContentResourcePack, path); err == nil {
			t.Fatal("wrapped resource pack accepted")
		}
	})

	t.Run("shader pack with shaders folder", func(t *testing.T) {
		path := writeArchive(t, map[string]string{
			"shaders/final.fsh":       "",
			"shaders.properties":      "",
			"shaders/lang/en_us.lang": "",
		})
		if err := ValidateContentArchive(ContentShaderPack, path); err != nil {
			t.Fatalf("valid shader pack rejected: %v", err)
		}
	})

	t.Run("resource pack is not a shader pack", func(t *testing.T) {
		path := writeArchive(t, map[string]string{
			"pack.mcmeta": `{}`,
			"assets/x/y":  "",
		})
		if err := ValidateContentArchive(ContentShaderPack, path); err == nil {
			t.Fatal("resource pack accepted as a shader pack")
		}
	})

	t.Run("zip traversal is rejected", func(t *testing.T) {
		path := writeArchive(t, map[string]string{
			"shaders/final.fsh": "",
			"../escape":         "",
		})
		if err := ValidateContentArchive(ContentShaderPack, path); err == nil {
			t.Fatal("archive traversal accepted")
		}
	})
}
