// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

const testAPIKey = "curseforge-secret-key"

type observedHeaders struct {
	mu       sync.Mutex
	hits     int
	keys     []string
	auth     []string
	proxy    []string
	cookie   []string
	userAgts []string
}

func (o *observedHeaders) record(r *http.Request) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hits++
	o.keys = append(o.keys, r.Header.Get("X-Api-Key"))
	o.auth = append(o.auth, r.Header.Get("Authorization"))
	o.proxy = append(o.proxy, r.Header.Get("Proxy-Authorization"))
	o.cookie = append(o.cookie, r.Header.Get("Cookie"))
	o.userAgts = append(o.userAgts, r.Header.Get("User-Agent"))
}

func (o *observedHeaders) assertNoCredentials(t *testing.T, where string) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.hits == 0 {
		t.Fatalf("%s: expected a request, got none", where)
	}
	for i := 0; i < o.hits; i++ {
		if o.keys[i] != "" {
			t.Errorf("%s: x-api-key leaked: %q", where, o.keys[i])
		}
		if o.auth[i] != "" || o.proxy[i] != "" || o.cookie[i] != "" {
			t.Errorf("%s: credential header leaked (auth=%q proxy=%q cookie=%q)",
				where, o.auth[i], o.proxy[i], o.cookie[i])
		}
	}
}

func (o *observedHeaders) assertKeyOnEveryHit(t *testing.T, want string) {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.hits == 0 {
		t.Fatal("expected a request to the API server, got none")
	}
	for i := 0; i < o.hits; i++ {
		if o.keys[i] != want {
			t.Errorf("api hit %d x-api-key = %q, want %q", i, o.keys[i], want)
		}
		if o.userAgts[i] != testUserAgent {
			t.Errorf("api hit %d user-agent = %q, want %q", i, o.userAgts[i], testUserAgent)
		}
	}
}

const testUserAgent = "CarbonPanel-OSS-364-Test/1.0"

// TestCurseForgeKeyStrippedOnCrossOriginRedirect proves that a 307 from the
// authorized API origin to a different httptest "CDN" origin delivers the key
// to the API only, and no credential header to the destination.
func TestCurseForgeKeyStrippedOnCrossOriginRedirect(t *testing.T) {
	var cdn observedHeaders
	cdnServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdn.record(r)
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("cdn-body"))
	}))
	defer cdnServer.Close()

	var api observedHeaders
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		api.record(r)
		// Different port than the API server => different origin.
		w.Header().Set("Location", cdnServer.URL+"/download/file.zip")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer apiServer.Close()

	client, err := NewCurseForge(testAPIKey, testUserAgent, apiServer.URL)
	if err != nil {
		t.Fatalf("NewCurseForge: %v", err)
	}
	finalURL, err := client.FetchDownloadURL(context.Background(), apiServer.URL+"/start")
	if err != nil {
		t.Fatalf("FetchDownloadURL: %v", err)
	}

	api.assertKeyOnEveryHit(t, testAPIKey)
	cdn.assertNoCredentials(t, "redirect destination")
	if !strings.HasPrefix(finalURL, cdnServer.URL+"/download/file.zip") {
		t.Errorf("final URL = %q, want CDN URL prefix %q", finalURL, cdnServer.URL)
	}
}

// TestCurseForgeFetchDownloadURLNoKeyToArbitraryURL is the direct proof that
// an operator-supplied URL cannot harvest the key: FetchDownloadURL uses the
// same credential-scoped http.Client as the API calls.
func TestCurseForgeFetchDownloadURLNoKeyToArbitraryURL(t *testing.T) {
	var dest observedHeaders
	destServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dest.record(r)
		_, _ = w.Write([]byte("file"))
	}))
	defer destServer.Close()

	// The client is configured for a DIFFERENT API origin than the URL passed
	// to FetchDownloadURL. If the key were attached at request-build time it
	// would arrive here; the scoped transport must omit it.
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request to API origin: %s", r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiServer.Close()

	client, err := NewCurseForge(testAPIKey, testUserAgent, apiServer.URL)
	if err != nil {
		t.Fatalf("NewCurseForge: %v", err)
	}
	rawURL := destServer.URL + "/arbitrary/operator-supplied.zip"
	resolved, err := client.FetchDownloadURL(context.Background(), rawURL)
	if err != nil {
		t.Fatalf("FetchDownloadURL(%q): %v", rawURL, err)
	}
	dest.assertNoCredentials(t, "arbitrary download URL")
	if !strings.HasPrefix(resolved, rawURL) {
		t.Errorf("resolved URL = %q, want prefix %q", resolved, rawURL)
	}
}

// TestScopedKeyTransportOriginMatch exercises the origin matcher directly,
// including scheme+host+port (not just host) and default-port canonicalization.
func TestScopedKeyTransportOriginMatch(t *testing.T) {
	apiOrigin := originKey(mustParseURL(t, "https://api.curseforge.com"))
	transport := &scopedKeyTransport{apiKey: testAPIKey, apiOrigin: apiOrigin}

	if key, err := transport.keyFor(mustParseURL(t, "https://api.curseforge.com/v1/categories")); err != nil || key != testAPIKey {
		t.Errorf("keyFor(API URL) = %q, %v; want key, nil", key, err)
	}
	if key, err := transport.keyFor(mustParseURL(t, "https://api.curseforge.com:443/v1/categories")); err != nil || key != testAPIKey {
		t.Errorf("keyFor(default-port API URL) = %q, %v; want key, nil", key, err)
	}
	for _, other := range []string{
		"http://api.curseforge.com/v1/categories",       // scheme change
		"https://api.curseforge.com:8443/v1/categories", // port change
		"https://cdn.curseforge.com/file.zip",           // host change
	} {
		key, err := transport.keyFor(mustParseURL(t, other))
		if key != "" || !errors.Is(err, ErrCredentialBoundary) {
			t.Errorf("keyFor(%q) = %q, %v; want empty key and ErrCredentialBoundary", other, key, err)
		}
	}
}

// TestCurseForgeEmptyAPIKeyRejected ensures credentials cannot be silently
// omitted through a misconfigured constructor.
func TestCurseForgeEmptyAPIKeyRejected(t *testing.T) {
	if _, err := NewCurseForge("  ", testUserAgent, ""); err == nil {
		t.Fatal("expected an error for an empty api key")
	} else if !errors.Is(err, ErrCredentialBoundary) {
		t.Errorf("error = %v, want ErrCredentialBoundary", err)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url.Parse(%q): %v", raw, err)
	}
	return u
}
