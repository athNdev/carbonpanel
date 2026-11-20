// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"sync"
)

const (
	faceCropX     = 8
	faceCropY     = 8
	faceCropSize  = 8
	faceScale     = 12
	faceOutputDim = faceCropSize * faceScale // 96
)

var errBadAtlasDimensions = errors.New("skin atlas must be 64×32 or 64×64")

// renderPortrait composites the front-facing parts in native texels. Legacy
// atlases mirror the right limbs; modern ones have distinct left and outer limbs.
func renderPortrait(src image.Image, slim bool) ([]byte, error) {
	bounds := src.Bounds()
	if bounds.Dx() != 64 || (bounds.Dy() != 32 && bounds.Dy() != 64) {
		return nil, errBadAtlasDimensions
	}
	legacy := bounds.Dy() == 32
	// Old clients treated a fully opaque right half as having no hat layer.
	// Preserve hats on legacy atlases that actually contain transparency.
	hasHat := !legacy || legacyHatUsesAlpha(src, bounds.Min)
	armWidth := 4
	if slim && !legacy {
		armWidth = 3
	}
	// Keep a 1:2 canvas for both models, centering the slim arms.
	out := image.NewNRGBA(image.Rect(0, 0, 16, 32))
	type part struct {
		dst, base, outer image.Point
		width, height    int
		mirror           bool
	}
	point := image.Pt
	parts := []part{
		{point(4, 0), point(8, 8), point(40, 8), 8, 8, false},
		{point(4, 8), point(20, 20), point(20, 36), 8, 12, false},
		{point(4-armWidth, 8), point(44, 20), point(44, 36), armWidth, 12, false},
		{point(12, 8), point(36, 52), point(52, 52), armWidth, 12, false},
		{point(4, 20), point(4, 20), point(4, 36), 4, 12, false},
		{point(8, 20), point(20, 52), point(4, 52), 4, 12, false},
	}
	if legacy {
		parts[3].base, parts[3].mirror = parts[2].base, true
		parts[5].base, parts[5].mirror = parts[4].base, true
	}
	for i, p := range parts {
		dst := image.Rectangle{Min: p.dst, Max: p.dst.Add(point(p.width, p.height))}
		if p.mirror {
			for x := range p.width {
				column := dst
				column.Min.X += x
				column.Max.X = column.Min.X + 1
				draw.Draw(out, column, src, bounds.Min.Add(p.base).Add(point(p.width-1-x, 0)), draw.Src)
			}
		} else {
			draw.Draw(out, dst, src, bounds.Min.Add(p.base), draw.Src)
		}
		if (!legacy && i != 0) || (i == 0 && hasHat) {
			draw.Draw(out, dst, src, bounds.Min.Add(p.outer), draw.Over)
		}
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, out); err != nil {
		return nil, err
	}
	return pngData.Bytes(), nil
}

func legacyHatUsesAlpha(src image.Image, origin image.Point) bool {
	for y := range 32 {
		for x := 32; x < 64; x++ {
			_, _, _, alpha := src.At(origin.X+x, origin.Y+y).RGBA()
			if alpha < 0x8000 {
				return true
			}
		}
	}
	return false
}

var errBadSkinDimensions = errors.New("skin is too small for the 8x8 face region")

var (
	silhouetteOnce  sync.Once
	silhouetteBytes []byte
)

// decodePNG verifies that the bounded upstream bytes are a real PNG. This is
// applied to skin/cape bytes too, so a HTML error page can never be cached as
// an image just because the upstream claimed image/png.
func decodePNG(data []byte) (image.Image, error) {
	return png.Decode(bytes.NewReader(data))
}

// renderFace crops the 8x8 face at (8,8) and scales it to 96x96 with
// nearest-neighbour sampling using only the stdlib image/draw/image/png stack.
// Both classic 64x32 and HD 64x64 skins use the same crop. The slim model also
// uses these exact coordinates: slim changes limb width, not the head.
func renderFace(src image.Image) ([]byte, error) {
	if src.Bounds().Dx() < 64 || src.Bounds().Dy() < 32 {
		return nil, errBadSkinDimensions
	}
	crop := image.Rect(faceCropX, faceCropY, faceCropX+faceCropSize, faceCropY+faceCropSize)
	if !crop.In(src.Bounds()) {
		return nil, errBadSkinDimensions
	}
	scaled := nearestNeighbor{Src: src, Rect: crop, Scale: faceScale}
	dst := image.NewNRGBA(image.Rect(0, 0, faceOutputDim, faceOutputDim))
	// draw.Src copies each sampled colour verbatim, so alpha is preserved and
	// no interpolation is introduced.
	draw.Draw(dst, dst.Bounds(), scaled, image.Point{}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// nearestNeighbor adapts an image.Image to the destination bounds by mapping
// each destination pixel back to its source pixel with integer division.
type nearestNeighbor struct {
	Src   image.Image
	Rect  image.Rectangle
	Scale int
}

func (n nearestNeighbor) ColorModel() color.Model { return color.NRGBAModel }

func (n nearestNeighbor) Bounds() image.Rectangle {
	return image.Rect(0, 0, n.Rect.Dx()*n.Scale, n.Rect.Dy()*n.Scale)
}

func (n nearestNeighbor) At(x, y int) color.Color {
	b := n.Bounds()
	if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
		return color.NRGBA{}
	}
	return n.Src.At(n.Rect.Min.X+x/n.Scale, n.Rect.Min.Y+y/n.Scale)
}

// silhouettePNG is a deterministic 96x96 neutral head-and-shoulders image with
// a transparent background. It is generated in code, never fetched, and
// returned as a copy so callers cannot mutate the shared bytes.
func silhouettePNG() []byte {
	silhouetteOnce.Do(func() { silhouetteBytes = renderSilhouette() })
	return bytes.Clone(silhouetteBytes)
}

func renderSilhouette() []byte {
	const dim = faceOutputDim
	img := image.NewNRGBA(image.Rect(0, 0, dim, dim))
	head := color.NRGBA{R: 150, G: 154, B: 160, A: 255}
	body := color.NRGBA{R: 112, G: 116, B: 122, A: 255}
	for y := 0; y < dim; y++ {
		for x := 0; x < dim; x++ {
			hx := float64(x-48) / 18
			hy := float64(y-35) / 22
			if hx*hx+hy*hy <= 1 {
				img.SetNRGBA(x, y, head)
				continue
			}
			bx := float64(x-48) / 34
			by := float64(y-94) / 28
			if bx*bx+by*by <= 1 {
				img.SetNRGBA(x, y, body)
			}
		}
	}
	var buf bytes.Buffer
	// png.Encode cannot fail for an in-memory NRGBA and a bytes.Buffer.
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}
