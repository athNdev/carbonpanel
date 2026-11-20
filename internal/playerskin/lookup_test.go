// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playerskin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// lookupFixture serves both first-party identity endpoints from one local
// host. All overrides are restored by t.Cleanup; tests in this package must
// not call t.Parallel (same constraint as the existing skin fixtures).
type lookupFixture struct {
	profileStatus int
	profileBody   string
	nameStatus    int
	nameBody      string
	redirectTo    string
	seenPaths     []string
}

func newLookupFixture(t *testing.T) *lookupFixture {
	t.Helper()
	f := &lookupFixture{profileStatus: http.StatusOK, nameStatus: http.StatusOK}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.seenPaths = append(f.seenPaths, r.URL.Path)
		if f.redirectTo != "" {
			http.Redirect(w, r, f.redirectTo, http.StatusFound)
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/session/minecraft/profile/"):
			w.WriteHeader(f.profileStatus)
			_, _ = w.Write([]byte(f.profileBody))
		case strings.HasPrefix(r.URL.Path, "/users/profiles/minecraft/"):
			w.WriteHeader(f.nameStatus)
			_, _ = w.Write([]byte(f.nameBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	oldProfile, oldName := profileBaseURL, nameBaseURL
	profileBaseURL = srv.URL + "/session/minecraft/profile/"
	nameBaseURL = srv.URL + "/users/profiles/minecraft/"
	t.Cleanup(func() { profileBaseURL, nameBaseURL = oldProfile, oldName })
	return f
}

func TestNormalizeQueryDisjointExactForms(t *testing.T) {
	for _, tc := range []struct{ in, kind, value string }{
		{"00112233-4455-6677-8899-aabbccddeeff", "uuid", "00112233-4455-6677-8899-aabbccddeeff"},
		{"00112233445566778899aabbccddeeff", "uuid", "00112233-4455-6677-8899-aabbccddeeff"},
		{"Steve", "name", "Steve"},
		{"Player_123", "name", "Player_123"},
		{"not a name!", "invalid", ""},
		{"", "invalid", ""},
		{"ThisNameIsWayTooLong123", "invalid", ""},
	} {
		kind, value := NormalizeQuery(tc.in)
		if kind != tc.kind || value != tc.value {
			t.Errorf("NormalizeQuery(%q) = (%q,%q), want (%q,%q)", tc.in, kind, value, tc.kind, tc.value)
		}
	}
}

func TestLookupByUUIDUsesSessionProfileName(t *testing.T) {
	f := newLookupFixture(t)
	f.profileBody = `{"name":"CurrentName","id":"00112233445566778899aabbccddeeff","properties":[]}`
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), testUUID)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != LookupFound || res.UUID != testUUID || res.Name != "CurrentName" {
		t.Fatalf("result = %+v", res)
	}
}

func TestLookupByNameUsesFirstPartyNameEndpoint(t *testing.T) {
	f := newLookupFixture(t)
	f.nameBody = `{"name":"Steve","id":"00112233445566778899aabbccddeeff"}`
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), "Steve")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != LookupFound || res.UUID != testUUID || res.Name != "Steve" {
		t.Fatalf("result = %+v", res)
	}
}

func TestLookupUUIDMismatchedProfileIsAmbiguous(t *testing.T) {
	f := newLookupFixture(t)
	f.profileBody = `{"name":"OtherName","id":"ffffffff-ffff-ffff-ffff-ffffffffffff"}`
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), testUUID)
	if err != nil || res.Status != LookupAmbiguous || res.Name != "" || res.UUID != "" {
		t.Fatalf("result = %+v err=%v", res, err)
	}
}

func TestLookupNotFoundIsDefinitive(t *testing.T) {
	f := newLookupFixture(t)
	f.nameStatus = http.StatusNotFound
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), "Ghost")
	if err != nil || res.Status != LookupNotFound || res.UUID != "" {
		t.Fatalf("result = %+v err=%v", res, err)
	}
}

func TestLookupRateLimitIsUnavailableNotAbsent(t *testing.T) {
	f := newLookupFixture(t)
	f.nameStatus = http.StatusTooManyRequests
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), "Steve")
	if err != nil || res.Status != LookupUnavailable {
		t.Fatalf("result = %+v err=%v", res, err)
	}
}

func TestLookupConflictingProviderDataIsAmbiguous(t *testing.T) {
	f := newLookupFixture(t)
	f.nameBody = `{"name":"Different","id":"00112233445566778899aabbccddeeff"}`
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), "Steve")
	if err != nil || res.Status != LookupAmbiguous || res.UUID != "" {
		t.Fatalf("result = %+v err=%v", res, err)
	}
}

