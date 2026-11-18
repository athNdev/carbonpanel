// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

func nrgbaAt(t *testing.T, img image.Image, x, y int) color.NRGBA {
	t.Helper()
	c, ok := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	if !ok {
		t.Fatalf("unexpected color type at (%d,%d)", x, y)
	}
	return c
}

func TestSSRFInvalidUUIDAndKind(t *testing.T) {
	f := newSkinFixture(t)
	c := newTestCache(t, time.Hour, nil)
	attacks := []struct{ uuid, kind string }{
		{"http://127.0.0.1/foo", KindFace},
		{"../../etc/passwd", KindFace},
		{"not-a-uuid", KindFace},
		{testUUID, "http://evil.example/x"},
		{testUUID, "face/../../etc"},
		{testUUID, "FACE"},
		{testUUID, ""},
	}
	for _, a := range attacks {
		if _, err := c.Get(context.Background(), a.uuid, a.kind); err == nil {
			t.Errorf("Get(%q, %q) unexpectedly accepted", a.uuid, a.kind)
		}
		if _, err := c.Refresh(context.Background(), a.uuid, a.kind); err == nil {
			t.Errorf("Refresh(%q, %q) unexpectedly accepted", a.uuid, a.kind)
		}
	}
	if p, tx, other := f.counts(); p != 0 || tx != 0 || other != 0 {
		t.Fatalf("invalid input caused network calls: %d %d %d", p, tx, other)
	}
}

func TestSSRFTextureHostAndHashInjection(t *testing.T) {
	f := newSkinFixture(t)
	scheme := "http"
	if u, err := url.Parse(textureBaseURL); err == nil {
		scheme = u.Scheme
	}
	hash := strings.Repeat("a", 64)
	host := pinnedTextureHost
	bad := []string{
		scheme + "://evil.example/texture/" + hash,
		scheme + "://" + host + ".evil.example/texture/" + hash,
		scheme + "://user:pass@" + host + "/texture/" + hash,
		scheme + "://" + host + "/texture/" + hash + "?token=SECRET_TOKEN",
		scheme + "://" + host + "/texture/" + hash + "#frag",
		scheme + "://" + host + "/texture/" + hash + "/../evil",
		scheme + "://" + host + "/texture/" + strings.Repeat("g", 64),
		scheme + "://" + host + "/texture/" + hash[:63],
		scheme + "://" + host + "/other/" + hash,
		"javascript:alert(1)",
	}
	for _, raw := range bad {
		f.setProfile(t, raw, "")
		c := newTestCache(t, time.Hour, nil)
		_, txBefore, _ := f.counts()
		a, err := c.Get(context.Background(), testUUID, KindFace)
		if err != nil {
			t.Fatalf("Get with texture URL %q: %v", raw, err)
		}
		if !strings.Contains(a.Note, "skin unavailable") || a.Confidence != ConfidenceUnknown {
			t.Errorf("URL %q produced asset %+v", raw, a)
		}
		if strings.Contains(a.Note, "SECRET_TOKEN") || strings.Contains(a.Note, "://") {
			t.Errorf("URL %q leaked into note: %q", raw, a.Note)
		}
		if _, err := decodePNG(a.Data); err != nil {
			t.Errorf("URL %q did not yield a valid silhouette PNG: %v", raw, err)
		}
		_, txAfter, _ := f.counts()
		if txAfter != txBefore {
			t.Errorf("unsafe URL %q caused a texture fetch (%d -> %d)", raw, txBefore, txAfter)
		}
	}
}

func TestSSRFCrossHostRedirectRefused(t *testing.T) {
	f := newSkinFixture(t)
	c := newTestCache(t, time.Hour, nil)

	f.mu.Lock()
	f.redirectProfileTo = f.other.URL
	f.mu.Unlock()
	first, err := c.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("profile redirect Get: %v", err)
	}
	if !strings.Contains(first.Note, "skin unavailable") {
		t.Fatalf("profile redirect asset = %+v", first)
	}

	f.mu.Lock()
	f.redirectProfileTo = ""
	f.redirectTextureTo = f.other.URL
	f.mu.Unlock()
	second, err := c.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("texture redirect Get: %v", err)
	}
	if !strings.Contains(second.Note, "skin unavailable") {
		t.Fatalf("texture redirect asset = %+v", second)
	}
	if _, _, other := f.counts(); other != 0 {
		t.Fatalf("cross-host redirect reached the other host %d times", other)
	}
}

