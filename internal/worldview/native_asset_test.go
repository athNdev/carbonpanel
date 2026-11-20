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
	if got, ok := parseNativeTileAsset("native/overworld/-5/0.png"); !ok || got != (nativeTileAsset{dimension: nativeOverworld, x: -5}) {
		t.Fatalf("valid native tile was rejected: %+v (%t)", got, ok)
	}
	if got, ok := parseNativeTileAsset("native/end/-5/9.png"); !ok || got != (nativeTileAsset{dimension: nativeEnd, x: -5, z: 9}) {
		t.Fatalf("valid End tile was rejected: %+v (%t)", got, ok)
	}
	for _, path := range []string{
		"native/overworld/0/0.png/extra", "native/nether/0/1.png",
		"native/overworld/../../level.dat", "native/overworld/0/1/2.png",
		"native/overworld/000/01.png", "native/overworld/+1/-0.png",
		"native/overworld/2147483648/0.png", "native/overworld/0/1.jpg",
		"native/overworld/-0/0.png", "native/overworld/1e3/0.png",
		"native/end/-0/0.png", "native/end/0/1/2.png", "native/the_end/0/0.png",
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
