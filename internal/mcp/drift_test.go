// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	_ "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// expectedScope computes the canonical scope key from the live RBAC maps.
func expectedScope(procedure string) string {
	if rbac.PublicProcedures[procedure] {
		return "public"
	}
	if rbac.AuthenticatedOnlyProcedures[procedure] {
		return "authenticated"
	}
	p, ok := rbac.ProcedurePermissions[procedure]
	if !ok {
		return ""
	}
	if p.ObjectIDField != "" {
		return p.Resource + "/" + p.Action + "[" + p.ObjectIDField + "]"
	}
	return p.Resource + "/" + p.Action
}

func mountedProcedures(t *testing.T) map[string]protoreflect.MethodDescriptor {
	t.Helper()
	out := make(map[string]protoreflect.MethodDescriptor)
	for _, service := range MountedServices {
		desc, err := protoregistry.GlobalFiles.FindDescriptorByName(protoreflect.FullName("carbonpanel.v1." + service))
		if err != nil {
			t.Fatalf("mounted service %s has no descriptor: %v", service, err)
		}
		sd, ok := desc.(protoreflect.ServiceDescriptor)
		if !ok {
			t.Fatalf("carbonpanel.v1.%s is not a service descriptor", service)
		}
		for i := 0; i < sd.Methods().Len(); i++ {
			md := sd.Methods().Get(i)
			procedure := "/" + string(sd.FullName()) + "/" + string(md.Name())
			if _, dup := out[procedure]; dup {
				t.Fatalf("duplicate procedure %s", procedure)
			}
			out[procedure] = md
		}
	}
	return out
}

func fieldsKey(m protoreflect.MessageDescriptor) string {
	var parts []string
	for i := 0; i < m.Fields().Len(); i++ {
		f := m.Fields().Get(i)
		typ := f.Kind().String()
		switch f.Kind() {
		case protoreflect.MessageKind, protoreflect.GroupKind:
			typ += ":" + string(f.Message().FullName())
		case protoreflect.EnumKind:
			typ += ":" + string(f.Enum().FullName())
		}
		parts = append(parts, fmt.Sprintf("%d:%s:%s:%s", f.Number(), f.Name(), f.Cardinality(), typ))
	}
	return strings.Join(parts, ";")
}

func TestSharedMountListMatchesServerRegistrations(t *testing.T) {
	// internal/rpc exercises actual handler registration against this list.
	want := slices.Clone(rpc.RegisteredConnectServices)
	got := make([]string, 0, len(MountedServices))
	for _, service := range MountedServices {
		got = append(got, "carbonpanel.v1."+service)
	}
	sort.Strings(want)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Fatalf("MCP service list differs from production: got %v, want %v", got, want)
	}
}

