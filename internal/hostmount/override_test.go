// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostmount

import (
	"context"
	"strings"
	"testing"
)

func TestCheckOverrideAcceptsAnAbsolutePath(t *testing.T) {
	t.Setenv("CARBONPANEL_HOST_DATA_PATH", "/srv/carbon-panel/data")
	if err := CheckOverride(); err != nil {
		t.Fatalf("CheckOverride: %v", err)
	}
}

func TestCheckOverrideRejectsARelativePath(t *testing.T) {
	t.Setenv("CARBONPANEL_HOST_DATA_PATH", "srv/carbon-panel/data")
	err := CheckOverride()
	if err == nil {
		t.Fatal("error = nil for a relative override")
	}
	if !strings.Contains(err.Error(), "mount path should be absolute") {
		t.Fatalf("error = %q, want the absolute-path reason", err)
	}
}

func TestCheckOverrideIsANoOpWhenUnset(t *testing.T) {
	t.Setenv("CARBONPANEL_HOST_DATA_PATH", "")
	if err := CheckOverride(); err != nil {
		t.Fatalf("CheckOverride: %v", err)
	}
}

// TestResolvePrefersTheMountOverADisagreeingOverride is the behaviour change
// that makes the variable redundant: the daemon is authoritative, and a stale
// override is reported instead of followed.
func TestResolvePrefersTheMountOverADisagreeingOverride(t *testing.T) {
	t.Setenv("CARBONPANEL_HOST_DATA_PATH", "/srv/stale")
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "/srv/carbon-panel/data", Destination: "/app/data", RW: true},
	}}

	mapping, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mapping.HostDir != "/srv/carbon-panel/data" {
		t.Fatalf("host dir = %q, want the detected mount", mapping.HostDir)
	}
	if !mapping.Mismatch {
		t.Error("Mismatch = false, want the stale override to be reported")
	}
	if mapping.OverrideDir != "/srv/stale" {
		t.Errorf("OverrideDir = %q, want the override value", mapping.OverrideDir)
	}
}

func TestResolveDoesNotFlagAnAgreeingOverride(t *testing.T) {
	t.Setenv("CARBONPANEL_HOST_DATA_PATH", "/srv/carbon-panel/data/")
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "/srv/carbon-panel/data", Destination: "/app/data", RW: true},
	}}

	mapping, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mapping.Mismatch {
		t.Errorf("Mismatch = true for an equivalent override (trailing slash): %+v", mapping)
	}
}
