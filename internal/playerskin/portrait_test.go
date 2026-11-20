// SPDX-License-Identifier: MPL-2.0

package playerskin

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"strings"
	"testing"
	"time"
)

func TestLookupPortraitAtlas(t *testing.T) {
	fixture := newSkinFixture(t)
	red := color.NRGBA{R: 255, A: 255}
	blue := color.NRGBA{B: 255, A: 255}
	green := color.NRGBA{G: 255, A: 255}
	for _, tc := range []struct {
		name       string
		height     int
		model      string
		wantPixels map[image.Point]color.NRGBA
	}{
		{"classic", 64, "", map[image.Point]color.NRGBA{
			{4, 0}: green, {4, 8}: green, {0, 8}: red, {3, 8}: blue, {12, 8}: blue,
			{4, 20}: red, {8, 20}: green,
		}},
		{"slim", 64, "slim", map[image.Point]color.NRGBA{
			{4, 0}: green, {0, 8}: {}, {1, 8}: red, {3, 8}: {}, {12, 8}: blue, {15, 8}: {},
			{4, 20}: red, {8, 20}: green,
		}},
		{"legacy", 32, "", map[image.Point]color.NRGBA{
			{4, 0}: green, {4, 8}: red, {0, 8}: red, {3, 8}: blue, {12, 8}: blue, {15, 8}: red,
			{4, 20}: red, {11, 20}: red, {8, 20}: {},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			atlas := image.NewNRGBA(image.Rect(0, 0, 64, tc.height))
			for point, value := range map[image.Point]color.NRGBA{
				{8, 8}: red, {40, 8}: green, {20, 20}: red, {20, 36}: green,
				{44, 20}: red, {47, 20}: blue, {36, 52}: blue, {4, 20}: red, {4, 52}: green,
			} {
				atlas.SetNRGBA(point.X, point.Y, value)
			}
			var source bytes.Buffer
			if err := png.Encode(&source, atlas); err != nil {
				t.Fatal(err)
			}
			fixture.setProfileWithSkin(t, fixture.textureURL(), tc.model, source.Bytes())
			cache := newTestCache(t, time.Hour, nil)
			asset, err := cache.Get(context.Background(), testUUID, KindPortrait)
			if err != nil || asset.Confidence != ConfidenceDetected || asset.FromCache {
				t.Fatalf("portrait not rendered: confidence=%s note=%q error=%v", asset.Confidence, asset.Note, err)
			}
			img, err := decodePNG(asset.Data)
			if err != nil || img.Bounds() != image.Rect(0, 0, 16, 32) {
				t.Fatalf("portrait PNG has invalid bounds or encoding: %v", err)
			}
			for point, want := range tc.wantPixels {
				got := color.NRGBAModel.Convert(img.At(point.X, point.Y)).(color.NRGBA)
				if got != want {
					t.Errorf("portrait pixel %v = %v, want %v", point, got, want)
				}
			}
			cached, err := cache.Get(context.Background(), testUUID, KindPortrait)
			if err != nil || !cached.FromCache || !bytes.Equal(cached.Data, asset.Data) {
				t.Fatalf("cached portrait changed or missed: %v", err)
			}
		})
	}
}

func TestLookupPortraitRefusesWrongProfileUUID(t *testing.T) {
	fixture := newSkinFixture(t)
	fixture.setProfile(t, fixture.textureURL(), "")
	cache := newTestCache(t, time.Hour, nil)
	asset, err := cache.Get(context.Background(), "bbbbbbbb-aaaa-1111-2222-777777777777", KindPortrait)
	if err != nil || asset.Confidence != ConfidenceUnknown {
		t.Fatalf("mismatched profile yielded a portrait: confidence=%s error=%v", asset.Confidence, err)
	}
	if _, textureCalls, _ := fixture.counts(); textureCalls != 0 {
		t.Fatalf("mismatched profile caused a texture fetch")
	}
}

func TestLegacyMojangTextureLocatorUsesPinnedHTTPSBase(t *testing.T) {
	hash := strings.Repeat("a", 64)
	locator := "http://textures.minecraft.net/texture/" + hash
	got, err := validateTextureURL(locator)
	if err != nil || got != hash || textureBaseURL+got != "https://textures.minecraft.net/texture/"+hash {
		t.Fatalf("legacy Mojang locator = %q, %v", got, err)
	}
}

func TestLegacyOpaqueAtlasDoesNotObscureFace(t *testing.T) {
	atlas := image.NewNRGBA(image.Rect(0, 0, 64, 32))
	draw.Draw(atlas, atlas.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	face := color.NRGBA{R: 238, G: 180, B: 159, A: 255}
	atlas.SetNRGBA(8, 8, face)
	portrait, err := renderPortrait(atlas, false)
	if err != nil {
		t.Fatal(err)
	}
	img, err := decodePNG(portrait)
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(img.At(4, 0)); got != face {
		t.Fatalf("opaque legacy hat obscured the face: got %v, want %v", got, face)
	}
}
