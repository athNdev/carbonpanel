// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Upstream endpoints are compile-time constants. The unexported vars below are
// identical in production and are overridden only by same-package httptest
// fixtures; they are deliberately absent from the public API so no caller can
// turn the proxy into an open URL fetcher.
const (
	profileBaseURLConst = "https://sessionserver.mojang.com/session/minecraft/profile/"
	// nameBaseURL is the verified first-party Minecraft Services name -> profile
	// endpoint. It resolves an exact current username to its online UUID; it is
	// NOT a name-history service (Mojang removed public name history on
	// 2022-09-13). Verified 200 with canonical {id,name} shape on 2026-11-18.
	nameBaseURLConst       = "https://api.minecraftservices.com/minecraft/profile/lookup/name/"
	textureBaseURLConst    = "https://textures.minecraft.net/texture/"
	pinnedTextureHostConst = "textures.minecraft.net"

	contentTypePNG = "image/png"

	maxProfileBytes = 2 << 20 // 2 MiB
	maxTextureBytes = 4 << 20 // 4 MiB
	maxRedirects    = 3
)

var (
	profileBaseURL    = profileBaseURLConst
	nameBaseURL       = nameBaseURLConst
	textureBaseURL    = textureBaseURLConst
	pinnedTextureHost = pinnedTextureHostConst

	textureHashPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

	errUnsafeTexture = errors.New("skin unavailable: identity profile contained an unsafe texture reference")
	errNoSkinTexture = errors.New("skin unavailable: identity profile has no skin texture")
	errNoCapeTexture = errors.New("skin unavailable: identity profile has no cape texture")
	errNoTexture     = errors.New("skin unavailable: texture is not available upstream")
	errTooLarge      = errors.New("skin unavailable: upstream response exceeded the size limit")
	errBadPNG        = errors.New("skin unavailable: texture is not a valid PNG")
	errBadProfile    = errors.New("skin unavailable: identity service returned an unreadable profile")
	errUpstream      = errors.New("skin unavailable: identity or texture service request failed")
	errRedirect      = errors.New("skin unavailable: upstream tried to redirect to another host")
)

// profilePayload accepts both the real Mojang shape (a base64 "textures"
// property) and a direct "textures" object used by simple fixtures.
type profilePayload struct {
	Name       string            `json:"name"`
	ID         string            `json:"id"`
	Textures   textureSet        `json:"textures"`
	Properties []profileProperty `json:"properties"`
}

type profileProperty struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type textureSet struct {
	SKIN *textureEntry `json:"SKIN"`
	CAPE *textureEntry `json:"CAPE"`
}

type textureEntry struct {
	URL      string `json:"url"`
	Metadata struct {
		Model string `json:"model"`
	} `json:"metadata"`
}

// fetchResult is internal: note != "" means unavailable, otherwise asset is a
// fully rendered/validated PNG. negative marks definitive 204/404 outcomes
// that may be kept in the short-lived negative cache.
type fetchResult struct {
	asset    Asset
	note     string
	negative bool
}

func unavailableResult(err error) fetchResult {
	return fetchResult{note: err.Error()}
}

func negativeResult(err error) fetchResult {
	return fetchResult{note: err.Error(), negative: true}
}

// fetchAndCache performs one upstream lookup and persists the outcome.
func (c *Cache) fetchAndCache(ctx context.Context, uuid, kind string) Asset {
	result := c.fetch(ctx, uuid, kind)
	now := c.clock.Now()
	if result.note != "" {
		a := unavailable(result.note, SourceIdentityService, false, now)
		if result.negative {
			c.write(uuid, kind, entryFromAsset(a, kind, true))
		}
		return a
	}
	a := result.asset
	c.write(uuid, kind, entryFromAsset(a, kind, false))
	return a
}

func entryFromAsset(a Asset, kind string, negative bool) cacheEntry {
	return cacheEntry{
		Version:     cacheVersion,
		Kind:        kind,
		Data:        a.Data,
		ContentType: a.ContentType,
		Source:      a.Source,
		ObservedAt:  a.ObservedAt,
		Confidence:  a.Confidence,
		Note:        a.Note,
		Negative:    negative,
	}
}

func (c *Cache) fetch(ctx context.Context, uuid, kind string) fetchResult {
	profile, status, err := c.getProfile(ctx, uuid)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return unavailableResult(errTooLarge)
		}
		return unavailableResult(errUpstream)
	}
	switch status {
	case http.StatusNoContent:
		return negativeResult(errors.New("skin unavailable: identity service reports no texture for this player"))
	case http.StatusNotFound:
		return negativeResult(errors.New("skin unavailable: identity profile not found"))
	case http.StatusOK:
		// continue
	default:
		return unavailableResult(errors.New("skin unavailable: identity service returned an unexpected status"))
	}

	hash, err := selectTextureHash(profile, kind)
	if err != nil {
		return unavailableResult(err)
	}
	// Rebuild the texture URL from the pinned base and the validated hash. The
	// URL from the payload is never used to make a request.
	textureURL := textureBaseURL + hash
	data, status, err := c.getTexture(ctx, textureURL)
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return unavailableResult(errTooLarge)
		}
		return unavailableResult(errUpstream)
	}
	if status == http.StatusNoContent || status == http.StatusNotFound {
		return negativeResult(errNoTexture)
	}
	if status != http.StatusOK {
		return unavailableResult(errors.New("skin unavailable: texture download returned an unexpected status"))
	}
	if int64(len(data)) > maxTextureBytes {
		return unavailableResult(errTooLarge)
	}
	img, err := decodePNG(data)
	if err != nil {
		return unavailableResult(errBadPNG)
	}

	a := Asset{
		Data:        data,
		ContentType: contentTypePNG,
		Source:      SourceIdentityService,
		ObservedAt:  c.clock.Now(),
		Confidence:  ConfidenceDetected,
	}
	if kind == KindFace {
		// The slim model differs only in arm width; its 8x8 face crop at
		// (8,8) is identical to the classic model, so no model branching is
		// needed here.
		rendered, err := renderFace(img)
		if err != nil {
			return unavailableResult(errBadPNG)
		}
		a.Data = rendered
	}
	return fetchResult{asset: a}
}

