// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rbac

import "testing"

// TestWorldViewDefaultGrantsAreFailClosed pins the built-in role grants for
// world viewing. Anonymous must not be able to open a map, and a normal user's
// map read must not imply player tracking, overlay edits or world_view/admin.
// Only the built-in admin role (which matches "*") holds the stronger actions.
func TestWorldViewDefaultGrantsAreFailClosed(t *testing.T) {
	policies := DefaultRolePolicies()

	has := func(role, resource, action string) bool {
		for _, p := range policies[role] {
			if len(p) >= 3 && p[1] == resource && p[2] == action {
				return true
			}
		}
		return false
	}

	if !has("user", ResourceWorldView, ActionRead) {
		t.Fatal("user role is missing world_view/read")
	}
	if has("anonymous", ResourceWorldView, ActionRead) {
		t.Fatal("anonymous role must not be granted world_view/read")
	}
	for _, action := range []string{ActionObservePlayers, ActionUpdate, ActionAdmin} {
		if has("user", ResourceWorldView, action) {
			t.Errorf("user role must not be granted world_view/%s by default", action)
		}
		if has("anonymous", ResourceWorldView, action) {
			t.Errorf("anonymous role must not be granted world_view/%s", action)
		}
	}

	admin := policies["admin"]
	if len(admin) != 1 || admin[0][1] != "*" || admin[0][2] != "*" || admin[0][3] != "*" {
		t.Fatalf("admin role no longer holds the global wildcard: %v", admin)
	}
}
