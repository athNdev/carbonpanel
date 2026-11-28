// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package indexers

import "testing"

func TestSupportsGameVersionCoverage(t *testing.T) {
	const platform = "paper"
	for _, test := range []struct {
		name      string
		listed    []string
		requested string
		want      bool
	}{
		{name: "LATEST matches any version", listed: []string{"LATEST"}, requested: "1.21.10", want: true},
		{name: "latest is case insensitive", listed: []string{"latest"}, requested: "1.20.1", want: true},
		{name: "range lower endpoint", listed: []string{"1.8-1.21.10"}, requested: "1.8", want: true},
		{name: "range upper endpoint", listed: []string{"1.8-1.21.10"}, requested: "1.21.10", want: true},
		{name: "range inside", listed: []string{"1.8-1.21.10"}, requested: "1.12.2", want: true},
		{name: "range inside a short endpoint", listed: []string{"1.8-1.21.10"}, requested: "1.21.1", want: true},
		{name: "range below", listed: []string{"1.8-1.21.10"}, requested: "1.7.10", want: false},
		{name: "range above", listed: []string{"1.8-1.21.10"}, requested: "1.21.11", want: false},
		{name: "range above another major", listed: []string{"1.8-1.21.10"}, requested: "1.22", want: false},
		{name: "bare version covers a patch", listed: []string{"1.21"}, requested: "1.21.1", want: true},
		{name: "patch never covers a longer patch", listed: []string{"1.21.1"}, requested: "1.21.10", want: false},
		{name: "exact version", listed: []string{"1.21.1"}, requested: "1.21.1", want: true},
		{name: "empty request is unfiltered", listed: []string{"1.21"}, requested: "", want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := SupportsGameVersion(map[string][]string{platform: test.listed}, platform, test.requested); got != test.want {
				t.Fatalf("SupportsGameVersion(%v, %q) = %v, want %v", test.listed, test.requested, got, test.want)
			}
		})
	}
}

func TestSupportsGameVersionOnlyUsesTheRequestedPlatform(t *testing.T) {
	platforms := map[string][]string{
		"paper":  {"1.21.1"},
		"purpur": {"LATEST"},
	}
	if SupportsGameVersion(platforms, "paper", "1.20.1") {
		t.Fatal("LATEST on another platform must not widen the requested platform")
	}
	if !SupportsGameVersion(platforms, "", "1.20.1") {
		t.Fatal("an empty platform consults every platform's list")
	}
}
