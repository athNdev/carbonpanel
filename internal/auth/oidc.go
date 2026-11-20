// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"golang.org/x/oauth2"
)

var ErrLinkRequired = errors.New("oidc identity requires explicit local admin linking")

const (
	oidcStateCookie    = "oidc_state"
	oidcSource         = "oidc"
	oidcLinkQuery      = "link_token"
	maxExtraClaimsBody = 1 << 20 // 1 MiB; bounded even if the operator URL misbehaves
)

// privilegedRoleNames are never granted from a raw claim value. Only an
// explicit reviewed role_mapping entry (operator configuration) may name them.
var privilegedRoleNames = map[string]struct{}{
	"admin": {}, "owner": {}, "root": {}, "superadmin": {}, "system": {},
}

type OIDCHandler struct {
	manager      *Manager
	store        *db.Store
	config       *config.OIDCConfig
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	oauth2Config *oauth2.Config
	httpClient   *http.Client
	log          *logger.Logger

	storesOnce       sync.Once
	txStore          *oidcTxStore
	exchangeStore    *oidcExchangeStore
	linkStore        *OIDCLinkStore
	loginThrottle    *LoginThrottle
	callbackThrottle *LoginThrottle
}

// oidcTransaction holds every parameter that must be consumed atomically
// before the authorization code is exchanged. It lives only in process memory.
type oidcTransaction struct {
	State        string
	Nonce        string
	CodeVerifier string
	CreatedAt    time.Time
	Issuer       string
	RedirectURL  string
	LinkUserID   string // set only for an explicit local-admin link flow
}

// oidcTxStore is a bounded, TTL'd, single-use transaction store. consume is a
// single locked compare-and-delete, so a state can be redeemed exactly once.
type oidcTxStore struct {
	mu      sync.Mutex
	entries map[string]*oidcTransaction
	max     int
	ttl     time.Duration
	now     func() time.Time
}

func newOIDCTxStore() *oidcTxStore {
	return &oidcTxStore{entries: make(map[string]*oidcTransaction), max: 10000, ttl: 5 * time.Minute, now: time.Now}
}

func (s *oidcTxStore) pruneLocked() {
	now := s.now()
	for key, tx := range s.entries {
		if now.Sub(tx.CreatedAt) > s.ttl {
			delete(s.entries, key)
		}
	}
}

func (s *oidcTxStore) create(tx *oidcTransaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	if len(s.entries) >= s.max {
		return errors.New("oidc transaction store at capacity")
	}
	s.entries[tx.State] = tx
	return nil
}

// consume atomically looks up, constant-time compares, and deletes the
// transaction. A second call with the same state always fails.
func (s *oidcTxStore) consume(state string) (*oidcTransaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, ok := s.entries[state]
	if !ok {
		return nil, false
	}
	delete(s.entries, state)
	if subtle.ConstantTimeCompare([]byte(tx.State), []byte(state)) != 1 {
		return nil, false
	}
	if s.now().Sub(tx.CreatedAt) > s.ttl {
		return nil, false
	}
	return tx, true
}

// oidcExchange is a one-time code that hands the SPA its session token without
// ever placing that token in a URL, Referer, history entry or access log.
type oidcExchange struct {
	Token     string
	RemoteIP  string
	CreatedAt time.Time
}

type oidcExchangeStore struct {
	mu      sync.Mutex
	entries map[string]*oidcExchange
	max     int
	ttl     time.Duration
	now     func() time.Time
}

func newOIDCExchangeStore() *oidcExchangeStore {
	return &oidcExchangeStore{entries: make(map[string]*oidcExchange), max: 1000, ttl: time.Minute, now: time.Now}
}

