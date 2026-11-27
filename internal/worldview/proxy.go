// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// Authenticator, Authorizer and SubuserStore are the narrow seams used by the
// proxy. The real auth.Manager, rbac.Enforcer and storage.Store satisfy them;
// tests inject fixtures that record whether an upstream was contacted.
type Authenticator interface {
	AuthenticateFromHeader(ctx context.Context, header string) (*auth.AuthenticatedUser, error)
}

type Authorizer interface {
	Enforce(roles []string, resource, action, objectID string) (bool, error)
}

type SubuserStore interface {
	SubuserPermissions(ctx context.Context, serverID, userID string) []string
}

type ServerGetter interface {
	GetServer(ctx context.Context, id string) (*storage.Server, error)
}

// Logger is the subset of pkg/logger used here.
type Logger interface {
	Error(format string, args ...any)
	Warn(format string, args ...any)
	Info(format string, args ...any)
}

// ProxyDeps are the dependencies of the same-origin renderer proxy.
type ProxyDeps struct {
	Auth       Authenticator
	Authorizer Authorizer
	Subusers   SubuserStore
	Servers    ServerGetter
	Resolver   *Resolver
	Sessions   *SessionRegistry
	Native     *NativeMap
	Log        Logger
	Client     HTTPDoer
}

// defaultRendererDocument is the prepared upstream document served for the
// canonical keyed iframe root (ProxyPrefix + key + "/"). It is fetched through
// the same auth, session, authorization, path-validation and cache gates as any
// other proxied renderer asset.
const defaultRendererDocument = "index.html"

// NewProxyHandler mounts GET/HEAD /api/v1/worldview/map/{key}/... . The key is
// an opaque session identifier, not a credential; each request re-authenticates
// with cp_session or an Authorization bearer token and re-checks
// world_view/read on the concrete server before contacting the renderer.
func NewProxyHandler(deps ProxyDeps) http.Handler {
	if deps.Client == nil {
		deps.Client = newProxyClient()
	}
	if deps.Sessions == nil {
		deps.Sessions = NewSessionRegistry()
	}
	return &proxyHandler{deps: deps}
}

type proxyHandler struct {
	deps ProxyDeps
}

// proxyCachePolicy is panel-owned and overrides every renderer-supplied
// caching directive. Every response from this path is protected by per-request
// cookie/Bearer auth and world_view/read, so no shared cache may store it.
const proxyCachePolicy = "private, no-store, no-cache, must-revalidate, max-age=0"

// setProxyCachePolicy runs BEFORE any authorization decision so success,
// denial, error and method-rejection paths all carry the same private policy.
// Renderer cache headers are never relayed; conditional validators are removed
// so an upstream 304 cannot be turned into a proxy error.
func setProxyCachePolicy(header http.Header) {
	for _, name := range []string{
		"Cache-Control", "Expires", "Pragma", "Surrogate-Control",
		"CDN-Cache-Control", "Age", "ETag", "Last-Modified", "Vary",
	} {
		header.Del(name)
	}
	header.Set("Cache-Control", proxyCachePolicy)
	header.Set("Pragma", "no-cache")
	header.Set("Expires", "0")
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setProxyCachePolicy(w.Header())
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// A renderer WebSocket is never exposed. The panel /ws route remains the
	// only upgrade endpoint; do not loosen CSP or write a permissive CORS header.
	if r.Header.Get("Upgrade") != "" || strings.EqualFold(strings.TrimSpace(r.Header.Get("Connection")), "upgrade") {
		http.Error(w, "websocket upgrade is not supported on the map proxy", http.StatusBadRequest)
		return
	}

	rel := strings.TrimPrefix(r.URL.Path, ProxyPrefix)
	if rel == r.URL.Path {
		http.NotFound(w, r)
		return
	}
	key, asset, ok := strings.Cut(rel, "/")
	if !ok || key == "" {
		http.Error(w, "invalid map session path", http.StatusForbidden)
		return
	}
	upstreamAsset := asset
	if asset == "" {
		// Canonical keyed slash root issued by GetWorldViewSession. Admitted
		// through the identical session identity, authorization and cache gates
		// below, then mapped to the prepared default renderer document.
		upstreamAsset = defaultRendererDocument
	} else if !validAssetPath(asset) {
		http.Error(w, "invalid map session path", http.StatusForbidden)
		return
	}

	token := auth.SessionTokenFromRequest(r)
	if token == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}
	user, err := h.deps.Auth.AuthenticateFromHeader(r.Context(), "Bearer "+token)
	if err != nil || user == nil || user.ID == "" {
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	sess, ok := h.deps.Sessions.Resolve(key)
	if !ok || sess.UserID != user.ID {
		// Expired, unknown, or issued to a different user: no upstream request.
		http.Error(w, "map session is not valid for this user", http.StatusForbidden)
		return
	}
	if !h.authorized(r.Context(), user, sess.ServerID) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	server, err := h.deps.Servers.GetServer(r.Context(), sess.ServerID)
	if err != nil || server == nil {
		http.Error(w, "renderer unavailable", http.StatusServiceUnavailable)
		return
	}
	if sess.native.serverID != "" {
		if h.deps.Native == nil {
			http.Error(w, "native map is unavailable", http.StatusServiceUnavailable)
			return
		}
		h.deps.Native.serveAsset(w, r, server, sess.native, asset, h.deps.Resolver)
		return
	}
	renderer, err := h.deps.Resolver.Resolve(r.Context(), server.NodeID, sess.ServerID)
	if err != nil || renderer == nil || renderer.ProxyBaseURL() == "" {
		h.logWarn("worldview proxy: renderer unavailable for server %s: %v", sess.ServerID, err)
		http.Error(w, "renderer unavailable", http.StatusServiceUnavailable)
		return
	}
	if sess.discovered {
		discovered, ok := renderer.(*discoveredRenderer)
		if !ok || !discovered.matchesSessionWorld(sess.WorldKey) {
			http.Error(w, "renderer no longer serves the selected world", http.StatusServiceUnavailable)
			return
		}
	}

	h.proxyAsset(w, r, renderer.ProxyBaseURL(), upstreamAsset, renderer.Type() == v1.RendererType_RENDERER_TYPE_BLUEMAP)
}