func TestOversizedBodiesBounded(t *testing.T) {
	f := newSkinFixture(t)
	f.mu.Lock()
	f.profileStatus = http.StatusOK
	f.profileBody = bytes.Repeat([]byte("A"), maxProfileBytes+1)
	f.mu.Unlock()
	c := newTestCache(t, time.Hour, nil)
	a, err := c.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("oversized profile Get: %v", err)
	}
	if !strings.Contains(a.Note, "size limit") {
		t.Fatalf("oversized profile note = %q", a.Note)
	}
	if _, tx, _ := f.counts(); tx != 0 {
		t.Fatalf("oversized profile still fetched a texture %d times", tx)
	}

	f.setProfileWithSkin(t, f.textureURL(), "", bytes.Repeat([]byte("P"), maxTextureBytes+1))
	c2 := newTestCache(t, time.Hour, nil)
	b, err := c2.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("oversized texture Get: %v", err)
	}
	if !strings.Contains(b.Note, "size limit") {
		t.Fatalf("oversized texture note = %q", b.Note)
	}
}

// credentialInjectingTransport simulates a caller-supplied client that adds
// headers; the cache must strip them before any upstream request.
type credentialInjectingTransport struct{}

func (credentialInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	r := req.Clone(req.Context())
	r.Header.Set("Cookie", "session=leak")
	r.Header.Set("Authorization", "Bearer leak")
	r.Header.Set("Proxy-Authorization", "Basic leak")
	return http.DefaultTransport.RoundTrip(r)
}

func TestNoCredentialsForwarded(t *testing.T) {
	f := newSkinFixture(t)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatalf("parse fixture URL: %v", err)
	}
	jar.SetCookies(u, []*http.Cookie{{Name: "panel", Value: "leak"}})
	client := &http.Client{Transport: credentialInjectingTransport{}, Jar: jar}

	// Same-host redirect so CheckRedirect is exercised as well.
	f.mu.Lock()
	f.redirectTextureTo = f.textureURL()
	f.mu.Unlock()

	c := newTestCache(t, time.Hour, client)
	if _, err := c.Get(context.Background(), testUUID, KindFace); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if client.Jar == nil || client.CheckRedirect != nil {
		t.Fatal("caller's http.Client was mutated")
	}
	for _, h := range f.headers() {
		for _, name := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
			if h.Get(name) != "" {
				t.Errorf("upstream received %s: %q", name, h.Get(name))
			}
		}
		for _, values := range h {
			for _, v := range values {
				if strings.Contains(v, "leak") {
					t.Errorf("credential leaked upstream in header: %q", v)
				}
			}
		}
	}
}

func TestFaceRendererDimensionsAlphaAndNearestNeighbour(t *testing.T) {
	f := newSkinFixture(t)
	for _, height := range []int{32, 64} {
		f.setProfileWithSkin(t, f.textureURL(), "", fixtureSkinPNG(t, height))
		c := newTestCache(t, time.Hour, nil)
		a, err := c.Get(context.Background(), testUUID, KindFace)
		if err != nil {
			t.Fatalf("Get(face, %dpx skin): %v", height, err)
		}
		if a.ContentType != contentTypePNG || a.Confidence != ConfidenceDetected {
			t.Fatalf("face asset metadata = %+v", a)
		}
		img, err := png.Decode(bytes.NewReader(a.Data))
		if err != nil {
			t.Fatalf("decode face PNG: %v", err)
		}
		if img.Bounds().Dx() != 96 || img.Bounds().Dy() != 96 {
			t.Fatalf("face dimensions = %v", img.Bounds())
		}
		if got := nrgbaAt(t, img, 0, 0); got != (color.NRGBA{R: 255, G: 0, B: 0, A: 255}) {
			t.Errorf("src (8,8) red -> dst (0,0) = %v", got)
		}
		if got := nrgbaAt(t, img, 11, 11); got != (color.NRGBA{R: 255, G: 0, B: 0, A: 255}) {
			t.Errorf("nearest-neighbour block boundary = %v", got)
		}
		if got := nrgbaAt(t, img, 12, 0); got != (color.NRGBA{R: 0, G: 255, B: 0, A: 128}) {
			t.Errorf("src (9,8) half-alpha green -> dst (12,0) = %v", got)
		}
		if got := nrgbaAt(t, img, 0, 12); got.A != 0 {
			t.Errorf("src (8,9) transparent -> dst (0,12) alpha = %d", got.A)
		}
		if got := nrgbaAt(t, img, 95, 95); got != (color.NRGBA{R: 200, G: 210, B: 220, A: 255}) {
			t.Errorf("src (15,15) -> dst (95,95) = %v", got)
		}
	}
}

