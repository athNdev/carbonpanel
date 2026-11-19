// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package packwiz

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	toml "github.com/pelletier/go-toml/v2"

	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/utils"
)

// extractArchiveBounded is the single import entry point into the shared
// bounded extractor. The uploaded reader is spilled to a temp .zip so the same
// pkg/files.ExtractArchiveWithLimits implementation used for generic archives
// governs imports: aggregate/per-file/entry caps, symlink and device refusal,
// and symlink-evaluating traversal containment. A hostile archive is rejected
// in a staging directory, before any pack directory is written.
//
// The returned cleanup must always be called. The staging path is only valid
// until then.
func (m *Manager) extractArchiveBounded(r io.ReaderAt, size int64) (staging string, cleanup func(), err error) {
	if size < 0 {
		return "", func() {}, fmt.Errorf("invalid archive size %d", size)
	}
	tmp, err := os.CreateTemp("", "carbon-panel-import-*.zip")
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create import temp file: %w", err)
	}
	cleanup = func() { _ = os.Remove(tmp.Name()) }

	if _, err := io.Copy(tmp, io.NewSectionReader(r, 0, size)); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, fmt.Errorf("failed to stage uploaded archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to close staged archive: %w", err)
	}

	staging, err = os.MkdirTemp("", "carbon-panel-import-stage-*")
	if err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("failed to create import staging directory: %w", err)
	}
	cleanup = func() {
		_ = os.RemoveAll(staging)
		_ = os.Remove(tmp.Name())
	}

	if _, err := files.ExtractArchiveWithLimits(context.Background(), tmp.Name(), staging, nil, m.importLimits); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("rejected unsafe archive: %w", err)
	}
	return staging, cleanup, nil
}

// copyStagedEntries copies the prefix-selected files from a validated staging
// tree into the pack directory. Both ends are rechecked with WithinReal so a
// stale symlink or an unexpected nested traversal is an explicit rejection,
// never a silent skip.
func copyStagedEntries(staging, pDir, prefix string) error {
	return filepath.WalkDir(staging, func(src string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !files.WithinReal(staging, src) {
			return fmt.Errorf("staged file escapes its root: %s", src)
		}
		rel, err := filepath.Rel(staging, src)
		if err != nil {
			return err
		}
		if prefix != "" {
			if !strings.HasPrefix(rel, prefix) {
				return nil
			}
			rel = strings.TrimPrefix(rel, prefix)
		}
		if rel == "" || rel == "." {
			return nil
		}
		target := filepath.Join(pDir, rel)
		if !files.WithinReal(pDir, target) {
			return fmt.Errorf("import entry escapes the pack directory: %s", rel)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return err
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer func() { _ = in.Close() }()
		out, err := os.Create(target)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			_ = out.Close()
			return err
		}
		return out.Close()
	})
}

// validateImportDownloadURLs applies the provider egress policy at import time,
// without DNS resolution. Credentials in a URL and non-http(s) schemes are
// rejected, and no raw URL (which may embed a secret) is reflected in an error.
func validateImportDownloadURLs(urls []string) error {
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		parsed, err := utils.ValidateURLStatic(raw, false)
		if err != nil {
			return fmt.Errorf("invalid mod downloads URL %s: %w", utils.RedactURL(raw), err)
		}
		if parsed.User != nil {
			return fmt.Errorf("mod downloads URL %s must not contain credentials", utils.RedactURL(raw))
		}
	}
	return nil
}

// ImportOptions defines settings for modpack import
type ImportOptions struct {
	Name          string `json:"name"`
	Author        string `json:"author"`
	Version       string `json:"version"`
	MCVersion     string `json:"mc_version"`
	ModLoader     string `json:"mod_loader"`
	LoaderVersion string `json:"loader_version"`
}

