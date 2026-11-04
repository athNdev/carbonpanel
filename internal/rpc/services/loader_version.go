// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
)

// loaderVersionPattern is the shape of every loader build string the images
// accept: NeoForge "21.1.84", Forge "54.0.1", Fabric "0.16.10", Quilt
// "0.26.1". It exists to keep a crafted value out of a container env var, not
// to prove the build exists - the loader's own version list does that.
var loaderVersionPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// loadersWithPinnableVersion lists the loaders whose image takes an exact
// build. Vanilla and the plugin loaders pick a build from the Minecraft
// version alone, so pinning one there would be silently ignored.
var loadersWithPinnableVersion = map[storage.ModLoader]bool{
	storage.ModLoaderNeoForge: true,
	storage.ModLoaderForge:    true,
	storage.ModLoaderFabric:   true,
	storage.ModLoaderQuilt:    true,
}

// validateLoaderVersion checks and normalises a pinned loader version. An empty
// value (or "latest") means "whatever the loader considers current", which is
// stored as empty so a later rebuild follows the loader rather than a stale pin.
func validateLoaderVersion(loader storage.ModLoader, raw string) (string, error) {
	version := strings.TrimSpace(raw)
	if version == "" || strings.EqualFold(version, "latest") {
		return "", nil
	}
	if strings.EqualFold(version, "none") {
		return "", nil
	}
	if !loadersWithPinnableVersion[loader] {
		return "", fmt.Errorf("%s servers do not take a loader version", loader)
	}
	if !loaderVersionPattern.MatchString(version) {
		return "", errors.New("loader version contains characters a loader build never has")
	}
	return version, nil
}
