// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/worldterrain"
)

const nativeManifestPath = "native/manifest.json"

type nativeTileAsset struct {
	x, z      int
	dimension worldterrain.Dimension
}

// Native paths allow one protocol-specific manifest and only canonical decimal
// tile coordinates under a served dimension. Tile coordinates are converted
// to storage paths by the renderer after resolving the session's current
// world on the controller.
func parseNativeTileAsset(asset string) (nativeTileAsset, bool) {
	rest, ok := strings.CutPrefix(asset, "native/")
	if !ok {
		return nativeTileAsset{}, false
	}
	dimensionID, remainder, ok := strings.Cut(rest, "/")
	if !ok {
		return nativeTileAsset{}, false
	}
	dimension, ok := worldterrain.DimensionByID(dimensionID)
	if !ok {
		return nativeTileAsset{}, false
	}
	remainder, ok = strings.CutSuffix(remainder, ".png")
	if !ok {
		return nativeTileAsset{}, false
	}
	xText, zText, ok := strings.Cut(remainder, "/")
	if !ok {
		return nativeTileAsset{}, false
	}
	x, xErr := strconv.ParseInt(xText, 10, 32)
	z, zErr := strconv.ParseInt(zText, 10, 32)
	if xErr != nil || zErr != nil || strconv.FormatInt(x, 10) != xText || strconv.FormatInt(z, 10) != zText {
		return nativeTileAsset{}, false
	}
	return nativeTileAsset{x: int(x), z: int(z), dimension: dimension}, true
}
