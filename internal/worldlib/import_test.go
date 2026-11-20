// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/files"
)

func newImportTestStore(t *testing.T) *db.Store {
	t.Helper()
	cfg := &config.Config{
		Database: config.DatabaseConfig{
			Path:        filepath.Join(t.TempDir(), "worldlib.db"),
			AutoMigrate: true,
		},
		Docker: config.DockerConfig{Host: "unix:///var/run/docker.sock"},
	}
	store, err := db.NewSQLiteStore(cfg)
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newTestImporter(t *testing.T) (*LocalImporter, string, *db.Store) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "library")
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatalf("create library root: %v", err)
	}
	store := newImportTestStore(t)
	return NewLocalImporter(store, root), root, store
}

func checksumOf(t *testing.T, path string) string {
	t.Helper()
	sum, err := files.SHA256File(path)
	if err != nil {
		t.Fatalf("hash fixture: %v", err)
	}
	return sum
}

func uploadRequest(path, checksum, requestID string) ImportRequest {
	return ImportRequest{
		Source:             SourceUpload,
		URL:                path,
		DisplayName:        "Audit Test World",
		SourcePlatform:     "manual",
		LicenseAttestation: "Operator confirms redistribution rights (CC0 source).",
		SHA256Checksum:     checksum,
		RequestID:          requestID,
		Operator:           "operator-1",
		NodeID:             "node-local",
	}
}

func countRows(t *testing.T, store *db.Store, model any) int64 {
	t.Helper()
	var n int64
	if err := store.DB().Model(model).Count(&n).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	return n
}

func assertLibraryOnlyRevisionArchive(t *testing.T, root, revisionID string) {
	t.Helper()
	top, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(top) != 1 || top[0].Name() != revisionID {
		t.Fatalf("library root entries = %v, want only %q", top, revisionID)
	}
	inner, err := os.ReadDir(filepath.Join(root, revisionID))
	if err != nil {
		t.Fatal(err)
	}
	if len(inner) != 1 || inner[0].Name() != "world.zip" {
		t.Fatalf("revision dir entries = %v, want only world.zip (no extraction)", inner)
	}
	info, err := inner[0].Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0111 != 0 {
		t.Fatalf("stored archive has executable bits: %v", info.Mode())
	}
}

func TestImportUploadStoresImmutableRevisionWithProvenance(t *testing.T) {
	importer, root, store := newTestImporter(t)
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "world.zip"),
		zipEntry{name: "world/"},
		zipEntry{name: "world/level.dat", data: []byte("nbt")},
	)
	sum := checksumOf(t, archive)
	req := uploadRequest(archive, sum, "req-happy")

	result, err := importer.Import(context.Background(), req)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if result.Entry == nil || result.Revision == nil || result.Job == nil {
		t.Fatalf("incomplete result: %+v", result)
	}
	if result.Revision.ID != RevisionID(sum) || result.Revision.SHA256 != sum {
		t.Fatalf("revision identity mismatch: %+v", result.Revision)
	}
	if result.Revision.SizeBytes != int64(len(readFile(t, archive))) {
		t.Fatalf("SizeBytes = %d, want archive size %d", result.Revision.SizeBytes, len(readFile(t, archive)))
	}
	if result.Revision.EntryCount != 2 {
		t.Fatalf("EntryCount = %d, want 2", result.Revision.EntryCount)
	}
	if result.Revision.StoragePath != filepath.ToSlash(filepath.Join(result.Revision.ID, "world.zip")) {
		t.Fatalf("StoragePath = %q", result.Revision.StoragePath)
	}
	if result.Entry.CurrentRevisionID != result.Revision.ID {
		t.Fatalf("entry does not point at its revision: %+v", result.Entry)
	}
	if result.Job.Phase != jobComplete || result.Job.ResultRevisionID != result.Revision.ID {
		t.Fatalf("job not complete: %+v", result.Job)
	}

	prov, err := db.DecodeWorldProvenance(result.Revision.Provenance)
	if err != nil {
		t.Fatalf("decode provenance: %v", err)
	}
	if prov.Platform != "manual" || prov.Operator != "operator-1" || prov.Creator != "operator-1" {
		t.Fatalf("provenance identity mismatch: %+v", prov)
	}
	if prov.License != req.LicenseAttestation || result.Revision.LicenseAttestation != req.LicenseAttestation {
		t.Fatalf("license attestation not recorded: prov=%+v revision=%q", prov, result.Revision.LicenseAttestation)
	}
	if prov.SHA256 != sum || prov.URL != "upload://world_zip" {
		t.Fatalf("provenance sha/url mismatch: %+v", prov)
	}
	if _, err := time.Parse(time.RFC3339, prov.DownloadedAt); err != nil {
		t.Fatalf("DownloadedAt = %q: %v", prov.DownloadedAt, err)
	}

	if countRows(t, store, &db.WorldRevision{}) != 1 || countRows(t, store, &db.WorldEntry{}) != 1 {
		t.Fatal("expected exactly one revision and entry")
	}
	assertLibraryOnlyRevisionArchive(t, root, result.Revision.ID)
}