// ImportMrpack imports a Modrinth .mrpack archive into a new Packwiz project
func (m *Manager) ImportMrpack(r io.ReaderAt, size int64, opts ImportOptions) (*Pack, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("failed to open mrpack zip: %w", err)
	}

	var indexFile *zip.File
	for _, f := range zr.File {
		if f.Name == "modrinth.index.json" {
			indexFile = f
			break
		}
	}
	if indexFile == nil {
		return nil, fmt.Errorf("invalid .mrpack: modrinth.index.json not found")
	}

	rc, err := indexFile.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to read modrinth.index.json: %w", err)
	}
	defer rc.Close()

	type mrpackIndex struct {
		FormatVersion int               `json:"formatVersion"`
		Game          string            `json:"game"`
		VersionID     string            `json:"versionId"`
		Name          string            `json:"name"`
		Summary       string            `json:"summary"`
		Dependencies  map[string]string `json:"dependencies"`
		Files         []struct {
			Path      string            `json:"path"`
			Hashes    map[string]string `json:"hashes"`
			Env       map[string]string `json:"env"`
			Downloads []string          `json:"downloads"`
			FileSize  int64             `json:"fileSize"`
		} `json:"files"`
	}

	var mrIdx mrpackIndex
	if err := json.NewDecoder(rc).Decode(&mrIdx); err != nil {
		return nil, fmt.Errorf("failed to parse modrinth.index.json: %w", err)
	}

	packID := uuid.New().String()
	packName := opts.Name
	if packName == "" {
		packName = mrIdx.Name
	}
	if packName == "" {
		packName = "Imported Modpack"
	}

	mcVer := opts.MCVersion
	if mcVer == "" && mrIdx.Dependencies != nil {
		mcVer = mrIdx.Dependencies["minecraft"]
	}
	if mcVer == "" {
		mcVer = "1.20.1"
	}

	loader := opts.ModLoader
	loaderVer := opts.LoaderVersion
	if loader == "" && mrIdx.Dependencies != nil {
		for dep, ver := range mrIdx.Dependencies {
			depLower := strings.ToLower(dep)
			if strings.Contains(depLower, "fabric") {
				loader = "fabric"
				loaderVer = ver
				break
			} else if strings.Contains(depLower, "neoforge") {
				loader = "neoforge"
				loaderVer = ver
				break
			} else if strings.Contains(depLower, "forge") {
				loader = "forge"
				loaderVer = ver
				break
			} else if strings.Contains(depLower, "quilt") {
				loader = "quilt"
				loaderVer = ver
				break
			}
		}
	}
	if loader == "" {
		loader = "fabric"
	}
	if loaderVer == "" {
		loaderVer = "latest"
	}

	packVersion := opts.Version
	if packVersion == "" {
		packVersion = mrIdx.VersionID
	}
	if packVersion == "" {
		packVersion = "1.0.0"
	}

	author := opts.Author
	if author == "" {
		author = "Imported"
	}

	pDir := m.packDir(packID)
	var mrSuccess bool
	defer func() {
		if !mrSuccess {
			_ = os.RemoveAll(pDir)
		}
	}()

	// 1. Extract overrides through the shared bounded extractor. Unsafe
	// entries are rejected in staging before the pack directory is written.
	staging, cleanup, err := m.extractArchiveBounded(r, size)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := os.MkdirAll(pDir, 0755); err != nil {
		return nil, err
	}
	modsDir := filepath.Join(pDir, "mods")
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		return nil, err
	}
	if err := copyStagedEntries(staging, pDir, "overrides"+string(filepath.Separator)); err != nil {
		return nil, fmt.Errorf("rejected unsafe override entry: %w", err)
	}

	// 2. Parse mods and generate .pw.toml files
	var mods []ModItem
	for _, file := range mrIdx.Files {
		if !strings.HasPrefix(file.Path, "mods/") {
			continue
		}

		fileName := filepath.Base(file.Path)
		rawSlug := strings.TrimSuffix(fileName, filepath.Ext(fileName))
		slug := sanitizeSlug(rawSlug)

		side := "both"
		if file.Env != nil {
			if file.Env["client"] == "unsupported" {
				side = "server"
			} else if file.Env["server"] == "unsupported" {
				side = "client"
			}
		}

		if err := validateImportDownloadURLs(file.Downloads); err != nil {
			return nil, fmt.Errorf("mod %s: %w", slug, err)
		}
		dlURL := ""
		if len(file.Downloads) > 0 {
			dlURL = file.Downloads[0]
		}

		hash := ""
		hashFormat := "sha256"
		if h, ok := file.Hashes["sha256"]; ok && h != "" {
			hash = h
			hashFormat = "sha256"
		} else if h, ok := file.Hashes["sha512"]; ok && h != "" {
			hash = h
			hashFormat = "sha512"
		} else if h, ok := file.Hashes["sha1"]; ok && h != "" {
			hash = h
			hashFormat = "sha1"
		}

		modItem := ModItem{
			Slug:        slug,
			Name:        rawSlug,
			FileName:    fileName,
			Side:        side,
			Platform:    "modrinth",
			ProjectID:   slug,
			VersionID:   hash,
			DownloadURL: dlURL,
			FileSize:    file.FileSize,
			Pinned:      false,
			Hash:        hash,
			HashFormat:  hashFormat,
		}
		mods = append(mods, modItem)

		tm := tomlMod{
			Name:     modItem.Name,
			FileName: modItem.FileName,
			Side:     modItem.Side,
			Download: tomlModDownload{
				URL:        dlURL,
				HashFormat: hashFormat,
				Hash:       hash,
			},
			Update: tomlModUpdate{
				Modrinth: &tomlModUpdateModrinth{
					ModID:   slug,
					Version: hash,
				},
			},
		}

		modBytes, err := toml.Marshal(tm)
		if err == nil {
			_ = os.WriteFile(filepath.Join(modsDir, slug+".pw.toml"), modBytes, 0644)
		}
	}

	pack := &Pack{
		ID:            packID,
		Name:          packName,
		Author:        author,
		Version:       packVersion,
		MCVersion:     mcVer,
		ModLoader:     loader,
		LoaderVersion: loaderVer,
		UpdatedAt:     time.Now(),
		Mods:          mods,
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.writePackFiles(pack); err != nil {
		return nil, err
	}

	mrSuccess = true
	return pack, nil
}

