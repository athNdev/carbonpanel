package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The panel listener must pin an explicit request-header bound. A request with
// ordinary headers still works; a header larger than the bound is rejected
// before it reaches the handler. The test server uses a loopback listener, so
// no firewall or elevation is involved.
func TestPanelHTTPServerHeaderLimit(t *testing.T) {
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	applyPanelRequestLimits(ts.Config)
	ts.Start()
	t.Cleanup(ts.Close)

	if ts.Config.MaxHeaderBytes != maxPanelHeaderBytes {
		t.Fatalf("MaxHeaderBytes = %d, want %d", ts.Config.MaxHeaderBytes, maxPanelHeaderBytes)
	}

	client := ts.Client()

	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("ordinary request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ordinary request status = %d, want 200", resp.StatusCode)
	}

	req, err := http.NewRequest(http.MethodGet, ts.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("X-Oversized", strings.Repeat("a", maxPanelHeaderBytes*2))
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("oversized header request: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("oversized header status = %d, want 431", resp.StatusCode)
	}
}
