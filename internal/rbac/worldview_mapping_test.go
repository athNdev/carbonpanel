// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rbac

import "testing"

// TestWorldViewProcedurePermissions pins the mapping for every generated
// WorldViewService procedure. A new procedure that is not added here is denied
// by the interceptor; this test makes the intended scope explicit so a future
// edit cannot quietly widen it (OSS-354).
func TestWorldViewProcedurePermissions(t *testing.T) {
	want := map[string]ProcedurePermission{
		"/carbonpanel.v1.WorldViewService/ListWorldViewTargets": {Resource: ResourceWorldView, Action: ActionRead},
		"/carbonpanel.v1.WorldViewService/GetWorldViewStatus":    {Resource: ResourceWorldView, Action: ActionRead, ObjectIDField: "server_id"},
		"/carbonpanel.v1.WorldViewService/GetWorldViewSession":   {Resource: ResourceWorldView, Action: ActionRead, ObjectIDField: "server_id"},
		"/carbonpanel.v1.WorldViewService/ListCapabilities":      {Resource: ResourceWorldView, Action: ActionRead, ObjectIDField: "server_id"},
		"/carbonpanel.v1.WorldViewService/ListMapOverlays":       {Resource: ResourceWorldView, Action: ActionRead, ObjectIDField: "server_id"},
		"/carbonpanel.v1.WorldViewService/SetMapOverlay":         {Resource: ResourceWorldView, Action: ActionUpdate, ObjectIDField: "server_id"},
		"/carbonpanel.v1.WorldViewService/SetPlayerVisibility":   {Resource: ResourceWorldView, Action: ActionObservePlayers, ObjectIDField: "server_id"},
	}
	for proc, expected := range want {
		got, ok := ProcedurePermissions[proc]
		if !ok {
			t.Errorf("%s has no permission mapping", proc)
			continue
		}
		if got != expected {
			t.Errorf("%s mapping = %+v, want %+v", proc, got, expected)
		}
		if PublicProcedures[proc] || AuthenticatedOnlyProcedures[proc] {
			t.Errorf("%s must not be public or authenticated-only", proc)
		}
	}

	if len(ProcedurePermissions) < len(want) {
		t.Fatal("permission table unexpectedly shrank")
	}
}

// TestWorldViewResourceRegistration checks that world_view is a first-class,
// server-scoped resource and that all of its actions appear in the display
// lists used by RoleService/GetPermissionMatrix.
func TestWorldViewResourceRegistration(t *testing.T) {
	if _, ok := ResourceActionsFromProceduresMap()[ResourceWorldView]; !ok {
		t.Fatal("world_view has no derived actions; check AllResources and ProcedurePermissions")
	}

	foundResource := false
	for _, r := range AllResources {
		if r == ResourceWorldView {
			foundResource = true
		}
	}
	if !foundResource {
		t.Fatal("world_view missing from AllResources")
	}
	for _, action := range []string{ActionRead, ActionObservePlayers, ActionUpdate, ActionAdmin} {
		found := false
		for _, a := range AllActions {
			if a == action {
				found = true
			}
		}
		if !found {
			t.Errorf("action %q missing from AllActions", action)
		}
	}
	if got := ResourceScopeSource[ResourceWorldView]; got != ResourceServers {
		t.Fatalf("world_view scope source = %q, want %q", got, ResourceServers)
	}

	derived := ResourceActionsFromProcedures()
	var entry *ResourceActionEntry
	for i := range derived {
		if derived[i].Resource == ResourceWorldView {
			entry = &derived[i]
		}
	}
	if entry == nil {
		t.Fatal("world_view missing from ResourceActionsFromProcedures")
	}
	want := []string{ActionRead, ActionUpdate, ActionObservePlayers}
	if len(entry.Actions) != len(want) {
		t.Fatalf("world_view derived actions = %v, want %v", entry.Actions, want)
	}
	for i := range want {
		if entry.Actions[i] != want[i] {
			t.Fatalf("world_view derived actions = %v, want %v", entry.Actions, want)
		}
	}
}

// ResourceActionsFromProceduresMap is a small local helper so this test asserts
// on the same derived data the role UI consumes without depending on ordering.
func ResourceActionsFromProceduresMap() map[string][]string {
	out := make(map[string][]string)
	for _, entry := range ResourceActionsFromProcedures() {
		out[entry.Resource] = entry.Actions
	}
	return out
}