func (s *oidcExchangeStore) create(token, remoteIP string) (string, error) {
	code, err := generateState()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, entry := range s.entries {
		if now.Sub(entry.CreatedAt) > s.ttl {
			delete(s.entries, key)
		}
	}
	if len(s.entries) >= s.max {
		return "", errors.New("oidc exchange store at capacity")
	}
	s.entries[code] = &oidcExchange{Token: token, RemoteIP: remoteIP, CreatedAt: now}
	return code, nil
}

// consume redeems the code once and only for the client that started the
// callback. A mismatched client consumes the code without receiving a token.
func (s *oidcExchangeStore) consume(code, remoteIP string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[code]
	if !ok {
		return "", false
	}
	delete(s.entries, code)
	if s.now().Sub(entry.CreatedAt) > s.ttl || subtle.ConstantTimeCompare([]byte(entry.RemoteIP), []byte(remoteIP)) != 1 {
		return "", false
	}
	return entry.Token, true
}

// OIDCLinkIntent is a short-lived intent created by an authenticated local
// admin. It authorises one OIDC sign-in to bind the verified (issuer, subject)
// pair to that admin's existing account. No IdP-provided identity value is
// accepted from the operator; those are proven only by the callback.
type OIDCLinkIntent struct {
	UserID    string
	CreatedAt time.Time
}

// OIDCLinkStore is a bounded, TTL'd, single-use intent store. The token is an
// opaque capability, not a session credential, and is never logged.
type OIDCLinkStore struct {
	mu      sync.Mutex
	intents map[string]*OIDCLinkIntent
	max     int
	ttl     time.Duration
	now     func() time.Time
}

func newOIDCLinkStore() *OIDCLinkStore {
	return &OIDCLinkStore{intents: make(map[string]*OIDCLinkIntent), max: 100, ttl: 5 * time.Minute, now: time.Now}
}

// CreateIntent records a local admin's consent to link exactly one verified
// OIDC identity to their own account.
func (s *OIDCLinkStore) CreateIntent(userID string) (string, error) {
	if s == nil || userID == "" {
		return "", errors.New("oidc link target is required")
	}
	token, err := generateState()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for key, intent := range s.intents {
		if now.Sub(intent.CreatedAt) > s.ttl {
			delete(s.intents, key)
		}
	}
	if len(s.intents) >= s.max {
		return "", errors.New("oidc link store at capacity")
	}
	s.intents[token] = &OIDCLinkIntent{UserID: userID, CreatedAt: now}
	return token, nil
}

// consume atomically redeems a link token once and only while it is fresh.
func (s *OIDCLinkStore) consume(token string) (*OIDCLinkIntent, bool) {
	if s == nil || token == "" {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, ok := s.intents[token]
	if !ok {
		return nil, false
	}
	delete(s.intents, token)
	if s.now().Sub(intent.CreatedAt) > s.ttl {
		return nil, false
	}
	return intent, true
}

// LinkStore exposes the process-local intent store for the admin RPC seam.
func (h *OIDCHandler) LinkStore() *OIDCLinkStore {
	h.ensureStores()
	return h.linkStore
}

// oidcInsecureSkipVerify reports whether config skip_tls_verify may disable
// IdP certificate checks. The config flag alone never does (MINE-197).
func oidcInsecureSkipVerify(requested bool) bool {
	return requested && os.Getenv("CARBON_ALLOW_INSECURE_OIDC_TLS") == "1"
}

func NewOIDCHandler(manager *Manager, store *db.Store, cfg *config.OIDCConfig, log *logger.Logger) (*OIDCHandler, error) {
	h := &OIDCHandler{manager: manager, store: store, config: cfg, log: log}
	h.ensureStores()
	if !cfg.Enabled {
		return h, nil
	}

	var httpClient *http.Client
	if oidcInsecureSkipVerify(cfg.SkipTLSVerify) {
		httpClient = &http.Client{
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
		log.Warn("OIDC: TLS verification disabled because CARBON_ALLOW_INSECURE_OIDC_TLS=1")
	} else if cfg.SkipTLSVerify && log != nil {
		log.Warn("OIDC skip_tls_verify is set but ignored; set CARBON_ALLOW_INSECURE_OIDC_TLS=1 to honor it")
	}

	ctx := context.Background()
	if httpClient != nil {
		ctx = oidc.ClientContext(ctx, httpClient)
	}
	provider, err := oidc.NewProvider(ctx, cfg.IssuerURI)
	if err != nil {
		return nil, fmt.Errorf("failed to create OIDC provider: %w", err)
	}

	algs := cfg.AllowedSigningAlgs
	if len(algs) == 0 {
		algs = []string{"RS256", "ES256"}
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: algs})

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "profile", "email"}
	}

	h.provider = provider
	h.verifier = verifier
	h.httpClient = httpClient
	h.oauth2Config = &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  cfg.RedirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}
	return h, nil
}

