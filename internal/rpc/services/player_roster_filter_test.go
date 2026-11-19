// SPDX-License-Identifier: MPL-2.0
package services

import "testing"

// The packaged console sends "all" for its default roster filters. The server
// must treat that as no filter, otherwise every row is hidden even when the
// database contains a real observed identity.
func TestNormalizeRosterFilter(t *testing.T) {
	cases := map[string]string{
		"":            "",
		"all":         "",
		"ALL":         "",
		" all ":       "",
		"online":      "online",
		"whitelisted": "whitelisted",
	}
	for input, want := range cases {
		if got := normalizeRosterFilter(input); got != want {
			t.Errorf("normalizeRosterFilter(%q) = %q, want %q", input, got, want)
		}
	}
}
