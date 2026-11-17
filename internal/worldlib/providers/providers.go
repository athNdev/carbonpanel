// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package providers implements axis-(a) API-backed artifact search for the
// World Library. It deliberately does NOT automate prebuilt-world discovery
// (axis b): a modpack or datapack is not a prebuilt world, and manual platforms
// are represented in capabilities but never queried.
package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/indexers"
)

// Provider identifiers. Manual platforms are never passed to Search.
const (
	ProviderModrinth         = "modrinth"
	ProviderCurseForge       = "curseforge"
	ProviderPlanetMinecraft  = "planet_minecraft"
	ProviderMinecraftMaps    = "minecraftmaps"
	ProviderBedrockMarketplace = "bedrock_marketplace"

	// DiscoveryGateD11 is the decision that gates prebuilt-world automation.
	DiscoveryGateD11 = "D11"

	defaultUserAgent = "CarbonPanel/1.0 (github.com/athNdev/carbon-panel)"
	maxResults       = 20
	requestTimeout   = 8 * time.Second
	maxRetryBackoff  = 2 * time.Second
)

// SearchRequest is the provider-neutral artifact search input. Cursor is an
// opaque provider token (an offset for Modrinth, an index for CurseForge).
type SearchRequest struct {
	Query       string
	MCVersion   string
	Loader      string
	ProjectType string
	Cursor      string
	Limit       int
}

// Artifact is one API-backed content artifact. DownloadURL is returned as an
// untrusted string only; credentials are never attached when it is fetched.
// SHA256 is populated only when the provider publishes a genuine SHA-256.
type Artifact struct {
	Provider    string
	ProjectID   string
	FileID      string
	Name        string
	ProjectType string
	MCVersion   string
	Loader      string
	DownloadURL string
	SHA256      string
	License     string
	SourceURL   string
}

// Capability describes what a provider actually supports. PrebuiltWorldCatalog
// is false for every provider in this implementation.
type Capability struct {
	Provider            string
	APIBackedArtifacts  bool
	PrebuiltWorldCatalog bool
	DiscoveryGate       string
	Note                string
}

// Searcher is implemented by the API-backed providers.
type Searcher interface {
	Search(ctx context.Context, req SearchRequest) ([]Artifact, string, bool, error)
}

// Typed errors let RPC handlers map failures without string matching.
var (
	ErrRateLimit              = errors.New("provider rate limit exceeded")
	ErrUnauthorized           = errors.New("provider rejected credentials")
	ErrForbidden              = errors.New("provider access forbidden")
	ErrNotFound               = errors.New("provider resource not found")
	ErrCategoryUnavailable    = errors.New("requested category is no longer available")
	ErrUnsupportedProjectType = errors.New("unsupported artifact project type")

	// ErrCredentialBoundary mirrors worldlib.ErrCredentialBoundary. It is kept
	// local so this leaf package stays independently buildable and does not
	// couple to in-progress worldlib implementation files.
	ErrCredentialBoundary = errors.New("credential may only be sent to the authorized API origin")
)

// APIError is the structured failure for a non-2xx provider response.
// It implements errors.Is for the status sentinels above.
type APIError struct {
	Provider      string
	StatusCode    int
	URL           string
	Message       string
	RetryAfter    time.Duration
	HasRetryAfter bool
}

func (e *APIError) Error() string {
	msg := e.Message
	if len(msg) > 256 {
		msg = msg[:256]
	}
	return fmt.Sprintf("%s API %d at %s: %s", e.Provider, e.StatusCode, e.URL, msg)
}

// Is maps the status code to a typed sentinel so callers can branch with
// errors.Is without depending on human-readable text.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrRateLimit:
		return e.StatusCode == http.StatusTooManyRequests
	case ErrUnauthorized:
		return e.StatusCode == http.StatusUnauthorized
	case ErrForbidden:
		return e.StatusCode == http.StatusForbidden
	case ErrNotFound:
		return e.StatusCode == http.StatusNotFound
	}
	return false
}

