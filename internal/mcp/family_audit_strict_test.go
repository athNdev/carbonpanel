//go:build mcpfullcoverage

// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp: STRICT full-coverage gate. This test is excluded from ordinary
// `go test ./...` by the mcpfullcoverage build tag and is invoked only by the
// explicit distribution-verify command. It is GREEN only while every supported
// operation is LISTED, REACHABLE and VERIFIED with executable tools/call
// evidence; the byte-transport lanes are now mounted and the PENDING inventory
// is empty. The gate is never waived: adding an unmounted operation back to
// PendingInventory re-reds it, and no skip or tolerance is permitted.
package mcp

import (
	"strings"
	"testing"
)

func TestFamilyAuditFullCoverage(t *testing.T) {
	matrix := FamilyAudit()
	listed := liveListedProcedures()
	reachable := liveReachableTools()
	fixtures := liveFixtureProcedures(t)
	var missing, liveListedN, liveReachableN, liveVerifiedN, liveExcludedN int
	var pending int
	for _, op := range matrix.Operations {
		if !op.Supported() {
			liveExcludedN++
			continue
		}
		if pendingReason(op) != "" {
			pending++
		}
		live := recomputeCoverage(op, listed, reachable, fixtures)
		if live.Listed {
			liveListedN++
		}
		if live.Reachable {
			liveReachableN++
		}
		if live.Verified {
			liveVerifiedN++
		}
		var absent []string
		if !live.Listed {
			absent = append(absent, "LISTED")
		}
		if !live.Reachable {
			absent = append(absent, "REACHABLE")
		}
		if !live.Verified {
			absent = append(absent, "VERIFIED")
		}
		if len(absent) != 0 {
			missing++
			t.Errorf("unsupported gap %s (%s): missing %s; tool=%s composedBy=%s; see %s",
				op.ID, op.Procedure, strings.Join(absent, "+"), op.SuggestedTool,
				op.ComposedBy, op.ReasonRef)
		}
	}
	if missing != 0 {
		t.Errorf("OSS-375 STRICT FULL-COVERAGE GATE: %d supported operations are not fully covered "+
			"(%d PENDING); live: supported=%d listed=%d reachable=%d verified=%d excluded=%d. "+
			"Publication is BLOCKED until every supported operation has real production "+
			"tools/call evidence and the PENDING inventory is empty.",
			missing, pending, len(matrix.Operations)-liveExcludedN, liveListedN,
			liveReachableN, liveVerifiedN, liveExcludedN)
	}
}
