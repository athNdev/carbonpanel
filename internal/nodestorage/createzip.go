// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodestorage

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"strings"

	"github.com/athNdev/carbon-panel/internal/storageworker"
)

// CreateZip walks source paths through acc and writes a zip to destRel through
// the same accessor. The controller filesystem is never used as the destination,
// so a transport-only node cannot grow a local copy of the world (ADR 0005).
func CreateZip(ctx context.Context, acc Accessor, paths []string, destRel string, compress bool) (int, error) {
	if acc == nil {
		return 0, fmt.Errorf("%w: accessor is required", ErrUnavailable)
	}
	destRel, err := zipRel(destRel)
	if err != nil {
		return 0, err
	}
	if destRel == "." {
		return 0, fmt.Errorf("%w: empty archive destination", ErrEscape)
	}

	w, err := acc.BeginWrite(ctx, destRel, storageworker.MaxStreamBytes)
	if err != nil {
		return 0, err
	}
	defer w.Close()
	n, err := writeZip(ctx, acc, paths, w, compress, destRel)
	if err != nil {
		return n, err
	}
	if err := w.Commit(); err != nil {
		return n, err
	}
	return n, nil
}

// WriteZip walks source paths through acc and writes a zip to w. Source bytes
// come only from the accessor; w may be a controller download temp file.
func WriteZip(ctx context.Context, acc Accessor, paths []string, w io.Writer, compress bool) (int, error) {
	if acc == nil {
		return 0, fmt.Errorf("%w: accessor is required", ErrUnavailable)
	}
	return writeZip(ctx, acc, paths, w, compress, "")
}

func writeZip(ctx context.Context, acc Accessor, paths []string, w io.Writer, compress bool, skipRel string) (count int, err error) {
	if len(paths) == 0 {
		return 0, fmt.Errorf("%w: no paths specified", ErrEscape)
	}
	zw := zip.NewWriter(w)
	defer func() {
		if closeErr := zw.Close(); closeErr != nil {
			if err == nil {
				err = fmt.Errorf("failed to finalize zip archive: %w", closeErr)
			} else {
				err = fmt.Errorf("%w (archive close: %v)", err, closeErr)
			}
		}
	}()

	method := uint16(zip.Store)
	if compress {
		method = zip.Deflate
	}
	for _, p := range paths {
		if err := ctx.Err(); err != nil {
			return count, err
		}
		rel, nerr := zipRel(p)
		if nerr != nil {
			return count, nerr
		}
		n, aerr := addZipPath(ctx, acc, zw, rel, method, skipRel)
		count += n
		if aerr != nil {
			return count, aerr
		}
	}
	return count, nil
}

func addZipPath(ctx context.Context, acc Accessor, zw *zip.Writer, rel string, method uint16, skipRel string) (int, error) {
	if IsWriteStaging(rel) {
		return 0, nil
	}
	if skipRel != "" && rel == skipRel {
		return 0, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	info, err := acc.Stat(rel)
	if err != nil {
		return 0, err
	}
	if info.IsDir() {
		if rel != "." {
			if _, err := zw.Create(rel + "/"); err != nil {
				return 0, err
			}
		}
		entries, err := acc.ReadDir(rel)
		if err != nil {
			return 0, err
		}
		count := 0
		for _, e := range entries {
			child := e.Name()
			if rel != "." {
				child = path.Join(rel, e.Name())
			}
			n, err := addZipPath(ctx, acc, zw, child, method, skipRel)
			count += n
			if err != nil {
				return count, err
			}
		}
		return count, nil
	}
	if !info.Mode().IsRegular() {
		return 0, nil
	}
	src, err := acc.OpenRead(ctx, rel)
	if err != nil {
		return 0, err
	}
	defer src.Close()
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return 0, err
	}
	header.Name = rel
	header.Method = method
	w, err := zw.CreateHeader(header)
	if err != nil {
		return 0, err
	}
	if _, err := io.CopyBuffer(w, src, make([]byte, storageworker.ChunkBytes)); err != nil {
		return 0, err
	}
	return 1, nil
}

// zipRel cleans a caller path for zip walk/write and rejects traversal.
func zipRel(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", fmt.Errorf("%w: empty path", ErrEscape)
	}
	rel = filepath.ToSlash(rel)
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") || filepath.VolumeName(filepath.FromSlash(rel)) != "" {
		return "", fmt.Errorf("%w: %q", ErrEscape, rel)
	}
	clean := path.Clean(rel)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", ErrEscape, rel)
	}
	return clean, nil
}