func (h *OIDCHandler) ensureStores() {
	h.storesOnce.Do(func() {
		if h.txStore == nil {
			h.txStore = newOIDCTxStore()
		}
		if h.exchangeStore == nil {
			h.exchangeStore = newOIDCExchangeStore()
		}
		if h.linkStore == nil {
			h.linkStore = newOIDCLinkStore()
		}
		if h.loginThrottle == nil {
			h.loginThrottle = NewLoginThrottle()
		}
		if h.callbackThrottle == nil {
			h.callbackThrottle = NewLoginThrottle()
		}
	})
}

func (h *OIDCHandler) IsEnabled() bool {
	return h.config != nil && h.config.Enabled && h.provider != nil
}

func (h *OIDCHandler) sessionTimeout() int {
	if h.manager != nil && h.manager.config != nil && h.manager.config.SessionTimeout > 0 {
		return h.manager.config.SessionTimeout
	}
	return 3600
}

func clientIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func writeCanonicalError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/login?error="+code, http.StatusFound)
}

// emitAudit writes a redacted, allowlisted security event. Values such as
// state, nonce, authorization code, code verifier, tokens and raw claims are
// deliberately never included.
func (h *OIDCHandler) emitAudit(kind, outcome, issuer, subjectHash, userID, detail string) {
	if h == nil || h.log == nil {
		return
	}
	h.log.Info("security_audit event=%s outcome=%s issuer=%s subject_hash=%s user_id=%s detail=%s",
		kind, outcome, issuer, subjectHash, userID, detail)
}

func subjectHash(subject string) string {
	sum := sha256.Sum256([]byte(subject))
	return hex.EncodeToString(sum[:])
}

// consumeLinkToken redeems an explicit local-admin intent. The token carries
// no identity claims; it only names an already authenticated local admin.
func (h *OIDCHandler) consumeLinkToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", nil
	}
	intent, ok := h.linkStore.consume(token)
	if !ok {
		return "", errors.New("invalid or expired oidc link token")
	}
	user, err := h.store.GetUser(ctx, intent.UserID)
	if err != nil || !user.IsActive || user.AuthProvider != "local" || !userHasRoleFromSource(ctx, h.store, user.ID, "admin", "local") {
		return "", errors.New("oidc link target is no longer an active local admin")
	}
	return user.ID, nil
}

func userHasRoleFromSource(ctx context.Context, store *db.Store, userID, role, source string) bool {
	got, err := store.GetUserRoleSource(ctx, userID, role)
	return err == nil && got == source
}

