// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.


package rpc

import (
	"net/http"
	"strings"
)

// contentSecurityPolicy is the default CSP for panel responses.
//
// The SPA needs inline scripts (SvelteKit hydration payload) and inline
// style attributes (progress bars, xterm), so script-src/style-src carry
// 'unsafe-inline'. Everything else is locked to same-origin: no plugins,
// no framing by foreign origins, no cross-origin form posts. connect-src
// covers the console WebSocket on the same origin (ws:/wss:).
//
// img-src carries blob: because player skins are fetched through the
// same-origin RPC and rendered from an object URL the page itself created.
// Without it the browser refuses the image and every avatar renders as a
// broken image instead of the skin. data: covers inline placeholder images.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' 'unsafe-inline'; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob: https:; " +
	"font-src 'self' data:; " +
	"connect-src 'self' ws: wss:; " +
	"frame-ancestors 'self'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"object-src 'none'"

// securityHeaders wraps h with the panel's default HTTP response headers
// (MINE-163, research P2-14). trustProxy must come from configuration: an
// untrusted client must not be able to make a plaintext response carry HSTS
// by sending X-Forwarded-Proto: https.
//
//   - Content-Security-Policy (see above for the SPA rationale)
//   - X-Frame-Options: SAMEORIGIN (belt-and-braces next to
//     frame-ancestors for older clients)
//   - X-Content-Type-Options: nosniff
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Strict-Transport-Security on TLS requests only, or on requests from a
//     configured trusted proxy that reports HTTPS (plaintext responses must
//     not carry it)
//
// Headers are set (not added) so inner handlers cannot stack duplicates,
// and the middleware never touches upgrade bodies — it only stamps the
// response header map before delegating.
func securityHeaders(h http.Handler, trustProxy bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", contentSecurityPolicy)
		header.Set("X-Frame-Options", "SAMEORIGIN")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		forwardedHTTPS := trustProxy && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
		if r.TLS != nil || forwardedHTTPS {
			header.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		h.ServeHTTP(w, r)
	})
}