// Capabilities reports the real support boundary for each known platform.
// Prebuilt-world catalog automation is never promised; manual platforms carry
// a D11 discovery gate and are never searched.
func Capabilities() []Capability {
	axisNote := "Axis (a) only: API-backed content artifacts (mods, modpacks, resource packs, shaders, datapacks). " +
		"A modpack or datapack is not a prebuilt world; axis (b) prebuilt-world catalog automation is not provided and is gated by D11. Import worlds manually."
	manualNote := "Manual platform: no API-backed artifact search and no verified prebuilt-world catalog. Import manually; discovery gated by D11."
	return []Capability{
		{
			Provider:            ProviderModrinth,
			APIBackedArtifacts:  true,
			PrebuiltWorldCatalog: false,
			DiscoveryGate:       DiscoveryGateD11,
			Note:                axisNote + " Modrinth documents no 'world' project type; no world category is requested or claimed.",
		},
		{
			Provider:            ProviderCurseForge,
			APIBackedArtifacts:  true,
			PrebuiltWorldCatalog: false,
			DiscoveryGate:       DiscoveryGateD11,
			Note:                axisNote + " CurseForge categories (including any world class) are discovered at runtime; class ids are not assumed.",
		},
		{
			Provider:            ProviderPlanetMinecraft,
			APIBackedArtifacts:  false,
			PrebuiltWorldCatalog: false,
			DiscoveryGate:       DiscoveryGateD11,
			Note:                manualNote,
		},
		{
			Provider:            ProviderMinecraftMaps,
			APIBackedArtifacts:  false,
			PrebuiltWorldCatalog: false,
			DiscoveryGate:       DiscoveryGateD11,
			Note:                manualNote,
		},
		{
			Provider:            ProviderBedrockMarketplace,
			APIBackedArtifacts:  false,
			PrebuiltWorldCatalog: false,
			DiscoveryGate:       DiscoveryGateD11,
			Note:                manualNote,
		},
	}
}

// DownloadURLIsAllowed is the static SSRF pre-check for operator-supplied or
// provider-supplied download URLs. It mirrors the policy idea of
// minecraft.DownloadAllowsPrivate: unresolvable or private/loopback targets
// fail closed. It performs no DNS resolution so it is deterministic offline;
// connection-time guards remain the responsibility of the downloading
// pipeline. A self-hosted private origin must be explicitly allowed by
// operator configuration rather than by weakening this default.
func DownloadURLIsAllowed(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return false
	}
	host := u.Hostname()
	if host == "" {
		return false
	}
	// A literal IP is refused outright: operators who deliberately trust a
	// private origin must opt in through their own configuration, not here.
	if net.ParseIP(host) != nil {
		return false
	}
	return true
}

// originKey canonicalises a URL to scheme://host:port with explicit ports so
// scheme+host+port scoping works for default and non-default ports alike.
func originKey(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + host + ":" + port
}

// credentialHeaders are stripped whenever a request is not addressed to the
// authorized API origin, including on cross-origin redirects.
var credentialHeaders = []string{"X-Api-Key", "Authorization", "Proxy-Authorization", "Cookie"}

func stripCredentialHeaders(h http.Header) {
	for _, name := range credentialHeaders {
		h.Del(name)
	}
}

// scopedKeyTransport attaches the CurseForge key only to requests whose
// scheme+host+port matches the configured API origin. It never mutates the
// caller's request: the credential lives only on the cloned request passed to
// the base transport, so the http.Client redirect logic never sees it.
type scopedKeyTransport struct {
	base      http.RoundTripper
	apiKey    string
	apiOrigin string
	userAgent string
}

func (t *scopedKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	stripCredentialHeaders(clone.Header)
	if t.userAgent != "" && clone.Header.Get("User-Agent") == "" {
		clone.Header.Set("User-Agent", t.userAgent)
	}
	if t.apiKey != "" {
		if key, err := t.keyFor(req.URL); err == nil {
			clone.Header.Set("X-Api-Key", key)
		}
	}
	return t.base.RoundTrip(clone)
}

