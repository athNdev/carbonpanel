// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.


// Merged in the MINE-172 integration: this file existed independently on
// both lines of development, so the union of both test sets is kept.

package rpc

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeaders_DefaultSet(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	securityHeaders(next, false).ServeHTTP(rec, req)

	res := rec.Result()
	defer res.Body.Close()

	if got := res.Header.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Errorf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}
	if got := res.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := res.Header.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Errorf("Referrer-Policy = %q, want strict-origin-when-cross-origin", got)
	}
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "frame-ancestors 'self'", "object-src 'none'", "connect-src", "ws:", "wss:"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP missing %q (got %q)", want, csp)
		}
	}
	if got := res.Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("HSTS must not be set on plaintext requests, got %q", got)
	}
}

func TestSecurityHeaders_HSTSOnTLS(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	newTLSRequest := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "https://panel.local/healthz", nil)
		req.TLS = &tls.ConnectionState{}
		return req
	}

	// Direct TLS.
	rec := httptest.NewRecorder()
	securityHeaders(next, false).ServeHTTP(rec, newTLSRequest())
	if got := rec.Result().Header.Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=") {
		t.Errorf("HSTS missing on TLS request, got %q", got)
	}

	// X-Forwarded-Proto from an untrusted client must be ignored.
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	securityHeaders(next, false).ServeHTTP(rec, req)
	if got := rec.Result().Header.Get("Strict-Transport-Security"); got != "" {
		t.Errorf("untrusted X-Forwarded-Proto must not set HSTS, got %q", got)
	}

	// Behind a configured TLS-terminating proxy it is honoured.
	rec = httptest.NewRecorder()
	securityHeaders(next, true).ServeHTTP(rec, req)
	if got := rec.Result().Header.Get("Strict-Transport-Security"); !strings.HasPrefix(got, "max-age=") {
		t.Errorf("HSTS missing behind trusted TLS-terminating proxy, got %q", got)
	}
}

func TestSecurityHeaders_NoDuplicatesWhenInnerSets(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	securityHeaders(next, false).ServeHTTP(rec, req)

	if values := rec.Result().Header.Values("X-Frame-Options"); len(values) != 1 {
		t.Errorf("expected exactly one X-Frame-Options value, got %q", values)
	}
}

// --- tests present only on the pre-merge line of development ---

func TestSecurityHeadersPresent(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})
	rec := httptest.NewRecorder()
	securityHeaders(inner, false).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/anything", nil))

	if got := rec.Result().StatusCode; got != http.StatusTeapot {
		t.Fatalf("status=%d: inner handler must still run", got)
	}
	h := rec.Result().Header
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q", got)
	}
	if got := h.Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options=%q (docs iframe needs same-origin)", got)
	}
	if got := h.Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("Referrer-Policy=%q", got)
	}
	csp := h.Get("Content-Security-Policy")
	for _, want := range []string{
		"default-src 'self'",
		"object-src 'none'",
		"frame-ancestors 'self'",
		"connect-src 'self' ws: wss:",
		"img-src 'self' data: blob: https:",
	} {
		if !strings.Contains(csp, want) {
			t.Fatalf("CSP %q missing %q", csp, want)
		}
	}
}

// TestSecurityHeadersCSPUnchanged pins the exact panel CSP. BlueMap's
// vue-i18n runtime template compilation is made CSP-compatible by the pinned
// JIT webapp build (ADR 0048), never by widening this policy. An unexpected
// edit here is a security regression and fails the suite.
//
// img-src carries blob: so the same-origin player skins, rendered from an
// object URL the page created, are not blocked. Everything else stays pinned.
func TestSecurityHeadersCSPUnchanged(t *testing.T) {
	const want = "default-src 'self'; " +
		"script-src 'self' 'unsafe-inline'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data: blob: https:; " +
		"font-src 'self' data:; " +
		"connect-src 'self' ws: wss:; " +
		"frame-ancestors 'self'; " +
		"base-uri 'self'; " +
		"form-action 'self'; " +
		"object-src 'none'"
	if contentSecurityPolicy != want {
		t.Fatalf("panel CSP changed:\n got %q\nwant %q", contentSecurityPolicy, want)
	}
	if strings.Contains(contentSecurityPolicy, "unsafe-eval") {
		t.Fatal("panel CSP must never allow unsafe-eval")
	}
}

func TestSecurityHeadersOnErrorPaths(t *testing.T) {
	// Headers must be stamped before the inner handler runs, so even
	// early 4xx/5xx responses carry them.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	})
	rec := httptest.NewRecorder()
	securityHeaders(inner, false).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ws", nil))
	if got := rec.Result().Header.Get("Content-Security-Policy"); got == "" {
		t.Fatal("CSP missing on error response")
	}
}