func TestLookupAuthoritativeEmpty200IsNotFound(t *testing.T) {
	f := newLookupFixture(t)
	f.nameStatus = http.StatusOK
	f.nameBody = "" // authoritative 200 with no profile
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), "Ghost")
	if err != nil || res.Status != LookupNotFound || res.UUID != "" {
		t.Fatalf("result = %+v err=%v", res, err)
	}
	// A 404 is likewise an authoritative negative, never an outage.
	f.nameStatus = http.StatusNotFound
	f.nameBody = ""
	res, err = cache.Lookup(context.Background(), "Ghost")
	if err != nil || res.Status != LookupNotFound {
		t.Fatalf("404 result = %+v err=%v", res, err)
	}
}

func TestLookupTimeoutIsUnavailableNotAbsent(t *testing.T) {
	f := newLookupFixture(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	oldName := nameBaseURL
	nameBaseURL = slow.URL + "/users/profiles/minecraft/"
	t.Cleanup(func() { nameBaseURL = oldName })
	f.nameStatus = http.StatusOK
	cache := newTestCache(t, 0, nil)
	// A shorter caller deadline must win over the provider's own bound and is
	// still an outage (UNAVAILABLE), never a negative result.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res, err := cache.Lookup(ctx, "Steve")
	if err != nil || res.Status != LookupUnavailable {
		t.Fatalf("timeout result = %+v err=%v", res, err)
	}
}

func TestLookupCrossHostRedirectIsUnavailable(t *testing.T) {
	f := newLookupFixture(t)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	f.nameBody = `{"name":"Steve","id":"00112233445566778899aabbccddeeff"}`
	f.redirectTo = other.URL
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), "Steve")
	if err != nil || res.Status != LookupUnavailable {
		t.Fatalf("result = %+v err=%v", res, err)
	}
	assertSanitizedIdentityUnavailable(t, res.Note)
}

func assertSanitizedIdentityUnavailable(t *testing.T, note string) {
	t.Helper()
	lower := strings.ToLower(note)
	if !strings.Contains(lower, "unavailable") || !strings.Contains(lower, "try again") {
		t.Fatalf("note not actionable unavailable: %q", note)
	}
	for _, leak := range []string{"http://", "https://", "context deadline exceeded", "skin unavailable", "texture download", "net/url", "users/profiles/minecraft", "session/minecraft/profile"} {
		if strings.Contains(lower, leak) {
			t.Fatalf("note leaked internal detail %q: %q", leak, note)
		}
	}
}

func TestLookupRateLimitUnavailableNoteIsSanitized(t *testing.T) {
	f := newLookupFixture(t)
	f.nameStatus = http.StatusTooManyRequests
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), "Steve")
	if err != nil || res.Status != LookupUnavailable {
		t.Fatalf("result = %+v err=%v", res, err)
	}
	assertSanitizedIdentityUnavailable(t, res.Note)
	if !strings.Contains(strings.ToLower(res.Note), "rate") {
		t.Fatalf("429 note should indicate rate limiting: %q", res.Note)
	}
}

func TestLookupTimeoutUnavailableNoteIsSanitized(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()
	oldName := nameBaseURL
	nameBaseURL = slow.URL + "/users/profiles/minecraft/"
	t.Cleanup(func() { nameBaseURL = oldName })
	cache := newTestCache(t, 0, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	res, err := cache.Lookup(ctx, "Steve")
	if err != nil || res.Status != LookupUnavailable {
		t.Fatalf("timeout result = %+v err=%v", res, err)
	}
	assertSanitizedIdentityUnavailable(t, res.Note)
	if !strings.Contains(strings.ToLower(res.Note), "timed out") {
		t.Fatalf("timeout note should indicate timeout: %q", res.Note)
	}
}

func TestLookupUUIDRateLimitUnavailableNoteIsSanitized(t *testing.T) {
	f := newLookupFixture(t)
	f.profileStatus = http.StatusTooManyRequests
	cache := newTestCache(t, 0, nil)
	res, err := cache.Lookup(context.Background(), testUUID)
	if err != nil || res.Status != LookupUnavailable {
		t.Fatalf("result = %+v err=%v", res, err)
	}
	assertSanitizedIdentityUnavailable(t, res.Note)
	if !strings.Contains(strings.ToLower(res.Note), "rate") {
		t.Fatalf("429 note should indicate rate limiting: %q", res.Note)
	}
}
