//go:build mcpfullcoverage

// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp: STRICT full-coverage gate. This test is excluded from ordinary
// `go test ./...` by the mcpfullcoverage build tag and is invoked by the
// full-coverage command (now also from ordinary CI, in addition to the
// dispatch-only publication lane). It is GREEN only while every supported
// operation is LISTED, REACHABLE and VERIFIED with executable tools/call
// evidence; the byte-transport lanes are now mounted and the PENDING inventory
// is empty. The gate is never waived: adding an unmounted operation back to
// PendingInventory re-reds it, and no skip or tolerance is permitted.
package mcp

import (
	"fmt"
	"strings"
	"testing"
)

// fullCoverageGap is one supported operation the STRICT gate refuses.
type fullCoverageGap struct {
	opID   string
	absent string
	detail string
}

// fullCoverageResult is the pure outcome of the STRICT evaluator. It contains
// no test assertions so the negative fixture can prove the gate is fail-closed
// without changing the gate's tolerance: every gap is still fatal to
// TestFamilyAuditFullCoverage.
type fullCoverageResult struct {
	gaps                                                      []fullCoverageGap
	pending, supported, listed, reachable, verified, excluded int
}

// evaluateFullCoverage is the single fail-closed evaluator. It is deliberately
// intolerant: every supported operation must carry all three live signals and
// every PENDING inventory entry is counted. There is no waiver path.
func evaluateFullCoverage(matrix FamilyAuditMatrix, listed, reachable, verified map[string]bool) fullCoverageResult {
	var result fullCoverageResult
	for _, op := range matrix.Operations {
		if !op.Supported() {
			result.excluded++
			continue
		}
		if pendingReason(op) != "" {
			result.pending++
		}
		live := recomputeCoverage(op, listed, reachable, verified)
		if live.Listed {
			result.listed++
		}
		if live.Reachable {
			result.reachable++
		}
		if live.Verified {
			result.verified++
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
			result.gaps = append(result.gaps, fullCoverageGap{
				opID:   op.ID,
				absent: strings.Join(absent, "+"),
				detail: fmt.Sprintf("unsupported gap %s (%s): missing %s; tool=%s composedBy=%s; see %s",
					op.ID, op.Procedure, strings.Join(absent, "+"), op.SuggestedTool,
					op.ComposedBy, op.ReasonRef),
			})
		}
	}
	result.supported = len(matrix.Operations) - result.excluded
	return result
}

func TestFamilyAuditFullCoverage(t *testing.T) {
	// The composed byte paths share procedures with canonical tools. Those
	// procedures' mock outcomes cannot establish that a byte transfer works.
	t.Run("AuditInventory", TestFamilyAudit)
	t.Run("ConnectMounts", TestSharedMountListMatchesServerRegistrations)
	t.Run("DownloadBytes", TestDownloadToolsTypedMappingAndBytes)
	t.Run("DownloadScope", TestDownloadToolsResourceScopeDenial)
	t.Run("UploadBytes", TestUploadFamilyComposedStreamCommit)
	t.Run("UploadScope", TestUploadFamilyFreshCreateAtCommit)
	t.Run("ScopedReads", TestScopedReadOperationalTools)

	matrix := FamilyAudit()
	result := evaluateFullCoverage(matrix, liveListedProcedures(), liveReachableTools(), liveFixtureProcedures(t))
	for _, gap := range result.gaps {
		t.Errorf("%s", gap.detail)
	}
	if len(result.gaps) != 0 {
		t.Errorf("OSS-375 STRICT FULL-COVERAGE GATE: %d supported operations are not fully covered "+
			"(%d PENDING); live: supported=%d listed=%d reachable=%d verified=%d excluded=%d. "+
			"Publication is BLOCKED until every supported operation has real production "+
			"tools/call evidence and the PENDING inventory is empty.",
			len(result.gaps), result.pending, result.supported, result.listed,
			result.reachable, result.verified, result.excluded)
	}
}
