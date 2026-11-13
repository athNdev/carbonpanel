// SPDX-License-Identifier: MPL-2.0
package handlers

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type modSearchFixtureTransport func(*http.Request) (*http.Response, error)

func (f modSearchFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveCurseForgeSlugKeepsKeyOffCommunityProxy(t *testing.T) {
	var calls int
	manager := &ModOnlineManager{httpClient: &http.Client{Transport: modSearchFixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.curse.tools" {
			t.Fatalf("unexpected destination %s", r.URL.Host)
		}
		if r.Header.Get("X-Api-Key") != "" {
			t.Error("community proxy received the official API key")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":123}]}`)), Header: make(http.Header), Request: r}, nil
	})}}
	if got := manager.resolveCurseForgeSlug(context.Background(), "test-key", "example"); got != "123" || calls != 1 {
		t.Fatalf("got %q after %d calls", got, calls)
	}
}

func TestResolveCurseForgeSlugOfficialFallbackKeepsItsKey(t *testing.T) {
	calls := 0
	manager := &ModOnlineManager{httpClient: &http.Client{Transport: modSearchFixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		status, body := http.StatusForbidden, `{}`
		switch r.URL.Host {
		case "api.curse.tools":
			if r.Header.Get("X-Api-Key") != "" {
				t.Error("community proxy received official key")
			}
		case "api.curseforge.com":
			if r.Header.Get("X-Api-Key") != "test-key" {
				t.Error("official fallback lost its key")
			}
			status, body = http.StatusOK, `{"data":[{"id":123}]}`
		default:
			t.Fatalf("unexpected destination %s", r.URL.Host)
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})}}
	if got := manager.resolveCurseForgeSlug(context.Background(), "test-key", "example"); got != "123" || calls != 2 {
		t.Fatalf("got %q after %d calls", got, calls)
	}
}