// keyFor refuses to release the credential for any other origin. The returned
// error wraps ErrCredentialBoundary for typed handling in tests and callers;
// the transport itself simply omits the header and proceeds.
func (t *scopedKeyTransport) keyFor(u *url.URL) (string, error) {
	if originKey(u) != t.apiOrigin {
		return "", fmt.Errorf("%w: refusing x-api-key for %q (authorized origin %q)",
			ErrCredentialBoundary, u.String(), t.apiOrigin)
	}
	return t.apiKey, nil
}

// newScopedClient builds every provider http.Client with both layers of
// protection: redirect stripping and origin-scoped credential attachment.
func newScopedClient(base *url.URL, apiKey, userAgent string) *http.Client {
	transport := &scopedKeyTransport{
		base: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			TLSHandshakeTimeout:   10 * time.Second,
			MaxIdleConns:          20,
			IdleConnTimeout:       30 * time.Second,
			ExpectContinueTimeout: time.Second,
			ForceAttemptHTTP2:     true,
		},
		apiKey:    apiKey,
		apiOrigin: originKey(base),
		userAgent: userAgent,
	}
	client := &http.Client{Transport: transport, Timeout: requestTimeout}
	return indexers.ProtectCredentialsOnRedirect(client)
}

func normalizeUserAgent(ua string) string {
	if trimmed := strings.TrimSpace(ua); trimmed != "" {
		return trimmed
	}
	return defaultUserAgent
}

func clampLimit(limit int) int {
	if limit <= 0 {
		return maxResults
	}
	if limit > maxResults {
		return maxResults
	}
	return limit
}

func clampOffset(cursor string) int {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return 0
	}
	var offset int
	if _, err := fmt.Sscanf(cursor, "%d", &offset); err != nil || offset < 0 {
		return 0
	}
	return offset
}

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// statusError classifies a non-2xx response, parsing Retry-After when present.
func statusError(provider string, resp *http.Response) *APIError {
	body := make([]byte, 0, 256)
	if resp.Body != nil {
		buf := make([]byte, 2048)
		n, _ := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
	}
	e := &APIError{
		Provider:   provider,
		StatusCode: resp.StatusCode,
		Message:    strings.TrimSpace(string(body)),
	}
	if resp.Request != nil && resp.Request.URL != nil {
		e.URL = resp.Request.URL.String()
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if raw := strings.TrimSpace(resp.Header.Get("Retry-After")); raw != "" {
			if secs, err := strconv.Atoi(raw); err == nil && secs >= 0 {
				e.RetryAfter = time.Duration(secs) * time.Second
				e.HasRetryAfter = true
			} else if when, err := http.ParseTime(raw); err == nil {
				e.RetryAfter = maxDuration(0, time.Until(when))
				e.HasRetryAfter = true
			}
		}
	}
	return e
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

// getJSON performs one bounded GET and decodes a JSON response.
func getJSON(ctx context.Context, client *http.Client, userAgent, provider, rawURL string, dest any) error {
	callCtx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(callCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("build provider request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("provider request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return statusError(provider, resp)
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("malformed provider response: %w", err)
	}
	return nil
}

// getJSONWithRetry performs a single bounded backoff retry on HTTP 429. It
// honors Retry-After when present (capped at maxRetryBackoff) and is always
// context-cancellable, keeping tests fast and deterministic.
func getJSONWithRetry(ctx context.Context, client *http.Client, userAgent, provider, rawURL string, dest any) (bool, error) {
	for attempt := 0; ; attempt++ {
		err := getJSON(ctx, client, userAgent, provider, rawURL, dest)
		if err == nil {
			return false, nil
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			delay := 100 * time.Millisecond
			if apiErr.HasRetryAfter {
				delay = apiErr.RetryAfter
			}
			if delay > maxRetryBackoff {
				delay = maxRetryBackoff
			}
			if waitErr := sleepContext(ctx, delay); waitErr != nil {
				return true, waitErr
			}
			continue
		}
		if errors.Is(err, ErrRateLimit) {
			return true, err
		}
		return false, err
	}
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
