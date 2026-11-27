// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type zipEntry struct {
	name string
	data []byte
	mode os.FileMode
}

func TestResolveRevisionArchiveFormats(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	library := filepath.Join("data", ".world-library")
	canonical := filepath.Join(library, "rev_imported", "world.zip")
	legacy := filepath.Join(library, "rev_generated.zip")
	if err := os.MkdirAll(filepath.Dir(canonical), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{canonical, legacy, "outside.zip"} {
		if err := os.WriteFile(name, []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct{ stored, want string }{
		{"rev_imported/world.zip", canonical},
		{legacy, legacy},
		{filepath.Join(root, legacy), filepath.Join(root, legacy)},
	} {
		if got, err := ResolveRevisionArchive(library, tc.stored); err != nil || got != tc.want {
			t.Errorf("resolve %q = %q, %v; want %q", tc.stored, got, err, tc.want)
		}
	}
	for _, invalid := range []string{
		"", "../outside.zip", filepath.Join(root, "outside.zip"),
		"missing.zip", "rev_imported",
	} {
		if _, err := ResolveRevisionArchive(library, invalid); !errors.Is(err, ErrArchiveRejected) {
			t.Errorf("resolve %q = %v; want ErrArchiveRejected", invalid, err)
		}
	}
}

// makeZipBytes builds a real ZIP archive in memory so nested fixtures can be
// embedded as entries without touching the filesystem.
func makeZipBytes(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		hdr := &zip.FileHeader{Name: e.name, Method: zip.Store}
		if e.mode != 0 {
			hdr.SetMode(e.mode)
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", e.name, err)
		}
		if _, err := w.Write(e.data); err != nil {
			t.Fatalf("write zip entry %q: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// writeTestZip writes a real ZIP fixture to path and returns the path.
func writeTestZip(t *testing.T, path string, entries ...zipEntry) string {
	t.Helper()
	if err := os.WriteFile(path, makeZipBytes(t, entries...), 0644); err != nil {
		t.Fatalf("write zip fixture: %v", err)
	}
	return path
}

// patchZipHeader changes declared size and/or the encryption flag of one
// entry in both the local and central headers. It lets a tiny fixture exercise
// header-level policy limits without allocating multi-gigabyte files.
func patchZipHeader(t *testing.T, data []byte, name string, declared uint64, flags uint16, patchDeclared, patchFlags bool) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	target := []byte(name)
	for i := 0; i+30 <= len(out); i++ {
		if binary.LittleEndian.Uint32(out[i:i+4]) != 0x04034b50 {
			continue
		}
		nlen := int(binary.LittleEndian.Uint16(out[i+26 : i+28]))
		if i+30+nlen > len(out) || !bytes.Equal(out[i+30:i+30+nlen], target) {
			continue
		}
		if patchFlags {
			binary.LittleEndian.PutUint16(out[i+6:i+8], flags)
		}
		if patchDeclared {
			binary.LittleEndian.PutUint32(out[i+22:i+26], uint32(declared))
		}
	}
	for i := 0; i+46 <= len(out); i++ {
		if binary.LittleEndian.Uint32(out[i:i+4]) != 0x02014b50 {
			continue
		}
		nlen := int(binary.LittleEndian.Uint16(out[i+28 : i+30]))
		if i+46+nlen > len(out) || !bytes.Equal(out[i+46:i+46+nlen], target) {
			continue
		}
		if patchFlags {
			binary.LittleEndian.PutUint16(out[i+8:i+10], flags)
		}
		if patchDeclared {
			binary.LittleEndian.PutUint32(out[i+24:i+28], uint32(declared))
		}
	}
	return out
}

func writeRawZip(t *testing.T, path string, data []byte) string {
	t.Helper()
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("write raw zip fixture: %v", err)
	}
	return path
}

func assertRejected(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected ErrArchiveRejected, got nil")
	}
	if !errors.Is(err, ErrArchiveRejected) {
		t.Fatalf("expected ErrArchiveRejected, got %v", err)
	}
}

func TestInspectArchivePreservesCancellationDuringEntryCopy(t *testing.T) {
	for _, name := range []string{"world/level.dat", "world/companion.zip"} {
		t.Run(name, func(t *testing.T) {
			data := makeZipBytes(t, zipEntry{name: name, data: []byte("payload")})
			archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			// Opening the entry happens after the inspection loop checks
			// ctx.Err, just before the bounded copy starts reading.
			archive.RegisterDecompressor(zip.Store, func(r io.Reader) io.ReadCloser {
				cancel()
				return io.NopCloser(r)
			})
			err = inspectZipCtx(ctx, archive, 1, &ArchiveReport{})
			if !errors.Is(err, context.Canceled) || errors.Is(err, ErrArchiveRejected) {
				t.Fatalf("inspect canceled entry = %v, want cancellation", err)
			}
		})
	}
}

func TestInspectArchiveCleanWorld(t *testing.T) {
	path := writeTestZip(t, filepath.Join(t.TempDir(), "world.zip"),
		zipEntry{name: "world/"},
		zipEntry{name: "world/level.dat", data: []byte("nbt-bytes")},
		zipEntry{name: "world/region/r.0.0.mca", data: []byte("region")},
	)
	report, err := InspectArchive(path)
	if err != nil {
		t.Fatalf("InspectArchive: %v", err)
	}
	if report.EntryCount != 3 {
		t.Fatalf("EntryCount = %d, want 3", report.EntryCount)
	}
	if report.SizeBytes != int64(len("nbt-bytes")+len("region")) {
		t.Fatalf("SizeBytes = %d, want %d", report.SizeBytes, len("nbt-bytes")+len("region"))
	}
	if report.HasExecutablePayload || len(report.Warnings) != 0 {
		t.Fatalf("unexpected executable payload: %+v", report)
	}
}

func TestInspectArchiveRejectsTraversal(t *testing.T) {
	names := []string{
		"../evil.txt",
		"/absolute/evil.txt",
		"C:/evil.txt",
		`..\evil.txt`,
		"world/../../evil.txt",
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			path := writeTestZip(t, filepath.Join(t.TempDir(), "evil.zip"),
				zipEntry{name: "world/level.dat", data: []byte("ok")},
				zipEntry{name: name, data: []byte("pwned")},
			)
			if _, err := InspectArchive(path); err == nil || !errors.Is(err, ErrArchiveRejected) {
				t.Fatalf("expected ErrArchiveRejected for %q, got %v", name, err)
			}
		})
	}
}

func TestInspectArchiveRejectsSymlinkAndSpecialEntries(t *testing.T) {
	path := writeTestZip(t, filepath.Join(t.TempDir(), "symlink.zip"),
		zipEntry{name: "world/level.dat", data: []byte("ok")},
		zipEntry{name: "world/link", mode: os.ModeSymlink | 0777, data: []byte("../../etc/passwd")},
	)
	assertRejected(t, func() error { _, err := InspectArchive(path); return err }())

	device := writeTestZip(t, filepath.Join(t.TempDir(), "device.zip"),
		zipEntry{name: "world/null", mode: os.ModeDevice | 0666},
	)
	assertRejected(t, func() error { _, err := InspectArchive(device); return err }())
}

func TestInspectArchiveRejectsEncryptedEntry(t *testing.T) {
	data := makeZipBytes(t, zipEntry{name: "world/level.dat", data: []byte("secret")})
	patched := patchZipHeader(t, data, "world/level.dat", 0, 0x1, false, true)
	path := writeRawZip(t, filepath.Join(t.TempDir(), "encrypted.zip"), patched)
	assertRejected(t, func() error { _, err := InspectArchive(path); return err }())
}

func TestInspectArchiveRejectsOversizedDeclaredEntry(t *testing.T) {
	// A real 4 GiB entry is infeasible in a unit test, so a header-level
	// fixture sets UncompressedSize64 directly on a synthetic reader.
	declared := uint64(maxEntryUncompressed) + 1
	zr := &zip.Reader{File: []*zip.File{{
		FileHeader: zip.FileHeader{Name: "world/huge.dat", UncompressedSize64: declared},
	}}}
	assertRejected(t, inspectZip(zr, 1, &ArchiveReport{}))
}

func TestInspectArchiveRejectsDecompressionTotal(t *testing.T) {
	entries := make([]zipEntry, 0, 17)
	big := uint64(1<<32 - 1) // 4 GiB - 1, below the per-file cap.
	for i := 0; i < 17; i++ {
		entries = append(entries, zipEntry{name: "world/big" + string(rune('a'+i)) + ".dat"})
	}
	data := makeZipBytes(t, entries...)
	for i := 0; i < 17; i++ {
		data = patchZipHeader(t, data, entries[i].name, big, 0, true, false)
	}
	path := writeRawZip(t, filepath.Join(t.TempDir(), "bomb.zip"), data)
	assertRejected(t, func() error { _, err := InspectArchive(path); return err }())
}

func TestInspectArchiveRejectsTooManyEntries(t *testing.T) {
	files := make([]*zip.File, maxArchiveEntries+1)
	for i := range files {
		files[i] = &zip.File{FileHeader: zip.FileHeader{Name: "world/f"}}
	}
	zr := &zip.Reader{File: files}
	assertRejected(t, inspectZip(zr, 1, &ArchiveReport{}))
}

func TestInspectArchiveRejectsOversizedManifest(t *testing.T) {
	data := makeZipBytes(t, zipEntry{name: "world/pack.mcmeta", data: []byte("{}")})
	patched := patchZipHeader(t, data, "world/pack.mcmeta", uint64(maxManifestBytes)+1, 0, true, false)
	path := writeRawZip(t, filepath.Join(t.TempDir(), "manifest.zip"), patched)
	assertRejected(t, func() error { _, err := InspectArchive(path); return err }())
}

func TestInspectArchiveRejectsNestedArchiveDepth(t *testing.T) {
	level3 := makeZipBytes(t, zipEntry{name: "deep.txt", data: []byte("x")})
	level2 := makeZipBytes(t, zipEntry{name: "third.zip", data: level3})
	level1 := makeZipBytes(t,
		zipEntry{name: "world/level.dat", data: []byte("ok")},
		zipEntry{name: "world/second.zip", data: level2},
	)
	path := writeRawZip(t, filepath.Join(t.TempDir(), "nested.zip"), level1)
	assertRejected(t, func() error { _, err := InspectArchive(path); return err }())
}

func TestInspectArchiveAcceptsDepthTwoArchive(t *testing.T) {
	inner := makeZipBytes(t, zipEntry{name: "inner.txt", data: []byte("payload")})
	outer := makeZipBytes(t,
		zipEntry{name: "world/level.dat", data: []byte("ok")},
		zipEntry{name: "world/mods/example.zip", data: inner},
	)
	path := writeRawZip(t, filepath.Join(t.TempDir(), "depth2.zip"), outer)
	report, err := InspectArchive(path)
	if err != nil {
		t.Fatalf("InspectArchive: %v", err)
	}
	if report.EntryCount != 3 {
		t.Fatalf("EntryCount = %d, want 3 (including nested entry)", report.EntryCount)
	}
}

func TestInspectArchiveRejectsEncryptedInnerArchive(t *testing.T) {
	inner := patchZipHeader(t,
		makeZipBytes(t, zipEntry{name: "secret.dat", data: []byte("x")}),
		"secret.dat", 0, 0x1, false, true)
	outer := makeZipBytes(t, zipEntry{name: "world/inner.zip", data: inner})
	path := writeRawZip(t, filepath.Join(t.TempDir(), "encrypted-inner.zip"), outer)
	assertRejected(t, func() error { _, err := InspectArchive(path); return err }())
}

func TestInspectArchiveReportsExecutablePayload(t *testing.T) {
	path := writeTestZip(t, filepath.Join(t.TempDir(), "modded.zip"),
		zipEntry{name: "world/level.dat", data: []byte("nbt")},
		zipEntry{name: "world/mods/sodium.jar", data: []byte("not-a-real-jar")},
		zipEntry{name: "world/datapacks/commands.mcfunction", data: []byte("say hi")},
		zipEntry{name: "world/plugins/plugin.yml", data: []byte("name: demo")},
	)
	report, err := InspectArchive(path)
	if err != nil {
		t.Fatalf("InspectArchive: %v", err)
	}
	if !report.HasExecutablePayload {
		t.Fatal("HasExecutablePayload = false, want true")
	}
	if len(report.Warnings) < 3 {
		t.Fatalf("warnings = %v, want one per executable payload", report.Warnings)
	}
	// Inspection must never extract: the temp dir still holds only the archive.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("InspectArchive extracted %d entries, want 0", len(entries)-1)
	}
}