func (c *Cache) getProfile(ctx context.Context, uuid string) (profilePayload, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, profileBaseURL+uuid, nil)
	if err != nil {
		return profilePayload{}, 0, err
	}
	resp, err := c.clientFor(expectedHost(profileBaseURL)).Do(req)
	if err != nil {
		return profilePayload{}, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return profilePayload{}, resp.StatusCode, nil
	}
	body, err := readBounded(resp.Body, maxProfileBytes)
	if err != nil {
		return profilePayload{}, 0, err
	}
	var payload profilePayload
	if json.Unmarshal(body, &payload) != nil {
		return profilePayload{}, 0, errBadProfile
	}
	return payload, http.StatusOK, nil
}

func (c *Cache) getTexture(ctx context.Context, textureURL string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, textureURL, nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.clientFor(expectedHost(textureBaseURL)).Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode, nil
	}
	body, err := readBounded(resp.Body, maxTextureBytes)
	if err != nil {
		return nil, 0, err
	}
	return body, http.StatusOK, nil
}

// selectTextureHash validates the profile payload and returns only a lowercase
// 64-hex CDN hash. Nothing from the payload other than that hash is used.
func selectTextureHash(payload profilePayload, kind string) (string, error) {
	textures := payload.Textures
	if textures.SKIN == nil && textures.CAPE == nil {
		for _, prop := range payload.Properties {
			if !strings.EqualFold(prop.Name, "textures") {
				continue
			}
			decoded, err := decodeBase64(prop.Value)
			if err != nil {
				return "", errUnsafeTexture
			}
			var inner profilePayload
			if json.Unmarshal(decoded, &inner) != nil {
				return "", errUnsafeTexture
			}
			textures = inner.Textures
			break
		}
	}

	var entry *textureEntry
	switch kind {
	case KindFace, KindSkin:
		entry = textures.SKIN
		if entry == nil {
			return "", errNoSkinTexture
		}
	case KindCape:
		entry = textures.CAPE
		if entry == nil {
			return "", errNoCapeTexture
		}
	default:
		return "", errUnsafeTexture
	}
	return validateTextureURL(entry.URL)
}

func validateTextureURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", errUnsafeTexture
	}
	base, err := url.Parse(textureBaseURL)
	if err != nil {
		return "", errUnsafeTexture
	}
	if u.Scheme == "" || !strings.EqualFold(u.Scheme, base.Scheme) {
		return "", errUnsafeTexture
	}
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errUnsafeTexture
	}
	if u.Host != pinnedTextureHost {
		return "", errUnsafeTexture
	}
	const prefix = "/texture/"
	if !strings.HasPrefix(u.Path, prefix) {
		return "", errUnsafeTexture
	}
	hash := strings.TrimPrefix(u.Path, prefix)
	if strings.Contains(hash, "/") || strings.ContainsAny(hash, "\\") || !textureHashPattern.MatchString(hash) {
		return "", errUnsafeTexture
	}
	// path.Base is used only as an extra confinement assertion; the hash
	// regex above already forbids separators.
	if path.Base(hash) != hash {
		return "", errUnsafeTexture
	}
	return strings.ToLower(hash), nil
}

func decodeBase64(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if decoded, err := enc.DecodeString(value); err == nil {
			return decoded, nil
		}
	}
	return nil, errors.New("invalid base64")
}

func readBounded(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, errTooLarge
	}
	return data, nil
}

func expectedHost(rawBase string) string {
	u, err := url.Parse(rawBase)
	if err != nil {
		return ""
	}
	return u.Host
}

// clientFor copies the caller's client without mutating it, removes its cookie
// jar, sanitises credentials on every hop and refuses cross-host redirects.
func (c *Cache) clientFor(expectedHost string) *http.Client {
	base := c.client
	if base == nil {
		base = &http.Client{}
	}
	hc := *base
	hc.Jar = nil
	// Only a plain *http.Transport is honoured (its TLS/proxy settings are
	// cloned). A custom RoundTripper wrapper is deliberately ignored: it could
	// re-add cookies or tokens after our sanitising layer runs.
	transport := http.RoundTripper(http.DefaultTransport)
	if t, ok := base.Transport.(*http.Transport); ok {
		transport = t.Clone()
	}
	hc.Transport = sanitizingTransport{base: transport}
	hc.CheckRedirect = sameHostRedirect(expectedHost)
	return &hc
}

// sanitizingTransport guarantees that no Cookie, Authorization or
// Proxy-Authorization header reaches an upstream, even if the caller supplied
// a custom transport that would otherwise add one.
type sanitizingTransport struct {
	base http.RoundTripper
}

func (t sanitizingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header.Del("Cookie")
	clone.Header.Del("Authorization")
	clone.Header.Del("Proxy-Authorization")
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(clone)
}

func sameHostRedirect(expected string) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxRedirects {
			return errRedirect
		}
		if req.URL.User != nil {
			req.URL.User = nil
		}
		req.Header.Del("Cookie")
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
		if req.URL.Host != expected {
			return errRedirect
		}
		return nil
	}
}
