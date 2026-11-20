// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"context"
	"errors"
	"slices"
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
	if _, err := bad.Daemon.BuildManifest(); err == nil {
		t.Fatal("plan refused by the host gates still produced a daemon manifest")
	}
}

func TestBuildPlanRefusesInvalidDaemonOptions(t *testing.T) {
	for _, mode := range []DaemonMode{ModeRootless, ModeRootful} {
		t.Run(string(mode), func(t *testing.T) {
			facts := qualifiedFacts()
			facts.UserNamespaces = mode == ModeRootless
			facts.ChosenExecRoot = facts.ChosenDataRoot
			if sel := ApplyGates(facts); sel.Mode != mode {
				t.Fatalf("fixture must pass host gates, got mode %q", sel.Mode)
			}
			const refusal = "OPTIONS_DEDICATED_ROOTS"
			plan := BuildPlan("node-fixture", facts, fixtureTime())
			if plan.Refusal != refusal || plan.Selection.Mode != ModeNone || plan.Report.GetStatus() != v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_FAILED {
				t.Fatalf("daemon options did not fail preflight: %+v", plan)
			}
			if !slices.ContainsFunc(plan.Report.GetChecks(), func(check *v1.NodePreflightCheck) bool {
				return check.GetCode() == refusal && check.GetStatus() == v1.NodePreflightStatus_NODE_PREFLIGHT_STATUS_FAILED && check.GetRemedy() != ""
			}) {
				t.Fatalf("missing actionable daemon refusal in report: %v", plan.Report)
			}
			want := []v1.NodeCapability{v1.NodeCapability_NODE_CAPABILITY_STORAGE_WORKER, v1.NodeCapability_NODE_CAPABILITY_DRAIN}
			if got := plan.Capabilities.GetCapabilities(); !slices.Equal(got, want) {
				t.Fatalf("refused plan granted %v, want %v", got, want)
			}
			if _, err := plan.Daemon.BuildManifest(); err == nil {
				t.Fatal("plan with invalid daemon options produced a manifest")
			}
		})
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
	bootstrap := Credential{Kind: CredentialBootstrap}
	operator := Credential{Kind: CredentialOperator}

	// The shared boundary enforces the bootstrap domain before either step.
	if _, err := r.BeginEnrollment(ctx, operator, "missing-enrollment"); !errors.Is(err, ErrScope) {
		t.Fatalf("operator reached the bootstrap challenge step: %v", err)
	}
	if _, err := r.ClaimEnrollment(ctx, operator, nil); !errors.Is(err, ErrScope) {
		t.Fatalf("operator reached the bootstrap claim step: %v", err)
	}
	if _, err := r.BeginEnrollment(ctx, bootstrap, "missing-enrollment"); err == nil {
		t.Fatal("unknown enrollment returned a challenge")
	}
	if _, err := r.ClaimEnrollment(ctx, bootstrap, nil); err == nil {
		t.Fatal("nil claim accepted")
	}

	// The enrolled-node domain is wired on the same shared boundary.
	node := Credential{Kind: CredentialNode, ObjectID: "node", Scopes: []string{"node:heartbeat"}}
	if err := r.AuthorizeNodeOperation(node, "other-node", "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("cross-node operation accepted: %v", err)
	}
	if err := r.AuthorizeNodeOperation(node, "node", "node:heartbeat"); err != nil {
		t.Fatalf("legitimate node operation refused: %v", err)
	}
}

// Authenticated enrollment/captive daemon lifecycle: enrolled-node preflight
// reports are authorized only within the node's own object and granted scopes.
func TestRunnerAuthorizePreflightLifecycleRefusals(t *testing.T) {
	r, err := NewRunner(TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := Credential{Kind: CredentialBootstrap}
	operator := Credential{Kind: CredentialOperator}
	reporter := Credential{Kind: CredentialNode, ObjectID: "node", Scopes: []string{"node:report", "node:heartbeat"}}

	if err := r.AuthorizePreflight(reporter, "node", []string{"node:report"}); err != nil {
		t.Fatalf("legitimate preflight report refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		cred Credential
		obj  string
		sc   []string
	}{
		{"bootstrap domain crossing", bootstrap, "node", []string{"node:report"}},
		{"operator domain crossing", operator, "node", []string{"node:report"}},
		{"cross node object", reporter, "other-node", []string{"node:report"}},
		{"over-scope report", reporter, "node", []string{"node:report", "nodes:create"}},
	} {
		if err := r.AuthorizePreflight(tc.cred, tc.obj, tc.sc); !errors.Is(err, ErrScope) {
			t.Fatalf("%s: expected scope refusal, got %v", tc.name, err)
		}
	}
}
