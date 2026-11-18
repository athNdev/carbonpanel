// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"context"
	"testing"
	"time"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestBuildPlanQualifiedAndRefused(t *testing.T) {
	plan := BuildPlan("node-fixture", qualifiedFacts(), fixtureTime())
	if plan.Selection.Mode != ModeRootless || plan.Refusal != "" {
		t.Fatalf("qualified plan unexpected: mode=%q refusal=%q", plan.Selection.Mode, plan.Refusal)
	}
	if plan.Report.GetStatus() != v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_PASSED {
		t.Fatal("qualified preflight not passed")
	}
	if plan.Daemon.ValidateOrRefuse() != "" {
		t.Fatal("qualified plan produced an invalid daemon spec")
	}

	f := qualifiedFacts()
	f.OS = "windows"
	bad := BuildPlan("node-fixture", f, fixtureTime())
	if bad.Refusal == "" || bad.Report.GetStatus() != v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_FAILED {
		t.Fatalf("failed plan not fail-closed: %+v", bad)
	}
}

func TestNegotiateVersionSkew(t *testing.T) {
	resp := NegotiateVersion(&v1.VersionNegotiation{
		MinSupportedVersion: 1, MaxSupportedVersion: 2,
	})
	if !resp.GetSupported() || resp.GetSelectedVersion() != 2 {
		t.Fatalf("expected supported v2, got %+v", resp)
	}
	tooOld := NegotiateVersion(&v1.VersionNegotiation{MinSupportedVersion: 9, MaxSupportedVersion: 10})
	if tooOld.GetSupported() || !containsUpgradeRequired(tooOld.GetReason()) {
		t.Fatalf("unsupported version not refused with UPGRADE_REQUIRED: %+v", tooOld)
	}
	if NegotiateVersion(nil).GetSupported() {
		t.Fatal("missing endpoint unexpectedly supported")
	}
}

func containsUpgradeRequired(s string) bool {
	return len(s) >= 16 && s[:16] == "UPGRADE_REQUIRED"
}

func TestRunnerComposesSharedContract(t *testing.T) {
	r, err := NewRunner(TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.Negotiate(ctx, &v1.VersionNegotiation{MinSupportedVersion: 1, MaxSupportedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.BeginEnrollment(ctx, "missing-enrollment"); err == nil {
		t.Fatal("unknown enrollment returned a challenge")
	}
	if _, err := r.ClaimEnrollment(ctx, nil); err == nil {
		t.Fatal("nil claim accepted")
	}
}
