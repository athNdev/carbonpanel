// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

const testUUID = "00112233-4455-6677-8899-aabbccddeeff"

// skinFixture is an httptest profile server plus a second host used only to
// prove that cross-host redirects are refused. All base URL overrides are
// restored by t.Cleanup; tests in this package must not call t.Parallel.
type skinFixture struct {
	mu sync.Mutex

	profileStatus int
	profileBody   []byte
	textureStatus int
	textureBody   []byte

	skinURL   string
	skinModel string
	capeURL   string

	redirectProfileTo string
	redirectTextureTo string

	profileCalls int
	textureCalls int
	otherCalls   int
	seenHeaders  []http.Header

	srv   *httptest.Server
	other *httptest.Server
}

func newSkinFixture(t *testing.T) *skinFixture {
	t.Helper()
	f := &skinFixture{
		profileStatus: http.StatusOK,
		textureStatus: http.StatusOK,
	}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	f.other = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.otherCalls++
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("unexpected"))
	}))
	t.Cleanup(f.other.Close)

	oldProfile, oldTexture, oldHost := profileBaseURL, textureBaseURL, pinnedTextureHost
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatalf("parse fixture URL: %v", err)
	}
	profileBaseURL = f.srv.URL + "/session/minecraft/profile/"
	textureBaseURL = f.srv.URL + "/texture/"
	pinnedTextureHost = u.Host
	t.Cleanup(func() {
		profileBaseURL, textureBaseURL, pinnedTextureHost = oldProfile, oldTexture, oldHost
	})

	f.setProfileWithSkin(t, f.textureURL(), "", fixtureSkinPNG(t, 64))
	return f
}

func (f *skinFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seenHeaders = append(f.seenHeaders, r.Header.Clone())
	switch {
	case strings.HasPrefix(r.URL.Path, "/session/minecraft/profile/"):
		f.profileCalls++
		if f.redirectProfileTo != "" {
			http.Redirect(w, r, f.redirectProfileTo, http.StatusFound)
			return
		}
		w.WriteHeader(f.profileStatus)
		_, _ = w.Write(f.profileBody)
	case strings.HasPrefix(r.URL.Path, "/texture/"):
		f.textureCalls++
		if f.redirectTextureTo != "" {
			http.Redirect(w, r, f.redirectTextureTo, http.StatusFound)
			return
		}
		w.WriteHeader(f.textureStatus)
		_, _ = w.Write(f.textureBody)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *skinFixture) textureURL() string {
	return textureBaseURL + strings.Repeat("a", 64)
}

func (f *skinFixture) setProfile(t *testing.T, textureURL, model string) {
	t.Helper()
	f.setProfileWithSkin(t, textureURL, model, fixtureSkinPNG(t, 64))
}

func (f *skinFixture) setProfileWithSkin(t *testing.T, textureURL, model string, skin []byte) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.profileStatus = http.StatusOK
	f.skinURL = textureURL
	f.skinModel = model
	f.textureStatus = http.StatusOK
	f.textureBody = skin
	f.rebuildProfileLocked(t)
}

func (f *skinFixture) setCape(t *testing.T, capeURL string) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.capeURL = capeURL
	f.rebuildProfileLocked(t)
}

func (f *skinFixture) rebuildProfileLocked(t *testing.T) {
	t.Helper()
	textures := map[string]any{}
	if f.skinURL != "" {
		textures["SKIN"] = map[string]any{
			"url":      f.skinURL,
			"metadata": map[string]any{"model": f.skinModel},
		}
	}
	if f.capeURL != "" {
		textures["CAPE"] = map[string]any{"url": f.capeURL}
	}
	f.profileBody = makeProfilePayload(t, textures)
}

func makeProfilePayload(t *testing.T, textures map[string]any) []byte {
	t.Helper()
	inner, err := json.Marshal(map[string]any{"textures": textures})
	if err != nil {
		t.Fatalf("marshal inner textures: %v", err)
	}
	outer, err := json.Marshal(map[string]any{
		"id":         strings.ReplaceAll(testUUID, "-", ""),
		"properties": []map[string]string{{"name": "textures", "value": base64.StdEncoding.EncodeToString(inner)}},
	})
	if err != nil {
		t.Fatalf("marshal profile: %v", err)
	}
	return outer
}

func fixtureSkinPNG(t *testing.T, height int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 64, height))
	for y := 0; y < height; y++ {
		for x := 0; x < 64; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
		}
	}
	for dy := 0; dy < 8; dy++ {
		for dx := 0; dx < 8; dx++ {
			img.SetNRGBA(8+dx, 8+dy, color.NRGBA{R: 200, G: 210, B: 220, A: 255})
		}
	}
	// Distinct alpha values so alpha preservation is observable after scaling.
	img.SetNRGBA(8, 8, color.NRGBA{R: 255, G: 0, B: 0, A: 255})
	img.SetNRGBA(9, 8, color.NRGBA{R: 0, G: 255, B: 0, A: 128})
	img.SetNRGBA(8, 9, color.NRGBA{R: 0, G: 0, B: 255, A: 0})
	return encodeNRGBA(t, img)
}

func encodeNRGBA(t *testing.T, img *image.NRGBA) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	return buf.Bytes()
}

func newTestCache(t *testing.T, ttl time.Duration, client *http.Client) *Cache {
	t.Helper()
	c := NewCache(t.TempDir(), ttl, client)
	t.Cleanup(c.Close)
	return c
}

func (f *skinFixture) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.profileCalls, f.textureCalls, f.otherCalls
}

func (f *skinFixture) headers() []http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]http.Header, len(f.seenHeaders))
	copy(out, f.seenHeaders)
	return out
}