// linkVerifiedOIDCIdentity binds a verified (issuer, subject) pair to the
// existing local admin named by an explicit intent. It never links by email,
// display name or locally unique subject, and never changes AuthProvider or
// existing role ownership.
func (h *OIDCHandler) linkVerifiedOIDCIdentity(ctx context.Context, userID, issuer, sub string) (*db.User, error) {
	if userID == "" || issuer == "" || sub == "" {
		return nil, errors.New("verified link identity is incomplete")
	}
	user, err := h.store.GetUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !user.IsActive || user.AuthProvider != "local" || !userHasRoleFromSource(ctx, h.store, user.ID, "admin", "local") {
		return nil, errors.New("oidc link target is not an active local admin")
	}
	if user.OIDCSubject != "" || user.OIDCIssuer != "" {
		return nil, ErrLinkRequired
	}
	if existing, err := h.store.GetUserByOIDCIssuerAndSubject(ctx, issuer, sub); err == nil && existing != nil {
		return nil, ErrLinkRequired
	}
	if existing, err := h.store.GetUserByOIDCSubject(ctx, sub); err == nil && existing != nil {
		return nil, ErrLinkRequired
	}
	user.OIDCIssuer = issuer
	user.OIDCSubject = sub
	if err := h.store.UpdateUser(ctx, user); err != nil {
		return nil, err
	}
	h.emitAudit("oidc.link.completed", "ok", issuer, subjectHash(sub), user.ID, "local_admin_explicit")
	return user, nil
}

func withoutRole(roles []string, role string) []string {
	// Allocate a fresh backing array: the caller removes roles while ranging
	// over the original slice, so in-place compaction would skip following
	// privileged roles and leave an unreconciled OIDC grant behind.
	out := make([]string, 0, len(roles))
	for _, item := range roles {
		if item != role {
			out = append(out, item)
		}
	}
	return out
}

