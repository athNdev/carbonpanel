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
	// SurfaceUnderRoof reports that WORLD_SURFACE can be an opaque bedrock
	// roof instead of the visible surface. The Nether's storage range reaches
	// y=255, but its logical height and full bedrock roof are at y=127 (with
	// partial layers down to y=123), so every column's WORLD_SURFACE block is
	// bedrock; the surface scan descends to the first terrain block under the
	// roof, and a column that holds only roof bedrock and air stays empty.
	SurfaceUnderRoof bool
}

var (
	Overworld = Dimension{ID: "overworld", Folder: "region", Span: worldlib.JavaOverworldSpan}
	Nether    = Dimension{ID: "nether", Folder: "DIM-1/region", Span: worldlib.JavaNetherSpan, SurfaceUnderRoof: true}
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