func TestCoverageDriftGate(t *testing.T) {
	mounted := mountedProcedures(t)
	if len(MountedServices) != 27 {
		t.Fatalf("expected 27 mounted services, got %d", len(MountedServices))
	}
	// OSS-387 added the three controller-owned bounded-frame transport RPCs
	// (OpenStream/ReadStreamFrame/CloseStream) to the mounted FileService.
	if len(mounted) != 245 {
		t.Fatalf("expected 245 mounted RPCs, got %d", len(mounted))
	}

	// Catalog is the declared table after exposure overrides are applied.
	declared := Catalog
	if len(declared) != len(rawCatalogEntries) {
		t.Fatalf("catalog has %d entries, generated table has %d (duplicate procedure?)", len(declared), len(rawCatalogEntries))
	}
	for _, e := range rawCatalogEntries {
		if _, ok := declared[e.Procedure]; !ok {
			t.Fatalf("generated entry %s is missing from the catalog", e.Procedure)
		}
	}

	// Missing or extra tools both fail the gate.
	for procedure := range mounted {
		if _, ok := declared[procedure]; !ok {
			t.Errorf("MISSING declared tool tuple for mounted RPC %s", procedure)
		}
	}
	for procedure, e := range declared {
		if _, ok := mounted[procedure]; !ok {
			t.Errorf("EXTRA declared tool tuple for unmounted RPC %s", procedure)
		}
		if e.Method != procedure[strings.LastIndex(procedure, "/")+1:] {
			t.Errorf("%s: typed client method %q does not match procedure", procedure, e.Method)
		}
		short := e.Service[strings.LastIndex(e.Service, ".")+1:]
		if e.Client != "New"+short+"Client" {
			t.Errorf("%s: client constructor %q does not match service %s", procedure, e.Client, short)
		}
	}

	// Every declared scope must equal the live mapping or a sentinel.
	for procedure, e := range declared {
		want := expectedScope(procedure)
		if want == "" {
			t.Errorf("%s has no live rbac mapping or sentinel", procedure)
			continue
		}
		if e.Scope != want {
			t.Errorf("%s: declared scope %q, live rbac scope %q", procedure, e.Scope, want)
		}
	}

	// NodeControl is present as generated code but must stay unmounted and
	// outside the catalog. WorldView must be inside (the stale 24-service
	// parity list in internal/rbac/mapping_test.go omits it).
	ncDesc, err := protoregistry.GlobalFiles.FindDescriptorByName("carbonpanel.v1.NodeControlService")
	if err != nil {
		t.Fatalf("NodeControlService descriptor missing: %v", err)
	}
	nc, ok := ncDesc.(protoreflect.ServiceDescriptor)
	if !ok || nc.Methods().Len() != 10 {
		t.Fatalf("unexpected NodeControlService descriptor: %v", ncDesc)
	}
	for _, service := range MountedServices {
		if service == "NodeControlService" {
			t.Fatal("NodeControlService must not be in the shared mount list")
		}
	}
	for procedure := range declared {
		if strings.Contains(procedure, "NodeControlService") {
			t.Errorf("NodeControl procedure declared as a tool: %s", procedure)
		}
	}
	worldViewCount := 0
	for procedure := range mounted {
		if strings.Contains(procedure, "WorldViewService/") {
			worldViewCount++
		}
	}
	if worldViewCount != 7 {
		t.Fatalf("expected 7 mounted WorldViewService RPCs, got %d", worldViewCount)
	}
	if _, ok := declared["/carbonpanel.v1.WorldViewService/ListWorldViewTargets"]; !ok {
		t.Fatal("WorldViewService must be included in the catalog")
	}

	// Pin request/response shapes: full names plus field numbers and types.
	var fingerprintParts []string
	for procedure, md := range mounted {
		fingerprintParts = append(fingerprintParts, strings.Join([]string{
			procedure,
			string(md.Input().FullName()),
			string(md.Output().FullName()),
			fieldsKey(md.Input()),
			fieldsKey(md.Output()),
		}, "|"))
	}
	sort.Strings(fingerprintParts)
	sum := sha256.Sum256([]byte(strings.Join(fingerprintParts, "\n")))
	if got := hex.EncodeToString(sum[:]); got != descriptorFingerprint {
		t.Fatalf("proto descriptor fingerprint changed: got %s, want %s", got, descriptorFingerprint)
	}

	// Exposed tool declarations must be a subset of the catalog with
	// consistent class, confirmation and capability qualification.
	if len(firstSliceTools) != FirstSliceToolCount {
		t.Fatalf("expected %d FIRST SLICE tools, got %d", FirstSliceToolCount, len(firstSliceTools))
	}
	seen := make(map[string]bool, len(firstSliceTools))
	for _, spec := range firstSliceTools {
		if seen[spec.Name] {
			t.Fatalf("duplicate tool %s", spec.Name)
		}
		seen[spec.Name] = true
		entry, ok := declared[spec.Procedure]
		if !ok {
			t.Errorf("tool %s declares unmounted procedure %s", spec.Name, spec.Procedure)
			continue
		}
		if entry.Tool != spec.Name || entry.Class != spec.Class || entry.Confirm != spec.Confirm {
			t.Errorf("tool %s does not match catalog entry %+v", spec.Name, entry)
		}
		if IsSmokeTool(spec.Name) && entry.Build != PanelBuildStableV140 {
			t.Errorf("smoke tool %s is not qualified for stable v1.0.40", spec.Name)
		}
	}
}

func TestNewServiceFamiliesRequirePostStableBuild(t *testing.T) {
	for _, service := range []string{"PlayerService", "WorldService", "WorldViewService", "ModBrowseService"} {
		count := 0
		for _, e := range rawCatalogEntries {
			if strings.Contains(e.Service, "."+service) {
				count++
				if e.Build != PanelBuildPostStable {
					t.Errorf("%s: build %q is not post-stable", e.Procedure, e.Build)
				}
			}
		}
		if count == 0 {
			t.Errorf("no catalog entries for %s", service)
		}
	}
}
