// SPDX-License-Identifier: MPL-2.0
package worldstore

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ZIP64 metadata models huge expanded sizes without allocating or writing bodies.
func TestStreamingRestorePreflightsTotalAndHeaderSizes(t *testing.T) {
	for _, test := range []struct {
		name             string
		count            int
		header, manifest int64
		want             string
	}{
		{"expanded total", 17, 4 << 30, 4 << 30, "exceeds streaming limit"},
		{"header mismatch", 1, 2, 1, "size mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "metadata.zip")
			file, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			writer := zip.NewWriter(file)
			manifest := archiveManifest{Version: 1}
			for i := 0; i < test.count; i++ {
				name := fmt.Sprintf("file-%d", i)
				header := &zip.FileHeader{Name: name, Method: zip.Store, UncompressedSize64: uint64(test.header)}
				header.SetMode(0600)
				if _, err := writer.CreateRaw(header); err != nil {
					t.Fatal(err)
				}
				manifest.Files = append(manifest.Files, Entry{Path: name, Size: test.manifest, SHA256: strings.Repeat("0", 64)})
			}
			body, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			header := &zip.FileHeader{Name: manifestName, Method: zip.Deflate}
			header.SetMode(0600)
			out, err := writer.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := out.Write(body); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			parsed, err := openZipArchive(archive)
			if parsed != nil {
				_ = parsed.Close()
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("preflight error %v, want %s", err, test.want)
			}
		})
	}
}
