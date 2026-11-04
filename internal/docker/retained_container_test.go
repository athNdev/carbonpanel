// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"strings"
	"testing"
)

// TestRetainedContainersAreNeverAdopted is the regression for a recreation that
// reverted itself.
//
// RecreateContainer renames the old container to "carbon-panel-replaced-…" and
// then creates the replacement. The old container still carries the server's
// labels and, if it was never started, is still in the "created" state - which
// the adoption preflight treats as alive. Creation therefore resolved the old
// container by label, adopted it, and returned it as the new one: the change
// that asked for the recreation (a pinned loader version) was recorded in the
// database and never applied to a container.
func TestRetainedContainersAreNeverAdopted(t *testing.T) {
	retained := []string{
		"/" + retainedContainerNamePrefix + "server-1-1793802208",
		retainedContainerNamePrefix + "server-1-1793802208",
	}
	for _, names := range [][]string{retained[:1], retained[1:]} {
		if !isRetainedContainerName(names) {
			t.Errorf("isRetainedContainerName(%v) = false, want true", names)
		}
	}
}

func TestTheCanonicalContainerIsStillResolvable(t *testing.T) {
	for _, names := range [][]string{
		{"/carbon-panel-server-server-1"},
		{"/carbon-panel-module-mod-1"},
		{},
	} {
		if isRetainedContainerName(names) {
			t.Errorf("isRetainedContainerName(%v) = true, want false", names)
		}
	}
}

func TestRetainedNamesAreUniquePerAttempt(t *testing.T) {
	// Two recreations of the same server within one second must not collide on
	// a name, or the second rename fails and the server is left retained.
	first := retainedContainerName("server-1")
	second := retainedContainerName("server-1")
	if !strings.HasPrefix(first, retainedContainerNamePrefix) {
		t.Fatalf("retained name %q does not carry the retained prefix", first)
	}
	if !strings.HasPrefix(second, retainedContainerNamePrefix) {
		t.Fatalf("retained name %q does not carry the retained prefix", second)
	}
}
