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
	"time"
)

var (
	ErrBlockedIP     = errors.New("connection blocked by SSRF guard")
	ErrInvalidScheme = errors.New("invalid URL scheme: must be http or https")
	ErrMissingHost   = errors.New("url is missing host")
)

// ValidateIP checks whether an IP address belongs to denied ranges (loopback, link-local, private RFC1918)
func ValidateIP(ip net.IP, allowPrivate bool) error {
	if ip == nil {
		return fmt.Errorf("%w: invalid IP", ErrBlockedIP)
	}

	// Always deny loopback (127.0.0.0/8, ::1) unless explicitly permitted for tests
	if ip.IsLoopback() {
		if os.Getenv("DISCO_ALLOW_LOOPBACK_TEST") == "1" {
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
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse url: %w", err)
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
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse url: %w", err)
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

// EgressClass names the three distinct outbound-policy decisions. Keeping
// them explicit prevents one class (an archive URL supplied by a user) from
// inheriting the private-network latitude of another (an operator-configured
// webhook receiver). Unknown classes fail closed.
type EgressClass string

const (
	// EgressClassProvider is a fixed provider allowlist. Private targets are
	// never permitted here; the provider adapter owns the host allowlist.
	EgressClassProvider EgressClass = "provider"
	// EgressClassWebhook targets an operator-configured receiver list. Private
	// receivers are permitted only when the operator explicitly allows them.
	EgressClassWebhook EgressClass = "webhook"
	// EgressClassArchive is a user-supplied remote archive URL. Private targets
	// are permitted only behind an explicit operator flag and a named node.
	EgressClassArchive EgressClass = "archive"
)

// allowsPrivate resolves the effective private-network decision for a class.
func (c EgressClass) allowsPrivate(requested bool) bool {
	switch c {
	case EgressClassWebhook, EgressClassArchive:
		return requested
	case EgressClassProvider:
		return false
	default:
		return false
	}
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

// ValidateURLForEgress applies the named egress policy. It never echoes the
// supplied URL: credentials are stripped, and URLs carrying userinfo are
// rejected outright. DNS is still resolved by ValidateURL for the actual
// outbound decision; import-time validation uses ValidateURLStatic instead.
func ValidateURLForEgress(rawURL string, class EgressClass, allowPrivate bool) (*url.URL, error) {
	redacted := RedactURL(rawURL)
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse url %s: %w", redacted, err)
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("url %s must not contain credentials", redacted)
	}
	if _, err := ValidateURL(rawURL, class.allowsPrivate(allowPrivate)); err != nil {
		return nil, fmt.Errorf("%s rejected by %s egress policy: %w", redacted, class, err)
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

// NewSafeHTTPClientForEgress builds the same guarded client but binds both the
// dial-time and redirect-time decisions to a named egress class. A redirect
// cannot escape into a private network by inheriting a looser policy.
func NewSafeHTTPClientForEgress(timeout time.Duration, class EgressClass, allowPrivate bool) *http.Client {
	return newSafeHTTPClient(timeout, class.allowsPrivate(allowPrivate), func(rawURL string) error {
		_, err := ValidateURLForEgress(rawURL, class, allowPrivate)
		return err
	})
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
		Proxy:                 http.ProxyFromEnvironment,
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
