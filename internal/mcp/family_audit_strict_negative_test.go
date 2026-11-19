//go:build mcpfullcoverage

// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"strings"
	"testing"
)

// TestFamilyAuditFullCoverageFailClosed is the OSS-406 negative fixture. It
// temporarily injects a supported-but-unmounted operation into a TEST-ONLY
// overlay of the audit matrix, confirms the shared STRICT evaluator goes RED,
// then removes the overlay and confirms it returns GREEN. The production
// matrix and PendingInventory are never mutated; no waiver or tolerance is
// added. The name deliberately shares the TestFamilyAuditFullCoverage prefix so
// the fail-closed proof runs under the exact gate command used by CI.
func TestFamilyAuditFullCoverageFailClosed(t *testing.T) {
	base := FamilyAudit()
	listed := liveListedProcedures()
	reachable := liveReachableTools()
	verified := liveFixtureProcedures(t)

	clean := evaluateFullCoverage(base, listed, reachable, verified)
	if len(clean.gaps) != 0 {
		t.Fatalf("baseline STRICT gate should be green: %+v", clean.gaps)
	}

	// NodeControlService is the only service the matrix permits to be
	// declared-but-unmounted. Reclassify one of its real RPCs as SUPPORTED only
	// inside this copied matrix to simulate a newly unmounted operation.
	injected := FamilyAuditOperation{
		ID:        "negative-fixture:/carbonpanel.v1.NodeControlService/CreateEnrollment",
		Kind:      "rpc",
		Procedure: "/carbonpanel.v1.NodeControlService/CreateEnrollment",
		Service:   "carbonpanel.v1.NodeControlService",
		Declared:  true,
		Mounted:   false,
		Support:   FamilyAuditSupported,
		ReasonRef: "OSS-406 negative fixture (test-only overlay, never committed)",
	}
	overlay := base
	overlay.Operations = append(append([]FamilyAuditOperation(nil), base.Operations...), injected)

	red := evaluateFullCoverage(overlay, listed, reachable, verified)
	if len(red.gaps) != 1 {
		t.Fatalf("injected unmounted operation was not rejected (gate stayed green): %+v", red.gaps)
	}
	gap := red.gaps[0]
	if gap.opID != injected.ID {
		t.Fatalf("gate rejected unexpected operation %q, want %q", gap.opID, injected.ID)
	}
	for _, signal := range []string{"LISTED", "REACHABLE", "VERIFIED"} {
		if !strings.Contains(gap.absent, signal) {
			t.Errorf("gap did not report missing %s: %q", signal, gap.absent)
		}
	}
	if red.supported != clean.supported+1 {
		t.Errorf("overlay supported count = %d, want %d", red.supported, clean.supported+1)
	}

	// Remove the injected op: the same evaluator must return to GREEN without
	// any waiver, tolerance or inventory edit.
	overlay.Operations = base.Operations
	restored := evaluateFullCoverage(overlay, listed, reachable, verified)
	if len(restored.gaps) != 0 {
		t.Fatalf("gate stayed RED after removing the overlay: %+v", restored.gaps)
	}
}
