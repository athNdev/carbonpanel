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
