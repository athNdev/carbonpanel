// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rbac

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

const panelServicePackage = "carbonpanel.v1"

// generatedServiceDescriptorExceptions lists carbonpanel.v1 service descriptors
// that exist in the generated contract but are intentionally not part of the
// panel's production Connect RPC registration inventory. Procedures on these
// services must not be added to RBAC policy maps as panel-exposed operations;
// separate seams (for example the node-control contract and MCP gates) own
// their fail-closed coverage.
var generatedServiceDescriptorExceptions = map[string]string{
	"carbonpanel.v1.NodeControlService": "declared-but-unmounted managed-node control-plane contract; never registered as a panel Connect handler",
}

// TestProductionRPCDescriptorInventoryHasRBACCoverage compares generated
// carbonpanel.v1 service descriptors to RBAC policy coverage for the panel's
// production Connect RPC inventory. A new generated service must either be
// covered by the fail-closed classification maps or be added as an explicit,
// justified non-panel exception. This is the descriptor-driven gate for
// OSS-403: it prevents hand-maintained service lists from drifting away from
// the generated contract.
func TestProductionRPCDescriptorInventoryHasRBACCoverage(t *testing.T) {
	services, err := discoverPanelServiceDescriptors()
	if err != nil {
		t.Fatalf("discover panel service descriptors: %v", err)
	}
	if len(services) == 0 {
		t.Fatal("discovered zero carbonpanel.v1 service descriptors; inventory enumeration is broken")
	}

	inventoryServices := 0
	exceptedServices := 0
	procedures := 0

	for _, service := range services {
		name := string(service.FullName())
		if reason, excepted := generatedServiceDescriptorExceptions[name]; excepted {
			exceptedServices++
			if reason == "" {
				t.Errorf("descriptor exception %s must include a justification", name)
			}
			assertServiceNotInRBACMaps(t, service)
			continue
		}

		inventoryServices++
		methods := service.Methods()
		for i := 0; i < methods.Len(); i++ {
			method := methods.Get(i)
			procedure := "/" + name + "/" + string(method.Name())
			procedures++
			assertProcedureClassifiedExactlyOnce(t, procedure)
		}
	}

	if inventoryServices == 0 {
		t.Fatal("no panel production RPC services discovered after applying descriptor exceptions")
	}
	if exceptedServices != len(generatedServiceDescriptorExceptions) {
		t.Errorf("descriptor exception count mismatch: declared %d, matched %d in registry", len(generatedServiceDescriptorExceptions), exceptedServices)
	}
	if procedures == 0 {
		t.Fatal("walked zero panel RPC procedures; inventory enumeration is broken")
	}

	t.Logf("verified %d panel RPC procedures across %d registered services (%d explicit descriptor exceptions)", procedures, inventoryServices, exceptedServices)
}

// TestRBACMapsReferenceKnownGeneratedProcedures ensures policy maps do not
// contain stale or duplicate entries. Every mapped procedure must exist in the
// generated carbonpanel.v1 descriptor inventory and each procedure may appear
// in only one classification map.
func TestRBACMapsReferenceKnownGeneratedProcedures(t *testing.T) {
	known, err := discoverPanelProcedureSet()
	if err != nil {
		t.Fatalf("discover panel procedures: %v", err)
	}
	if len(known) == 0 {
		t.Fatal("discovered zero panel procedures; stale-mapping check is broken")
	}

	seen := make(map[string]string, len(PublicProcedures)+len(AuthenticatedOnlyProcedures)+len(ProcedurePermissions))
	classCounts := map[string]int{}

	record := func(class, procedure string) {
		classCounts[class]++
		if prev, exists := seen[procedure]; exists {
			t.Errorf("procedure %s classified as %s and %s; RBAC policy maps must not duplicate classifications", procedure, prev, class)
			return
		}
		seen[procedure] = class
		if _, ok := known[procedure]; !ok {
			t.Errorf("RBAC map entry %s (%s) does not correspond to any generated carbonpanel.v1 procedure", procedure, class)
		}
	}

	for procedure := range PublicProcedures {
		record("public", procedure)
	}
	for procedure := range AuthenticatedOnlyProcedures {
		record("authenticated-only", procedure)
	}
	for procedure := range ProcedurePermissions {
		record("permissioned", procedure)
	}

	if classCounts["public"] != len(PublicProcedures) || classCounts["authenticated-only"] != len(AuthenticatedOnlyProcedures) || classCounts["permissioned"] != len(ProcedurePermissions) {
		t.Errorf("classification counts diverged from map sizes: public %d/%d authenticated-only %d/%d permissioned %d/%d", classCounts["public"], len(PublicProcedures), classCounts["authenticated-only"], len(AuthenticatedOnlyProcedures), classCounts["permissioned"], len(ProcedurePermissions))
	}
	if len(seen) != len(PublicProcedures)+len(AuthenticatedOnlyProcedures)+len(ProcedurePermissions) {
		t.Errorf("unique classified procedures (%d) differ from total map entries (%d); duplicate classifications exist", len(seen), len(PublicProcedures)+len(AuthenticatedOnlyProcedures)+len(ProcedurePermissions))
	}
}

