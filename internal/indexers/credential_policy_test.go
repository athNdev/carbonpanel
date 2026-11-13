// SPDX-License-Identifier: MPL-2.0
package indexers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientRetainsSameOriginCredentialsAndCallerPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("same-origin request lost its configured key")
		}
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/target", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	baseClient := &http.Client{}
	headers := map[string]string{"X-Api-Key": "test-key"}
	client := NewHTTPClientWithClient("test", "", headers, baseClient)
	headers["X-Api-Key"] = "changed-after-construction"
	var result map[string]any
	if err := client.DoJSON(context.Background(), server.URL, &result); err != nil {
		t.Fatal(err)
	}
	if baseClient.CheckRedirect != nil {
		t.Fatal("constructor changed the caller's client")
	}
	stop := errors.New("caller stopped redirect")
	baseClient.CheckRedirect = func(*http.Request, []*http.Request) error { return stop }
	client = NewHTTPClientWithClient("test", "", map[string]string{"X-Api-Key": "test-key"}, baseClient)
	if err := client.DoJSON(context.Background(), server.URL, &result); !errors.Is(err, stop) {
		t.Fatalf("caller redirect policy lost: %v", err)
	}
}

func TestHTTPClientStripsAPIKeyAcrossOriginsOnRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "" {
			t.Error("redirect destination received the API key")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer target.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "test-key" {
			t.Error("origin did not receive configured key")
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer primary.Close()
	client := NewHTTPClient("test", "", map[string]string{"x-api-key": "test-key"})
	var result map[string]any
	if err := client.DoJSON(context.Background(), primary.URL, &result); err != nil {
		t.Fatal(err)
	}
}
