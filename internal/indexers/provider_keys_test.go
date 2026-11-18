// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package indexers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// withFixtureBases points the provider credential endpoints at local fixture
// servers and restores the real base URLs when the test finishes.
func withFixtureBases(t *testing.T, curseforge, modrinth string) {
	t.Helper()
	oldCF, oldMR := curseforgeAPIBase, modrinthAPIBase
	curseforgeAPIBase, modrinthAPIBase = curseforge, modrinth
	t.Cleanup(func() {
		curseforgeAPIBase, modrinthAPIBase = oldCF, oldMR
	})
}

func newCaptureServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *[]*http.Request) {
	t.Helper()
	captured := &[]*http.Request{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*captured = append(*captured, r)
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func TestValidateProviderKeyCurseForgeSoftResults(t *testing.T) {
	cases := []struct {
		name        string
		categories  int
		search      int
		searchOK    bool
		wantValid   bool
		wantMessage string
	}{
		{name: "empty key", wantMessage: "CurseForge API key is empty"},
		{name: "rejected", categories: http.StatusForbidden, wantMessage: "CurseForge rejected API key (403 Forbidden). Please verify your key at https://console.curseforge.com/#/api-keys."},
		{name: "approved for search", categories: http.StatusOK, searchOK: true, search: http.StatusOK, wantValid: true, wantMessage: "Connected successfully! CurseForge API key is valid and approved for mod searches."},
		{name: "keyless community proxy", categories: http.StatusOK, searchOK: true, search: http.StatusForbidden, wantValid: true, wantMessage: "CurseForge API key accepted, but Overwolf restricts direct mod search (403). Carbon Panel will automatically use the keyless community proxy for mod searches!"},
		{name: "plain valid", categories: http.StatusOK, searchOK: true, search: http.StatusInternalServerError, wantValid: true, wantMessage: "Connected successfully! CurseForge API key is valid."},
		{name: "search request failure stays valid", categories: http.StatusOK, searchOK: false, wantValid: true, wantMessage: "Connected successfully! CurseForge API key is valid."},
		{name: "other status", categories: http.StatusTeapot, wantMessage: "CurseForge returned unexpected status 418"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream, requests := newCaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/categories" {
					w.WriteHeader(tc.categories)
					return
				}
				if !tc.searchOK {
					// Simulate a connection failure for the search probe by
					// hijacking and closing the connection.
					hj, ok := w.(http.Hijacker)
					if !ok {
						t.Fatal("hijack unsupported")
					}
					conn, _, err := hj.Hijack()
					if err != nil {
						t.Fatal(err)
					}
					_ = conn.Close()
					return
				}
				w.WriteHeader(tc.search)
			})
			withFixtureBases(t, upstream.URL, "http://127.0.0.1:1")

			key := "  cf-key  "
			if tc.name == "empty key" {
				key = ""
			}
			result := ValidateProviderKey(context.Background(), "curseforge", key, "")
			if result.Valid != tc.wantValid {
				t.Fatalf("valid=%v want %v (message %q)", result.Valid, tc.wantValid, result.Message)
			}
			if result.Message != tc.wantMessage {
				t.Fatalf("message=%q want %q", result.Message, tc.wantMessage)
			}
			_ = requests
		})
	}
}

func TestValidateProviderKeyModrinthSoftResults(t *testing.T) {
	t.Run("authenticated username", func(t *testing.T) {
		upstream, _ := newCaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v2/user" {
				t.Errorf("unexpected path %q", r.URL.Path)
			}
			if got := r.Header.Get("User-Agent"); got != "Fixture/1.0" {
				t.Errorf("user agent %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "mr-token" {
				t.Errorf("authorization %q", got)
			}
			_, _ = w.Write([]byte(`{"username":"ops-admin"}`))
		})
		withFixtureBases(t, "http://127.0.0.1:1", upstream.URL)

		result := ValidateProviderKey(context.Background(), "Modrinth", "mr-token", "Fixture/1.0")
		if !result.Valid || result.Message != "Authenticated successfully as ops-admin on Modrinth." {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("rejected token", func(t *testing.T) {
		upstream, _ := newCaptureServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		})
		withFixtureBases(t, "http://127.0.0.1:1", upstream.URL)

		result := ValidateProviderKey(context.Background(), "modrinth", "bad-token", "")
		if result.Valid || result.Message != "Modrinth rejected Personal Access Token (401 Unauthorized)." {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("anonymous public access", func(t *testing.T) {
		upstream, _ := newCaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v2/search" {
				t.Errorf("unexpected path %q", r.URL.Path)
			}
			if got := r.Header.Get("User-Agent"); got != "CarbonPanel/1.0 (CarbonPanel-admin)" {
				t.Errorf("default user agent %q", got)
			}
		})
		withFixtureBases(t, "http://127.0.0.1:1", upstream.URL)

		result := ValidateProviderKey(context.Background(), "modrinth", "", "")
		if !result.Valid || result.Message != "Modrinth API connected successfully (public access)." {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("other status", func(t *testing.T) {
		upstream, _ := newCaptureServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		withFixtureBases(t, "http://127.0.0.1:1", upstream.URL)

		result := ValidateProviderKey(context.Background(), "modrinth", "tok", "")
		if result.Valid || result.Message != "Modrinth returned status 503" {
			t.Fatalf("result=%+v", result)
		}
	})
}

func TestValidateProviderKeyUnsupportedProvider(t *testing.T) {
	result := ValidateProviderKey(context.Background(), "unknown", "key", "")
	if result.Valid || result.Message != "Unsupported provider: unknown" {
		t.Fatalf("result=%+v", result)
	}
}

// TestValidateProviderKeyRedirectNeverCarriesCredentials pins the credential
// guard on the raw validator's client with a real redirect: a cross-origin
// (cross-port) redirect must strip the credential headers, while a same-origin
// redirect keeps them.
func TestValidateProviderKeyRedirectNeverCarriesCredentials(t *testing.T) {
	var seen *[]*http.Request
	var destination string
	upstream, requests := newCaptureServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/categories" {
			http.Redirect(w, r, destination, http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	seen = requests

	// A second server on a different port is a different origin for the
	// credential policy.
	other, otherRequests := newCaptureServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	withFixtureBases(t, upstream.URL, "http://127.0.0.1:1")

	t.Run("cross-origin strips credentials", func(t *testing.T) {
		*otherRequests = nil
		destination = other.URL + "/v1/mods/search?gameId=432&pageSize=1"
		result := ValidateProviderKey(context.Background(), "curseforge", "secret-key", "")
		if !result.Valid {
			t.Fatalf("expected the redirect chain to complete as valid: %+v", result)
		}
		if len(*otherRequests) != 1 {
			t.Fatalf("redirect target saw %d requests", len(*otherRequests))
		}
		if got := (*otherRequests)[0].Header.Get("X-Api-Key"); got != "" {
			t.Fatalf("cross-origin redirect carried the API key: %q", got)
		}
	})

	t.Run("same-origin keeps credentials", func(t *testing.T) {
		*seen = nil
		destination = upstream.URL + "/v1/mods/search?gameId=432&pageSize=1"
		result := ValidateProviderKey(context.Background(), "curseforge", "secret-key", "")
		if !result.Valid {
			t.Fatalf("expected the redirect chain to complete as valid: %+v", result)
		}
		if len(*seen) == 0 {
			t.Fatal("fixture never saw the same-origin redirect chain")
		}
		for _, req := range *seen {
			if got := req.Header.Get("X-Api-Key"); got != "secret-key" {
				t.Fatalf("same-origin redirect lost the API key on %s: %q", req.URL.Path, got)
			}
		}
	})
}
