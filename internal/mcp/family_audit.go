// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// OSS-375 full-coverage audit lane.
//
// family_audit_matrix.json is the exhaustive, machine-checkable evidence
// artifact: every declared RPC (251), every mounted RPC (241) and every
// non-RPC product surface is classified once, with an exact source reason and,
// for an exclusion, a supported equivalent flow. The three coverage counts
// (LISTED / REACHABLE / VERIFIED) are kept separate on purpose: a declared
// catalog tuple is not a callable tool, and a callable tool is not a fixture-
// verified one. family_audit_test.go recomputes those counts from live source
// and fails when a SUPPORTED operation is unlisted or unverified.
//
// This file only loads and exposes the matrix. It does not edit the shared
// catalog (catalog.go / catalog_data.go / tools.go / server.go / client.go /
// errors.go), which the single integration owner generates.

//go:embed family_audit_matrix.json
var familyAuditMatrixJSON []byte

// FamilyAudit support decisions.
const (
	FamilyAuditSupported = "supported"
	FamilyAuditExcluded  = "excluded"
)

// FamilyAuditCoverage holds the three independent coverage signals for one
// audited operation. They must never be collapsed into a single "covered" flag.
type FamilyAuditCoverage struct {
	Listed    bool `json:"listed"`
	Reachable bool `json:"reachable"`
	Verified  bool `json:"verified"`
}

// Complete reports whether the operation is listed, reachable and verified.
func (c FamilyAuditCoverage) Complete() bool {
	return c.Listed && c.Reachable && c.Verified
}

// FamilyAuditOperation is one row of the coverage matrix.
type FamilyAuditOperation struct {
	ID             string              `json:"id"`
	Kind           string              `json:"kind"`
	Procedure      string              `json:"procedure"`
	Service        string              `json:"service"`
	Method         string              `json:"method"`
	Client         string              `json:"client"`
	Scope          string              `json:"scope"`
	Declared       bool                `json:"declared"`
	Mounted        bool                `json:"mounted"`
	Support        string              `json:"support"`
	Class          string              `json:"class"`
	Reason         string              `json:"reason"`
	ReasonRef      string              `json:"reasonRef"`
	EquivalentFlow string              `json:"equivalentFlow"`
	HTTPMounts     []string            `json:"httpMounts,omitempty"`
	ComposedBy     string              `json:"composedBy"`
	SuggestedTool  string              `json:"suggestedTool"`
	Coverage       FamilyAuditCoverage `json:"coverage"`
}

// Supported reports whether the operation is an ordinary supported operation
// that must be mapped to a tested callable tool or composed workflow.
func (o FamilyAuditOperation) Supported() bool { return o.Support == FamilyAuditSupported }

// FamilyAuditCounts are kept separate so a catalog declaration cannot be
// mistaken for a reachable tool or a fixture-verified path.
type FamilyAuditCounts struct {
	DeclaredRPCs              int `json:"declared_rpcs"`
	MountedReachable          int `json:"mounted_reachable"`
	ListedInToolCatalog       int `json:"listed_in_tool_catalog"`
	ReachableByToolOrComposed int `json:"reachable_by_tool_or_composed"`
	VerifiedByFixture         int `json:"verified_by_fixture"`
	ExcludedByReason          int `json:"excluded_by_reason"`
	SupportedOperations       int `json:"supported_operations"`
	UnmountedRPCs             int `json:"unmounted_rpcs"`
	NonRPCSurfaces            int `json:"non_rpc_surfaces"`

	// Unambiguous RPC-vs-surface breakdowns. A surface is covered through its
	// bounded composed target procedure, never by naming a raw transport id.
	ListedRPCs        int `json:"listed_rpcs"`
	ListedSurfaces    int `json:"listed_surfaces"`
	ReachableRPCs     int `json:"reachable_rpcs"`
	ReachableSurfaces int `json:"reachable_surfaces"`
	VerifiedRPCs      int `json:"verified_rpcs"`
	VerifiedSurfaces  int `json:"verified_surfaces"`
	ExcludedRPCs      int `json:"excluded_rpcs"`
	ExcludedSurfaces  int `json:"excluded_surfaces"`
	SupportedRPCs     int `json:"supported_rpcs"`
	SupportedSurfaces int `json:"supported_surfaces"`
}

// FamilyAuditMatrix is the parsed evidence artifact.
type FamilyAuditMatrix struct {
	SchemaVersion int                    `json:"schemaVersion"`
	Issue         string                 `json:"issue"`
	Head          string                 `json:"head"`
	SourceOfTruth map[string]string      `json:"sourceOfTruth"`
	Policy        string                 `json:"policy"`
	Counts        FamilyAuditCounts      `json:"counts"`
	Operations    []FamilyAuditOperation `json:"operations"`
}

var familyAuditMatrix = func() FamilyAuditMatrix {
	var m FamilyAuditMatrix
	if err := json.Unmarshal(familyAuditMatrixJSON, &m); err != nil {
		panic(fmt.Sprintf("mcp: invalid embedded family_audit_matrix.json: %v", err))
	}
	return m
}()

// FamilyAudit returns the parsed OSS-375 coverage matrix. Callers must treat
// the returned value as read-only.
func FamilyAudit() FamilyAuditMatrix { return familyAuditMatrix }

// FamilyAuditSupportedOperations returns every operation classified IN SCOPE.
func FamilyAuditSupportedOperations() []FamilyAuditOperation {
	out := make([]FamilyAuditOperation, 0, familyAuditMatrix.Counts.SupportedOperations)
	for _, op := range familyAuditMatrix.Operations {
		if op.Supported() {
			out = append(out, op)
		}
	}
	return out
}

// FamilyAuditExcludedOperations returns every operation classified OUT OF SCOPE
// together with its exact reason and supported equivalent flow.
func FamilyAuditExcludedOperations() []FamilyAuditOperation {
	out := make([]FamilyAuditOperation, 0, familyAuditMatrix.Counts.ExcludedByReason)
	for _, op := range familyAuditMatrix.Operations {
		if !op.Supported() {
			out = append(out, op)
		}
	}
	return out
}

// FamilyAuditOperationByProcedure looks up an audited Connect procedure.
func FamilyAuditOperationByProcedure(procedure string) (FamilyAuditOperation, bool) {
	for _, op := range familyAuditMatrix.Operations {
		if op.Procedure == procedure {
			return op, true
		}
	}
	return FamilyAuditOperation{}, false
}

// FamilyAuditGaps returns supported operations whose recorded coverage is
// incomplete. The integration owner expands the combined catalog until this
// list is empty; family_audit_test.go performs the live-source recomputation.
func FamilyAuditGaps() []FamilyAuditOperation {
	out := make([]FamilyAuditOperation, 0)
	for _, op := range familyAuditMatrix.Operations {
		if op.Supported() && !op.Coverage.Complete() {
			out = append(out, op)
		}
	}
	return out
}
