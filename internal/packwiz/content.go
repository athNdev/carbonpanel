// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package packwiz

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/internal/minecraft"
)

// ContentPatch is a partial update for one metafile. Nil fields are left alone,
// so a caller can patch options without re-sending the download block.
type ContentPatch struct {
	Name              *string `json:"name,omitempty"`
	FileName          *string `json:"file_name,omitempty"`
	Side              *string `json:"side,omitempty"`
	Pinned            *bool   `json:"pinned,omitempty"`
	Optional          *bool   `json:"optional,omitempty"`
	Default           *bool   `json:"default,omitempty"`
	OptionDescription *string `json:"option_description,omitempty"`
	DownloadURL       *string `json:"download_url,omitempty"`
	HashFormat        *string `json:"hash_format,omitempty"`
	Hash              *string `json:"hash,omitempty"`
	SHA512            *string `json:"sha512,omitempty"`
}

// DirectURLContentRequest adds any content kind from a direct download URL.
type DirectURLContentRequest struct {
	Kind              string `json:"kind"`
	Name              string `json:"name"`
	FileName          string `json:"file_name"`
	DownloadURL       string `json:"download_url"`
	Side              string `json:"side"`
	Pinned            bool   `json:"pinned"`
	Optional          bool   `json:"optional"`
	Default           bool   `json:"default"`
	OptionDescription string `json:"option_description"`
}

// ContentUploadMetadata carries the packwiz options for an uploaded archive.
type ContentUploadMetadata struct {
	Name              string
	Side              string
	Pinned            bool
	Optional          bool
	Default           bool
	OptionDescription string
}

func defaultSideForKind(kind string) string {
	switch kind {
	case ContentKindDatapack:
		return "server"
	case ContentKindResourcePack, ContentKindShaderPack:
		return "client"
	default:
		return "both"
	}
}

func validSide(side string) bool {
	switch side {
	case "", "both", "client", "server":
		return true
	}
	return false
}

func normalizeHashFormat(format, hash string) (string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		if strings.TrimSpace(hash) == "" {
			return "", nil
		}
		return "sha512", nil
	}
	switch format {
	case "sha256", "sha512", "sha1", "md5":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported hash format %q", format)
	}
}

// AddContent inserts or replaces one content entry. Slug uniqueness is scoped to
// the kind, so a datapack and a mod may share a display slug.
func (m *Manager) AddContent(packID string, item ModItem) error {
	if !ValidContentKind(item.Kind) {
		return fmt.Errorf("invalid content kind %q", item.Kind)
	}
	if strings.TrimSpace(item.Name) == "" {
		return fmt.Errorf("content name is required")
	}
	if strings.TrimSpace(item.DownloadURL) == "" {
		return fmt.Errorf("download URL is required")
	}
	if !validSide(item.Side) {
		return fmt.Errorf("invalid side %q", item.Side)
	}
	if item.Side == "" {
		item.Side = defaultSideForKind(item.Kind)
	}
	if item.Slug == "" {
		item.Slug = sanitizeSlug(item.Name)
	}
	if strings.TrimSpace(item.FileName) == "" {
		item.FileName = item.Slug + contentExtension(item.Kind)
	}
	fileName, err := cleanContentFileName(item.FileName)
	if err != nil {
		return err
	}
	item.FileName = fileName
	item.HashFormat, err = normalizeHashFormat(item.HashFormat, item.Hash)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	pack, err := m.readPack(packID)
	if err != nil {
		return err
	}
	items := contentList(pack, item.Kind)
	found := false
	for i := range items {
		if items[i].Slug == item.Slug {
			items[i] = item
			found = true
			break
		}
	}
	if !found {
		items = append(items, item)
	}
	setContentList(pack, item.Kind, items)
	pack.UpdatedAt = time.Now()
	return m.writePackFiles(pack)
}

