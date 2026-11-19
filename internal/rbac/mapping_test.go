// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rbac_test

import (
	"testing"

	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	carbonpanelv1connect "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// classifyProcedure counts how many of the three RBAC tables classify a
// procedure. Exactly one is required: zero means the authorization interceptor
// denies the procedure by default, more than one is an ambiguous mapping.
//
// This helper is deliberately pure so the fail-closed behaviour can be proven
// with synthetic tables in the negative tests below, without mutating the
// package-level maps shared by the rest of the suite.
func classifyProcedure(procedure string, public, authenticatedOnly map[string]bool, permissions map[string]rbac.ProcedurePermission) int {
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

// TestEveryProcedureAppearsInExactlyOneMap is the CI gate that keeps the
// fail-closed authorization interceptor honest. The interceptor denies any
// procedure that is not present in one of PublicProcedures,
// AuthenticatedOnlyProcedures, or ProcedurePermissions, so a forgotten (or
// duplicated) mapping entry silently degrades security. This test enumerates
// every procedure declared on EVERY service registered as a Connect handler
// (the authoritative rpc.RegisteredConnectServices list) and asserts each
// appears in exactly one of the three tables.
//
// Because new methods on a registered service are discovered from the protobuf
// descriptor at test time, adding an RPC without a permission mapping fails CI
// here. Exact per-procedure expectations for the services named in OSS-403 live
// in the dedicated authz tests (mod_browse_authz_test.go,
// packwiz_authz_test.go, staged_config_authz_test.go,
// worldview_authz_test.go, ...); this gate is the complementary drift net.
func TestEveryProcedureAppearsInExactlyOneMap(t *testing.T) {
	services := 0
	procedures := 0

	for _, serviceName := range rpc.RegisteredConnectServices {
		desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName(serviceName))
		if err != nil {
			t.Fatalf("service %q is not registered in the global proto registry: %v (is the carbonpanelv1 package linked into the test binary?)", serviceName, err)
		}
		service, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			t.Fatalf("descriptor %q is a %T, expected a service descriptor", serviceName, desc)
		}

		services++
		methods := service.Methods()
		for i := 0; i < methods.Len(); i++ {
			method := methods.Get(i)
			procedure := "/" + serviceName + "/" + string(method.Name())
			procedures++

			count := classifyProcedure(procedure, rbac.PublicProcedures, rbac.AuthenticatedOnlyProcedures, rbac.ProcedurePermissions)
			switch count {
			case 0:
				t.Errorf("procedure %s is not present in PublicProcedures, AuthenticatedOnlyProcedures, or ProcedurePermissions; it will be denied by default", procedure)
			case 1:
				// Correct: exactly one classification.
			default:
				t.Errorf("procedure %s appears in %d of the three RBAC maps; it must appear in exactly one", procedure, count)
			}
		}
	}

	if services != len(rpc.RegisteredConnectServices) {
		t.Errorf("expected %d services, walked %d", len(rpc.RegisteredConnectServices), services)
	}
	if procedures == 0 {
		t.Fatal("walked zero procedures; the procedure enumeration is broken")
	}
	t.Logf("verified %d procedures across %d services are each mapped exactly once", procedures, services)
}

// TestEnumerationGateCoversAuditServices pins the four services OSS-403 called
// out as missing from the old hand-maintained gate. It also rejects a duplicate
// entry, which would otherwise mask a drift in the authoritative list.
func TestEnumerationGateCoversAuditServices(t *testing.T) {
	seen := make(map[string]struct{}, len(rpc.RegisteredConnectServices))
	for _, name := range rpc.RegisteredConnectServices {
		if _, dup := seen[name]; dup {
			t.Errorf("service %q listed more than once in rpc.RegisteredConnectServices", name)
		}
		seen[name] = struct{}{}
	}

	for _, name := range []string{
		carbonpanelv1connect.ModBrowseServiceName,
		carbonpanelv1connect.PackwizServiceName,
		carbonpanelv1connect.StagedConfigServiceName,
		carbonpanelv1connect.WorldViewServiceName,
	} {
		if _, ok := seen[name]; !ok {
			t.Errorf("registered service %q is missing from rpc.RegisteredConnectServices; its new RPCs would get no CI signal", name)
		}
	}
}

// TestEnumerationGateFailsClosedOnMissingMapping proves the gate reports a
// procedure that appears in none of the three tables (the deny-by-default
// case the interceptor enforces).
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
	permissions := map[string]rbac.ProcedurePermission{
		procedure: {Resource: "future", Action: "read"},
	}
	if got := classifyProcedure(procedure, public, authenticatedOnly, permissions); got != 2 {
		t.Fatalf("duplicated procedure classified %d times, want 2", got)
	}
}
