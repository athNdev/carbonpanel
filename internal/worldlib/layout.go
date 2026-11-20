// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
)

// ErrUnsupportedWorldLayout flags a revision archive whose world layout cannot
// be mapped safely to a configured level. It wraps ErrArchiveRejected so the
// existing RPC error mapping and archive-rejection tests keep working.
var ErrUnsupportedWorldLayout = fmt.Errorf("%w: unsupported world archive layout", ErrArchiveRejected)

// WorldFamilyFromMarker is the single, bounded definition of a usable world
// family. A marker is a forward-slash relative path with a DIRECT child
// level.dat inside a top-level, non-hidden folder. Nested markers, case
// variants, root-level files and hidden folders are not usable families. The
// binder's staged-family scan uses this same rule.
func WorldFamilyFromMarker(marker string) string {
	// Extraction on Linux uses filepath.ToSlash, which leaves backslashes as
	// literal filename characters. Refuse them here rather than classifying a
	// member that will extract to a different, unbindable name.
	if strings.Contains(marker, `\`) {
		return ""
	}
	name := strings.TrimPrefix(marker, "./")
	parts := strings.Split(name, "/")
	if len(parts) != 2 || parts[1] != "level.dat" {
		return ""
	}
	top := parts[0]
	if top == "" || top == "." || top == ".." || strings.HasPrefix(top, ".") {
		return ""
	}
	return top
}

// ArchiveLayout is the read-only world-folder classification of a revision
// archive. NamedFamilies are top-level folders that contain a direct
// level.dat; RootLevelDat records an unbindable level.dat at the archive root.
type ArchiveLayout struct {
	NamedFamilies     []string
	RootLevelDat      bool
	EmptyLevelMarkers []string
}

// Validate refuses layouts the binder cannot map. A single named world family
// (or several families for dimension siblings) stays supported, as do the
// documented named backup archives. Archives with no usable family are refused
// so preview and bind cannot disagree.
func (l *ArchiveLayout) Validate() error {
	if l == nil {
		return errors.New("worldlib: nil archive layout")
	}
	if l.RootLevelDat {
		if len(l.NamedFamilies) > 0 {
			return fmt.Errorf("%w: ambiguous archive has a root-level level.dat and named world folder(s) %v; put the world in exactly one named folder before import",
				ErrUnsupportedWorldLayout, l.NamedFamilies)
		}
		return fmt.Errorf("%w: archive has level.dat at its root; Carbon Panel needs a named world folder (for example world/level.dat). Compress the world folder itself and import again",
			ErrUnsupportedWorldLayout)
	}
	if len(l.EmptyLevelMarkers) > 0 {
		return fmt.Errorf("%w: level.dat is empty for family/families %v; the binder requires a non-empty level marker before publishing",
			ErrUnsupportedWorldLayout, l.EmptyLevelMarkers)
	}
	if len(l.NamedFamilies) == 0 {
		return fmt.Errorf("%w: archive has no usable level family; a top-level world folder with a direct non-empty level.dat (for example world/level.dat) is required. Repack the world folder and import again",
			ErrUnsupportedWorldLayout)
	}
	return nil
}

// SelectWorldFamilies resolves the archive families to the configured target
// level BEFORE any live data changes. Dimension siblings are preserved, but a
// family named differently from the target level is never silently published
// into the target's place.
// SameLevelFamily reports whether a family is the selected level or one of its
// recognised dimension siblings. Only these may be published with that level;
// an unrelated named world must never be replaced by a bind for another target.
func SameLevelFamily(family, levelName string) bool {
	if family == levelName {
		return true
	}
	for _, suffix := range []string{"_nether", "_the_end", "_end"} {
		if family == levelName+suffix {
			return true
		}
	}
	return false
}

func SelectWorldFamilies(families []string, levelName string) ([]string, error) {
	levelName = strings.TrimSpace(levelName)
	if levelName == "" {
		return families, nil
	}
	selected := []string{levelName}
	unrelated := []string{}
	found := false
	for _, family := range families {
		if family == levelName {
			found = true
			continue
		}
		if SameLevelFamily(family, levelName) {
			selected = append(selected, family)
			continue
		}
		unrelated = append(unrelated, family)
	}
	if !found {
		if len(families) == 1 {
			return nil, fmt.Errorf("%w: archive family %q does not match target level %q; rename the archive folder to %q or import a matching archive",
				ErrUnsupportedWorldLayout, families[0], levelName, levelName)
		}
		return nil, fmt.Errorf("%w: archive families %v do not include target level %q",
			ErrUnsupportedWorldLayout, families, levelName)
	}
	if len(unrelated) > 0 {
		return nil, fmt.Errorf("%w: archive contains unrelated world families %v for target level %q; remove them or use an explicit mapping before binding",
			ErrUnsupportedWorldLayout, unrelated, levelName)
	}
	sort.Strings(selected)
	return selected, nil
}

func (l *ArchiveLayout) SelectFamilies(levelName string) ([]string, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return SelectWorldFamilies(l.NamedFamilies, levelName)
}

// InspectRevisionArchive applies the existing bounded archive inspection and
// the layout classification in ONE read, with context cancellation. It is
// read-only and never extracts or mutates revision bytes.
func InspectRevisionArchive(ctx context.Context, archive string) (*ArchiveReport, *ArchiveLayout, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open archive %q: %v", ErrArchiveRejected, archive, err)
	}
	defer func() { _ = zr.Close() }()

	report := &ArchiveReport{}
	if err := inspectZipCtx(ctx, &zr.Reader, 1, report); err != nil {
		return nil, nil, err
	}
	// The extractor may normalize different member names onto one destination
	// (for example on Windows). Refuse collisions before any staging or live
	// mutation, using the extractor's own canonical path rule.
	// Case-folded canonical destinations, tracking explicit directories AND
	// regular files. Every ancestor is walked independently of sort order, so
	// a similarly named sibling (dir- between dir and dir/child) cannot hide a
	// file-as-ancestor collision.
	files := make(map[string]string, len(zr.File))
	dirs := make(map[string]string)
	layout := &ArchiveLayout{}
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if strings.Contains(f.Name, `\`) {
			return nil, nil, fmt.Errorf("%w: ZIP entry %q uses backslashes; use forward slashes",
				ErrUnsupportedWorldLayout, f.Name)
		}
		canonical, err := nodestorage.CanonicalZipMember(f.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ErrArchiveRejected, err)
		}
		key := strings.ToLower(canonical)
		if f.FileInfo().IsDir() {
			if previous, ok := files[key]; ok {
				return nil, nil, fmt.Errorf("%w: archive members %q and %q share destination %q",
					ErrArchiveRejected, previous, f.Name, canonical)
			}
			dirs[key] = f.Name
			continue
		}
		if previous, ok := files[key]; ok {
			return nil, nil, fmt.Errorf("%w: archive members %q and %q share destination %q",
				ErrArchiveRejected, previous, f.Name, canonical)
		}
		if previous, ok := dirs[key]; ok {
			return nil, nil, fmt.Errorf("%w: archive members %q and %q share destination %q",
				ErrArchiveRejected, previous, f.Name, canonical)
		}
		files[key] = f.Name

		if strings.EqualFold(canonical, "level.dat") {
			layout.RootLevelDat = true
		} else if family := WorldFamilyFromMarker(canonical); family != "" {
			if f.UncompressedSize64 == 0 {
				layout.EmptyLevelMarkers = append(layout.EmptyLevelMarkers, family)
			} else {
				layout.NamedFamilies = append(layout.NamedFamilies, family)
			}
		}
	}
	for _, members := range []map[string]string{files, dirs} {
		for key, name := range members {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			for ancestor := path.Dir(key); ancestor != "." && ancestor != "/"; ancestor = path.Dir(ancestor) {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				if previous, ok := files[ancestor]; ok {
					return nil, nil, fmt.Errorf("%w: archive member %q is a file but also a parent directory of %q",
						ErrArchiveRejected, previous, name)
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	sort.Strings(layout.NamedFamilies)
	sort.Strings(layout.EmptyLevelMarkers)
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return report, layout, nil
}

// InspectWorldLayout is the read-only layout-only view used by callers that
// already ran the security inspection.
func InspectWorldLayout(archive string) (*ArchiveLayout, error) {
	_, layout, err := InspectRevisionArchive(context.Background(), archive)
	return layout, err
}

// ValidateRevisionArchive gates an already stored revision archive in preview
// and bind, including revisions stored before this gate existed.
func ValidateRevisionArchive(ctx context.Context, archive string) (*ArchiveLayout, error) {
	_, layout, err := InspectRevisionArchive(ctx, archive)
	if err != nil {
		return nil, err
	}
	return layout, layout.Validate()
}

// IsUnsupportedWorldLayout reports whether err was caused by an unusable world
// layout (as opposed to a corrupt or unsafe archive).
func IsUnsupportedWorldLayout(err error) bool {
	return errors.Is(err, ErrUnsupportedWorldLayout)
}
