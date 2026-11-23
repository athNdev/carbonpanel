// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/athNdev/carbon-panel/internal/worldterrain"
)

func TestNativeTileAssetUsesCanonicalCoordinates(t *testing.T) {
	if got, ok := parseNativeTileAsset("native/overworld/-5/0.png"); !ok || got.x != -5 || got.z != 0 || got.dimension != worldterrain.Overworld {
		t.Fatalf("valid native tile was rejected: %+v (%t)", got, ok)
	}
	if got, ok := parseNativeTileAsset("native/nether/-5/0.png"); !ok || got.dimension != worldterrain.Nether {
		t.Fatalf("nether tile was rejected: %+v (%t)", got, ok)
	}
	if got, ok := parseNativeTileAsset("native/end/3/4.png"); !ok || got.dimension != worldterrain.End {
		t.Fatalf("end tile was rejected: %+v (%t)", got, ok)
	}
	for _, path := range []string{
		"native/overworld/0/0.png/extra",
		"native/overworld/../../level.dat", "native/overworld/0/1/2.png",
		"native/overworld/000/01.png", "native/overworld/+1/-0.png",
		"native/overworld/2147483648/0.png", "native/overworld/0/1.jpg",
		"native/overworld/-0/0.png", "native/overworld/1e3/0.png",
		"native/DIM-1/0/0.png", "native/overworld%2f/0/0.png",
		"native//0/0.png", "native/0/0.png",
	} {
		if got, ok := parseNativeTileAsset(path); ok {
			t.Errorf("unsafe or noncanonical map asset %q selected %+v", path, got)
		}
	}
}

func TestNativeRegionSaveAsksForTileRetry(t *testing.T) {
	response := httptest.NewRecorder()
	writeNativeAssetError(response, worldterrain.ErrRegionChanged)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != "1" {
		t.Fatalf("region save response code = %d, headers = %v", response.Code, response.Header())
	}
}