// ImportCurseForge imports a CurseForge .zip modpack into a new Packwiz project
func (m *Manager) ImportCurseForge(r io.ReaderAt, size int64, opts ImportOptions) (*Pack, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("failed to open curseforge zip: %w", err)
	}

	var manifestFile *zip.File
	for _, f := range zr.File {
		if f.Name == "manifest.json" {
			manifestFile = f
			break
		}
	}
	if manifestFile == nil {
		return nil, fmt.Errorf("invalid CurseForge pack: manifest.json not found")
	}

	rc, err := manifestFile.Open()
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest.json: %w", err)
	}
	defer rc.Close()

	type cfManifest struct {
		Minecraft struct {
			Version    string `json:"version"`
			ModLoaders []struct {
				ID      string `json:"id"`
				Primary bool   `json:"primary"`
			} `json:"modLoaders"`
		} `json:"minecraft"`
		ManifestType    string `json:"manifestType"`
		ManifestVersion int    `json:"manifestVersion"`
		Name            string `json:"name"`
		Version         string `json:"version"`
		Author          string `json:"author"`
		Files           []struct {
			ProjectID int  `json:"projectID"`
			FileID    int  `json:"fileID"`
			Required  bool `json:"required"`
		} `json:"files"`
		Overrides string `json:"overrides"`
	}

	var manifest cfManifest
	if err := json.NewDecoder(rc).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest.json: %w", err)
	}

	packID := uuid.New().String()
	packName := opts.Name
	if packName == "" {
		packName = manifest.Name
	}
	if packName == "" {
		packName = "Imported CurseForge Pack"
	}

	mcVer := opts.MCVersion
	if mcVer == "" {
		mcVer = manifest.Minecraft.Version
	}
	if mcVer == "" {
		mcVer = "1.20.1"
	}

	loader := opts.ModLoader
	loaderVer := opts.LoaderVersion
	if loader == "" && len(manifest.Minecraft.ModLoaders) > 0 {
		rawLoader := manifest.Minecraft.ModLoaders[0].ID
		parts := strings.SplitN(rawLoader, "-", 2)
		if len(parts) >= 1 {
			loader = strings.ToLower(parts[0])
		}
		if len(parts) >= 2 {
			loaderVer = parts[1]
		}
	}
	if loader == "" {
		loader = "forge"
	}
	if loaderVer == "" {
		loaderVer = "latest"
	}

	packVersion := opts.Version
	if packVersion == "" {
		packVersion = manifest.Version
	}
	if packVersion == "" {
		packVersion = "1.0.0"
	}

	author := opts.Author
	if author == "" {
		author = manifest.Author
	}
	if author == "" {
		author = "Imported"
	}

	pDir := m.packDir(packID)
	var cfSuccess bool
	defer func() {
		if !cfSuccess {
			_ = os.RemoveAll(pDir)
		}
	}()

	// Extract overrides through the shared bounded extractor before writing
	// anything into the pack directory.
	staging, cleanup, err := m.extractArchiveBounded(r, size)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := os.MkdirAll(pDir, 0755); err != nil {
		return nil, err
	}
	modsDir := filepath.Join(pDir, "mods")
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		return nil, err
	}

	overridesDir := manifest.Overrides
	if overridesDir == "" {
		overridesDir = "overrides"
	}
	prefix := filepath.Clean(filepath.FromSlash(strings.TrimSuffix(overridesDir, "/"))) + string(filepath.Separator)
	if err := copyStagedEntries(staging, pDir, prefix); err != nil {
		return nil, fmt.Errorf("rejected unsafe override entry: %w", err)
	}

	// Register CurseForge mods
	var mods []ModItem
	for _, file := range manifest.Files {
		slug := fmt.Sprintf("cf-%d", file.ProjectID)
		name := fmt.Sprintf("CurseForge Mod %d", file.ProjectID)
		fileName := fmt.Sprintf("cf-%d-%d.jar", file.ProjectID, file.FileID)

		side := "both"
		if !file.Required {
			side = "client"
		}

		modItem := ModItem{
			Slug:        slug,
			Name:        name,
			FileName:    fileName,
			Side:        side,
			Platform:    "curseforge",
			ProjectID:   strconv.Itoa(file.ProjectID),
			VersionID:   strconv.Itoa(file.FileID),
			DownloadURL: "",
			Pinned:      false,
		}
		mods = append(mods, modItem)

		tm := tomlMod{
			Name:     modItem.Name,
			FileName: modItem.FileName,
			Side:     modItem.Side,
			Download: tomlModDownload{
				URL:        "",
				HashFormat: "sha256",
				Hash:       "",
			},
			Update: tomlModUpdate{
				Curseforge: &tomlModUpdateCurseforge{
					ProjectID: file.ProjectID,
					FileID:    file.FileID,
				},
			},
		}

		modBytes, err := toml.Marshal(tm)
		if err == nil {
			_ = os.WriteFile(filepath.Join(modsDir, slug+".pw.toml"), modBytes, 0644)
		}
	}

	pack := &Pack{
		ID:            packID,
		Name:          packName,
		Author:        author,
		Version:       packVersion,
		MCVersion:     mcVer,
		ModLoader:     loader,
		LoaderVersion: loaderVer,
		UpdatedAt:     time.Now(),
		Mods:          mods,
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.writePackFiles(pack); err != nil {
		return nil, err
	}

	cfSuccess = true
	return pack, nil
}

