// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

var (
	ErrBlockedIP     = errors.New("connection blocked by SSRF guard")
	ErrInvalidScheme = errors.New("invalid URL scheme: must be http or https")
	ErrMissingHost   = errors.New("url is missing host")
	// ErrInvalidURL is returned instead of chaining url.Parse's error, which
	// embeds the entire raw URL (including userinfo credentials) in its text.
	ErrInvalidURL = errors.New("invalid URL")
)

// parseURLForPolicy parses a URL for an SSRF decision without ever reflecting
// the raw input. url.Error contains the whole URL, so it is replaced by a
// sentinel and the caller's redacted form.
func parseURLForPolicy(rawURL string) (*url.URL, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidURL, RedactURL(rawURL))
	}
	return parsed, nil
}

// loopbackTestAllowed is the whole test-only loopback escape hatch, kept as a
// pure predicate so its fail-closed production branch is directly testable.
//
// The historical DISCO_ALLOW_LOOPBACK_TEST switch is honoured ONLY in a Go test
// binary: testing.Testing() is false in every production build, regardless of
// the process environment. Loopback httptest fixtures opt in explicitly with
// t.Setenv. This satisfies the hardening spec's "explicit test configuration"
// requirement without a mutable production variable or a build tag that could
// be omitted from a release build.
func loopbackTestAllowed(isTestBinary bool, envValue string) bool {
	return isTestBinary && envValue == "1"
}

// ValidateIP checks whether an IP address belongs to denied ranges (loopback, link-local, private RFC1918)
func ValidateIP(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return fmt.Errorf("%w: invalid IP", ErrBlockedIP)
	}

	// Always deny loopback (127.0.0.0/8, ::1) unless an explicit test binary
	// fixture opted in. Production builds can never take this branch.
	if ip.IsLoopback() {
		if loopbackTestAllowed(testing.Testing(), os.Getenv("DISCO_ALLOW_LOOPBACK_TEST")) {
			return nil
		}
		return fmt.Errorf("%w: loopback address %s is prohibited", ErrBlockedIP, ip)
	}

	// Always deny link-local (169.254.0.0/16, fe80::/10)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return fmt.Errorf("%w: link-local address %s is prohibited", ErrBlockedIP, ip)
	}

	// Always deny unspecified (0.0.0.0, ::)
	if ip.IsUnspecified() {
		return fmt.Errorf("%w: unspecified address %s is prohibited", ErrBlockedIP, ip)
	}

	// Always deny multicast
	if ip.IsMulticast() {
		return fmt.Errorf("%w: multicast address %s is prohibited", ErrBlockedIP, ip)
	}

	// Deny private RFC1918 / RFC4193 unless explicitly allowed
	if !allowPrivate && ip.IsPrivate() {
		return fmt.Errorf("%w: private network address %s is prohibited", ErrBlockedIP, ip)
	}

	return nil
}

// ValidateURLStatic validates a URL without performing DNS resolution. It
// enforces the scheme and, when the host is an IP literal, the address policy.
// Use it where a DNS lookup must not happen (e.g. saving configuration);
// ValidateURL remains the resolving variant for actual outbound requests.
func ValidateURLStatic(rawURL string, allowPrivate bool) (*url.URL, error) {
	parsed, err := parseURLForPolicy(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, ErrInvalidScheme
	}
	hostname := parsed.Hostname()
	if hostname == "" {
		return nil, ErrMissingHost
	}
	if ip := net.ParseIP(hostname); ip != nil {
		if err := ValidateIP(ip, allowPrivate); err != nil {
			return nil, err
		}
	}
	return parsed, nil
}

