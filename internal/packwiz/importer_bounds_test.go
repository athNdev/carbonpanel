// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package packwiz

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

func smallImportLimits() files.ExtractLimits {
	return files.ExtractLimits{
		MaxBytes:     4 << 20,
		MaxFileBytes: 1 << 20,
		MaxEntries:   100,
	}
}

const harmlessMRIndex = `{"formatVersion":1,"game":"minecraft","name":"p","versionId":"1.0.0","dependencies":{"minecraft":"1.20.1"},"files":[]}`

// TestImportMrpackUnsafeArchiveRegression covers traversal, symlink, oversized,
// aggregate bomb and downloads[] URL validation against the real import seam.
func TestImportMrpackUnsafeArchiveRegression(t *testing.T) {
	makeManager := func(t *testing.T) (*Manager, string) {
		t.Helper()
		base := t.TempDir()
		root := filepath.Join(base, "packs")
		return NewManager(root, logger.New(), ManagerOptions{ImportLimits: smallImportLimits()}), base
	}

	t.Run("traversal is rejected explicitly and writes nothing", func(t *testing.T) {
		m, base := makeManager(t)
		r := zipWith(t, map[string]string{
			"modrinth.index.json":           harmlessMRIndex,
			"overrides/../../escape-mr.txt": "pwned",
		})
		_, err := m.ImportMrpack(r, int64(r.Len()), ImportOptions{Name: "p"})
		if err == nil {
			t.Fatal("expected traversal import to be rejected")
		}
		if _, statErr := os.Stat(filepath.Join(base, "escape-mr.txt")); !os.IsNotExist(statErr) {
			t.Fatalf(" traversal escape was written: %v", statErr)
		}
	})

	t.Run("symlink entry is rejected", func(t *testing.T) {
		m, _ := makeManager(t)
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		idx, _ := zw.Create("modrinth.index.json")
		_, _ = idx.Write([]byte(harmlessMRIndex))
		h := &zip.FileHeader{Name: "overrides/link.txt"}
		h.SetMode(os.ModeSymlink | 0777)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte("/etc/passwd"))
		_ = zw.Close()
		r := bytes.NewReader(buf.Bytes())
		if _, err := m.ImportMrpack(r, int64(r.Len()), ImportOptions{Name: "p"}); err == nil {
			t.Fatal("expected symlink archive entry to be rejected")
		}
	})

	t.Run("oversized entry is rejected", func(t *testing.T) {
		m, _ := makeManager(t)
		r := zipWith(t, map[string]string{
			"modrinth.index.json": harmlessMRIndex,
			"overrides/big.bin":   strings.Repeat("A", (1<<20)+16),
		})
		if _, err := m.ImportMrpack(r, int64(r.Len()), ImportOptions{Name: "p"}); err == nil {
			t.Fatal("expected oversized archive entry to be rejected")
		}
	})

	t.Run("aggregate bomb is rejected", func(t *testing.T) {
		m, _ := makeManager(t)
		r := zipWith(t, map[string]string{
			"modrinth.index.json": harmlessMRIndex,
			"overrides/a.bin":     strings.Repeat("A", 3<<20),
			"overrides/b.bin":     strings.Repeat("B", 3<<20),
		})
		if _, err := m.ImportMrpack(r, int64(r.Len()), ImportOptions{Name: "p"}); err == nil {
			t.Fatal("expected aggregate extraction limit to be rejected")
		}
	})

	t.Run("malicious downloads URL is rejected at import", func(t *testing.T) {
		for _, bad := range []string{
			"file:///etc/passwd",
			"http://127.0.0.1:8080/mod.jar",
			"http://192.168.1.5/mod.jar",
			"https://user:secret@example.test/mod.jar",
			"ftp://example.test/mod.jar",
		} {
			t.Run(bad, func(t *testing.T) {
				m, _ := makeManager(t)
				idx := strings.Replace(harmlessMRIndex, `"files":[]`,
					`"files":[{"path":"mods/x.jar","hashes":{"sha256":"abc"},"downloads":["`+bad+`"],"fileSize":1}]`, 1)
				r := zipWith(t, map[string]string{"modrinth.index.json": idx})
				if _, err := m.ImportMrpack(r, int64(r.Len()), ImportOptions{Name: "p"}); err == nil {
					t.Fatalf("expected malicious downloads URL %q to be rejected", bad)
				}
			})
		}
	})

	t.Run("malformed downloads URL does not reflect credentials", func(t *testing.T) {
		m, _ := makeManager(t)
		secret := "import-secret-token"
		bad := "https://alice:" + secret + "@example.test/mod%zz.jar"
		idx := strings.Replace(harmlessMRIndex, `"files":[]`,
			`"files":[{"path":"mods/x.jar","hashes":{"sha256":"abc"},"downloads":["`+bad+`"],"fileSize":1}]`, 1)
		r := zipWith(t, map[string]string{"modrinth.index.json": idx})
		_, err := m.ImportMrpack(r, int64(r.Len()), ImportOptions{Name: "p"})
		if err == nil {
			t.Fatal("malformed credentialed URL must be rejected")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "alice") {
			t.Fatalf("import error reflected a credential: %v", err)
		}
	})

	t.Run("curseforge traversal is rejected", func(t *testing.T) {
		m, _ := makeManager(t)
		const manifest = `{"manifestType":"minecraftModpack","manifestVersion":1,"name":"p","version":"1.0.0","overrides":"overrides","minecraft":{"version":"1.20.1","modLoaders":[]},"files":[]}`
		r := zipWith(t, map[string]string{
			"manifest.json":                  manifest,
			"overrides/../../escape-cf.txt": "pwned",
		})
		if _, err := m.ImportCurseForge(r, int64(r.Len()), ImportOptions{Name: "p"}); err == nil {
			t.Fatal("curseforge traversal must be rejected")
		}
	})

	t.Run("native packwiz traversal is rejected", func(t *testing.T) {
		m, _ := makeManager(t)
		r := zipWith(t, map[string]string{
			"pack.toml":         "name = \"p\"\n",
			"../../escape-pw.txt": "pwned",
		})
		if _, err := m.ImportPackwizZip(r, int64(r.Len()), ImportOptions{Name: "p"}); err == nil {
			t.Fatal("native packwiz traversal must be rejected")
		}
	})

	t.Run("legitimate large pack still imports", func(t *testing.T) {
		m, _ := makeManager(t)
		idx := strings.Replace(harmlessMRIndex, `"files":[]`,
			`"files":[{"path":"mods/ok.jar","hashes":{"sha256":"abc"},"downloads":["https://cdn.example.test/mod.jar"],"fileSize":1}]`, 1)
		r := zipWith(t, map[string]string{
			"modrinth.index.json":    idx,
			"overrides/config/x.txt": strings.Repeat("A", 900<<10),
		})
		pack, err := m.ImportMrpack(r, int64(r.Len()), ImportOptions{Name: "p"})
		if err != nil {
			t.Fatalf("legitimate import rejected: %v", err)
		}
		if len(pack.Mods) != 1 {
			t.Fatalf("expected 1 mod, got %d", len(pack.Mods))
		}
	})
}