func (h *OIDCHandler) HandleLogin(w http.ResponseWriter, r *http.Request) {
	h.ensureStores()
	if !h.IsEnabled() {
		http.Error(w, "OIDC is not enabled", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	if ok, wait := h.loginThrottle.Allow("start:" + ip); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		writeCanonicalError(w, r, "locked_out")
		return
	}

	linkUserID, err := h.consumeLinkToken(r.Context(), r.URL.Query().Get(oidcLinkQuery))
	if err != nil {
		h.emitAudit("oidc.link.started", "rejected", h.config.IssuerURI, "", "", "invalid_or_ineligible_intent")
		writeCanonicalError(w, r, "link_invalid")
		return
	}

	state, err := generateState()
	if err != nil {
		http.Error(w, "Failed to generate state", http.StatusInternalServerError)
		return
	}
	nonce, err := generateState()
	if err != nil {
		http.Error(w, "Failed to generate nonce", http.StatusInternalServerError)
		return
	}
	verifierRaw := make([]byte, 32)
	if _, err := rand.Read(verifierRaw); err != nil {
		http.Error(w, "Failed to generate PKCE verifier", http.StatusInternalServerError)
		return
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierRaw)

	tx := &oidcTransaction{
		State:        state,
		Nonce:        nonce,
		CodeVerifier: verifier,
		CreatedAt:    time.Now(),
		Issuer:       h.config.IssuerURI,
		RedirectURL:  h.config.RedirectURL,
		LinkUserID:   linkUserID,
	}
	if err := h.txStore.create(tx); err != nil {
		h.emitAudit("oidc.login.started", "rejected", h.config.IssuerURI, "", "", "transaction_store_full")
		http.Error(w, "Too many sign-in attempts", http.StatusServiceUnavailable)
		return
	}

	cookie := &http.Cookie{
		Name:     oidcStateCookie,
		Value:    state,
		Path:     "/",
		MaxAge:   300,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || h.config.CookieSecure,
	}
	http.SetCookie(w, cookie)

	authURL := h.oauth2Config.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
	h.emitAudit("oidc.login.started", "ok", h.config.IssuerURI, "", "", "")
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *OIDCHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	h.ensureStores()
	if !h.IsEnabled() {
		http.Error(w, "OIDC is not enabled", http.StatusBadRequest)
		return
	}
	ip := clientIP(r)
	if ok, wait := h.callbackThrottle.Allow("callback:" + ip); !ok {
		w.Header().Set("Retry-After", fmt.Sprintf("%d", int(wait.Seconds())+1))
		writeCanonicalError(w, r, "locked_out")
		return
	}

	stateCookie, err := r.Cookie(oidcStateCookie)
	queryState := r.URL.Query().Get("state")
	if err != nil || !sameOIDCState(stateCookie.Value, queryState) {
		// Best-effort consume so a mismatched callback cannot leave a live tx.
		h.txStore.consume(queryState)
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "invalid_state")
		writeCanonicalError(w, r, "invalid_state")
		return
	}

	// Single atomic compare-and-delete: all transaction parameters are spent
	// before any code exchange. Replay and concurrent callbacks get nothing.
	tx, ok := h.txStore.consume(queryState)
	if !ok || tx.Issuer != h.config.IssuerURI {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "invalid_or_replayed_state")
		writeCanonicalError(w, r, "invalid_state")
		return
	}

	// Clear the state cookie. The cookie is not the security boundary; the
	// atomic transaction consume above is.
	http.SetCookie(w, &http.Cookie{Name: oidcStateCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: r.TLS != nil || h.config.CookieSecure})

	if idpErr := r.URL.Query().Get("error"); idpErr != "" {
		code := "access_denied"
		if idpErr == "temporarily_unavailable" {
			code = "oidc_degraded"
		}
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "idp_error")
		writeCanonicalError(w, r, code)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "missing_code")
		writeCanonicalError(w, r, "invalid_state")
		return
	}

	ctx := r.Context()
	if h.httpClient != nil {
		ctx = oidc.ClientContext(ctx, h.httpClient)
	}

	oauth2Token, err := h.oauth2Config.Exchange(ctx, code, oauth2.VerifierOption(tx.CodeVerifier))
	if err != nil {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "code_exchange_failed")
		writeCanonicalError(w, r, "oidc_degraded")
		return
	}

	rawIDToken, ok := oauth2Token.Extra("id_token").(string)
	if !ok {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "missing_id_token")
		writeCanonicalError(w, r, "oidc_degraded")
		return
	}

	idToken, err := h.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "id_token_verification_failed")
		writeCanonicalError(w, r, "access_denied")
		return
	}
	// go-oidc's Verify does not check nonce (oidc/verify.go:183); the RP must.
	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(tx.Nonce)) != 1 {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "nonce_mismatch")
		writeCanonicalError(w, r, "access_denied")
		return
	}
	if idToken.Issuer != h.config.IssuerURI {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "issuer_mismatch")
		writeCanonicalError(w, r, "invalid_issuer")
		return
	}

	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "claims_parse_failed")
		writeCanonicalError(w, r, "access_denied")
		return
	}

	tokenSource := h.oauth2Config.TokenSource(ctx, oauth2Token)
	if userInfo, err := h.provider.UserInfo(ctx, tokenSource); err == nil {
		var uiClaims map[string]any
		if err := userInfo.Claims(&uiClaims); err == nil {
			for k, v := range uiClaims {
				if _, exists := claims[k]; !exists {
					claims[k] = v
				}
			}
		}
	}

	if h.config.ExtraClaimsURL != "" {
		extra, err := h.fetchExtraClaims(ctx, oauth2Token.AccessToken)
		if err != nil {
			h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "extra_claims_failed")
			writeCanonicalError(w, r, "membership_check_failed")
			return
		}
		mergeExtraClaims(claims, extra, h.config.RoleClaim)
	}

	if h.config.RequiredClaim != "" && len(h.config.RequiredValues) > 0 && !h.checkRequiredClaim(claims) {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "required_claim_unsatisfied")
		writeCanonicalError(w, r, "access_denied")
		return
	}

	sub := idToken.Subject
	if sub == "" {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, "", "", "missing_subject")
		writeCanonicalError(w, r, "access_denied")
		return
	}
	hash := subjectHash(sub)
	email, _ := claims["email"].(string)
	username, _ := claims["preferred_username"].(string)
	if username == "" {
		username, _ = claims["name"].(string)
	}
	if username == "" {
		username = email
	}
	if username == "" {
		username = sub
	}

	desired := h.resolveClaimRoles(claims)
	if len(desired) == 0 && h.config.RejectUnmapped {
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, hash, "", "no_mapped_roles")
		writeCanonicalError(w, r, "no_mapped_roles")
		return
	}
	if len(desired) == 0 {
		switch h.config.AbsentClaimPolicy {
		case "defaults_only":
			desired = append([]string(nil), h.config.DefaultRoles...)
		default:
			// deny is the fail-closed default: revoke every OIDC-sourced role.
			desired = nil
		}
	}

	var user *db.User
	linkedLocalAdmin := tx.LinkUserID != ""
	if linkedLocalAdmin {
		user, err = h.linkVerifiedOIDCIdentity(ctx, tx.LinkUserID, h.config.IssuerURI, sub)
	} else {
		user, err = h.findOrCreateOIDCUser(ctx, h.config.IssuerURI, sub, username, email)
	}
	if err != nil {
		if errors.Is(err, ErrLinkRequired) {
			h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, hash, "", "link_required")
			writeCanonicalError(w, r, "link_required")
			return
		}
		h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, hash, "", "user_resolution_failed")
		writeCanonicalError(w, r, "account_disabled")
		return
	}

	for _, role := range desired {
		if !isPrivilegedRole(role) {
			continue
		}
		source, srcErr := h.store.GetUserRoleSource(ctx, user.ID, role)
		if srcErr == nil && source != "" && source != oidcSource {
			// An explicitly linked local admin keeps local ownership of their
			// privileged grant. The OIDC mapping is accepted as proof of the
			// sign-in, but it does not silently reattribute or revoke that
			// break-glass grant.
			if linkedLocalAdmin && user.AuthProvider == "local" && source == "local" &&
				user.OIDCIssuer == h.config.IssuerURI && user.OIDCSubject == sub {
				desired = withoutRole(desired, role)
				continue
			}
			h.emitAudit("oidc.login.callback", "rejected", h.config.IssuerURI, hash, user.ID, "privileged_role_conflict")
			writeCanonicalError(w, r, "role_conflict")
			return
		}
	}

	added, removed, err := h.store.SetUserRolesBySource(ctx, user.ID, oidcSource, desired)
	if err != nil {
		h.emitAudit("oidc.roles.reconciled", "failed", h.config.IssuerURI, hash, user.ID, "transaction_failed")
		writeCanonicalError(w, r, "oidc_degraded")
		return
	}

	// Rotate sessions on sign-in: any prior credential for this user stops
	// working before the new one is issued.
	if err := h.manager.InvalidateUserSessions(ctx, user.ID, ""); err != nil {
		h.emitAudit("oidc.session.rotated", "failed", h.config.IssuerURI, hash, user.ID, "")
		writeCanonicalError(w, r, "oidc_degraded")
		return
	}

	roleNames, err := h.store.GetUserRoleNames(ctx, user.ID)
	if err != nil {
		roleNames = []string{}
	}
	expiresAt := time.Now().Add(time.Duration(h.sessionTimeout()) * time.Second)
	token, err := h.manager.generateJWT(user.ID, user.Username, roleNames, expiresAt)
	if err != nil {
		h.emitAudit("oidc.session.issued", "failed", h.config.IssuerURI, hash, user.ID, "")
		writeCanonicalError(w, r, "oidc_degraded")
		return
	}
	session := &db.Session{ID: uuid.New().String(), UserID: user.ID, Token: token, ExpiresAt: expiresAt}
	if err := h.store.CreateSession(ctx, session); err != nil {
		h.emitAudit("oidc.session.issued", "failed", h.config.IssuerURI, hash, user.ID, "")
		writeCanonicalError(w, r, "oidc_degraded")
		return
	}

	exchangeCode, err := h.exchangeStore.create(token, ip)
	if err != nil {
		_ = h.store.DeleteSession(ctx, token)
		h.emitAudit("oidc.session.issued", "failed", h.config.IssuerURI, hash, user.ID, "exchange_store_full")
		writeCanonicalError(w, r, "oidc_degraded")
		return
	}

	h.emitAudit("oidc.login.succeeded", "ok", h.config.IssuerURI, hash, user.ID,
		fmt.Sprintf("added=%d removed=%d", len(added), len(removed)))
	http.Redirect(w, r, "/login?code="+exchangeCode, http.StatusFound)
}

