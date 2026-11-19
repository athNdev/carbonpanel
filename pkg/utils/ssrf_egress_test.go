// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestLoopbackOverrideFailsClosedOutsideTests proves the production branch of
// the only loopback escape hatch cannot be reached by setting an environment
// variable. The pure predicate keeps this assertion independent of the test
// binary's own testing.Testing() result.
func TestLoopbackOverrideFailsClosedOutsideTests(t *testing.T) {
	if loopbackTestAllowed(false, "1") {
		t.Fatal("production binary honoured the loopback test override")
	}
	if loopbackTestAllowed(true, "1") != true {
		t.Fatal("explicit test fixture lost the loopback override")
	}
	if loopbackTestAllowed(true, "0") || loopbackTestAllowed(true, "") {
		t.Fatal("loopback override must require an explicit value")
	}
}

func TestValidateIPLoopbackRequiresExplicitTestConfig(t *testing.T) {
	loopback := net.ParseIP("127.0.0.1")
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "")
	if err := ValidateIP(loopback, true); err == nil {
		t.Fatal("loopback accepted without the explicit test override")
	}
	// Existing loopback httptest fixtures keep working with t.Setenv.
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	if err := ValidateIP(loopback, true); err != nil {
		t.Fatalf("test fixture lost its explicit loopback override: %v", err)
	}
}

// TestProviderEgressEnforcesFixedHostAllowlist proves the provider decision is
// no longer a bare boolean shared with webhook/archive traffic.
func TestProviderEgressEnforcesFixedHostAllowlist(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "")

	for name, policy := range map[string]EgressPolicy{
		"empty allowlist":     ProviderEgressPolicy(true),
		"host not in list":    ProviderEgressPolicy(false, "cdn.modrinth.com"),
		"public indexer LAN":  ProviderEgressPolicy(false, "10.0.0.5"),
		"loopback literal":    ProviderEgressPolicy(true, "127.0.0.1"),
		"unknown class value": {class: EgressClass("bogus"), allowPrivate: true},
	} {
		raw := "http://10.0.0.5/a.jar"
		if name == "loopback literal" {
			raw = "http://127.0.0.1/a.jar"
		}
		if _, err := ValidateURLForEgress(raw, policy); err == nil {
			t.Fatalf("%s: provider egress must fail closed", name)
		}
	}

	// A public provider host resolves to a public address and is allowed.
	if _, err := ValidateURLForEgress("http://1.1.1.1/a.jar",
		ProviderEgressPolicy(false, "1.1.1.1")); err != nil {
		t.Fatalf("public provider host in the fixed allowlist was refused: %v", err)
	}
	// A self-hosted indexer may serve a private download only from its own
	// explicitly listed host.
	if _, err := ValidateURLForEgress("http://10.0.0.5/a.jar",
		ProviderEgressPolicy(true, "10.0.0.5")); err != nil {
		t.Fatalf("self-hosted provider host refused: %v", err)
	}
}

// TestWebhookEgressEnforcesExplicitReceiverList proves a private receiver is
// allowed only because it is named, and never because of a shared boolean.
func TestWebhookEgressEnforcesExplicitReceiverList(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "")
	const receiver = "http://10.0.0.5:8080/hook"
	const other = "http://10.0.0.6:8080/hook"

	for name, policy := range map[string]EgressPolicy{
		"empty receiver list": WebhookEgressPolicy(),
		"different receiver":  WebhookEgressPolicy(other),
		"different port":      WebhookEgressPolicy("http://10.0.0.5:9090/hook"),
		"different scheme":    WebhookEgressPolicy("https://10.0.0.5:8080/hook"),
	} {
		if _, err := ValidateURLForEgress(receiver, policy); err == nil {
			t.Fatalf("%s: webhook egress must fail closed", name)
		}
	}

	if _, err := ValidateURLForEgress(receiver, WebhookEgressPolicy(receiver)); err != nil {
		t.Fatalf("explicit operator-configured receiver was refused: %v", err)
	}
	for _, blocked := range []string{
		"http://127.0.0.1:8080/hook",
		"http://169.254.169.254/latest/meta-data/",
	} {
		if _, err := ValidateURLForEgress(blocked, WebhookEgressPolicy(blocked)); err == nil {
			t.Fatalf("loopback/link-local receiver must stay blocked: %s", blocked)
		}
	}
}

