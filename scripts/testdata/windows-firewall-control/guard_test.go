// SPDX-License-Identifier: MPL-2.0
package main

import "testing"

func TestControlRequiresEveryDisposableOptIn(t *testing.T) {
	values := map[string]string{
		"GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted",
		"CARBON_WINDOWS_POSITIVE_CONTROL": "1",
	}
	read := func(key string) string { return values[key] }
	args := []string{"--allow-disposable-public-listener"}
	if !controlAllowed(args, read) {
		t.Fatal("complete opt-in rejected")
	}
	for key, original := range values {
		values[key] = ""
		if controlAllowed(args, read) {
			t.Fatalf("missing %s allowed a public control", key)
		}
		values[key] = original
	}
	for _, other := range [][]string{nil, {"--run"}, {args[0], "extra"}} {
		if controlAllowed(other, read) {
			t.Fatalf("unexpected arguments allowed: %v", other)
		}
	}
}