// HandleExchange redeems a one-time OIDC exchange code for the session token.
// The token itself never appears in a URL, Referer, history entry or access log.
func (h *OIDCHandler) HandleExchange(w http.ResponseWriter, r *http.Request) {
	h.ensureStores()
	w.Header().Set("Cache-Control", "no-store")
	if !h.IsEnabled() {
		http.Error(w, "OIDC is not enabled", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	token, ok := h.exchangeStore.consume(req.Code, clientIP(r))
	if !ok {
		h.emitAudit("oidc.exchange", "rejected", "", "", "", "invalid_or_replayed_code")
		http.Error(w, "invalid exchange code", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// findOrCreateOIDCUser resolves a verified (issuer, subject) pair. It never
// links by email/display name and never auto-binds an issuer-less legacy row.
func (h *OIDCHandler) findOrCreateOIDCUser(ctx context.Context, issuer, sub, username, email string) (*db.User, error) {
	user, err := h.store.GetUserByOIDCIssuerAndSubject(ctx, issuer, sub)
	if err == nil {
		if !user.IsActive {
			return nil, ErrUserNotActive
		}
		if email != "" {
			user.Email = &email
		}
		now := time.Now()
		user.LastLogin = &now
		_ = h.store.UpdateUser(ctx, user)
		return user, nil
	}

	// A subject is unique only within an issuer. Any pre-existing row with the
	// same subject but a different or unknown issuer cannot prove ownership of
	// this (issuer, subject) pair: refuse and require explicit admin linking.
	if _, lerr := h.store.GetUserByOIDCSubject(ctx, sub); lerr == nil {
		return nil, ErrLinkRequired
	}

	var emailPtr *string
	if email != "" {
		emailPtr = &email
	}
	user = &db.User{
		ID:           uuid.New().String(),
		Username:     username,
		Email:        emailPtr,
		AuthProvider: oidcSource,
		OIDCSubject:  sub,
		OIDCIssuer:   issuer,
		IsActive:     true,
	}
	if err := h.store.CreateUser(ctx, user); err != nil {
		return nil, fmt.Errorf("failed to create OIDC user: %w", err)
	}
	// No implicit admin. New OIDC identities receive only configured defaults,
	// already applied through source-scoped reconciliation by the caller.
	return user, nil
}

func isPrivilegedRole(role string) bool {
	_, ok := privilegedRoleNames[strings.ToLower(role)]
	return ok
}

// Resolve OIDC claim values to local roles.
func (h *OIDCHandler) resolveClaimRoles(claims map[string]any) []string {
	if h.config.RoleClaim == "" {
		return nil
	}

	var claimValues []string
	claimValue, ok := claims[h.config.RoleClaim]
	if !ok {
		return nil
	}
	switch v := claimValue.(type) {
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok {
				claimValues = append(claimValues, s)
			}
		}
	case []string:
		claimValues = append(claimValues, v...)
	case string:
		var arr []string
		if err := json.Unmarshal([]byte(v), &arr); err == nil {
			claimValues = arr
		} else {
			claimValues = []string{v}
		}
	}

	var resolved []string
	if len(h.config.RoleMapping) > 0 {
		for _, claimVal := range claimValues {
			for mapKey, localRole := range h.config.RoleMapping {
				if strings.EqualFold(claimVal, mapKey) {
					resolved = append(resolved, localRole)
					break
				}
			}
		}
		return dedupeRoles(resolved)
	}
	// Claim values are used directly only with an explicit opt-in. Even then,
	// privileged role names from a raw claim are never granted.
	if h.config.AllowRawClaimRoles && !h.config.RejectUnmapped {
		for _, role := range claimValues {
			if isPrivilegedRole(role) {
				continue
			}
			resolved = append(resolved, role)
		}
	}
	return dedupeRoles(resolved)
}

func dedupeRoles(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, role := range in {
		if role == "" {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		out = append(out, role)
	}
	return out
}

// Calls the configured extra claims URL with the access token.
func (h *OIDCHandler) fetchExtraClaims(ctx context.Context, accessToken string) (map[string]any, error) {
	client := http.DefaultClient
	if h.httpClient != nil {
		client = h.httpClient
	}

	req, err := http.NewRequestWithContext(ctx, "GET", h.config.ExtraClaimsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, body)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxExtraClaimsBody))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if len(body) >= maxExtraClaimsBody {
		return nil, fmt.Errorf("response exceeds %d bytes", maxExtraClaimsBody)
	}

	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("response is not valid JSON")
	}

	name := h.config.ExtraClaimsName
	if name == "" {
		name = "extra"
	}

	if h.config.ExtraClaimsKey == "" {
		var parsed any
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, fmt.Errorf("failed to parse response: %w", err)
		}
		return map[string]any{name: parsed}, nil
	}

	result := gjson.GetBytes(body, h.config.ExtraClaimsKey)
	if !result.Exists() {
		return nil, fmt.Errorf("key %q not found in response", h.config.ExtraClaimsKey)
	}
	return map[string]any{name: gjsonToAny(result)}, nil
}

func gjsonToAny(r gjson.Result) any {
	if r.IsArray() {
		var out []any
		r.ForEach(func(_, v gjson.Result) bool {
			out = append(out, gjsonToAny(v))
			return true
		})
		return out
	}
	if r.IsObject() {
		out := map[string]any{}
		r.ForEach(func(k, v gjson.Result) bool {
			out[k.String()] = gjsonToAny(v)
			return true
		})
		return out
	}
	return r.Value()
}

// mergeExtraClaims enriches claims without ever overwriting a verified value.
// Identity claims and the configured role-claim key are protected outright, so
// a configured extra_claims_url cannot impersonate a subject or elevate roles
// even when the IdP omitted the role claim. The source map is never mutated.
func mergeExtraClaims(claims, extra map[string]any, roleClaim string) {
	protected := map[string]struct{}{
		"sub": {}, "iss": {}, "aud": {}, "exp": {}, "iat": {}, "nbf": {},
		"nonce": {}, "jti": {}, "email": {}, "preferred_username": {}, "name": {},
	}
	if roleClaim != "" {
		protected[roleClaim] = struct{}{}
	}
	for key, value := range extra {
		if _, blocked := protected[key]; blocked {
			continue
		}
		if _, exists := claims[key]; exists {
			continue
		}
		claims[key] = value
	}
}

func (h *OIDCHandler) checkRequiredClaim(claims map[string]any) bool {
	value, ok := claims[h.config.RequiredClaim]
	if !ok {
		return false
	}
	required := make(map[string]bool, len(h.config.RequiredValues))
	for _, v := range h.config.RequiredValues {
		required[v] = true
	}
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			if required[fmt.Sprint(item)] {
				return true
			}
		}
	case []string:
		for _, item := range v {
			if required[item] {
				return true
			}
		}
	case string:
		return required[v]
	default:
		return required[fmt.Sprint(v)]
	}
	return false
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

func sameOIDCState(cookieState, callbackState string) bool {
	if cookieState == "" || len(cookieState) != len(callbackState) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(cookieState), []byte(callbackState)) == 1
}
