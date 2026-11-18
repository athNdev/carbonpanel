// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestIntegrationCombinedCatalog pins the honest coverage counts of the
// combined OSS-370 catalog. The three signals stay separate: a catalog
// declaration is not a mounted tool, and a mounted tool is not a fixture
// verified one. Declared/mounted/excluded totals come from the generated
// artifact; LISTED/REACHABLE/VERIFIED are recomputed from live source.
func TestIntegrationCombinedCatalog(t *testing.T) {
	matrix := FamilyAudit()
	if matrix.Counts.DeclaredRPCs != IntegrationDeclaredRPCs || IntegrationDeclaredRPCs != 230 {
		t.Fatalf("declared RPCs: matrix=%d generated=%d want 230", matrix.Counts.DeclaredRPCs, IntegrationDeclaredRPCs)
	}
	if matrix.Counts.MountedReachable != IntegrationMountedRPCs || IntegrationMountedRPCs != 220 {
		t.Fatalf("mounted RPCs: matrix=%d generated=%d want 220", matrix.Counts.MountedReachable, IntegrationMountedRPCs)
	}
	if matrix.Counts.ExcludedByReason != IntegrationExcludedOps || IntegrationExcludedOps != 58 {
		t.Fatalf("excluded ops: matrix=%d generated=%d want 58", matrix.Counts.ExcludedByReason, IntegrationExcludedOps)
	}
	if IntegrationSupportedOps != 200 {
		t.Fatalf("supported ops: got %d want 200", IntegrationSupportedOps)
	}
	if len(exposedTools) != IntegrationCombinedTools {
		t.Fatalf("generated tool count: got %d want %d", len(exposedTools), IntegrationCombinedTools)
	}

	// Single registration: no duplicate tool name or procedure in the
	// combined catalog (the first-slice list_files/get_file collisions are
	// resolved by generator precedence).
	names := map[string]bool{}
	procs := map[string]bool{}
	for _, spec := range exposedTools {
		if names[spec.Name] {
			t.Errorf("duplicate tool name %s", spec.Name)
		}
		if procs[spec.Procedure] {
			t.Errorf("duplicate tool procedure %s", spec.Procedure)
		}
		names[spec.Name] = true
		procs[spec.Procedure] = true
	}

	listed := liveListedProcedures()
	reachable := liveReachableTools()
	fixtures := liveFixtureProcedures(t)
	var listedN, reachableN, verifiedN int
	for _, op := range matrix.Operations {
		if !op.Supported() {
			continue
		}
		cov := recomputeCoverage(op, listed, reachable, fixtures)
		if !cov.Complete() {
			t.Errorf("%s: live coverage incomplete: %+v", op.ID, cov)
			continue
		}
		if cov.Listed {
			listedN++
		}
		if cov.Reachable {
			reachableN++
		}
		if cov.Verified {
			verifiedN++
		}
	}
	if listedN != 200 || reachableN != 200 || verifiedN != 200 {
		t.Fatalf("live coverage: LISTED=%d REACHABLE=%d VERIFIED=%d; want 200/200/200", listedN, reachableN, verifiedN)
	}

	// Every audited exclusion stays out of the combined tool set, with its
	// exact reason and supported equivalent intact in the matrix.
	for _, op := range FamilyAuditExcludedOperations() {
		if op.EquivalentFlow == "" || op.Reason == "" || op.ReasonRef == "" {
			t.Errorf("%s: exclusion lost reason/equivalent", op.ID)
		}
		if op.Kind == "rpc" && procs[op.Procedure] {
			t.Errorf("%s: excluded procedure was mounted as a tool", op.ID)
		}
	}
}

// TestIntegrationFixtureEvidenceIsMounted proves the VERIFIED signal is not
// merely derived from a string constant: every supported procedure has a real
// registerOK handler line behind the production auth interceptor in the
// combined fixture.
func TestIntegrationFixtureEvidenceIsMounted(t *testing.T) {
	src, err := os.ReadFile("mcp_test.go")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`registerOK\[[^]]+\]\(mux,\s*"([^"]+)"`)
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		seen[m[1]] = true
	}
	for _, op := range FamilyAuditSupportedOperations() {
		target := op.Procedure
		if op.Kind != "rpc" {
			target = op.ComposedBy
		}
		if target == "" {
			continue
		}
		if !seen[target] && !strings.Contains(string(src), `"`+target+`"`) {
			t.Errorf("%s: no mounted fixture for %s", op.ID, target)
		}
	}
}