// TestImportRefusesFlatRootLayout proves an unsupported flat archive is refused
// at import with an actionable message and never becomes a stored revision.
func TestImportRefusesFlatRootLayout(t *testing.T) {
	importer, _, store := newTestImporter(t)
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "flat.zip"),
		zipEntry{name: "level.dat", data: []byte("nbt")},
		zipEntry{name: "region/r.0.0.mca", data: []byte("region")},
	)
	result, err := importer.Import(context.Background(), uploadRequest(archive, checksumOf(t, archive), "req-flat-layout"))
	if err == nil || !errors.Is(err, ErrUnsupportedWorldLayout) || !strings.Contains(err.Error(), "named world folder") {
		t.Fatalf("Import flat layout = %v, want actionable unsupported-layout error", err)
	}
	if result == nil || result.Revision != nil || result.Entry != nil {
		t.Fatalf("flat archive produced a revision/entry: %+v", result)
	}
	if countRows(t, store, &db.WorldRevision{}) != 0 || countRows(t, store, &db.WorldEntry{}) != 0 {
		t.Fatal("flat archive must not create revision or entry rows")
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestImportChecksumMismatchRefusesRevision(t *testing.T) {
	importer, root, store := newTestImporter(t)
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "world.zip"),
		zipEntry{name: "world/level.dat", data: []byte("nbt")},
	)
	req := uploadRequest(archive, strings.Repeat("0", 64), "req-bad-sum")

	result, err := importer.Import(context.Background(), req)
	if err == nil || !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("expected ErrChecksumMismatch, got %v", err)
	}
	if result == nil || result.Job == nil || result.Job.Phase != jobFailed {
		t.Fatalf("job not marked failed: %+v", result)
	}
	if result.Revision != nil || result.Entry != nil {
		t.Fatalf("mismatch produced rows: %+v", result)
	}
	if countRows(t, store, &db.WorldRevision{}) != 0 || countRows(t, store, &db.WorldEntry{}) != 0 {
		t.Fatal("checksum mismatch left a revision or entry row")
	}
	if countRows(t, store, &db.WorldImportJob{}) != 1 {
		t.Fatal("expected one failed job row")
	}
	stored, err := store.GetWorldImportJob(context.Background(), result.Job.ID)
	if err != nil || stored.Phase != jobFailed {
		t.Fatalf("persisted job = %+v, err = %v", stored, err)
	}
	top, err := os.ReadDir(root)
	if err != nil || len(top) != 0 {
		t.Fatalf("library root not intact: %v, err = %v", top, err)
	}
}

func TestImportCancelledContextCreatesNothing(t *testing.T) {
	importer, root, store := newTestImporter(t)
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "world.zip"),
		zipEntry{name: "world/level.dat", data: []byte("nbt")},
	)
	req := uploadRequest(archive, checksumOf(t, archive), "req-cancelled")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := importer.Import(ctx, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	for _, model := range []any{&db.WorldRevision{}, &db.WorldEntry{}, &db.WorldImportJob{}} {
		if n := countRows(t, store, model); n != 0 {
			t.Fatalf("cancelled import left %d rows of %T", n, model)
		}
	}
	top, err := os.ReadDir(root)
	if err != nil || len(top) != 0 {
		t.Fatalf("cancelled import modified library root: %v, err = %v", top, err)
	}
}

