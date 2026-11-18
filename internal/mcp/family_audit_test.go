// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"strings"
	"testing"

	_ "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// everyDeclaredProcedure enumerates all 26 proto services, including the
// unmounted NodeControlService. Reflection (19 names) and the RBAC parity test
// (24 names) are deliberately not used as sources of truth.
func everyDeclaredProcedure(t *testing.T) map[string]bool {
	t.Helper()
	out := make(map[string]bool)
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if fd.Package() != "carbonpanel.v1" {
			return true
		}
		for i := 0; i < fd.Services().Len(); i++ {
			sd := fd.Services().Get(i)
			for j := 0; j < sd.Methods().Len(); j++ {
				out["/"+string(sd.FullName())+"/"+string(sd.Methods().Get(j).Name())] = true
			}
		}
		return true
	})
	if len(out) != 231 {
		t.Fatalf("declared proto RPCs: got %d, want 231", len(out))
	}
	return out
}

// liveListedProcedures are the procedures with a declared catalog tuple after
// exposure overrides (catalog_data.go -> Catalog). A declaration is not a
// reachable tool: that is a separate count.
func liveListedProcedures() map[string]bool {
	out := make(map[string]bool, len(Catalog))
	for procedure := range Catalog {
		out[procedure] = true
	}
	return out
}

func liveReachableTools() map[string]bool {
	out := make(map[string]bool, len(exposedTools))
	for _, spec := range exposedTools {
		out[spec.Procedure] = true
	}
	return out
}

// liveFixtureProcedures are the typed procedures actually mounted behind the
// production auth interceptor in the disposable MCP fixture. A raw
// registerOK/new-handler line is the evidence that a tool's typed client path
// is exercised end to end without a live panel, Docker daemon or world.
func liveFixtureProcedures(t *testing.T) map[string]bool {
	t.Helper()
	// VERIFIED is executable invocation evidence: a real tools/call over the
	// SDK stdio wire that reached the typed method with mapped arguments,
	// observed the handler outcome/Connect status and exercised resource
	// scope. Source regexes and quoted procedure strings never count.
	evidence := runInvocationEvidence(t)
	out := make(map[string]bool, len(evidence))
	for procedure, ev := range evidence {
		if ev.Complete() {
			out[procedure] = true
		}
	}
	return out
}

// recomputeCoverage derives the three independent signals from live source for
// one audited operation. A non-RPC surface is covered through its composed
// target procedure, never by naming raw session ids, byte streams or keys.
func recomputeCoverage(op FamilyAuditOperation, listed, reachable, verified map[string]bool) FamilyAuditCoverage {
	target := op.Procedure
	if op.Kind != "rpc" {
		target = op.ComposedBy
	}
	if target == "" {
		return FamilyAuditCoverage{}
	}
	return FamilyAuditCoverage{
		Listed:    listed[target],
		Reachable: reachable[target],
		Verified:  verified[target],
	}
}

