// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"strconv"
	"strings"
)

const nativeManifestPath = "native/manifest.json"

type nativeDimension string

const (
	nativeOverworld nativeDimension = "overworld"
	nativeEnd       nativeDimension = "end"
)

type nativeTileAsset struct {
	dimension nativeDimension
	x, z      int
}

// Native paths allow one protocol-specific manifest and only canonical decimal
// tile coordinates. Tile coordinates are converted to storage paths by the
// renderer after resolving the session's current world on the controller.
func parseNativeTileAsset(asset string) (nativeTileAsset, bool) {
	path, ok := strings.CutPrefix(asset, "native/")
	if !ok {
		return nativeTileAsset{}, false
	}
	name, path, ok := strings.Cut(path, "/")
	dimension := nativeDimension(name)
	if !ok || (dimension != nativeOverworld && dimension != nativeEnd) {
		return nativeTileAsset{}, false
	}
	path, ok = strings.CutSuffix(path, ".png")
	if !ok {
		return nativeTileAsset{}, false
	}
	xText, zText, ok := strings.Cut(path, "/")
	if !ok {
		return nativeTileAsset{}, false
	}
	x, xErr := strconv.ParseInt(xText, 10, 32)
	z, zErr := strconv.ParseInt(zText, 10, 32)
	if xErr != nil || zErr != nil || strconv.FormatInt(x, 10) != xText || strconv.FormatInt(z, 10) != zText {
		return nativeTileAsset{}, false
	}
	return nativeTileAsset{dimension: dimension, x: int(x), z: int(z)}, true
}