func discoverPanelServiceDescriptors() ([]protoreflect.ServiceDescriptor, error) {
	var services []protoreflect.ServiceDescriptor
	protoregistry.GlobalFiles.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		if string(fd.Package()) != panelServicePackage {
			return true
		}
		fileServices := fd.Services()
		for i := 0; i < fileServices.Len(); i++ {
			services = append(services, fileServices.Get(i))
		}
		return true
	})
	sort.Slice(services, func(i, j int) bool {
		return services[i].FullName() < services[j].FullName()
	})
	if len(services) == 0 {
		return nil, fmt.Errorf("no services found for package %s", panelServicePackage)
	}
	return services, nil
}

func discoverPanelProcedureSet() (map[string]struct{}, error) {
	services, err := discoverPanelServiceDescriptors()
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{})
	for _, service := range services {
		methods := service.Methods()
		for i := 0; i < methods.Len(); i++ {
			procedure := "/" + string(service.FullName()) + "/" + string(methods.Get(i).Name())
			known[procedure] = struct{}{}
		}
	}
	return known, nil
}

func assertServiceNotInRBACMaps(t *testing.T, service protoreflect.ServiceDescriptor) {
	t.Helper()
	serviceName := string(service.FullName())
	methods := service.Methods()
	for i := 0; i < methods.Len(); i++ {
		procedure := "/" + serviceName + "/" + string(methods.Get(i).Name())
		if PublicProcedures[procedure] || AuthenticatedOnlyProcedures[procedure] {
			t.Errorf("excepted descriptor procedure %s must not be classified as panel-public/authenticated-only", procedure)
		}
		if _, ok := ProcedurePermissions[procedure]; ok {
			t.Errorf("excepted descriptor procedure %s must not appear in panel RBAC permission mappings", procedure)
		}
	}
}

// classifyProcedure counts how many of the three RBAC tables classify a
// procedure. Exactly one is required: zero means the authorization interceptor
// denies the procedure by default, more than one is an ambiguous mapping.
//
// This helper is deliberately pure so the fail-closed and duplicate branches
// can be proven with synthetic tables in the negative tests below, without
// mutating the package-level maps shared by the rest of the suite.
func classifyProcedure(procedure string, public, authenticatedOnly map[string]bool, permissions map[string]ProcedurePermission) int {
	count := 0
	if public[procedure] {
		count++
	}
	if authenticatedOnly[procedure] {
		count++
	}
	if _, ok := permissions[procedure]; ok {
		count++
	}
	return count
}

func assertProcedureClassifiedExactlyOnce(t *testing.T, procedure string) {
	t.Helper()
	if !strings.HasPrefix(procedure, "/"+panelServicePackage+".") {
		t.Fatalf("procedure %s is outside %s inventory", procedure, panelServicePackage)
	}

	count := classifyProcedure(procedure, PublicProcedures, AuthenticatedOnlyProcedures, ProcedurePermissions)
	switch count {
	case 0:
		t.Errorf("procedure %s is not present in PublicProcedures, AuthenticatedOnlyProcedures, or ProcedurePermissions; it will be denied by default", procedure)
	case 1:
		// Correct: exactly one fail-closed classification.
	default:
		t.Errorf("procedure %s appears in %d of the three RBAC maps; it must appear in exactly one", procedure, count)
	}
}

// TestEnumerationGateFailsClosedOnMissingMapping proves the gate reports a
// procedure that appears in none of the three tables (the deny-by-default case
// the interceptor enforces).
func TestEnumerationGateFailsClosedOnMissingMapping(t *testing.T) {
	const procedure = "/carbonpanel.v1.FutureService/FutureRpc"
	if got := classifyProcedure(procedure, nil, nil, nil); got != 0 {
		t.Fatalf("unmapped procedure classified %d times, want 0 (deny by default)", got)
	}
}

// TestEnumerationGateRejectsDuplicateMapping proves the gate reports a
// procedure that appears in more than one table (an ambiguous classification).
func TestEnumerationGateRejectsDuplicateMapping(t *testing.T) {
	const procedure = "/carbonpanel.v1.FutureService/FutureRpc"
	public := map[string]bool{procedure: true}
	authenticatedOnly := map[string]bool{}
	permissions := map[string]ProcedurePermission{
		procedure: {Resource: "future", Action: "read"},
	}
	if got := classifyProcedure(procedure, public, authenticatedOnly, permissions); got != 2 {
		t.Fatalf("duplicated procedure classified %d times, want 2", got)
	}
}
