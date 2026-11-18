// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// VolumeBinding records which persistent data directory belongs to this guest.
// It is secret-free; the volume ID is a digest of the file manifest, not a key.
type VolumeBinding struct {
	Schema     string    `json:"schema"`
	DataDir    string    `json:"data_dir"`
	VolumeID   string    `json:"volume_id"`
	FileCount  int       `json:"file_count"`
	ImportedAt time.Time `json:"imported_at"`
}

type volumeFile struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

// BackupManifest is the checksum record for one persistent-volume snapshot.
type BackupManifest struct {
	Schema    string                `json:"schema"`
	CreatedAt time.Time             `json:"created_at"`
	SourceDir string                `json:"source_dir"`
	Files     map[string]volumeFile `json:"files"`
}

func walkFiles(root string) (map[string]volumeFile, error) {
	files := map[string]volumeFile{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: persistent volume contains a symlink: %s", ErrIntegrity, path)
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		h, err := hashFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = volumeFile{SHA256: h, Size: info.Size(), Mode: uint32(info.Mode().Perm())}
		return nil
	})
	return files, err
}

func manifestDigest(files map[string]volumeFile) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		f := files[k]
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(f.SHA256))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ImportVolume binds an existing persistent data directory to this guest and
// records a digest of its file manifest. It never moves, copies or deletes the
// directory: worlds and databases stay at their existing path.
func ImportVolume(stateDir, dataDir string) (*VolumeBinding, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, err
	}
	if dataDir == "" {
		return nil, fmt.Errorf("%w: data directory is required", ErrInvalidInput)
	}
	abs, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: data directory %s is not available", ErrNotFound, abs)
	}
	var existing VolumeBinding
	if err := readJSON(filepath.Join(dir, volumeBindingFile), &existing); err == nil {
		if existing.DataDir == abs && existing.VolumeID != "" {
			return &existing, nil // idempotent import
		}
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	files, err := walkFiles(abs)
	if err != nil {
		return nil, err
	}
	binding := &VolumeBinding{
		Schema: "carbon-panel-volume-binding/v1", DataDir: abs,
		VolumeID: manifestDigest(files), FileCount: len(files), ImportedAt: time.Now().UTC(),
	}
	if err := writeJSON(filepath.Join(dir, volumeBindingFile), binding, 0o644); err != nil {
		return nil, err
	}
	return binding, nil
}

// BackupVolume writes a verified snapshot of the persistent data directory to
// a NEW backup directory. The source is read-only; no world or DB file there
// is changed or deleted.
func BackupVolume(dataDir, backupDir string) (*BackupManifest, error) {
	if dataDir == "" || backupDir == "" {
		return nil, fmt.Errorf("%w: data and backup directories are required", ErrInvalidInput)
	}
	src, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	dst, err := filepath.Abs(backupDir)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(src); err != nil {
		return nil, fmt.Errorf("%w: data directory: %v", ErrNotFound, err)
	}
	if _, err := os.Stat(dst); err == nil {
		return nil, fmt.Errorf("%w: backup directory already exists: %s", ErrAlreadyExists, dst)
	}
	tmp := dst + ".partial"
	if err := os.RemoveAll(tmp); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		return nil, err
	}
	// Cleanup of the .partial directory is best effort on every error path: the
	// original failure is the one the caller needs. Ignoring the remove error
	// is deliberate, so the primary error is never masked by cleanup noise.
	files, err := walkFiles(src)
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	for rel, f := range files {
		if err := safeRelPath(rel); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
		in, err := os.Open(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(f.Mode))
		if err != nil {
			_ = in.Close()
			_ = os.RemoveAll(tmp)
			return nil, err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		_ = in.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.RemoveAll(tmp)
			return nil, fmt.Errorf("backup copy failed: %v %v", copyErr, closeErr)
		}
	}
	m := &BackupManifest{Schema: "carbon-panel-volume-backup/v1", CreatedAt: time.Now().UTC(), SourceDir: src, Files: files}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if err := writeFile(filepath.Join(tmp, backupManifestName), append(b, '\n'), 0o600); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	return m, nil
}

const backupManifestName = "backup-manifest.json"

// RecoverVolume verifies a backup and copies its files back over the current
// data directory. It overwrites only files named in the verified manifest;
// it never deletes anything, so unrelated worlds and databases survive.
func RecoverVolume(backupDir, dataDir string) error {
	if backupDir == "" || dataDir == "" {
		return fmt.Errorf("%w: backup and data directories are required", ErrInvalidInput)
	}
	src, err := filepath.Abs(backupDir)
	if err != nil {
		return err
	}
	dst, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	var m BackupManifest
	if err := readJSON(filepath.Join(src, backupManifestName), &m); err != nil {
		return err
	}
	if m.Schema != "carbon-panel-volume-backup/v1" || len(m.Files) == 0 {
		return fmt.Errorf("%w: backup manifest is incomplete", ErrIntegrity)
	}
	for rel, want := range m.Files {
		if err := safeRelPath(rel); err != nil {
			return err
		}
		got, err := hashFile(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("%w: backup file %s: %v", ErrIntegrity, rel, err)
		}
		if got != want.SHA256 {
			return fmt.Errorf("%w: backup file %s hash mismatch", ErrIntegrity, rel)
		}
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for rel, f := range m.Files {
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		in, err := os.Open(filepath.Join(src, filepath.FromSlash(rel)))
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(f.Mode))
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		_ = in.Close()
		if copyErr != nil || closeErr != nil {
			return fmt.Errorf("recovery copy failed: %v %v", copyErr, closeErr)
		}
	}
	return nil
}