func TestImportRequestIDIsIdempotent(t *testing.T) {
	importer, root, store := newTestImporter(t)
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "world.zip"),
		zipEntry{name: "world/level.dat", data: []byte("nbt")},
	)
	req := uploadRequest(archive, checksumOf(t, archive), "req-replay")

	first, err := importer.Import(context.Background(), req)
	if err != nil {
		t.Fatalf("first Import: %v", err)
	}
	second, err := importer.Import(context.Background(), req)
	if err != nil {
		t.Fatalf("replayed Import: %v", err)
	}
	if first.Revision.ID != second.Revision.ID || first.Entry.ID != second.Entry.ID {
		t.Fatalf("replay produced different rows: %+v / %+v", first, second)
	}
	if second.Job.ID != first.Job.ID {
		t.Fatalf("replay produced a second job: %q vs %q", first.Job.ID, second.Job.ID)
	}
	if n := countRows(t, store, &db.WorldRevision{}); n != 1 {
		t.Fatalf("revision rows = %d, want 1", n)
	}
	if n := countRows(t, store, &db.WorldImportJob{}); n != 1 {
		t.Fatalf("job rows = %d, want 1", n)
	}
	assertLibraryOnlyRevisionArchive(t, root, first.Revision.ID)
}

func TestImportRejectsMaliciousArchives(t *testing.T) {
	level3 := makeZipBytes(t, zipEntry{name: "deep.txt", data: []byte("x")})
	level2 := makeZipBytes(t, zipEntry{name: "third.zip", data: level3})
	nestedBomb := makeZipBytes(t,
		zipEntry{name: "world/level.dat", data: []byte("ok")},
		zipEntry{name: "world/second.zip", data: level2},
	)
	// Header-level oversized fixture: 17 entries each declaring 4 GiB-1,
	// below the per-file cap but above the 64 GiB aggregate limit.
	bigEntries := make([]zipEntry, 17)
	for i := range bigEntries {
		bigEntries[i] = zipEntry{name: "world/big" + string(rune('a'+i)) + ".dat"}
	}
	oversized := makeZipBytes(t, bigEntries...)
	for i := range bigEntries {
		oversized = patchZipHeader(t, oversized, bigEntries[i].name, uint64(1<<32-1), 0, true, false)
	}

	cases := []struct {
		name    string
		entries []zipEntry
		raw     []byte
	}{
		{"traversal", []zipEntry{
			{name: "world/level.dat", data: []byte("ok")},
			{name: "../escape.txt", data: []byte("pwned")},
		}, nil},
		{"symlink", []zipEntry{
			{name: "world/level.dat", data: []byte("ok")},
			{name: "world/link", mode: os.ModeSymlink | 0777, data: []byte("../../etc/passwd")},
		}, nil},
		{"nested-bomb", nil, nestedBomb},
		{"oversized", nil, oversized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			importer, root, store := newTestImporter(t)
			path := filepath.Join(t.TempDir(), "malicious.zip")
			if tc.raw != nil {
				writeRawZip(t, path, tc.raw)
			} else {
				writeTestZip(t, path, tc.entries...)
			}
			req := uploadRequest(path, checksumOf(t, path), "req-"+tc.name)

			result, err := importer.ImportLocal(context.Background(), req, path)
			if err == nil || !errors.Is(err, ErrArchiveRejected) {
				t.Fatalf("expected ErrArchiveRejected, got %v", err)
			}
			if result == nil || result.Job == nil || result.Job.Phase != jobFailed {
				t.Fatalf("job not marked failed: %+v", result)
			}
			if countRows(t, store, &db.WorldRevision{}) != 0 || countRows(t, store, &db.WorldEntry{}) != 0 {
				t.Fatal("malicious archive produced a revision or entry")
			}
			top, err := os.ReadDir(root)
			if err != nil || len(top) != 0 {
				t.Fatalf("library root not intact: %v, err = %v", top, err)
			}
		})
	}
}

