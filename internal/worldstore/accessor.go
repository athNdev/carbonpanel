// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldstore

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

const (
	restoreStageRel = ".worldstore-restore-stage"
	restoreOldRel   = ".worldstore-restore-old"
)

// BackupToAccessor streams a worldstore manifest archive of the tree visible
// through acc to destPath on the controller. Source bytes come only from the
// accessor, so a transport-only node cannot grow a controller-local world
// copy (ADR 0004/0005).
func BackupToAccessor(acc nodestorage.Accessor, destPath string, skip func(rel string) bool) (string, error) {
	if acc == nil {
		return "", fmt.Errorf("%w: accessor is required", nodestorage.ErrUnavailable)
	}
	entries, err := listAccessor(acc)
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })

	partial := destPath + ".partial"
	_ = os.Remove(partial)
	out, err := os.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	z := zip.NewWriter(io.MultiWriter(out, hash))

	manifest := archiveManifest{Version: 1, Files: make([]Entry, 0, len(entries))}
	var total int64
	failed := func(err error) (string, error) {
		_ = z.Close()
		_ = out.Close()
		_ = os.Remove(partial)
		return "", err
	}
	for _, e := range entries {
		if skip != nil && skip(e.Path) {
			continue
		}
		if skipRestoreRel(e.Path) {
			continue
		}
		data, err := acc.ReadFile(e.Path)
		if err != nil {
			return failed(err)
		}
		total += int64(len(data))
		if total > streamMaxWorldBytes {
			return failed(errors.New("worldstore: world exceeds streaming backup limit"))
		}
		if int64(len(data)) > streamMaxFileBytes {
			return failed(fmt.Errorf("worldstore: file too large: %s", e.Path))
		}
		sum := sha256.Sum256(data)
		manifest.Files = append(manifest.Files, Entry{Path: e.Path, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
		h := &zip.FileHeader{Name: e.Path, Method: zip.Deflate}
		h.SetMode(0600)
		h.Modified = time.Unix(0, 0).UTC()
		w, err := z.CreateHeader(h)
		if err != nil {
			return failed(err)
		}
		if _, err := w.Write(data); err != nil {
			return failed(err)
		}
	}
	mb, err := json.Marshal(manifest)
	if err != nil {
		return failed(err)
	}
	mh := &zip.FileHeader{Name: manifestName, Method: zip.Deflate}
	mh.SetMode(0600)
	mh.Modified = time.Unix(0, 0).UTC()
	mw, err := z.CreateHeader(mh)
	if err != nil {
		return failed(err)
	}
	if _, err := mw.Write(mb); err != nil {
		return failed(err)
	}
	if err := z.Close(); err != nil {
		return failed(err)
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	if err := os.Rename(partial, destPath); err != nil {
		_ = os.Remove(partial)
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// RestoreFromAccessor verifies a worldstore archive on the controller and
// swaps it into acc through an in-tree stage directory. The accessor cannot
// rename the data root itself, so staging stays inside the confined tree.
func RestoreFromAccessor(acc nodestorage.Accessor, archivePath string) error {
	if acc == nil {
		return fmt.Errorf("%w: accessor is required", nodestorage.ErrUnavailable)
	}
	sx, err := openZipArchive(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = sx.Close() }()

	_ = acc.RemoveAll(restoreStageRel)
	_ = acc.RemoveAll(restoreOldRel)
	if err := acc.MkdirAll(restoreStageRel); err != nil {
		return err
	}
	if err := sx.extractAccessor(acc, restoreStageRel); err != nil {
		_ = acc.RemoveAll(restoreStageRel)
		return err
	}
	return nodestorage.SwapAccessorTree(acc, restoreStageRel, restoreOldRel)
}

func (sx *zipArchive) extractAccessor(acc nodestorage.Accessor, stage string) error {
	paths := make([]string, 0, len(sx.byPath))
	for p := range sx.byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var total int64
	for _, p := range paths {
		zf := sx.byPath[p]
		want := sx.manifest[p]
		dest := p
		if stage != "" && stage != "." {
			dest = path.Join(stage, p)
		}
		if parent := path.Dir(dest); parent != "." && parent != "" {
			if err := acc.MkdirAll(parent); err != nil {
				return err
			}
		}
		src, err := zf.Open()
		if err != nil {
			return err
		}
		hash := sha256.New()
		data, err := io.ReadAll(io.LimitReader(io.TeeReader(src, hash), streamMaxFileBytes+1))
		serr := src.Close()
		if err != nil {
			return err
		}
		if serr != nil {
			return serr
		}
		if int64(len(data)) > streamMaxFileBytes {
			return fmt.Errorf("worldstore: file too large: %s", p)
		}
		if int64(len(data)) != want.Size {
			return fmt.Errorf("worldstore: size mismatch for %s: manifest %d, archive %d", p, want.Size, len(data))
		}
		if got := hex.EncodeToString(hash.Sum(nil)); got != want.SHA256 {
			return fmt.Errorf("worldstore: hash mismatch for %s", p)
		}
		if err := acc.WriteFile(dest, data); err != nil {
			return err
		}
		total += int64(len(data))
		if total > streamMaxWorldBytes {
			return errors.New("worldstore: expanded archive exceeds streaming limit")
		}
	}
	return nil
}

func listAccessor(acc nodestorage.Accessor) ([]Entry, error) {
	var entries []Entry
	var walk func(rel string) error
	walk = func(rel string) error {
		entriesDir, err := acc.ReadDir(rel)
		if err != nil {
			return err
		}
		for _, d := range entriesDir {
			child := d.Name()
			if rel != "." && rel != "" {
				child = path.Join(rel, d.Name())
			}
			if skipRestoreRel(child) {
				continue
			}
			info, err := acc.Stat(child)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := walk(child); err != nil {
					return err
				}
				continue
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("%w: non-regular file: %s", ErrPath, child)
			}
			data, err := acc.ReadFile(child)
			if err != nil {
				return err
			}
			if int64(len(data)) > streamMaxFileBytes {
				return fmt.Errorf("worldstore: file too large: %s", child)
			}
			sum := sha256.Sum256(data)
			entries = append(entries, Entry{Path: child, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
			if len(entries) > streamMaxEntries {
				return errors.New("worldstore: too many files")
			}
		}
		return nil
	}
	if err := walk("."); err != nil {
		return nil, err
	}
	return entries, nil
}

func skipRestoreRel(rel string) bool {
	first, _, _ := strings.Cut(filepathToSlash(rel), "/")
	return first == restoreStageRel || first == restoreOldRel
}

func filepathToSlash(rel string) string {
	return strings.ReplaceAll(rel, "\\", "/")
}
