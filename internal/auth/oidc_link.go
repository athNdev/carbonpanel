// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	oidcLinkCookie = "oidc_link_browser"
	oidcLinkPath   = "/api/v1/auth/oidc"
	oidcLinkPage   = "/settings?tab=auth&oidc_link="
	oidcLinkTTL    = 5 * time.Minute
)

var ErrOIDCLinkIneligible = errors.New("OIDC linking requires an active local admin session and an unlinked account")

type oidcLinkIntent struct {
	userID    string
	sessionID string
	browser   string
	createdAt time.Time
}

type oidcLinkStore struct {
	mu      sync.Mutex
	intents map[string]*oidcLinkIntent
	now     func() time.Time
}

func newOIDCLinkStore() *oidcLinkStore {
	return &oidcLinkStore{intents: make(map[string]*oidcLinkIntent), now: time.Now}
}

func (s *oidcLinkStore) create(userID, sessionID string) (string, *oidcLinkIntent, error) {
	token, err := generateState()
	if err != nil {
		return "", nil, err
	}
	browser, err := generateState()
	if err != nil {
		return "", nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, intent := range s.intents {
		if now.Sub(intent.createdAt) >= oidcLinkTTL {
			delete(s.intents, key)
		}
	}
	if len(s.intents) >= 100 {
		return "", nil, errors.New("oidc link store at capacity")
	}
	intent := &oidcLinkIntent{userID: userID, sessionID: sessionID, browser: browser, createdAt: now}
	s.intents[token] = intent
	return token, intent, nil
}

func (s *oidcLinkStore) consume(token, browser string) *oidcLinkIntent {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent := s.intents[token]
	if intent == nil || !sameOIDCState(intent.browser, browser) {
		return nil
	}
	delete(s.intents, token)
	if s.now().Sub(intent.createdAt) >= oidcLinkTTL {
		return nil
	}
	return intent
}

// CanLinkOIDC reports whether this credential can start a self-service link.
// API tokens, synthetic callers and sessions without a known origin cannot.
func (h *OIDCHandler) CanLinkOIDC(ctx context.Context, caller *AuthenticatedUser) bool {
	if h == nil || !h.IsEnabled() || caller == nil || caller.SessionProvider != "local" || caller.SessionID == "" {
		return false
	}
	ok, err := h.store.CanLinkOIDC(ctx, caller.ID, caller.SessionID)
	return err == nil && ok
}

func (h *OIDCHandler) BeginLink(ctx context.Context, caller *AuthenticatedUser) (string, *http.Cookie, error) {
	if !h.CanLinkOIDC(ctx, caller) {
		return "", nil, ErrOIDCLinkIneligible
	}
	token, intent, err := h.linkStore.create(caller.ID, caller.SessionID)
	if err != nil {
		return "", nil, err
	}
	h.emitAudit("oidc.link.begin", "ok", h.config.IssuerURI, "", caller.ID, "local_admin_explicit")
	return token, h.linkCookie(intent.browser, int(oidcLinkTTL.Seconds()), false), nil
}

func (h *OIDCHandler) linkCookie(value string, maxAge int, tls bool) *http.Cookie {
	return &http.Cookie{
		Name: oidcLinkCookie, Value: value, Path: oidcLinkPath, MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: tls || h.config.CookieSecure || strings.HasPrefix(h.config.RedirectURL, "https://"),
	}
}

// ClearLinkCookie ends the browser's pending link when its sign-in changes.
func (h *OIDCHandler) ClearLinkCookie() *http.Cookie {
	return h.linkCookie("", -1, false)
}

func linkBrowser(r *http.Request) string {
	cookie, err := r.Cookie(oidcLinkCookie)
	if err != nil {
		return ""
	}
	return cookie.Value
}

func (h *OIDCHandler) liveLink(ctx context.Context, intent *oidcLinkIntent) bool {
	if intent == nil || h.linkStore.now().Sub(intent.createdAt) >= oidcLinkTTL {
		return false
	}
	ok, err := h.store.CanLinkOIDC(ctx, intent.userID, intent.sessionID)
	return err == nil && ok
}

func writeLinkResult(w http.ResponseWriter, r *http.Request, result string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, oidcLinkPage+result, http.StatusFound)
}
