// SPDX-License-Identifier: MPL-2.0
package fuego

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProviderFallbackFailurePolicy(t *testing.T) {
	for _, status := range []int{401, 403, 404, 429, 500, 200} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				body := `not-json`
				code := status
				if r.URL.Host == "api.curse.tools" {
					if r.Header.Get("X-Api-Key") != "" {
						t.Fatal("fallback received credential")
					}
					code, body = 200, `{"data":[]}`
				}
				return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
			})
			c := NewClient("test-key", testCfg(), WithBaseURL("https://official.fixture.invalid/v1"), WithHTTPClient(&http.Client{Transport: transport}))
			if _, err := c.SearchModpacks(context.Background(), "", "", ModLoaderAny, 0, 10); err != nil || calls != 2 {
				t.Fatalf("fallback: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestProviderCancellationDoesNotStartFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) { calls++; cancel(); return nil, context.Canceled })
	c := NewClient("test-key", testCfg(), WithBaseURL("https://official.fixture.invalid/v1"), WithHTTPClient(&http.Client{Transport: transport}))
	_, err := c.SearchModpacks(ctx, "", "", ModLoaderAny, 0, 10)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation: err=%v calls=%d", err, calls)
	}
}

func TestExplicitKeylessProviderNeverReceivesKey(t *testing.T) {
	for _, endpoint := range []string{
		KeylessBaseURL,
		KeylessBaseURL + "/",
		"https://API.CURSE.TOOLS/v1/cf",
		"https://api.curse.tools/alternate-path",
		"http://api.curse.tools:8080/v1/cf",
		"https://api.curse.tools./v1/cf",
	} {
		t.Run(endpoint, func(t *testing.T) {
			calls := 0
			transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("X-Api-Key") != "" {
					t.Error("keyless provider received credential")
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
			})
			c := NewClient("test-key", testCfg(), WithBaseURL(endpoint), WithHTTPClient(&http.Client{Transport: transport}))
			if _, err := c.SearchModpacks(context.Background(), "", "", ModLoaderAny, 0, 10); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("request count %d, want 1", calls)
			}
		})
	}
}

func TestKeylessProviderFailureDoesNotRetryItself(t *testing.T) {
	calls := 0
	transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("X-Api-Key") != "" {
			t.Error("keyless failure request received credential")
		}
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
	})
	c := NewClient("test-key", testCfg(), WithBaseURL(KeylessBaseURL+"/"), WithHTTPClient(&http.Client{Transport: transport}))
	if _, err := c.SearchModpacks(context.Background(), "", "", ModLoaderAny, 0, 10); err == nil || calls != 1 {
		t.Fatalf("error=%v calls=%d, want one failed request", err, calls)
	}
}

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFallbackNeverReceivesOfficialAPIKey(t *testing.T) {
	for _, operation := range []string{"search", "files", "project"} {
		t.Run(operation, func(t *testing.T) {
			var official, fallback int
			transport := fixtureTransport(func(r *http.Request) (*http.Response, error) {
				status, body := http.StatusUnauthorized, `{}`
				switch r.URL.Host {
				case "official.fixture.invalid":
					official++
					if r.Header.Get("X-Api-Key") != "test-key" {
						t.Error("official request did not receive the configured key")
					}
				case "api.curse.tools":
					fallback++
					if r.Header.Get("X-Api-Key") != "" {
						t.Error("community fallback received the official API key")
					}
					status, body = http.StatusOK, `{"data":[]}`
					if operation == "project" {
						body = `{"data":{"id":123}}`
					}
				default:
					t.Fatalf("unexpected destination %s", r.URL.Host)
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
			})
			c := NewClient("test-key", testCfg(), WithBaseURL("https://official.fixture.invalid/v1"), WithHTTPClient(&http.Client{Transport: transport}))
			var err error
			switch operation {
			case "search":
				_, err = c.SearchModpacks(context.Background(), "", "", ModLoaderAny, 0, 10)
			case "files":
				_, err = c.GetModpackFiles(context.Background(), 123)
			case "project":
				_, err = c.GetModpack(context.Background(), 123)
			}
			if err != nil || official != 1 || fallback != 1 {
				t.Fatalf("err=%v official=%d fallback=%d", err, official, fallback)
			}
		})
	}
}

func TestDatapackSearchUsesCredentialBoundary(t *testing.T) {
	calls := 0
	c := NewClient("fixture-key", testCfg(), WithBaseURL("https://API.CURSE.TOOLS/alternate"), WithHTTPClient(&http.Client{Transport: fixtureTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("X-Api-Key") != "" || r.URL.Query().Get("classId") != "6945" || r.URL.Query().Get("sortField") != "6" {
			t.Errorf("datapack policy: %+v", r)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: make(http.Header)}, nil
	})}))
	_, err := (&FuegoDatapackIndexer{client: c}).SearchDatapacks(context.Background(), "", "", 0, 20)
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
