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
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
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
	Log        Logger
	Client     HTTPDoer
}

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

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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
	if !ok || key == "" || asset == "" || !validAssetPath(asset) {
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
	renderer, err := h.deps.Resolver.Resolve(r.Context(), server.NodeID, sess.ServerID)
	if err != nil || renderer == nil || renderer.ProxyBaseURL() == "" {
		h.logWarn("worldview proxy: renderer unavailable for server %s: %v", sess.ServerID, err)
		http.Error(w, "renderer unavailable", http.StatusServiceUnavailable)
		return
	}

	h.proxyAsset(w, r, renderer.ProxyBaseURL(), asset)
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

func (h *proxyHandler) proxyAsset(w http.ResponseWriter, r *http.Request, baseURL, asset string) {
	target := strings.TrimRight(baseURL, "/") + "/" + asset
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, target, nil)
	if err != nil {
		http.Error(w, "invalid renderer asset path", http.StatusForbidden)
		return
	}
	for _, name := range []string{"Accept", "Range", "If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(name); v != "" {
			upstream.Header.Set(name, v)
		}
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
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		http.Error(w, "renderer upgrade refused", http.StatusBadGateway)
		return
	}

	// Only safe, end-to-end headers are forwarded; hop-by-hop headers,
	// Set-Cookie and renderer-defined security headers never pass through.
	if length := resp.Header.Get("Content-Length"); length != "" {
		if n, convErr := strconv.ParseInt(length, 10, 64); convErr == nil && n > MaxAssetBytes {
			http.Error(w, "renderer asset too large", http.StatusRequestEntityTooLarge)
			return
		}
	}
	for _, name := range []string{"Content-Type", "Content-Range", "Accept-Ranges", "ETag", "Cache-Control", "Last-Modified", "Expires"} {
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

	buf := make([]byte, 32*1024)
	limited := &io.LimitedReader{R: resp.Body, N: MaxAssetBytes + 1}
	n, _ := io.CopyBuffer(w, limited, buf)
	if n > MaxAssetBytes {
		// Content-Length was absent or wrong; truncate at the hard cap rather
		// than buffering an unbounded renderer response.
		h.logWarn("worldview proxy: asset exceeded %d bytes and was truncated", MaxAssetBytes)
	}
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