// TestArchiveEgressRequiresFlagAndNamedNode proves the archive class no longer
// succeeds on a bare boolean with a comment about a named node.
func TestArchiveEgressRequiresFlagAndNamedNode(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "")
	const private = "http://10.0.0.5/a.zip"

	for name, policy := range map[string]EgressPolicy{
		"flag off":        ArchiveEgressPolicy(false, "node-a"),
		"node missing":    ArchiveEgressPolicy(true, ""),
		"node whitespace": ArchiveEgressPolicy(true, "  "),
	} {
		if _, err := ValidateURLForEgress(private, policy); err == nil {
			t.Fatalf("%s: archive egress must fail closed", name)
		}
	}
	if _, err := ValidateURLForEgress(private, ArchiveEgressPolicy(true, "node-a")); err != nil {
		t.Fatalf("flagged archive to a named node was refused: %v", err)
	}
	for _, blocked := range []string{
		"http://127.0.0.1/a.zip",
		"http://169.254.169.254/latest/meta-data/",
	} {
		if _, err := ValidateURLForEgress(blocked, ArchiveEgressPolicy(true, "node-a")); err == nil {
			t.Fatalf("loopback/link-local archive target must stay blocked: %s", blocked)
		}
	}
}

// TestEgressClientRevalidatesRedirects uses only local httptest fixtures and a
// non-routable address that is refused before any outbound dial; no live egress
// is performed. Each class must reject a redirect that its policy does not name.
func TestEgressClientRevalidatesRedirects(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")

	var webhookDestinationHits int
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		webhookDestinationHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()

	redirectTo := func(target string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target, http.StatusFound)
		}))
	}

	// Provider: the redirect host is public but not in the fixed allowlist.
	providerOrigin := redirectTo("http://1.1.1.1/a.jar")
	defer providerOrigin.Close()
	providerClient := NewSafeHTTPClientForEgress(time.Second,
		ProviderEgressPolicy(false, providerOrigin.URL))
	if _, err := providerClient.Get(providerOrigin.URL); err == nil ||
		!strings.Contains(err.Error(), "fixed allowlist") {
		t.Fatalf("provider redirect escaped the fixed allowlist: %v", err)
	}

	// Webhook: the receiver list names only the first origin, so a redirect to
	// the second loopback origin is refused by origin comparison.
	webhookOrigin := redirectTo(destination.URL)
	defer webhookOrigin.Close()
	webhookClient := NewSafeHTTPClientForEgress(time.Second,
		WebhookEgressPolicy(webhookOrigin.URL))
	if _, err := webhookClient.Get(webhookOrigin.URL); err == nil ||
		!strings.Contains(err.Error(), "explicit receiver") {
		t.Fatalf("webhook redirect escaped the receiver list: %v", err)
	}
	if webhookDestinationHits != 0 {
		t.Fatal("webhook redirect was followed to an unlisted receiver")
	}

	// Archive: a named node and flag do not legalise a link-local redirect.
	archiveOrigin := redirectTo("http://169.254.169.254/latest/meta-data/")
	defer archiveOrigin.Close()
	archiveClient := NewSafeHTTPClientForEgress(time.Second,
		ArchiveEgressPolicy(true, "node-a"))
	if _, err := archiveClient.Get(archiveOrigin.URL); err == nil ||
		!strings.Contains(err.Error(), "archive egress policy") {
		t.Fatalf("archive redirect reached a link-local target: %v", err)
	}
}

// TestEgressErrorsDoNotReflectCredentials guards the no-credential-reflection
// requirement across the whole policy surface.
func TestEgressErrorsDoNotReflectCredentials(t *testing.T) {
	const secret = "supersecret-token-value"
	raw := "https://alice:" + secret + "@example.test/private.jar?token=" + secret

	_, err := ValidateURLForEgress(raw, ArchiveEgressPolicy(true, "node-a"))
	if err == nil {
		t.Fatal("URL with credentials must be rejected")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("egress error reflected a credential: %v", err)
	}

	redacted := RedactURL(raw)
	if strings.Contains(redacted, secret) || strings.Contains(redacted, "alice") {
		t.Fatalf("RedactURL leaked userinfo or token: %s", redacted)
	}
	if !strings.Contains(redacted, "[redacted-userinfo]") {
		t.Fatalf("RedactURL should mark stripped userinfo: %s", redacted)
	}

	// A malformed URL makes url.Parse embed the whole raw string (including
	// userinfo) in its error. The policy must never chain that error verbatim.
	malformed := "https://alice:" + secret + "@10.0.0.5/mod%zz.jar"
	for name, err := range map[string]error{
		"class": func() error {
			_, err := ValidateURLForEgress(malformed, ArchiveEgressPolicy(true, "node-a"))
			return err
		}(),
		"static":  func() error { _, err := ValidateURLStatic(malformed, false); return err }(),
		"resolve": func() error { _, err := ValidateURL(malformed, false); return err }(),
	} {
		if err == nil {
			t.Fatalf("%s: malformed URL unexpectedly accepted", name)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "alice") {
			t.Fatalf("%s: parse error reflected a credential: %v", name, err)
		}
	}
}