// authorized mirrors the RPC interceptor: role grant first, then the
// server-scoped subuser grant fallback. Missing dependencies fail closed.
func (h *proxyHandler) authorized(ctx context.Context, user *auth.AuthenticatedUser, serverID string) bool {
	if h.deps.Authorizer == nil || serverID == "" {
		return false
	}
	allowed, err := h.deps.Authorizer.Enforce(user.Roles, rbac.ResourceWorldView, rbac.ActionRead, serverID)
	if err != nil {
		h.logError("worldview proxy: RBAC error: %v", err)
		return false
	}
	if !allowed && h.deps.Subusers != nil {
		want := rbac.ResourceWorldView + "." + rbac.ActionRead
		for _, p := range h.deps.Subusers.SubuserPermissions(ctx, serverID, user.ID) {
			if p == want {
				allowed = true
				break
			}
		}
	}
	return allowed
}

// blueMapCompressedSuffix is the on-disk compression BlueMap uses for map data
// (Compression.GZIP carries the ".gz" suffix in the pinned v5.29 source). The
// webapp has two modes: with clientDecompression it requests the literal ".gz"
// path and decompresses in the browser (no Content-Encoding); otherwise it
// requests the plain path and expects the webserver to negotiate gzip through
// Content-Encoding. Both routes are reproduced here without widening the
// admitted path set: every auth, session, scope and path gate runs first.
const blueMapCompressedSuffix = ".gz"

// assetRoute describes the exact upstream request for a browser asset and how
// its response must be labelled.
type assetRoute struct {
	upstreamPath    string
	contentType     string // empty: keep the upstream value
	contentEncoding string
	forceEncoding   bool // true: force contentEncoding, even when it is empty
}

// literalCompressedRoute handles clientDecompression mode: the browser asks
// for the literal ".gz" file and decompresses it itself, so the response must
// NOT carry Content-Encoding. Plain static servers label that file
// application/gzip; BlueMap's own webserver labels it with the media type of
// the uncompressed path. We always use the latter, matching BlueMap.
func literalCompressedRoute(asset string) (assetRoute, bool) {
	if !strings.HasSuffix(asset, blueMapCompressedSuffix) {
		return assetRoute{}, false
	}
	return assetRoute{
		upstreamPath:    asset,
		contentEncoding: "",
		forceEncoding:   true,
		contentType:     blueMapContentType(strings.TrimSuffix(asset, blueMapCompressedSuffix)),
	}, true
}

// compressedVariant implements BlueMap's exact gzip routing for a plain asset
// that the upstream answered with 404. BlueMap's own webserver normally
// answers the plain path itself through Accept-Encoding negotiation, so this
// is only a fallback for web roots that store the compressed file directly
// (the CPython http.server used in disposable review stacks is one such root).
// It is gzip-only because BlueMap v5.29 produces no other compression format.
func compressedVariant(asset, acceptEncoding string) (assetRoute, bool) {
	if strings.HasSuffix(asset, blueMapCompressedSuffix) || !acceptsEncoding(acceptEncoding, "gzip") {
		return assetRoute{}, false
	}
	return assetRoute{
		upstreamPath:    asset + blueMapCompressedSuffix,
		contentEncoding: "gzip",
		forceEncoding:   true,
		contentType:     blueMapContentType(asset),
	}, true
}

