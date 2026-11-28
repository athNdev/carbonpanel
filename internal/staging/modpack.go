// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/google/uuid"
)

const stagedContentRoot = ".carbon-panel/staged"

// modpackPendingCommitFile is controller state written by the modpack scheduler.
// A legacy copy could still sit in the clone cache, so staging and the legacy
// sync must never turn it into a pack target.
const modpackPendingCommitFile = ".carbon-panel_pending_update"

// modpackGitDir is the clone cache directory name. The scheduler keeps the live
// pending marker beside it, so that marker's basename is the cache directory
// name joined with the marker file name.
const modpackGitDir = ".carbon-panel_modpack_git"

// modpackPendingCommitSibling is the basename of the live pending marker:
// cacheDir + modpackPendingCommitFile. A pack must never publish it.
const modpackPendingCommitSibling = modpackGitDir + modpackPendingCommitFile

func isModpackInternalPath(rel string) bool {
	rel = path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	// The scheduler's live marker is a sibling of the clone cache, so its
	// basename is the cache directory name joined with the marker file name.
	// Both the prefixed sibling and the legacy in-cache marker are controller
	// state, never pack content.
	for _, name := range []string{modpackPendingCommitSibling, modpackPendingCommitFile} {
		if rel == name || strings.HasPrefix(rel, name+"/") {
			return true
		}
	}
	return false
}

// StageModpackTree copies a fetched pack into the open batch. The live pack
// and the container are left as they are. A later window copies the tree.
func StageModpackTree(ctx context.Context, store *storage.Store, server *storage.Server, srcDir, summary string, resolvers ...*nodestorage.Resolver) (*storage.StagedContentChange, error) {
	resolver := firstResolver(resolvers)
	if store == nil || server == nil || server.ID == "" || server.DataPath == "" || srcDir == "" {
		return nil, fmt.Errorf("modpack stage requires a store, a server, and a source tree")
	}
	write, err := datawrite.TryShared(server.ID)
	if err != nil {
		return nil, err
	}
	defer write.Release()
	acc, err := openAccessor(ctx, store, server, resolver)
	if err != nil {
		return nil, err
	}
	defer func() { _ = acc.Close() }()

	id := uuid.NewString()
	stageRoot := stagedContentRoot + "/" + id + "/tree"
	files := 0
	walkErr := filepath.Walk(srcDir, func(current string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, current)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("modpack stage refuses symlink %s", rel)
		}
		if isModpackInternalPath(rel) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		parts := strings.Split(rel, "/")
		if parts[0] == ".git" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		live, err := modpackLivePath(rel)
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		source, err := os.Open(current)
		if err != nil {
			return err
		}
		defer func() { _ = source.Close() }()
		if _, err := nodestorage.WriteFrom(ctx, acc, stageRoot+"/"+live, source, 64<<30); err != nil {
			return err
		}
		files++
		return nil
	})
	if walkErr != nil || files == 0 {
		_ = acc.RemoveAll(stagedContentRoot + "/" + id)
		if walkErr != nil {
			return nil, walkErr
		}
		return nil, fmt.Errorf("modpack tree is empty")
	}
	if summary == "" {
		summary = "modpack"
	}
	row, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ID:         id,
		ServerID:   server.ID,
		Kind:       storage.StagedKindModpack,
		Action:     storage.StagedActionReplace,
		TargetPath: "modpack",
		StagePath:  stageRoot,
		FileName:   summary,
	})
	if err != nil {
		_ = acc.RemoveAll(stagedContentRoot + "/" + id)
		return nil, err
	}
	return row, nil
}

func modpackLivePath(rel string) (string, error) {
	rel = path.Clean(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("modpack path %q is not inside the pack", rel)
	}
	top := strings.SplitN(rel, "/", 2)[0]
	switch strings.ToLower(top) {
	case ".git", ".carbon-panel", modpackGitDir, ".carbon-panel_modpack_staged", ".carbon-panel_snapshots", modpackPendingCommitFile, modpackPendingCommitSibling:
		return "", fmt.Errorf("modpack path %q is reserved", rel)
	}
	return rel, nil
}

type modpackFile struct {
	stage string
	live  string
}

func modpackFiles(acc nodestorage.Accessor, stageRoot string) ([]modpackFile, error) {
	stageRoot = path.Clean(stageRoot)
	var files []modpackFile
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := acc.ReadDir(dir)
		if err != nil {
			return err
		}
		names := make([]string, 0, len(entries))
		byName := make(map[string]os.DirEntry, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
			byName[entry.Name()] = entry
		}
		sort.Strings(names)
		for _, name := range names {
			if name == "" || name == "." || name == ".." {
				continue
			}
			entry := byName[name]
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("staged modpack refuses symlink %s", name)
			}
			child := dir + "/" + name
			if entry.IsDir() {
				if err := walk(child); err != nil {
					return err
				}
				continue
			}
			rel := strings.TrimPrefix(child, stageRoot+"/")
			if isModpackInternalPath(rel) {
				continue
			}
			live, err := modpackLivePath(rel)
			if err != nil {
				return err
			}
			files = append(files, modpackFile{stage: child, live: live})
		}
		return nil
	}
	if err := walk(stageRoot); err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("staged modpack is empty")
	}
	return files, nil
}
