// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"strings"
	"testing"
)

// isCapabilityTool reports whether a tool is a deliberately procedure-sharing
// capability hand-off rather than a canonical catalog tool. The set is explicit:
// a prefix match would also exempt future canonical tools such as
// download_archive from the duplicate-procedure guard. Each listed tool has a
// distinct name and its own executable stdio fixture.
func isCapabilityTool(name string) bool {
	if strings.HasPrefix(name, "scoped_stream_") {
		return true
	}
	switch name {
	case "download_get_file", "download_init_file", "download_archive_bundle",
		"download_export_world_bundle", "download_stream",
		UploadToolSaveUploadedFile, UploadToolInitUpload, UploadToolUploadChunk, UploadToolStreamCommit:
		return true
	default:
		return false
	}
}

// TestIntegrationCombinedCatalog pins the honest coverage counts of the
// combined OSS-370 catalog. The three signals stay separate: a catalog
// declaration is not a mounted tool, and a mounted tool is not a fixture
// verified one. Declared/mounted/excluded totals come from the generated
// artifact; LISTED/REACHABLE/VERIFIED are recomputed from live source.
func TestIntegrationCombinedCatalog(t *testing.T) {
	matrix := FamilyAudit()
	if matrix.Counts.DeclaredRPCs != IntegrationDeclaredRPCs || IntegrationDeclaredRPCs != 255 {
		t.Fatalf("declared RPCs: matrix=%d generated=%d want 255", matrix.Counts.DeclaredRPCs, IntegrationDeclaredRPCs)
	}
	if matrix.Counts.MountedReachable != IntegrationMountedRPCs || IntegrationMountedRPCs != 245 {
		t.Fatalf("mounted RPCs: matrix=%d generated=%d want 245", matrix.Counts.MountedReachable, IntegrationMountedRPCs)
	}
	if matrix.Counts.ExcludedByReason != IntegrationExcludedOps || IntegrationExcludedOps != 76 {
		t.Fatalf("excluded ops: matrix=%d generated=%d want 76", matrix.Counts.ExcludedByReason, IntegrationExcludedOps)
	}
	if IntegrationSupportedOps != 207 {
		t.Fatalf("supported ops: got %d want 207", IntegrationSupportedOps)
	}
	if len(exposedTools) != IntegrationCombinedTools {
		t.Fatalf("generated tool count: got %d want %d", len(exposedTools), IntegrationCombinedTools)
	}

	// Single registration: no duplicate tool name. Capability tools from the
	// stream, download and upload lanes deliberately share a typed RPC's
	// procedure with a canonical tool (for example get_file and
	// download_get_file both use GetFile), so procedure sharing is expected for
	// those names only; any other duplicate would be a double registration.
	names := map[string]bool{}
	procs := map[string]bool{}
	canonicalProcs := map[string]string{}
	for _, spec := range exposedTools {
		if names[spec.Name] {
			t.Errorf("duplicate tool name %s", spec.Name)
		}
		if !isCapabilityTool(spec.Name) {
			if previous, dup := canonicalProcs[spec.Procedure]; dup {
				t.Errorf("duplicate canonical tool procedure %s: %s and %s", spec.Procedure, previous, spec.Name)
			}
			canonicalProcs[spec.Procedure] = spec.Name
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
		if cov.Listed {
			listedN++
		}
		if cov.Reachable {
			reachableN++
		}
		if cov.Verified {
			verifiedN++
		}
		if pendingReason(op) != "" {
			// PENDING is honest partial coverage: no production transport.
			if cov.Verified {
				t.Errorf("%s: pending operation was marked VERIFIED", op.ID)
			}
			continue
		}
		if !cov.Complete() {
			t.Errorf("%s: live coverage incomplete: %+v", op.ID, cov)
		}
	}
	if listedN != IntegrationSupportedOps || reachableN != IntegrationSupportedOps || verifiedN != IntegrationSupportedOps {
		t.Fatalf("live coverage: LISTED=%d REACHABLE=%d VERIFIED=%d; want all %d supported operations verified",
			listedN, reachableN, verifiedN, IntegrationSupportedOps)
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

// TestIntegrationLiveToolMetadataMatchesCatalog closes the last-wins gap:
// go-sdk replaces a tool by name, so the six ConfigService names declared by
// both the server and admin families must resolve to the SAME spec the
// generator promoted. This compares the live tools/list metadata (name and
// description) with the generated catalog, not just the counts.
func TestIntegrationLiveToolMetadataMatchesCatalog(t *testing.T) {
	f := newFixture(t, "", true)
	listed, err := f.sessions["admin"].ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(listed.Tools) != len(exposedTools) {
		t.Fatalf("live tools/list returned %d tools, generated catalog has %d", len(listed.Tools), len(exposedTools))
	}
	want := make(map[string]ToolSpec, len(exposedTools))
	for _, spec := range exposedTools {
		want[spec.Name] = spec
	}
	seen := make(map[string]bool, len(listed.Tools))
	for _, live := range listed.Tools {
		spec, ok := want[live.Name]
		if !ok {
			t.Errorf("live tool %s is absent from the generated catalog", live.Name)
			continue
		}
		if seen[live.Name] {
			t.Errorf("live tool %s was mounted more than once", live.Name)
		}
		seen[live.Name] = true
		if live.Description != spec.Description {
			t.Errorf("live tool %s description diverged from generated catalog:\n live=%q\n want=%q",
				live.Name, live.Description, spec.Description)
		}
	}
	for name := range want {
		if !seen[name] {
			t.Errorf("generated tool %s is not mounted live", name)
		}
	}
}

// TestIntegrationFixtureEvidenceIsMounted proves the VERIFIED signal comes
// from executable invocation evidence, not a mounted handler string or a
// quoted procedure. Each supported operation must have reached its typed
// method with mapped arguments, observed a real outcome/Connect status and
// exercised its resource scope.
func TestIntegrationFixtureEvidenceIsMounted(t *testing.T) {
	evidence := runInvocationEvidence(t)
	for _, op := range FamilyAuditSupportedOperations() {
		target := targetProcedure(op)
		if target == "" || pendingReason(op) != "" {
			continue
		}
		ev, ok := evidence[target]
		if !ok || !ev.Complete() {
			t.Errorf("%s: no executable invocation evidence for %s: %+v", op.ID, target, ev)
			continue
		}
		if ev.Tool == "" || !ev.Reached || !ev.ArgsMapped || !ev.OutcomeObserved || !ev.ScopeEvidence {
			t.Errorf("%s: incomplete evidence for %s: %+v", op.ID, target, ev)
		}
	}
}