// isBlueMapMapDataPath limits the compressed route to the map data BlueMap
// actually stores gzipped: a map's textures.json and its PRBM/PNG tile
// payloads. Browser assets (index.html, JS, CSS, fonts, locales) and every
// non-BlueMap renderer keep their existing pass-through behavior.
func isBlueMapMapDataPath(asset string) bool {
	clean := strings.TrimPrefix(asset, "/")
	base := path.Base(clean)
	if strings.HasPrefix(clean, "maps/") && base == "textures.json" {
		return true
	}
	if strings.Contains(clean, "/tiles/") && (strings.HasSuffix(base, ".prbm") || strings.HasSuffix(base, ".png")) {
		return true
	}
	return false
}

// acceptsEncoding mirrors BlueMap's own Accept-Encoding check: q=0 refuses a
// coding and "*" is a wildcard. It only decides whether a gzip variant may be
// offered; it never participates in authorization.
func acceptsEncoding(header, encoding string) bool {
	wildcard, explicitlyRefused := false, false
	for _, value := range strings.Split(header, ",") {
		parts := strings.Split(value, ";")
		coding := strings.TrimSpace(parts[0])
		if !strings.EqualFold(coding, encoding) && coding != "*" {
			continue
		}
		q := 1.0
		for _, param := range parts[1:] {
			param = strings.TrimSpace(param)
			if len(param) >= 2 && strings.EqualFold(param[:2], "q=") {
				if parsed, err := strconv.ParseFloat(strings.TrimSpace(param[2:]), 64); err == nil {
					q = parsed
				}
			}
		}
		if q > 0 {
			if coding == "*" {
				wildcard = true
			} else {
				return true
			}
		} else if strings.EqualFold(coding, encoding) {
			// An explicit q=0 overrides a wildcard (RFC 9110): a client that
			// refused gzip must never be served a gzip representation.
			explicitlyRefused = true
		}
	}
	return wildcard && !explicitlyRefused
}

// blueMapContentType is an explicit, OS-independent media-type table for the
// assets BlueMap serves. mime.TypeByExtension is deliberately avoided: its
// answer depends on the host's mime.types database and is not portable.
func blueMapContentType(asset string) string {
	switch strings.ToLower(path.Ext(asset)) {
	case ".json", ".webmanifest":
		return "application/json"
	case ".prbm":
		return "application/octet-stream"
	case ".png":
		return "image/png"
	case ".js", ".mjs":
		return "text/javascript"
	case ".css":
		return "text/css"
	case ".wasm":
		return "application/wasm"
	case ".html":
		return "text/html; charset=utf-8"
	case ".conf", ".txt":
		return "text/plain; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".ttf":
		return "font/ttf"
	default:
		return "application/octet-stream"
	}
}

// upstreamRequest builds the renderer request. Accept-Encoding is forwarded so
// a real BlueMap webserver performs its native negotiation; because the header
// is set explicitly, Go's transport does not transparently decompress and
// strip Content-Encoding, so the exact upstream encoding reaches the browser.
// Conditional validators are never sent: every asset is private and must be
// re-authenticated, so a cached 304 must never be part of this path.
func (h *proxyHandler) upstreamRequest(r *http.Request, baseURL, upstreamPath string) (*http.Request, error) {
	target := strings.TrimRight(baseURL, "/") + "/" + upstreamPath
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"Accept", "Range", "Accept-Encoding"} {
		if v := r.Header.Get(name); v != "" {
			upstream.Header.Set(name, v)
		}
	}
	return upstream, nil
}

