// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"slices"
	"testing"

	"github.com/athNdev/carbon-panel/internal/db"
)

func TestLoaderVersionEnvUsesTheLoaderOwnVariable(t *testing.T) {
	// Each itzg image reads a different variable. Sending the wrong one is
	// silently ignored, which makes a pinned server quietly run "latest".
	cases := []struct {
		loader db.ModLoader
		want   string
	}{
		{db.ModLoaderNeoForge, "NEOFORGE_VERSION=21.1.84"},
		{db.ModLoaderForge, "FORGE_VERSION=21.1.84"},
		{db.ModLoaderFabric, "FABRIC_LOADER_VERSION=21.1.84"},
		{db.ModLoaderQuilt, "QUILT_LOADER_VERSION=21.1.84"},
	}
	for _, tc := range cases {
		env := loaderVersionEnv(&db.Server{ModLoader: tc.loader, LoaderVersion: "21.1.84"})
		if len(env) != 1 || env[0] != tc.want {
			t.Errorf("%s env = %v, want [%s]", tc.loader, env, tc.want)
		}
	}
}

func TestLoaderVersionEnvIsSilentWhenNothingIsPinned(t *testing.T) {
	for _, version := range []string{"", "   ", "latest"} {
		env := loaderVersionEnv(&db.Server{ModLoader: db.ModLoaderNeoForge, LoaderVersion: version})
		if len(env) != 0 {
			t.Errorf("version %q produced %v, want no variable", version, env)
		}
	}
}

func TestLoaderVersionEnvIgnoresLoadersThatCannotPin(t *testing.T) {
	// A stale value left on a row that switched loaders must not leak into the
	// new container's environment.
	for _, loader := range []db.ModLoader{db.ModLoaderVanilla, db.ModLoaderPaper} {
		env := loaderVersionEnv(&db.Server{ModLoader: loader, LoaderVersion: "21.1.84"})
		if len(env) != 0 {
			t.Errorf("%s produced %v, want nothing", loader, env)
		}
	}
}

func TestLoaderVersionEnvNeverDuplicatesAnotherVariable(t *testing.T) {
	env := loaderVersionEnv(&db.Server{ModLoader: db.ModLoaderNeoForge, LoaderVersion: "21.1.84"})
	if slices.Contains(env, "") {
		t.Fatalf("env contains an empty entry: %v", env)
	}
}
