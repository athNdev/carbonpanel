// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package minecraft

import (
	"archive/zip"
	"fmt"
	"path/filepath"
	"strings"
)

// MaxContentArchiveBytes caps an uploaded datapack, resource pack or shader
// pack. Texture and shader bundles can be large, but a modpack archive that
// exceeds this is almost certainly a mistake or an attack.
const MaxContentArchiveBytes = 512 << 20 // 512 MiB

// ContentArchiveKind identifies the Minecraft client/server content folder an
// archive belongs in.
type ContentArchiveKind string

const (
	ContentDatapack     ContentArchiveKind = "datapack"
	ContentResourcePack ContentArchiveKind = "resourcepack"
	ContentShaderPack   ContentArchiveKind = "shaderpack"
)

// IsContentArchiveFile reports whether a filename has the ZIP extension used by
// datapacks, resource packs and shader packs.
func IsContentArchiveFile(filename string) bool {
	return strings.EqualFold(filepath.Ext(filename), ".zip")
}

// ValidateContentArchive checks that an archive is the kind the caller asked
// for. A datapack is a ZIP with pack.mcmeta at its root; a resource pack needs
// pack.mcmeta plus an assets/ tree; a shader pack needs a shaders/ tree.
func ValidateContentArchive(kind ContentArchiveKind, path string) error {
	switch kind {
	case ContentDatapack:
		return ValidateDatapackArchive(path)
	case ContentResourcePack:
		return validateArchiveWithMarker(path, "resource pack", true)
	case ContentShaderPack:
		return validateArchiveWithMarker(path, "shader pack", false)
	default:
		return fmt.Errorf("unknown content kind %q", kind)
	}
}

// validateArchiveWithMarker opens once and checks the common layouts produced
// by Modrinth, CurseForge and hand-made archives. Resource packs must carry a
// pack.mcmeta; shader packs are identified by their shaders/ directory.
func validateArchiveWithMarker(path, label string, needsResourceAssets bool) error {
	r, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("not a valid zip archive: %w", err)
	}
	defer func() { _ = r.Close() }()

	rootMeta := false
	rootShaders := false
	rootAssets := false
	wrapMeta := ""
	for _, f := range r.File {
		name := strings.TrimPrefix(filepath.ToSlash(f.Name), "./")
		parts := strings.Split(name, "/")
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, "/") || hasDotDotSegment(name) {
			return fmt.Errorf("archive contains unsafe entry %q", name)
		}
		switch {
		case name == "pack.mcmeta" && !f.FileInfo().IsDir():
			rootMeta = true
		case len(parts) == 2 && parts[1] == "pack.mcmeta" && !f.FileInfo().IsDir():
			if wrapMeta != "" && wrapMeta != parts[0] {
				return fmt.Errorf("archive has multiple top-level folders and no root pack.mcmeta")
			}
			wrapMeta = parts[0]
		case parts[0] == "shaders":
			rootShaders = true
		case parts[0] == "assets":
			rootAssets = true
		}
	}

	if needsResourceAssets {
		if rootMeta && rootAssets {
			return nil
		}
		if wrapMeta != "" {
			// A single wrapper directory is valid, but only when it also holds
			// the assets tree. The installer cannot safely strip arbitrary
			// wrappers, so the user must re-zip it at the root.
			return fmt.Errorf("%s is wrapped in %q; re-zip it with pack.mcmeta at the archive root", label, wrapMeta)
		}
		return fmt.Errorf("archive has no pack.mcmeta and assets/ tree, so it is not a resource pack")
	}

	if rootShaders {
		return nil
	}
	return fmt.Errorf("archive has no shaders/ folder, so it is not a shader pack")
}
