// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodestorage

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// maxExtractedBytes matches pkg/files.DefaultMaxExtractedBytes so a
// decompression bomb is refused on the accessor path too.
const maxExtractedBytes int64 = 20 << 30

// ExtractZip reads a zip through acc and writes its members through the same
// accessor. The controller filesystem is never used as the destination, so a
// transport-only node cannot grow a local copy of the world (ADR 0005).
func ExtractZip(ctx context.Context, acc Accessor, archiveRel, destRel string, counter *atomic.Int32) (int, error) {
	if acc == nil {
		return 0, fmt.Errorf("%w: accessor is required", ErrUnavailable)
	}
	archiveRel = strings.TrimSpace(archiveRel)
	destRel = strings.TrimSpace(destRel)
	if archiveRel == "" || destRel == "" {
		return 0, fmt.Errorf("%w: empty archive or destination", ErrEscape)
	}

	info, err := acc.Stat(archiveRel)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		return 0, fmt.Errorf("path is a directory, not an archive")
	}

	raw, err := acc.ReadFile(archiveRel)
	if err != nil {
		return 0, err
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return 0, fmt.Errorf("failed to open zip: %w", err)
	}

	if err := acc.MkdirAll(destRel); err != nil {
		return 0, err
	}

	var total int64
	extracted := 0
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return extracted, err
		}
		rel, err := zipMemberRel(destRel, f.Name)
		if err != nil {
			return extracted, err
		}
		mode := f.Mode()
		if !strings.HasSuffix(f.Name, "/") && !f.FileInfo().IsDir() && !mode.IsRegular() {
			return extracted, fmt.Errorf("illegal archive entry type %s: %s", mode.Type(), f.Name)
		}
		if strings.HasSuffix(f.Name, "/") || f.FileInfo().IsDir() {
			if err := acc.MkdirAll(rel); err != nil {
				return extracted, err
			}
			continue
		}
		if err := extractZipFile(acc, f, rel, &total); err != nil {
			return extracted, err
		}
		extracted++
		if counter != nil {
			counter.Add(1)
		}
	}
	return extracted, nil
}

func extractZipFile(acc Accessor, f *zip.File, rel string, total *int64) error {
	if parent := path.Dir(filepath.ToSlash(rel)); parent != "." && parent != "" {
		if err := acc.MkdirAll(parent); err != nil {
			return err
		}
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("failed to open zip entry %s: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	remaining := maxExtractedBytes - *total
	if remaining <= 0 {
		return fmt.Errorf("archive exceeds maximum extracted size of %d bytes", maxExtractedBytes)
	}
	limited := io.LimitReader(rc, remaining+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("failed to extract %s: %w", f.Name, err)
	}
	if int64(len(data)) > remaining {
		return fmt.Errorf("archive exceeds maximum extracted size of %d bytes", maxExtractedBytes)
	}
	*total += int64(len(data))
	return acc.WriteFile(rel, data)
}

// zipMemberRel joins destRel with a zip entry name and rejects traversal,
// absolute paths, and volume-prefixed names before they reach the accessor.
func zipMemberRel(destRel, nameInArchive string) (string, error) {
	name := filepath.ToSlash(nameInArchive)
	name = strings.TrimPrefix(name, "./")
	if name == "" || name == "." {
		return "", fmt.Errorf("%w: %q", ErrEscape, nameInArchive)
	}
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || filepath.VolumeName(filepath.FromSlash(name)) != "" {
		return "", fmt.Errorf("%w: %q", ErrEscape, nameInArchive)
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", ErrEscape, nameInArchive)
	}
	dest := strings.Trim(filepath.ToSlash(destRel), "/")
	if dest == "" || dest == "." {
		return clean, nil
	}
	joined := path.Clean(path.Join(dest, clean))
	if joined != dest && !strings.HasPrefix(joined, dest+"/") {
		return "", fmt.Errorf("%w: %q", ErrEscape, nameInArchive)
	}
	return joined, nil
}

// DestRelFromArchive returns the extraction folder next to the archive, named
// after the archive without its extension (including .tar.gz).
func DestRelFromArchive(archiveRel string) string {
	slash := filepath.ToSlash(strings.TrimSpace(archiveRel))
	base := path.Base(slash)
	folder := strings.TrimSuffix(base, path.Ext(base))
	if strings.HasSuffix(strings.ToLower(folder), ".tar") {
		folder = strings.TrimSuffix(folder, ".tar")
	}
	if folder == "" || folder == "." {
		folder = "extracted"
	}
	dir := path.Dir(slash)
	if dir == "." || dir == "" {
		return folder
	}
	return path.Join(dir, folder)
}
