// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package utils

import (
	"strings"
	"testing"
)

// TestEgressClassesAreDistinct proves the three outbound decisions no longer
// share one "allow private" boolean.
func TestEgressClassesAreDistinct(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "")
	const private = "http://10.0.0.5/archive.zip"

	if _, err := ValidateURLForEgress(private, EgressClassProvider, true); err == nil {
		t.Fatal("provider class must never allow private targets, even when asked")
	}
	if _, err := ValidateURLForEgress(private, EgressClassArchive, false); err == nil {
		t.Fatal("archive class must fail closed without the explicit flag")
	}
	if _, err := ValidateURLForEgress(private, EgressClassWebhook, false); err == nil {
		t.Fatal("webhook class must fail closed without an operator receiver grant")
	}
	if _, err := ValidateURLForEgress(private, EgressClassArchive, true); err != nil {
		t.Fatalf("archive class with explicit flag must allow an RFC1918 target: %v", err)
	}
	if _, err := ValidateURLForEgress(private, EgressClassWebhook, true); err != nil {
		t.Fatalf("webhook class with operator grant must allow an RFC1918 target: %v", err)
	}

	for _, target := range []string{
		"http://127.0.0.1/archive.zip",
		"http://169.254.169.254/latest/meta-data/",
	} {
		if _, err := ValidateURLForEgress(target, EgressClassArchive, true); err == nil {
			t.Fatalf("loopback/link-local target %s must stay blocked for every class", target)
		}
	}

	unknown := EgressClass("unrecognised")
	if _, err := ValidateURLForEgress(private, unknown, true); err == nil {
		t.Fatal("unknown egress class must fail closed")
	}
}

// TestEgressErrorsDoNotReflectCredentials guards the no-credential-reflection
// requirement. A URL with userinfo must be rejected and the secret must not
// appear in the error text.
func TestEgressErrorsDoNotReflectCredentials(t *testing.T) {
	const secret = "supersecret-token-value"
	raw := "https://alice:" + secret + "@example.test/private.jar?token=" + secret

	_, err := ValidateURLForEgress(raw, EgressClassArchive, true)
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
}