// ValidateURL performs static and DNS resolution validation on a URL
func ValidateURL(rawURL string, allowPrivate bool) (*url.URL, error) {
	parsed, err := parseURLForPolicy(rawURL)
	if err != nil {
		return nil, err
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, ErrInvalidScheme
	}

	hostname := parsed.Hostname()
	if hostname == "" {
		return nil, ErrMissingHost
	}

	// If hostname is directly an IP literal
	if ip := net.ParseIP(hostname); ip != nil {
		if err := ValidateIP(ip, allowPrivate); err != nil {
			return nil, err
		}
		return parsed, nil
	}

	// Resolve DNS records
	ips, err := net.LookupIP(hostname)
	if err != nil {
		return nil, fmt.Errorf("DNS resolution failed for %s: %w", hostname, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("no IP addresses resolved for %s", hostname)
	}

	for _, ip := range ips {
		if err := ValidateIP(ip, allowPrivate); err != nil {
			return nil, err
		}
	}

	return parsed, nil
}

// EgressClass names the three distinct outbound-policy families. It is kept
// exported for logging and audits, but the decision itself lives in
// EgressPolicy: a bare class plus a boolean was not enough to keep a
// user-supplied archive URL from inheriting a webhook receiver's latitude.
type EgressClass string

const (
	// EgressClassProvider uses a fixed host allowlist. allowPrivate is derived
	// from the trusted indexer policy, never from the URL being fetched.
	EgressClassProvider EgressClass = "provider"
	// EgressClassWebhook uses an explicit origin list of operator-configured
	// receivers. A redirect to any other origin is refused.
	EgressClassWebhook EgressClass = "webhook"
	// EgressClassArchive is a user-supplied remote archive URL. RFC1918 targets
	// require both the operator flag and a named destination node.
	EgressClassArchive EgressClass = "archive"
)

// EgressPolicy is the complete, class-specific outbound decision. Its zero
// value fails closed. Construct one with ProviderEgressPolicy,
// WebhookEgressPolicy or ArchiveEgressPolicy; fields are unexported so a
// caller cannot confuse one family's grant with another's.
type EgressPolicy struct {
	class            EgressClass
	providerHosts    []string
	webhookReceivers []string
	allowPrivate     bool
	namedNode        string
}

// ProviderEgressPolicy binds a fixed allowlist of provider hosts. Private
// targets are permitted only when allowPrivate comes from the trusted indexer
// policy (a self-hosted indexer), and only for a host in the allowlist.
func ProviderEgressPolicy(allowPrivate bool, hosts ...string) EgressPolicy {
	normalized := make([]string, 0, len(hosts))
	for _, host := range hosts {
		if h := normalizeHost(host); h != "" {
			normalized = append(normalized, h)
		}
	}
	return EgressPolicy{class: EgressClassProvider, allowPrivate: allowPrivate, providerHosts: normalized}
}

// WebhookEgressPolicy binds the explicit operator-configured receiver origins.
// An empty or malformed list permits nothing.
func WebhookEgressPolicy(receiverOrigins ...string) EgressPolicy {
	normalized := make([]string, 0, len(receiverOrigins))
	for _, origin := range receiverOrigins {
		if o := normalizeOrigin(origin); o != "" {
			normalized = append(normalized, o)
		}
	}
	return EgressPolicy{class: EgressClassWebhook, allowPrivate: true, webhookReceivers: normalized}
}

// ArchiveEgressPolicy binds the archive flag to a named node. Private RFC1918
// targets are refused unless BOTH the operator flag is set and a node name is
// supplied; loopback and link-local targets stay blocked in every case.
func ArchiveEgressPolicy(allowPrivate bool, namedNode string) EgressPolicy {
	return EgressPolicy{class: EgressClassArchive, allowPrivate: allowPrivate, namedNode: strings.TrimSpace(namedNode)}
}

// Class returns the policy family for audit messages.
func (p EgressPolicy) Class() EgressClass { return p.class }

// dialAllowPrivate is the coarse connection-time decision. The URL-time policy
// is always re-applied by CheckRedirect, so a redirect cannot widen this grant.
func (p EgressPolicy) dialAllowPrivate() bool {
	switch p.class {
	case EgressClassProvider:
		return p.allowPrivate && len(p.providerHosts) > 0
	case EgressClassWebhook:
		return len(p.webhookReceivers) > 0
	case EgressClassArchive:
		return p.allowPrivate && p.namedNode != ""
	default:
		return false
	}
}

func normalizeHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
		raw = parsed.Hostname()
	}
	return strings.ToLower(strings.Trim(raw, "."))
}

// normalizeOrigin reduces a URL (or bare origin) to a comparable
// scheme://host:port form. It never returns raw userinfo or path material.
func normalizeOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return ""
	}
	host := strings.ToLower(strings.Trim(parsed.Hostname(), "."))
	port := parsed.Port()
	if port == "" {
		if scheme == "http" {
			port = "80"
		} else {
			port = "443"
		}
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

// RedactURL removes credentials and sensitive query values so a URL can be
// logged or returned in an error without reflecting a secret. The raw URL is
// never used in a diagnostic message.
func RedactURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "<invalid-url>"
	}
	hadUser := parsed.User != nil
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "key") ||
			strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
			strings.Contains(lower, "sig") || strings.Contains(lower, "auth") {
			query.Set(key, "<redacted>")
		}
	}
	parsed.RawQuery = query.Encode()
	if hadUser {
		return "[redacted-userinfo] " + parsed.String()
	}
	return parsed.String()
}