// UpdateContent applies a partial patch to one entry in a kind.
func (m *Manager) UpdateContent(packID, kind, slug string, patch ContentPatch) error {
	if !ValidContentKind(kind) {
		return fmt.Errorf("invalid content kind %q", kind)
	}
	if patch.Side != nil && !validSide(*patch.Side) {
		return fmt.Errorf("invalid side %q", *patch.Side)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	pack, err := m.readPack(packID)
	if err != nil {
		return err
	}
	items := contentList(pack, kind)
	idx := -1
	for i := range items {
		if items[i].Slug == slug {
			idx = i
			break
		}
	}
	if idx == -1 {
		return fmt.Errorf("%s %q not found in pack", kind, slug)
	}
	item := &items[idx]
	if patch.Name != nil {
		item.Name = strings.TrimSpace(*patch.Name)
	}
	if patch.FileName != nil {
		fileName, err := cleanContentFileName(*patch.FileName)
		if err != nil {
			return err
		}
		item.FileName = fileName
	}
	if patch.Side != nil {
		item.Side = *patch.Side
		if item.Side == "" {
			item.Side = defaultSideForKind(kind)
		}
	}
	if patch.Pinned != nil {
		item.Pinned = *patch.Pinned
	}
	if patch.Optional != nil {
		item.Optional = *patch.Optional
	}
	if patch.Default != nil {
		item.Default = *patch.Default
	}
	if patch.OptionDescription != nil {
		item.OptionDescription = strings.TrimSpace(*patch.OptionDescription)
	}
	if patch.DownloadURL != nil && strings.TrimSpace(*patch.DownloadURL) != "" {
		item.DownloadURL = strings.TrimSpace(*patch.DownloadURL)
		// A manual URL is no longer a tracked Modrinth/CurseForge entry unless
		// the caller keeps the platform separately; preserve it when only the
		// URL changes, which is how mirror swaps work.
	}
	if patch.HashFormat != nil || patch.Hash != nil {
		hashFormat := item.HashFormat
		hash := item.Hash
		if patch.HashFormat != nil {
			hashFormat = *patch.HashFormat
		}
		if patch.Hash != nil {
			hash = *patch.Hash
		}
		hashFormat, err = normalizeHashFormat(hashFormat, hash)
		if err != nil {
			return err
		}
		item.HashFormat = hashFormat
		item.Hash = strings.TrimSpace(hash)
	}
	if patch.SHA512 != nil {
		item.SHA512 = strings.TrimSpace(*patch.SHA512)
	}

	items[idx] = *item
	setContentList(pack, kind, items)
	pack.UpdatedAt = time.Now()
	return m.writePackFiles(pack)
}

// DeleteContent removes a metafile and, when the download points at this
// panel's raw file endpoint, the uploaded archive as well.
func (m *Manager) DeleteContent(packID, kind, slug string) error {
	if !ValidContentKind(kind) {
		return fmt.Errorf("invalid content kind %q", kind)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	pack, err := m.readPack(packID)
	if err != nil {
		return err
	}
	items := contentList(pack, kind)
	kept := make([]ModItem, 0, len(items))
	var removed *ModItem
	for i := range items {
		if items[i].Slug == slug {
			removed = &items[i]
			continue
		}
		kept = append(kept, items[i])
	}
	if removed == nil {
		return fmt.Errorf("%s %q not found in pack", kind, slug)
	}
	setContentList(pack, kind, kept)

	folder := contentFolderFor(pack, kind)
	metaPath := filepath.Join(m.packDir(packID), filepath.FromSlash(folder), slug+".pw.toml")
	_ = os.Remove(metaPath)

	// Delete archives the panel hosts itself; never touch a remote URL.
	prefix := fmt.Sprintf("/api/v1/packwiz/%s/%s/", packID, folder)
	if idx := strings.Index(removed.DownloadURL, prefix); idx != -1 {
		rest := strings.Split(removed.DownloadURL[idx+len(prefix):], "?")[0]
		if decoded, err := url.PathUnescape(rest); err == nil {
			name := filepath.Base(decoded)
			if name != "." && name != "/" {
				_ = os.Remove(filepath.Join(m.packDir(packID), filepath.FromSlash(folder), name))
			}
		}
	}

	pack.UpdatedAt = time.Now()
	return m.writePackFiles(pack)
}

// SetDatapackFolder moves a pack's datapack metadata (and any panel-hosted
// archives) into a new pack-relative folder and records packwiz's
// [options] datapack-folder.
func (m *Manager) SetDatapackFolder(packID, folder string) error {
	newFolder, err := cleanContentFolder(folder)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	pack, err := m.readPack(packID)
	if err != nil {
		return err
	}
	oldFolder := pack.DatapackFolder
	if oldFolder == "" {
		oldFolder = "datapacks"
	}
	if oldFolder == newFolder {
		pack.DatapackFolder = newFolder
		pack.UpdatedAt = time.Now()
		return m.writePackFiles(pack)
	}

	pDir := m.packDir(packID)
	oldDir := filepath.Join(pDir, filepath.FromSlash(oldFolder))
	newDir := filepath.Join(pDir, filepath.FromSlash(newFolder))
	if err := os.MkdirAll(newDir, 0755); err != nil {
		return err
	}

	// Rewrite panel-hosted download URLs before moving the files they point at.
	oldURL := fmt.Sprintf("/api/v1/packwiz/%s/%s/", packID, oldFolder)
	newURL := fmt.Sprintf("/api/v1/packwiz/%s/%s/", packID, newFolder)
	for i := range pack.Datapacks {
		pack.Datapacks[i].DownloadURL = strings.Replace(pack.Datapacks[i].DownloadURL, oldURL, newURL, 1)
	}

	if err := moveDirContents(oldDir, newDir); err != nil {
		return err
	}
	_ = os.Remove(oldDir)

	pack.DatapackFolder = newFolder
	pack.UpdatedAt = time.Now()
	return m.writePackFiles(pack)
}

func moveDirContents(oldDir, newDir string) error {
	entries, err := os.ReadDir(oldDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(oldDir, entry.Name())
		dst := filepath.Join(newDir, entry.Name())
		if entry.IsDir() {
			if err := os.MkdirAll(dst, 0755); err != nil {
				return err
			}
			if err := moveDirContents(src, dst); err != nil {
				return err
			}
			_ = os.Remove(src)
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	}
	return nil
}

// AddContentFromURL verifies and hashes a direct download URL. No copy is kept:
// the metafile points at the origin URL and mrpack exports carry SHA-512.
func (m *Manager) AddContentFromURL(packID string, req DirectURLContentRequest) (*ModItem, error) {
	if !ValidContentKind(req.Kind) {
		return nil, fmt.Errorf("invalid content kind %q", req.Kind)
	}
	url := strings.TrimSpace(req.DownloadURL)
	if url == "" {
		return nil, fmt.Errorf("download URL is required")
	}

	fileName := strings.TrimSpace(req.FileName)
	if fileName == "" {
		base := filepath.Base(strings.Split(url, "?")[0])
		fileName = base
	}
	if fileName == "" || fileName == "." || fileName == "/" {
		fileName = req.Kind + ".zip"
	}
	if !hasContentExtension(req.Kind, fileName) {
		fileName += contentExtension(req.Kind)
	}
	fileName, err := cleanContentFileName(fileName)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = strings.TrimSuffix(fileName, filepath.Ext(fileName))
	}

	resp, err := m.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch download URL: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d when fetching content URL", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "carbon-content-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	sha256Hash := sha256.New()
	sha512Hash := sha512.New()
	written, err := io.Copy(io.MultiWriter(tmp, sha256Hash, sha512Hash), io.LimitReader(resp.Body, minecraft.MaxContentArchiveBytes+1))
	closeErr := tmp.Close()
	if err != nil {
		return nil, fmt.Errorf("failed reading content stream: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("failed writing content stream: %w", closeErr)
	}
	if written > minecraft.MaxContentArchiveBytes {
		return nil, fmt.Errorf("archive exceeds %d bytes", minecraft.MaxContentArchiveBytes)
	}
	if req.Kind != ContentKindMod {
		if err := minecraft.ValidateContentArchive(minecraft.ContentArchiveKind(req.Kind), tmpPath); err != nil {
			return nil, err
		}
	}
	sha256Hex := hex.EncodeToString(sha256Hash.Sum(nil))
	sha512Hex := hex.EncodeToString(sha512Hash.Sum(nil))

	item := ModItem{
		Slug:              sanitizeSlug(name),
		Name:              name,
		FileName:          fileName,
		Side:              req.Side,
		Platform:          "url",
		ProjectID:         url,
		VersionID:         sha256Hex[:12],
		DownloadURL:       url,
		FileSize:          written,
		Pinned:            req.Pinned,
		Kind:              req.Kind,
		Optional:          req.Optional,
		Default:           req.Default,
		OptionDescription: strings.TrimSpace(req.OptionDescription),
		HashFormat:        "sha512",
		Hash:              sha512Hex,
		SHA512:            sha512Hex,
	}
	if err := m.AddContent(packID, item); err != nil {
		return nil, err
	}
	return &item, nil
}

// AddContentFile stores an uploaded archive at its install path and creates a
// metafile that points at the panel's public raw-file endpoint. serveURL must
// be absolute (the handler builds it from the request host).
func (m *Manager) AddContentFile(packID, kind, fileName string, meta ContentUploadMetadata, serveURL string, r io.Reader) (*ModItem, error) {
	if !ValidContentKind(kind) {
		return nil, fmt.Errorf("invalid content kind %q", kind)
	}
	if strings.TrimSpace(serveURL) == "" {
		return nil, fmt.Errorf("content serve URL is required")
	}
	fileName, err := cleanContentFileName(fileName)
	if err != nil {
		return nil, err
	}
	if !hasContentExtension(kind, fileName) {
		return nil, fmt.Errorf("%s files must use the %s extension", kind, contentExtension(kind))
	}

	m.mu.Lock()
	pack, err := m.readPack(packID)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	folder := contentFolderFor(pack, kind)
	dir := filepath.Join(m.packDir(packID), filepath.FromSlash(folder))
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp(dir, ".content-*.tmp")
	if err != nil {
		return nil, err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	written, copyErr := io.Copy(tmp, io.LimitReader(r, minecraft.MaxContentArchiveBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		cleanup()
		return nil, fmt.Errorf("failed to write upload: %w", copyErr)
	}
	if closeErr != nil {
		cleanup()
		return nil, fmt.Errorf("failed to close upload: %w", closeErr)
	}
	if written > minecraft.MaxContentArchiveBytes {
		cleanup()
		return nil, fmt.Errorf("archive exceeds %d bytes", minecraft.MaxContentArchiveBytes)
	}
	if err := minecraft.ValidateContentArchive(minecraft.ContentArchiveKind(kind), tmpPath); err != nil {
		cleanup()
		return nil, err
	}
	if kind == ContentKindDatapack {
		if _, err := minecraft.NormalizeDatapackArchive(tmpPath); err != nil {
			cleanup()
			return nil, err
		}
	}

	finalPath := filepath.Join(dir, fileName)
	_ = os.Remove(finalPath)
	if err := os.Rename(tmpPath, finalPath); err != nil {
		cleanup()
		return nil, err
	}

	sha256Hex, sha512Hex, err := hashFile(finalPath)
	if err != nil {
		return nil, err
	}

	name := strings.TrimSpace(meta.Name)
	if name == "" {
		name = strings.TrimSuffix(fileName, filepath.Ext(fileName))
	}
	item := ModItem{
		Slug:              sanitizeSlug(name),
		Name:              name,
		FileName:          fileName,
		Side:              meta.Side,
		Platform:          "url",
		ProjectID:         serveURL,
		VersionID:         sha256Hex[:12],
		DownloadURL:       serveURL,
		FileSize:          written,
		Pinned:            meta.Pinned,
		Kind:              kind,
		Optional:          meta.Optional,
		Default:           meta.Default,
		OptionDescription: strings.TrimSpace(meta.OptionDescription),
		HashFormat:        "sha512",
		Hash:              sha512Hex,
		SHA512:            sha512Hex,
	}
	if err := m.AddContent(packID, item); err != nil {
		return nil, err
	}
	return &item, nil
}

func hasContentExtension(kind, fileName string) bool {
	ext := strings.ToLower(filepath.Ext(fileName))
	switch kind {
	case ContentKindMod:
		return ext == ".jar"
	default:
		return ext == ".zip"
	}
}

func contentExtension(kind string) string {
	if kind == ContentKindMod {
		return ".jar"
	}
	return ".zip"
}

func hashFile(path string) (sha256Hex, sha512Hex string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = f.Close() }()
	h256 := sha256.New()
	h512 := sha512.New()
	if _, err := io.Copy(io.MultiWriter(h256, h512), f); err != nil {
		return "", "", err
	}
	return hex.EncodeToString(h256.Sum(nil)), hex.EncodeToString(h512.Sum(nil)), nil
}

// CheckContentUpdates queries Modrinth for every unpinned content entry. Mods
// filter by the pack loader; datapacks, resource packs and shader packs only
// filter by Minecraft version.
func (m *Manager) CheckContentUpdates(packID string) ([]ModUpdateInfo, error) {
	return m.CheckContentUpdatesContext(context.Background(), packID)
}

func (m *Manager) CheckContentUpdatesContext(ctx context.Context, packID string) ([]ModUpdateInfo, error) {
	pack, err := m.GetPack(packID)
	if err != nil {
		return nil, err
	}
	groups := []struct {
		kind  string
		items []ModItem
	}{
		{ContentKindMod, pack.Mods},
		{ContentKindDatapack, pack.Datapacks},
		{ContentKindResourcePack, pack.ResourcePacks},
		{ContentKindShaderPack, pack.ShaderPacks},
	}
	var updates []ModUpdateInfo
	for _, group := range groups {
		for _, item := range group.items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			updates = append(updates, m.checkContentUpdate(ctx, pack, group.kind, item))
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return updates, nil
}

func (m *Manager) checkContentUpdate(ctx context.Context, pack *Pack, kind string, item ModItem) ModUpdateInfo {
	info := ModUpdateInfo{
		Slug:             item.Slug,
		Name:             item.Name,
		Kind:             kind,
		Platform:         item.Platform,
		CurrentVersionID: item.VersionID,
		Pinned:           item.Pinned,
	}
	if item.Pinned || item.Platform != "modrinth" || item.ProjectID == "" {
		return info
	}

	provider := modrinth.NewClientWithOptions(&config.Config{}, modrinth.Options{BaseURL: m.modrinthBaseURL, HTTPClient: m.httpClient})
	versions, err := provider.GetVersions(ctx, item.ProjectID, "", "")
	if err != nil {
		return info
	}

	for _, v := range versions {
		if !containsString(v.GameVersions, pack.MCVersion) {
			continue
		}
		if kind == ContentKindMod && !containsString(v.Loaders, strings.ToLower(pack.ModLoader)) {
			continue
		}
		file := primaryVersionFile(v.Files)
		if file == nil {
			continue
		}
		info.LatestVersionID = v.ID
		info.LatestFileName = file.Filename
		info.LatestDownloadURL = file.URL
		info.LatestSHA512 = file.Hashes.SHA512
		if file.Hashes.SHA512 != "" {
			info.LatestHashFormat = "sha512"
			info.LatestHash = file.Hashes.SHA512
		} else if file.Hashes.SHA1 != "" {
			info.LatestHashFormat = "sha1"
			info.LatestHash = file.Hashes.SHA1
		}
		info.UpdateAvailable = v.ID != item.VersionID
		break
	}
	return info
}

func primaryVersionFile(files []modrinth.File) *modrinth.File {
	for i := range files {
		if files[i].Primary {
			return &files[i]
		}
	}
	if len(files) > 0 {
		return &files[0]
	}
	return nil
}

func containsString(list []string, want string) bool {
	for _, value := range list {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

// ApplyContentUpdate updates the version and download metadata for one entry.
func (m *Manager) ApplyContentUpdate(packID, kind, slug, targetVersionID, targetFileName, targetURL, hashFormat, hash, sha512 string) error {
	if !ValidContentKind(kind) {
		return fmt.Errorf("invalid content kind %q", kind)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	pack, err := m.readPack(packID)
	if err != nil {
		return err
	}
	items := contentList(pack, kind)
	found := false
	for i := range items {
		if items[i].Slug != slug {
			continue
		}
		items[i].VersionID = targetVersionID
		if targetFileName != "" {
			if fileName, err := cleanContentFileName(targetFileName); err == nil {
				items[i].FileName = fileName
			}
		}
		if targetURL != "" {
			items[i].DownloadURL = targetURL
		}
		if hash != "" {
			items[i].HashFormat = hashFormat
			items[i].Hash = hash
		}
		if sha512 != "" {
			items[i].SHA512 = sha512
		}
		found = true
		break
	}
	if !found {
		return fmt.Errorf("%s %q not found", kind, slug)
	}
	setContentList(pack, kind, items)
	pack.UpdatedAt = time.Now()
	return m.writePackFiles(pack)
}