func (h *proxyHandler) proxyAsset(w http.ResponseWriter, r *http.Request, baseURL, asset string, blueMap bool) {
	// Range requests identify a byte range of a specific representation. Never
	// switch representation (plain <-> gzip) for them: keep the upstream's own
	// answer and its Range/Content-Range semantics exactly as before.
	ranged := r.Header.Get("Range") != ""
	special := blueMap && !ranged
	route := assetRoute{upstreamPath: asset}
	if special {
		if literalRoute, ok := literalCompressedRoute(asset); ok {
			route = literalRoute
		}
	}
	upstream, err := h.upstreamRequest(r, baseURL, route.upstreamPath)
	if err != nil {
		http.Error(w, "invalid renderer asset path", http.StatusForbidden)
		return
	}
	resp, err := h.deps.Client.Do(upstream)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || r.Context().Err() != nil {
			http.Error(w, "request cancelled", http.StatusRequestTimeout)
			return
		}
		h.logWarn("worldview proxy: upstream request failed: %v", err)
		http.Error(w, "renderer unavailable", http.StatusBadGateway)
		return
	}

	// A web root that stores BlueMap's compressed file directly has no plain
	// entry. Retry the exact ".gz" sibling once under the SAME gates and label
	// it exactly as BlueMap's own webserver would. No other path is admitted;
	// a failed retry keeps the original 404 so a missing tile never becomes an
	// empty or black canvas.
	if resp.StatusCode == http.StatusNotFound && special && isBlueMapMapDataPath(asset) {
		if variant, ok := compressedVariant(asset, r.Header.Get("Accept-Encoding")); ok {
			if retry, retryErr := h.upstreamRequest(r, baseURL, variant.upstreamPath); retryErr == nil {
				if alt, altErr := h.deps.Client.Do(retry); altErr == nil {
					if alt.StatusCode >= 200 && alt.StatusCode < 300 {
						_ = resp.Body.Close()
						resp, route = alt, variant
					} else {
						_ = alt.Body.Close()
					}
				}
			}
		}
	}
	defer func() { _ = resp.Body.Close() }()

	// A 304 has no body to stream. Since this proxy never sends validators, a
	// renderer 304 is anomalous; refuse it explicitly rather than falling into
	// the generic 3xx branch or writing an empty protected asset.
	if resp.StatusCode == http.StatusNotModified {
		h.logWarn("worldview proxy: renderer returned an unsolicited 304")
		http.Error(w, "renderer conditional response refused", http.StatusBadGateway)
		return
	}
	// Defense in depth: never relay a renderer redirect under the panel origin,
	// even when a custom client was injected without CheckRedirect.
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		http.Error(w, "renderer redirect refused", http.StatusBadGateway)
		return
	}
	if resp.StatusCode == http.StatusSwitchingProtocols {
		http.Error(w, "renderer upgrade refused", http.StatusBadGateway)
		return
	}

	// Only safe, end-to-end headers are forwarded; hop-by-hop headers,
	// Set-Cookie and renderer-defined security headers never pass through.
	// Content-Encoding is end-to-end and MUST be relayed: BlueMap's webserver
	// uses it to serve gzipped tiles/textures with the uncompressed media type.
	if length := resp.Header.Get("Content-Length"); length != "" {
		if n, convErr := strconv.ParseInt(length, 10, 64); convErr == nil && n > MaxAssetBytes {
			http.Error(w, "renderer asset too large", http.StatusRequestEntityTooLarge)
			return
		}
	}
	if route.contentType != "" {
		w.Header().Set("Content-Type", route.contentType)
	} else if v := resp.Header.Get("Content-Type"); v != "" {
		w.Header().Set("Content-Type", v)
	}
	if route.forceEncoding {
		if route.contentEncoding != "" {
			w.Header().Set("Content-Encoding", route.contentEncoding)
		}
	} else if v := resp.Header.Get("Content-Encoding"); v != "" {
		w.Header().Set("Content-Encoding", v)
	}
	for _, name := range []string{"Content-Range", "Accept-Ranges"} {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	if length := resp.Header.Get("Content-Length"); length != "" {
		w.Header().Set("Content-Length", length)
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}

	copyAsset(w, resp.Body, h.deps.Log)
}

// proxyCopyBufferSize is the fixed panel-side streaming buffer. It is bounded
// independently of the renderer asset size so a large tile can never require a
// matching allocation on the controller.
const proxyCopyBufferSize = 32 * 1024

// copyAsset streams at most MaxAssetBytes from src to dst. It never buffers a
// whole asset in panel memory; the only allocation is the fixed-size copy
// buffer. Kept as a seam so the memory and bandwidth bounds are measurable by
// an owned fixture without a live renderer.
func copyAsset(dst io.Writer, src io.Reader, log Logger) int64 {
	buf := make([]byte, proxyCopyBufferSize)
	// Limit reads to the hard cap; the +1 probe below only DETECTS an oversize
	// response and is never forwarded to the browser.
	limited := &io.LimitedReader{R: src, N: MaxAssetBytes}
	n, _ := io.CopyBuffer(dst, limited, buf)
	if n == MaxAssetBytes {
		var probe [1]byte
		if got, err := src.Read(probe[:]); got > 0 && err == nil {
			if log != nil {
				// Content-Length was absent or wrong; the response was capped
				// at MaxAssetBytes rather than buffered without bound.
				log.Warn("worldview proxy: asset exceeded %d bytes and was truncated", MaxAssetBytes)
			}
		}
	}
	return n
}

func validAssetPath(asset string) bool {
	if asset == "" || strings.HasPrefix(asset, "/") || strings.ContainsAny(asset, "\\\x00\r\n") || strings.Contains(asset, "..") {
		return false
	}
	for _, segment := range strings.Split(asset, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func (h *proxyHandler) logWarn(format string, args ...any) {
	if h.deps.Log != nil {
		h.deps.Log.Warn(format, args...)
	}
}

func (h *proxyHandler) logError(format string, args ...any) {
	if h.deps.Log != nil {
		h.deps.Log.Error(format, args...)
	}
}