// ValidateURLForEgress applies the complete class-specific egress policy. It
// never echoes the supplied URL: credentials are stripped and URLs carrying
// userinfo are rejected outright. DNS is still resolved by ValidateURL for the
// actual outbound decision; import-time validation uses ValidateURLStatic.
func ValidateURLForEgress(rawURL string, policy EgressPolicy) (*url.URL, error) {
	redacted := RedactURL(rawURL)
	parsed, err := parseURLForPolicy(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("url %s must not contain credentials", redacted)
	}

	host := normalizeHost(parsed.Hostname())
	switch policy.class {
	case EgressClassProvider:
		if len(policy.providerHosts) == 0 {
			return nil, fmt.Errorf("%s rejected by provider egress policy: no fixed host allowlist", redacted)
		}
		allowed := false
		for _, candidate := range policy.providerHosts {
			if host == candidate {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("%s rejected by provider egress policy: host is not in the fixed allowlist", redacted)
		}
	case EgressClassWebhook:
		if len(policy.webhookReceivers) == 0 {
			return nil, fmt.Errorf("%s rejected by webhook egress policy: no explicit receiver list", redacted)
		}
		origin := normalizeOrigin(parsed.Scheme + "://" + parsed.Host)
		allowed := false
		for _, receiver := range policy.webhookReceivers {
			if origin == receiver {
				allowed = true
				break
			}
		}
		if !allowed {
			return nil, fmt.Errorf("%s rejected by webhook egress policy: origin is not an explicit receiver", redacted)
		}
	case EgressClassArchive:
		// No host allowlist: the only private-network latitude is the flag
		// bound to a named node, applied by ValidateURL below.
	default:
		return nil, fmt.Errorf("%s rejected by unrecognised egress class %q", redacted, policy.class)
	}

	if _, err := ValidateURL(rawURL, policy.dialAllowPrivate()); err != nil {
		return nil, fmt.Errorf("%s rejected by %s egress policy: %w", redacted, policy.class, err)
	}
	return parsed, nil
}

// NewSafeHTTPClient returns an http.Client equipped with SSRF guards at connection time,
// socket dialing, and redirect following. It retains the historical provider
// behaviour (private targets only when explicitly requested).
func NewSafeHTTPClient(timeout time.Duration, allowPrivate bool) *http.Client {
	return newSafeHTTPClient(timeout, allowPrivate, func(rawURL string) error {
		_, err := ValidateURL(rawURL, allowPrivate)
		return err
	})
}

// egressPolicyTransport applies the class policy to every outbound request,
// including the initial one. CheckRedirect alone is not enough: a caller can
// hand the client an indexer-supplied URL that was never policy-checked.
type egressPolicyTransport struct {
	base   http.RoundTripper
	policy EgressPolicy
}

func (t *egressPolicyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if _, err := ValidateURLForEgress(req.URL.String(), t.policy); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(req)
}

// NewSafeHTTPClientForEgress builds a guarded client whose complete policy is
// applied to the initial request, dial time, and every redirect. A caller can
// neither start from an unlisted host nor escape onto one through a redirect.
func NewSafeHTTPClientForEgress(timeout time.Duration, policy EgressPolicy) *http.Client {
	client := newSafeHTTPClient(timeout, policy.dialAllowPrivate(), func(rawURL string) error {
		_, err := ValidateURLForEgress(rawURL, policy)
		return err
	})
	client.Transport = &egressPolicyTransport{base: client.Transport, policy: policy}
	return client
}

func newSafeHTTPClient(timeout time.Duration, allowPrivate bool, checkRedirectURL func(string) error) *http.Client {
	dialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
		ControlContext: func(ctx context.Context, network, address string, c syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				host = address
			}
			ip := net.ParseIP(host)
			if ip != nil {
				return ValidateIP(ip, allowPrivate)
			}
			return nil
		},
	}

	transport := &http.Transport{
		// SSRF-safe clients must not inherit env proxies: a CONNECT tunnel makes
		// DialContext see the proxy address, bypassing the dial-time IP guard.
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("too many redirects")
			}
			return checkRedirectURL(req.URL.String())
		},
	}
}
