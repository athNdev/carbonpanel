// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
)

func TestValidateLoaderVersionAcceptsPublishedShapes(t *testing.T) {
	cases := map[string]string{
		"21.1.84": "21.1.84", // NeoForge
		"54.0.1":  "54.0.1",  // Forge
		"0.16.10": "0.16.10", // Fabric
		"0.26.1":  "0.26.1",  // Quilt
	}
	for raw, want := range cases {
		got, err := validateLoaderVersion(storage.ModLoaderNeoForge, raw)
		if err != nil {
			t.Errorf("validateLoaderVersion(%q): %v", raw, err)
			continue
		}
		if got != want {
			t.Errorf("validateLoaderVersion(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestValidateLoaderVersionTreatsEmptyAndLatestAsUnpinned(t *testing.T) {
	// An unpinned server has to stay unpinned: storing "latest" would freeze it
	// at whatever that meant on the day it was created.
	for _, raw := range []string{"", "   ", "latest", "LATEST"} {
		got, err := validateLoaderVersion(storage.ModLoaderNeoForge, raw)
		if err != nil {
			t.Errorf("validateLoaderVersion(%q): %v", raw, err)
		}
		if got != "" {
			t.Errorf("validateLoaderVersion(%q) = %q, want empty", raw, got)
		}
	}
}

func TestValidateLoaderVersionRejectsLoadersThatCannotPin(t *testing.T) {
	for _, loader := range []storage.ModLoader{
		storage.ModLoaderVanilla,
		storage.ModLoaderPaper,
		storage.ModLoaderSpigot,
	} {
		if _, err := validateLoaderVersion(loader, "21.1.84"); err == nil {
			t.Errorf("%s accepted a loader version it will silently ignore", loader)
		}
	}
}

func TestValidateLoaderVersionRejectsEnvInjection(t *testing.T) {
	// The value ends up in a container env var, so anything that could smuggle
	// another variable must be refused rather than quoted and hoped for.
	for _, raw := range []string{
		"21.1.84\nADMIN_TOKEN=steal",
		"21.1.84 FOO=bar",
		"../../etc/passwd",
		"$(id)",
		strings_Repeat("9", 200),
	} {
		if _, err := validateLoaderVersion(storage.ModLoaderNeoForge, raw); err == nil {
			t.Errorf("validateLoaderVersion(%q) accepted an unsafe value", raw)
		}
	}
}

func strings_Repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