func TestSlimModelUsesSameFaceCrop(t *testing.T) {
	f := newSkinFixture(t)
	faces := map[string][]byte{}
	for _, model := range []string{"slim", "classic"} {
		f.setProfile(t, f.textureURL(), model)
		c := newTestCache(t, time.Hour, nil)
		a, err := c.Get(context.Background(), testUUID, KindFace)
		if err != nil {
			t.Fatalf("Get(%s): %v", model, err)
		}
		faces[model] = a.Data
	}
	if !bytes.Equal(faces["slim"], faces["classic"]) {
		t.Fatal("slim and classic face crops differ")
	}
}

func TestRawSkinCapeAndMissingCape(t *testing.T) {
	f := newSkinFixture(t)
	f.setCape(t, f.textureURL())
	raw := bytes.Clone(f.textureBody)
	c := newTestCache(t, time.Hour, nil)
	skin, err := c.Get(context.Background(), testUUID, KindSkin)
	if err != nil {
		t.Fatalf("Get(skin): %v", err)
	}
	cape, err := c.Get(context.Background(), testUUID, KindCape)
	if err != nil {
		t.Fatalf("Get(cape): %v", err)
	}
	face, err := c.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("Get(face): %v", err)
	}
	if !bytes.Equal(skin.Data, raw) || !bytes.Equal(cape.Data, raw) {
		t.Fatal("skin/cape did not return the bounded raw PNG")
	}
	if bytes.Equal(face.Data, raw) {
		t.Fatal("face should be the rendered crop, not the raw skin")
	}

	f.setCape(t, "")
	c2 := newTestCache(t, time.Hour, nil)
	missing, err := c2.Get(context.Background(), testUUID, KindCape)
	if err != nil {
		t.Fatalf("Get(missing cape): %v", err)
	}
	if !strings.Contains(missing.Note, "no cape texture") || missing.Confidence != ConfidenceUnknown {
		t.Fatalf("missing cape asset = %+v", missing)
	}
}

func TestUnavailableSilhouetteIsRealPNG(t *testing.T) {
	a := unavailable("skin unavailable: test", SourcePanelDB, false, time.Now().UTC())
	if a.ContentType != contentTypePNG || a.Confidence != ConfidenceUnknown {
		t.Fatalf("unavailable asset metadata = %+v", a)
	}
	img, err := png.Decode(bytes.NewReader(a.Data))
	if err != nil {
		t.Fatalf("silhouette is not a PNG: %v", err)
	}
	if img.Bounds().Dx() != 96 || img.Bounds().Dy() != 96 {
		t.Fatalf("silhouette bounds = %v", img.Bounds())
	}
	if nrgbaAt(t, img, 0, 0).A != 0 {
		t.Error("silhouette background should be transparent")
	}
	opaque := 0
	for y := 0; y < 96; y++ {
		for x := 0; x < 96; x++ {
			if nrgbaAt(t, img, x, y).A > 0 {
				opaque++
			}
		}
	}
	if opaque < 1000 {
		t.Fatalf("silhouette has only %d opaque pixels", opaque)
	}
}

func TestRedactionOfNotesAndErrors(t *testing.T) {
	f := newSkinFixture(t)
	scheme := "http"
	if u, err := url.Parse(textureBaseURL); err == nil {
		scheme = u.Scheme
	}
	f.setProfile(t, scheme+"://"+pinnedTextureHost+"/texture/"+strings.Repeat("a", 64)+"?token=SECRET_TOKEN", "")
	c := newTestCache(t, time.Hour, nil)
	a, err := c.Get(context.Background(), testUUID, KindFace)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(a.Note, "SECRET_TOKEN") || strings.Contains(a.Note, "token=") {
		t.Fatalf("secret leaked into note: %q", a.Note)
	}
	_, err = c.Get(context.Background(), "http://evil.example/SECRET_UUID", KindFace)
	if err == nil || strings.Contains(err.Error(), "SECRET_UUID") {
		t.Fatalf("invalid UUID error leaked input: %v", err)
	}
}
