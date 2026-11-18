// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// Archive hardening limits. These are deliberately stricter than the generic
// extraction caps because a library revision is retained on the node: a small
// upload that expands to a huge tree must be refused at import time, never
// staged into a live world directory (ADR 0012).
const (
	// maxNestingDepth is the deepest archive-in-archive level allowed. The
	// outer revision archive is depth 1; a zip inside it is depth 2.
	maxNestingDepth = 2
	// maxDecompressedTotal bounds the sum of uncompressed bytes across all
	// inspected archives (outer plus nested).
	maxDecompressedTotal int64 = 64 << 30 // 64 GiB
	// maxEntryUncompressed bounds a single entry's declared and actual size.
	maxEntryUncompressed int64 = 4 << 30 // 4 GiB
	// maxArchiveEntries bounds the entry count of every inspected archive.
	maxArchiveEntries = 500000
	// maxManifestBytes bounds descriptor files that a loader would parse whole.
	maxManifestBytes int64 = 1 << 20 // 1 MiB
	// maxNestedInspectBytes bounds how much of a nested archive is buffered
	// for structural inspection. Larger candidates are treated as bombs.
	maxNestedInspectBytes int64 = 256 << 20 // 256 MiB
	// maxWarnings caps per-entry warnings so a hostile archive cannot grow an
	// unbounded report slice.
	maxWarnings = 64
)

// ArchiveReport is the read-only security report for one revision archive.
// SizeBytes is the total uncompressed size across all inspected archive
// levels, not the on-disk archive size.
type ArchiveReport struct {
	EntryCount           int
	SizeBytes            int64
	HasExecutablePayload bool
	Warnings             []string
}

// executableExts are payloads that can run code when a world is loaded. They
// are reported, never installed by the importer.
var executableExts = map[string]bool{
	".jar": true, ".dll": true, ".so": true, ".dylib": true,
	".exe": true, ".bin": true, ".mcfunction": true,
	".sh": true, ".bat": true, ".cmd": true, ".ps1": true,
}

// executableConfigs are plugin/mod descriptor files that declare executable
// behaviour even though their extension is plain text.
var executableConfigs = map[string]bool{
	"plugin.yml": true, "paper-plugin.yml": true, "bukkit.yml": true,
	"fabric.mod.json": true, "quilt.mod.json": true,
	"mods.toml": true, "neoforge.mods.toml": true,
}

// manifestNames are descriptor files a loader reads in full; an oversized one
// is a denial-of-service vector.
var manifestNames = map[string]bool{
	"manifest.json": true, "pack.mcmeta": true, "manifest.mf": true,
	"mods.toml": true, "neoforge.mods.toml": true,
	"fabric.mod.json": true, "quilt.mod.json": true,
	"plugin.yml": true, "paper-plugin.yml": true, "bukkit.yml": true,
}

// nestedArchiveExts are entry names worth checking for an embedded zip.
var nestedArchiveExts = map[string]bool{
	".zip": true, ".jar": true, ".war": true, ".ear": true,
	".mcworld": true, ".mcpack": true,
}

// InspectArchive reads a ZIP archive without extracting it and refuses
// archives that violate the World Library hardening policy. Rejections wrap
// ErrArchiveRejected so RPC handlers can map them to a typed code. Executable
// payloads are reported as warnings but do not, by themselves, reject the
// archive: the importer stores bytes, it never installs or runs them.
func InspectArchive(path string) (*ArchiveReport, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open archive %q: %v", ErrArchiveRejected, path, err)
	}
	defer func() { _ = r.Close() }()

	report := &ArchiveReport{}
	if err := inspectZip(&r.Reader, 1, report); err != nil {
		return nil, err
	}
	return report, nil
}

func rejected(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrArchiveRejected, fmt.Sprintf(format, args...))
}