func TestFamilyAudit(t *testing.T) {
	matrix := FamilyAudit()

	t.Run("MatrixIntegrity", func(t *testing.T) {
		if matrix.SchemaVersion != 1 || matrix.Issue != "OSS-375" {
			t.Fatalf("unexpected matrix identity: version=%d issue=%q", matrix.SchemaVersion, matrix.Issue)
		}
		if matrix.Head == "" || matrix.Policy == "" || len(matrix.SourceOfTruth) < 4 {
			t.Fatal("matrix is missing provenance / policy / source-of-truth metadata")
		}

		declared := everyDeclaredProcedure(t)
		listed := liveListedProcedures()
		reachable := liveReachableTools()
		fixtures := liveFixtureProcedures(t)

		seen := make(map[string]bool, len(matrix.Operations))
		seenRPC := make(map[string]bool, len(declared))
		var declaredOps, mountedOps, unmountedOps, nonRPC int
		var recordedListed, recordedReachable, recordedVerified, supportedN, excludedN int
		var listedRPC, listedSurface, reachableRPC, reachableSurface, verifiedRPC, verifiedSurface int
		var excludedRPCN, excludedSurfaceN, supportedRPCN, supportedSurfaceN int
		for _, op := range matrix.Operations {
			if seen[op.ID] {
				t.Errorf("duplicate matrix operation id %s", op.ID)
			}
			seen[op.ID] = true
			if op.Kind == "rpc" {
				if !declared[op.Procedure] {
					t.Errorf("matrix RPC %s is not a declared proto procedure", op.Procedure)
				}
				if seenRPC[op.Procedure] {
					t.Errorf("duplicate RPC row %s", op.Procedure)
				}
				seenRPC[op.Procedure] = true
				declaredOps++
				if op.Mounted {
					mountedOps++
				} else {
					unmountedOps++
				}
				if op.Declared && !op.Mounted && !strings.Contains(op.Procedure, "NodeControlService/") {
					t.Errorf("%s: only NodeControlService may be declared-but-unmounted", op.Procedure)
				}
				if op.Mounted && op.Scope == "" {
					t.Errorf("%s: mounted RPC has no scope", op.Procedure)
				}
			} else {
				nonRPC++
			}

			if op.Reason == "" || op.ReasonRef == "" {
				t.Errorf("%s: missing exact source reason", op.ID)
			}
			if op.Support != FamilyAuditSupported && op.Support != FamilyAuditExcluded {
				t.Errorf("%s: invalid support decision %q", op.ID, op.Support)
			}
			if op.Kind == "rpc" {
				if op.Support == FamilyAuditSupported {
					supportedRPCN++
				} else {
					excludedRPCN++
				}
			} else {
				if op.Support == FamilyAuditSupported {
					supportedSurfaceN++
				} else {
					excludedSurfaceN++
				}
			}

			if op.Support == FamilyAuditSupported {
				supportedN++
				if op.Class == "" || (op.SuggestedTool == "" && op.Kind == "rpc") {
					t.Errorf("%s: supported operation lacks class / suggested tool", op.ID)
				}
				if op.Kind != "rpc" && op.ComposedBy == "" {
					t.Errorf("%s: supported non-RPC surface lacks a composed target", op.ID)
				}
			} else {
				excludedN++
				if op.EquivalentFlow == "" {
					t.Errorf("%s: exclusion has no supported equivalent flow", op.ID)
				}
			}

			// Recorded coverage is a baseline snapshot pinned to matrix.Head.
			// It may never claim more than live source proves (no false green),
			// but the integration owner may add coverage without editing this
			// audit artifact: a live signal may be ahead of the snapshot.
			live := recomputeCoverage(op, listed, reachable, fixtures)
			if op.Coverage.Listed && !live.Listed {
				t.Errorf("%s: recorded LISTED but procedure is not in the live catalog", op.ID)
			}
			if op.Coverage.Reachable && !live.Reachable {
				t.Errorf("%s: recorded REACHABLE but no live tool/composed path", op.ID)
			}
			if op.Coverage.Verified && !live.Verified {
				t.Errorf("%s: recorded VERIFIED but no live fixture", op.ID)
			}
			if op.Coverage.Listed {
				recordedListed++
				if op.Kind == "rpc" {
					listedRPC++
				} else {
					listedSurface++
				}
			}
			if op.Coverage.Reachable {
				recordedReachable++
				if op.Kind == "rpc" {
					reachableRPC++
				} else {
					reachableSurface++
				}
			}
			if op.Coverage.Verified {
				recordedVerified++
				if op.Kind == "rpc" {
					verifiedRPC++
				} else {
					verifiedSurface++
				}
			}
		}

		for procedure := range declared {
			if !seenRPC[procedure] {
				t.Errorf("declared RPC %s is missing from the matrix", procedure)
			}
		}
		if declaredOps != 231 || mountedOps != 221 || unmountedOps != 10 || nonRPC != 28 {
			t.Fatalf("matrix row counts: declared=%d mounted=%d unmounted=%d nonRPC=%d; want 231/221/10/28",
				declaredOps, mountedOps, unmountedOps, nonRPC)
		}

		want := FamilyAuditCounts{
			DeclaredRPCs:              231,
			MountedReachable:          221,
			ListedInToolCatalog:       recordedListed,
			ReachableByToolOrComposed: recordedReachable,
			VerifiedByFixture:         recordedVerified,
			ExcludedByReason:          excludedN,
			SupportedOperations:       supportedN,
			UnmountedRPCs:             unmountedOps,
			NonRPCSurfaces:            nonRPC,
			ListedRPCs:                listedRPC,
			ListedSurfaces:            listedSurface,
			ReachableRPCs:             reachableRPC,
			ReachableSurfaces:         reachableSurface,
			VerifiedRPCs:              verifiedRPC,
			VerifiedSurfaces:          verifiedSurface,
			ExcludedRPCs:              excludedRPCN,
			ExcludedSurfaces:          excludedSurfaceN,
			SupportedRPCs:             supportedRPCN,
			SupportedSurfaces:         supportedSurfaceN,
		}
		if matrix.Counts != want {
			t.Fatalf("matrix counts %+v differ from live %+v", matrix.Counts, want)
		}

		// Every mounted RPC's recorded scope still matches the live RBAC map.
		for _, op := range matrix.Operations {
			if op.Kind == "rpc" && op.Mounted {
				if got := expectedScope(op.Procedure); got != op.Scope {
					t.Errorf("%s: matrix scope %q, live rbac scope %q", op.Procedure, op.Scope, got)
				}
			}
		}
	})

	t.Run("ScopeSurfaces", func(t *testing.T) {
		// Every explicit exclusion class from the OSS-375 scope statement must
		// be present with a reason, a decision and a supported equivalent flow.
		cases := []struct {
			id       string
			refToken string
		}{
			{"rpc:/carbonpanel.v1.NodeControlService/CreateEnrollment", "node_control.proto"},
			{"rpc:/carbonpanel.v1.NodeControlService/DispatchOperation", "node_control.proto"},
			{"rpc:/carbonpanel.v1.NodeService/ScanNodes", "node.proto"},
			{"surface:node_discovery_scan", "node_scan.go"},
			{"surface:node_enrollment_tls_secrets", "node.proto"},
			{"surface:ws", "server.go"},
			{"surface:oidc_login", "server.go"},
			{"surface:oidc_callback", "server.go"},
			{"surface:oidc_exchange", "oidc.go"},
			{"surface:healthz", "server.go"},
			{"surface:readyz", "server.go"},
			{"surface:health_detail", "server.go"},
			{"surface:cli_operations", "operations.go"},
			{"surface:cli_maintenance_flags", "main.go"},
			{"surface:worker_protocol_jsonl", "protocol.go"},
			{"surface:carbon_storage_process", "transport.go"},
			{"surface:worldstore_blob", "store.go"},
			{"surface:docker_clientpool", "ClientPool"},
			{"surface:grpc_reflection", "server.go"},
			{"surface:openapi", "server.go"},
			{"surface:frontend_catchall", "server.go"},
			{"surface:worldview_map_proxy", "proxy.go"},
			{"rpc:/carbonpanel.v1.ServerService/SendCommand", "server.proto"},
			{"rpc:/carbonpanel.v1.NodeService/ListNodes", "node.proto"},
			{"rpc:/carbonpanel.v1.ProxyService/GetVelocitySecret", "proxy.proto"},
			{"rpc:/carbonpanel.v1.AuthService/CreateAPIToken", "auth.proto"},
			{"rpc:/carbonpanel.v1.AuthService/UseRecoveryKey", "mapping.go"},
			{"rpc:/carbonpanel.v1.WorldViewService/SetPlayerVisibility", "worldview.proto"},
			{"rpc:/carbonpanel.v1.ServerService/UploadToMCLogs", "server.proto"},
			{"surface:rest_upload_stream", "upload.go"},
			{"surface:rest_download_stream", "download.go"},
			{"surface:rest_metrics", "metrics.go"},
		}
		for _, tc := range cases {
			var op FamilyAuditOperation
			var ok bool
			for _, candidate := range matrix.Operations {
				if candidate.ID == tc.id {
					op, ok = candidate, true
					break
				}
			}
			if !ok {
				t.Errorf("scope surface %s is absent from the matrix", tc.id)
				continue
			}
			if op.ReasonRef == "" || !strings.Contains(strings.ToLower(op.ReasonRef), strings.ToLower(tc.refToken)) {
				t.Errorf("%s: reasonRef %q lacks expected source token %q", tc.id, op.ReasonRef, tc.refToken)
			}
			if op.EquivalentFlow == "" {
				t.Errorf("%s: no supported equivalent flow", tc.id)
			}
			if tc.id == "rpc:/carbonpanel.v1.ServerService/SendCommand" ||
				tc.id == "rpc:/carbonpanel.v1.WorldViewService/SetPlayerVisibility" ||
				tc.id == "surface:ws" || tc.id == "surface:oidc_exchange" ||
				tc.id == "surface:worldview_map_proxy" || tc.id == "surface:cli_operations" ||
				strings.HasPrefix(tc.id, "rpc:/carbonpanel.v1.NodeControlService/") {
				if op.Support != FamilyAuditExcluded {
					t.Errorf("%s: private / operator-only surface must stay excluded", tc.id)
				}
			}
		}

		// The supported equivalent of the browser world-map proxy is the
		// WorldViewService RPC family, which must stay IN SCOPE.
		for _, procedure := range []string{
			"/carbonpanel.v1.WorldViewService/GetWorldViewStatus",
			"/carbonpanel.v1.WorldViewService/GetWorldViewSession",
			"/carbonpanel.v1.WorldViewService/ListCapabilities",
			"/carbonpanel.v1.WorldViewService/ListMapOverlays",
		} {
			op, ok := FamilyAuditOperationByProcedure(procedure)
			if !ok || !op.Supported() {
				t.Errorf("world-view proxy equivalent %s must be supported, got %+v", procedure, op)
			}
		}
	})

	t.Run("SupportedOperationsGate", func(t *testing.T) {
		// THIS IS THE GATE. It is intentionally red while the first-slice
		// catalog is the only one integrated: every operation classified as a
		// supported workflow must be LISTED in the catalog, REACHABLE by a
		// registered tool (or bounded composed path), and VERIFIED by a fixture
		// behind the production auth interceptor. The integration owner expands
		// the combined catalog until this passes; the matrix names each missing
		// tool in suggestedTool/composedBy.
		listed := liveListedProcedures()
		reachable := liveReachableTools()
		fixtures := liveFixtureProcedures(t)
		var missing, liveListedN, liveReachableN, liveVerifiedN, liveExcludedN int
		for _, op := range matrix.Operations {
			if !op.Supported() {
				liveExcludedN++
				continue
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
			t.Errorf("OSS-375 full-coverage gate: %d supported operations are not listed, reachable and verified "+
				"(live: supported=%d listed=%d reachable=%d verified=%d excluded=%d; matrix baseline: %+v). "+
				"See internal/mcp/family_audit_matrix.json for the exhaustive machine-checkable list.",
				missing, len(matrix.Operations)-liveExcludedN, liveListedN, liveReachableN,
				liveVerifiedN, liveExcludedN, matrix.Counts)
		}
	})

	t.Run("PartialSliceRegressionGate", func(t *testing.T) {
		// PASSING regression gate for the explicitly enumerated PARTIAL SLICE.
		// Every operation that is NOT in the exact PENDING inventory must be
		// LISTED, REACHABLE and VERIFIED with executable tools/call evidence.
		// This gate is intentionally independent from the full gate above,
		// which stays RED for the 9 PENDING streaming operations.
		listed := liveListedProcedures()
		reachable := liveReachableTools()
		fixtures := liveFixtureProcedures(t)
		inScope, verified := 0, 0
		for _, op := range matrix.Operations {
			if !op.Supported() || pendingReason(op) != "" {
				continue
			}
			inScope++
			cov := recomputeCoverage(op, listed, reachable, fixtures)
			if !cov.Complete() {
				t.Errorf("partial-slice regression: %s missing %+v", op.ID, cov)
				continue
			}
			verified++
		}
		if inScope == 0 || verified != inScope {
			t.Fatalf("partial slice: %d/%d in-scope operations verified", verified, inScope)
		}
		// The pending inventory must be exact: every listed entry must still
		// be absent from the fixture evidence set, and no other operation may
		// be silently treated as pending.
		if len(PendingInventory) != 9 {
			t.Fatalf("PENDING inventory has %d entries, want 9", len(PendingInventory))
		}
		for _, pending := range PendingInventory {
			if fixtures[pending.Target] {
				t.Errorf("PENDING operation %s unexpectedly has VERIFIED evidence", pending.ID)
			}
		}
		t.Logf("PARTIAL SLICE REGRESSION GATE PASS: %d/%d non-pending operations verified; 9 PENDING explicitly excluded", verified, inScope)
	})

	t.Run("GateDetectsUnlistedSupportedOperation", func(t *testing.T) {
		// Prove the gate is not vacuous: a synthetic supported RPC row that is
		// absent from the catalog and fixtures is reported as a gap.
		listed := liveListedProcedures()
		reachable := liveReachableTools()
		fixtures := liveFixtureProcedures(t)
		// Under full coverage the first-slice gap is closed, so the original
		// ImportWorld probe is no longer absent. Prove non-vacuity with a
		// synthetic procedure that is absent from the catalog and fixtures by
		// construction, while keeping the distinct LISTED/REACHABLE signals.
		const syntheticProcedure = "/carbonpanel.v1.WorldService/ImportWorldSyntheticGap"
		synthetic := FamilyAuditOperation{
			ID: "rpc:" + syntheticProcedure,
			Kind: "rpc", Procedure: syntheticProcedure,
			Support: FamilyAuditSupported,
		}
		got := recomputeCoverage(synthetic, listed, reachable, fixtures)
		if got.Listed && got.Reachable && got.Verified {
			t.Fatal("synthetic unlisted operation unexpectedly reported as complete")
		}
		if listed[syntheticProcedure] || reachable[syntheticProcedure] || fixtures[syntheticProcedure] {
			t.Fatalf("synthetic gap procedure unexpectedly present: %+v", got)
		}
		// ImportWorld is now a fully covered operation: assert that the gate
		// recognizes its completion rather than assuming a first-slice gap.
		importWorld, ok := FamilyAuditOperationByProcedure("/carbonpanel.v1.WorldService/ImportWorld")
		if !ok || !importWorld.Supported() {
			t.Fatal("ImportWorld must remain a supported audited operation")
		}
		importCoverage := recomputeCoverage(importWorld, listed, reachable, fixtures)
		if !importCoverage.Complete() {
			t.Fatalf("ImportWorld must be complete under full coverage, got %+v", importCoverage)
		}
	})
}
