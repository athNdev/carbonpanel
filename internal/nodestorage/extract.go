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
	"os"
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

	src, err := acc.OpenRead(ctx, archiveRel)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	return ExtractZipReader(ctx, acc, src, info.Size(), destRel, counter)
}

// ExtractZipBytes writes zip members through acc. data may come from a
// controller temp file (remote download, snapshot rollback); the destination
// is still only the accessor.
func ExtractZipBytes(ctx context.Context, acc Accessor, data []byte, destRel string, counter *atomic.Int32) (int, error) {
	if acc == nil {
		return 0, fmt.Errorf("%w: accessor is required", ErrUnavailable)
	}
	destRel = strings.TrimSpace(destRel)
	if destRel == "" {
		return 0, fmt.Errorf("%w: empty archive or destination", ErrEscape)
	}
	return ExtractZipReader(ctx, acc, bytes.NewReader(data), int64(len(data)), destRel, counter)
}

// ExtractZipReader extracts a caller-owned archive without an archive-sized buffer.
func ExtractZipReader(ctx context.Context, acc Accessor, source io.ReaderAt, size int64, destRel string, counter *atomic.Int32) (int, error) {
	if acc == nil {
		return 0, fmt.Errorf("%w: accessor is required", ErrUnavailable)
	}
	if strings.TrimSpace(destRel) == "" {
		return 0, fmt.Errorf("%w: empty archive destination", ErrEscape)
	}
	zr, err := zip.NewReader(source, size)
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
		if err := extractZipFile(ctx, acc, f, rel, &total); err != nil {
			return extracted, err
		}
		extracted++
		if counter != nil {
			counter.Add(1)
		}
	}
	return extracted, nil
}

const (
	restoreStageRel = ".carbon-restore-stage"
	restoreOldRel   = ".carbon-restore-old"
)

// ReplaceTreeFromZip extracts a controller-side zip through acc and swaps it
// over the existing tree. Staging stays inside the accessor so a transport-only
// node cannot grow a controller-local copy (ADR 0005).
func ReplaceTreeFromZip(ctx context.Context, acc Accessor, data []byte) (int, error) {
	return replaceTreeFromZipReader(ctx, acc, bytes.NewReader(data), int64(len(data)))
}

// ReplaceTreeFromZipFile restores a controller-side archive through the accessor
// while keeping its bytes on disk and preserving interrupted recovery trees.
func ReplaceTreeFromZipFile(ctx context.Context, acc Accessor, archivePath string) (int, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return replaceTreeFromZipReader(ctx, acc, f, fi.Size())
}

func replaceTreeFromZipReader(ctx context.Context, acc Accessor, source io.ReaderAt, size int64) (int, error) {
	if acc == nil {
		return 0, fmt.Errorf("%w: accessor is required", ErrUnavailable)
	}
	for _, rel := range []string{restoreStageRel, restoreOldRel} {
		if _, err := acc.Stat(rel); err == nil {
			return 0, fmt.Errorf("storage: recovery staging %q exists; recover or remove it before restoring", rel)
		} else if !os.IsNotExist(err) {
			return 0, err
		}
	}
	n, err := ExtractZipReader(ctx, acc, source, size, restoreStageRel, nil)
	if err != nil {
		return 0, fmt.Errorf("storage: restore incomplete; recovery staging %q: %w", restoreStageRel, err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := SwapAccessorTree(acc, restoreStageRel, restoreOldRel); err != nil {
		return 0, err
	}
	return n, nil
}

// SwapAccessorTree moves live top-level entries aside, promotes stageRel to
// the accessor root, then drops the staging directories.
func SwapAccessorTree(acc Accessor, stageRel, oldRel string) error {
	if acc == nil {
		return fmt.Errorf("%w: accessor is required", ErrUnavailable)
	}
	entries, err := acc.ReadDir(".")
	if err != nil {
		return err
	}
	moved := false
	for _, e := range entries {
		name := e.Name()
		if name == stageRel || name == oldRel {
			continue
		}
		if !moved {
			if err := acc.MkdirAll(oldRel); err != nil {
				return err
			}
			moved = true
		}
		if err := acc.Rename(name, path.Join(oldRel, name)); err != nil {
			return err
		}
	}
	stageEntries, err := acc.ReadDir(stageRel)
	if err != nil {
		return err
	}
	for _, e := range stageEntries {
		if err := acc.Rename(path.Join(stageRel, e.Name()), e.Name()); err != nil {
			return err
		}
	}
	_ = acc.RemoveAll(stageRel)
	_ = acc.RemoveAll(oldRel)
	return nil
}

func extractZipFile(ctx context.Context, acc Accessor, f *zip.File, rel string, total *int64) error {
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
	w, err := acc.BeginWrite(ctx, rel, remaining)
	if err != nil {
		return err
	}
	defer w.Close()
	n, err := io.CopyBuffer(w, io.LimitReader(rc, remaining+1), make([]byte, 256<<10))
	if err != nil {
		return fmt.Errorf("failed to extract %s: %w", f.Name, err)
	}
	if n > remaining {
		return fmt.Errorf("archive exceeds maximum extracted size of %d bytes", maxExtractedBytes)
	}
	*total += n
	return w.Commit()
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