// inspectZip walks one archive level. The report is shared across levels so
// totals and warnings cover the whole nested tree.
func inspectZip(zr *zip.Reader, depth int, report *ArchiveReport) error {
	if len(zr.File) > maxArchiveEntries {
		return rejected("archive has %d entries, exceeding the %d entry limit", len(zr.File), maxArchiveEntries)
	}

	for _, f := range zr.File {
		name := normalizeMemberName(f.Name)
		if unsafeMemberName(name) {
			return rejected("unsafe entry path %q", f.Name)
		}

		info := f.FileInfo()
		if info.IsDir() {
			report.EntryCount++
			continue
		}
		if mode := f.Mode(); !mode.IsRegular() {
			return rejected("illegal entry type %s: %q", mode.Type(), f.Name)
		}
		// Traditional PKWARE encryption and strong encryption both set bit 0.
		if f.Flags&0x1 != 0 {
			return rejected("encrypted entry %q", f.Name)
		}

		declared := f.UncompressedSize64
		if declared > uint64(maxEntryUncompressed) {
			return rejected("entry %q declares %d bytes, exceeding the %d byte per-file limit", f.Name, declared, maxEntryUncompressed)
		}
		if declared > uint64(maxDecompressedTotal-report.SizeBytes) {
			return rejected("archive exceeds the %d byte decompressed total limit", maxDecompressedTotal)
		}
		report.SizeBytes += int64(declared)
		report.EntryCount++

		if manifestNames[strings.ToLower(path.Base(name))] && declared > uint64(maxManifestBytes) {
			return rejected("manifest-like entry %q exceeds %d bytes", f.Name, maxManifestBytes)
		}
		noteExecutable(name, report)

		// Nested candidates are buffered (bounded) so their structure can be
		// checked. Everything else is read to EOF to verify the stored CRC.
		if nestedArchiveExts[strings.ToLower(path.Ext(name))] {
			var buf bytes.Buffer
			n, err := copyEntry(&buf, f, maxNestedInspectBytes)
			if err != nil {
				return rejected("unreadable entry %q: %v", f.Name, err)
			}
			if n > maxNestedInspectBytes {
				return rejected("nested archive candidate %q exceeds the %d byte inspection limit", f.Name, maxNestedInspectBytes)
			}
			if isZipPayload(buf.Bytes()) {
				nested, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
				if err != nil {
					return rejected("encrypted or unreadable inner archive %q: %v", f.Name, err)
				}
				if depth+1 > maxNestingDepth {
					return rejected("nested archive depth exceeds %d at %q", maxNestingDepth, f.Name)
				}
				if err := inspectZip(nested, depth+1, report); err != nil {
					return err
				}
			}
			continue
		}

		if _, err := copyEntry(io.Discard, f, maxEntryUncompressed); err != nil {
			return rejected("unreadable entry %q: %v", f.Name, err)
		}
	}
	return nil
}

// copyEntry reads an entry to EOF through a hard bound. Reading to EOF is what
// makes a corrupt or encrypted entry fail here rather than during a later
// extraction; LimitReader(max+1) lets us detect an overshoot without buffering
// an unbounded payload.
func copyEntry(dst io.Writer, f *zip.File, max int64) (int64, error) {
	rc, err := f.Open()
	if err != nil {
		return 0, err
	}
	n, copyErr := io.Copy(dst, io.LimitReader(rc, max+1))
	closeErr := rc.Close() // zip CRC mismatch is reported on Close.
	if copyErr != nil {
		return n, copyErr
	}
	if closeErr != nil {
		return n, closeErr
	}
	return n, nil
}

func isZipPayload(b []byte) bool {
	return len(b) >= 4 && ((b[0] == 'P' && b[1] == 'K' && b[2] == 3 && b[3] == 4) ||
		(b[0] == 'P' && b[1] == 'K' && b[2] == 5 && b[3] == 6))
}

func normalizeMemberName(name string) string {
	n := strings.ReplaceAll(name, `\`, "/")
	return strings.TrimPrefix(n, "./")
}

// unsafeMemberName rejects traversal, absolute paths, Windows drive/UNC
// volumes and any residual ".." segment. It uses path.Clean confinement and
// is independent of the host OS so a crafted name is refused everywhere.
func unsafeMemberName(name string) bool {
	if name == "" || name == "." || strings.HasPrefix(name, "/") {
		return true
	}
	if len(name) >= 2 && name[1] == ':' && isASCIILetter(name[0]) {
		return true // C: or similar Windows volume prefix.
	}
	if filepath.IsAbs(filepath.FromSlash(name)) {
		return true
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return true
	}
	for _, segment := range strings.Split(name, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

func isASCIILetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func noteExecutable(name string, report *ArchiveReport) {
	base := strings.ToLower(path.Base(name))
	flagged := executableExts[strings.ToLower(path.Ext(base))] || executableConfigs[base]
	if !flagged {
		return
	}
	report.HasExecutablePayload = true
	if len(report.Warnings) < maxWarnings {
		report.Warnings = append(report.Warnings, "executable payload present but not installed: "+name)
	}
}

// ArchiveFileSize returns the on-disk archive size. It is used for the
// revision's SizeBytes column; InspectArchive reports uncompressed size.
func ArchiveFileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("archive path %q is a directory", path)
	}
	return info.Size(), nil
}
