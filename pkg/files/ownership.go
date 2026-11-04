// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package files

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// AdoptOwnership makes target owned by the same uid/gid as reference.
//
// The panel usually runs as root while the game container runs as an
// unprivileged user, and the two share a bind-mounted data directory. A file
// the panel writes into that directory as root is mode 0644 and root-owned, so
// the game cannot rewrite it: a modpack server then crash-loops on
// server.properties with "Failed to update server.properties", which looks like
// a mod problem and is really an ownership problem.
//
// It is a no-op when the platform has no uid/gid model, when reference is
// missing, or when ownership already matches.
func AdoptOwnership(reference, target string, recursive bool) error {
	owner, ok := ownerOf(reference)
	if !ok {
		return nil
	}
	return applyOwnership(owner, target, recursive)
}

// AdoptTreeOwnership applies AdoptOwnership to root and everything under it.
func AdoptTreeOwnership(reference, root string) error {
	return AdoptOwnership(reference, root, true)
}

type ownership struct{ uid, gid int }

// applyOwnership sets uid/gid on target, skipping paths that already match so a
// no-op call costs one stat per path instead of one syscall per path.
func applyOwnership(owner ownership, target string, recursive bool) error {
	if target == "" {
		return nil
	}
	if !recursive {
		return chownIfNeeded(owner, target)
	}
	return filepath.WalkDir(target, func(path string, _ fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A path that vanished mid-walk is not a reason to fail the
			// whole adoption: the files that do exist still need the owner.
			if errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		return chownIfNeeded(owner, path)
	})
}

func chownIfNeeded(owner ownership, path string) error {
	current, ok := ownerOf(path)
	if !ok || current == owner {
		return nil
	}
	return os.Chown(path, owner.uid, owner.gid)
}