// ImportPackwizZip imports a native Packwiz archive (.zip containing pack.toml, index.toml, mods/)
func (m *Manager) ImportPackwizZip(r io.ReaderAt, size int64, opts ImportOptions) (*Pack, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("failed to open packwiz zip: %w", err)
	}

	// Check if pack.toml exists
	var rootPrefix string
	var hasPackToml bool
	for _, f := range zr.File {
		if f.Name == "pack.toml" {
			hasPackToml = true
			rootPrefix = ""
			break
		} else if strings.HasSuffix(f.Name, "/pack.toml") && strings.Count(f.Name, "/") == 1 {
			hasPackToml = true
			rootPrefix = strings.TrimSuffix(f.Name, "pack.toml")
			break
		}
	}

	if !hasPackToml {
		return nil, fmt.Errorf("invalid Packwiz archive: pack.toml not found")
	}

	packID := uuid.New().String()
	pDir := m.packDir(packID)
	staging, cleanup, err := m.extractArchiveBounded(r, size)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := os.MkdirAll(pDir, 0755); err != nil {
		return nil, err
	}
	if err := copyStagedEntries(staging, pDir, filepath.FromSlash(rootPrefix)); err != nil {
		return nil, fmt.Errorf("rejected unsafe archive entry: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	pack, err := m.readPack(packID)
	if err != nil {
		_ = os.RemoveAll(pDir)
		return nil, fmt.Errorf("failed to load imported pack: %w", err)
	}

	if opts.Name != "" {
		pack.Name = opts.Name
	}
	if opts.Author != "" {
		pack.Author = opts.Author
	}
	if opts.Version != "" {
		pack.Version = opts.Version
	}
	pack.UpdatedAt = time.Now()

	if err := m.writePackFiles(pack); err != nil {
		return nil, err
	}

	return pack, nil
}