func TestImportStoresExecutablePayloadWithoutInstallingIt(t *testing.T) {
	importer, root, _ := newTestImporter(t)
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "modded.zip"),
		zipEntry{name: "world/level.dat", data: []byte("nbt")},
		zipEntry{name: "world/mods/example.jar", data: []byte("opaque-jar-bytes")},
		zipEntry{name: "world/plugins/config.yml", data: []byte("name: demo")},
	)
	report, err := InspectArchive(archive)
	if err != nil || !report.HasExecutablePayload {
		t.Fatalf("executable payload not reported: report=%+v err=%v", report, err)
	}
	req := uploadRequest(archive, checksumOf(t, archive), "req-executable")

	result, err := importer.Import(context.Background(), req)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(root, result.Revision.ID, "world.zip"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stored, []byte("opaque-jar-bytes")) {
		t.Fatal("stored archive lost the executable payload bytes")
	}
	assertLibraryOnlyRevisionArchive(t, root, result.Revision.ID)
}

func TestImportFromURL(t *testing.T) {
	// The production SSRF guard refuses loopback; the repo's existing test
	// escape hatch permits an httptest server for this streaming seam only.
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	archive := makeZipBytes(t, zipEntry{name: "world/level.dat", data: []byte("remote-nbt")})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/world.zip" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	importer, root, _ := newTestImporter(t)
	importer.client = server.Client() // test-only injection; the URL is loopback.
	sum := checksumOf(t, writeRawZip(t, filepath.Join(t.TempDir(), "local-copy.zip"), archive))
	req := uploadRequest("", sum, "req-url")
	req.Source = SourceURL
	req.URL = server.URL + "/world.zip"

	result, err := importer.Import(context.Background(), req)
	if err != nil {
		t.Fatalf("Import from URL: %v", err)
	}
	prov, err := db.DecodeWorldProvenance(result.Revision.Provenance)
	if err != nil {
		t.Fatal(err)
	}
	if prov.URL != server.URL+"/world.zip" {
		t.Fatalf("canonical URL = %q, want %q", prov.URL, server.URL+"/world.zip")
	}
	if result.Revision.SHA256 != sum {
		t.Fatalf("revision sha = %q, want %q", result.Revision.SHA256, sum)
	}
	assertLibraryOnlyRevisionArchive(t, root, result.Revision.ID)
}

func TestImportRefusesPrivateSourceURL(t *testing.T) {
	importer, _, store := newTestImporter(t)
	req := uploadRequest("", strings.Repeat("1", 64), "req-private")
	req.Source = SourceURL
	req.URL = "http://127.0.0.1:65534/world.zip"

	if _, err := importer.Import(context.Background(), req); err == nil {
		t.Fatal("expected private/loopback URL to be refused")
	}
	if n := countRows(t, store, &db.WorldImportJob{}); n != 0 {
		t.Fatalf("refused URL created %d job rows", n)
	}
}

func TestImportRefusesUnsupportedSourceAndMissingLicense(t *testing.T) {
	archive := writeTestZip(t, filepath.Join(t.TempDir(), "world.zip"),
		zipEntry{name: "world/level.dat", data: []byte("nbt")},
	)
	sum := checksumOf(t, archive)

	t.Run("unsupported source", func(t *testing.T) {
		importer, _, _ := newTestImporter(t)
		req := uploadRequest(archive, sum, "req-unsupported")
		req.Source = "backup"
		_, err := importer.Import(context.Background(), req)
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("expected ErrUnsupported, got %v", err)
		}
	})
	t.Run("missing license", func(t *testing.T) {
		importer, _, store := newTestImporter(t)
		req := uploadRequest(archive, sum, "req-no-license")
		req.LicenseAttestation = ""
		_, err := importer.Import(context.Background(), req)
		if err == nil {
			t.Fatal("expected empty license attestation to be refused")
		}
		if n := countRows(t, store, &db.WorldImportJob{}); n != 0 {
			t.Fatalf("missing license created %d job rows", n)
		}
	})
}
