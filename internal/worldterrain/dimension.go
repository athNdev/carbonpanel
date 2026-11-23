// SPDX-License-Identifier: MPL-2.0

package worldterrain

import (
	"path"
	"strconv"

	"github.com/athNdev/carbon-panel/internal/worldlib"
)

// Dimension names one saved vanilla dimension of a Java level. The ID is the
// manifest and tile-path segment; the Folder is the level-relative directory
// holding the dimension's region/*.mca files.
type Dimension struct {
	ID     string
	Folder string
	Span   worldlib.JavaDimensionSpan
	// HideBedrockSurface drops columns whose WORLD_SURFACE block is bedrock.
	// The Nether's roof is a solid bedrock sheet; without this it would hide
	// every column behind an opaque ceiling. Legitimate surface-level bedrock
	// is rare in the Nether and the manual refresh still shows the raw
	// heightmap data through the standard unsupported-column checks.
	HideBedrockSurface bool
}

var (
	Overworld = Dimension{ID: "overworld", Folder: "region", Span: worldlib.JavaOverworldSpan}
	Nether    = Dimension{ID: "nether", Folder: "DIM-1/region", Span: worldlib.JavaNetherSpan, HideBedrockSurface: true}
	End       = Dimension{ID: "end", Folder: "DIM1/region", Span: worldlib.JavaEndSpan}
)

// Dimensions lists the served dimension IDs in manifest order.
func Dimensions() []string { return []string{Overworld.ID, Nether.ID, End.ID} }

// DimensionByID resolves a manifest or tile-path dimension name. Bukkit-style
// sibling worlds and modded dimension folders are intentionally absent: their
// folder layout is loader-specific and stays out of scope (see OSS-463).
func DimensionByID(id string) (Dimension, bool) {
	switch id {
	case Overworld.ID:
		return Overworld, true
	case Nether.ID:
		return Nether, true
	case End.ID:
		return End, true
	default:
		return Dimension{}, false
	}
}

// valid reports whether the dimension was produced by this package. Callers
// pass one of the package-level dimensions; anything else is refused.
func (d Dimension) valid() bool {
	switch d {
	case Overworld, Nether, End:
		return true
	default:
		return false
	}
}

// RegionPath returns the level-relative Anvil region file that backs the
// tile, e.g. "world/DIM-1/region/r.0.0.mca".
func (d Dimension) RegionPath(level string, coord TileCoord) string {
	rx, rz := floorDiv(coord.X, tilesPerRegion), floorDiv(coord.Z, tilesPerRegion)
	return path.Join(level, d.Folder, "r."+strconv.FormatInt(int64(rx), 10)+"."+strconv.FormatInt(int64(rz), 10)+".mca")
}
