// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rbac

import "testing"

// playerProcedures is the complete set of PlayerService RPCs. The generic
// mapping_test gate proves each is mapped exactly once; this file pins the
// *meaning* of each mapping so a permissive edit cannot pass unnoticed.
var playerProcedures = map[string]ProcedurePermission{
	"/carbonpanel.v1.PlayerService/ListPlayers":            {Resource: ResourcePlayers, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/GetPlayer":              {Resource: ResourcePlayers, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/ListPlayerSessions":     {Resource: ResourcePlayers, Action: ActionPresence, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/ListPlayerAccess":       {Resource: ResourcePlayers, Action: ActionAccessRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/ListPlayerHistory":      {Resource: ResourcePlayers, Action: ActionHistoryRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/ListPlayerCapabilities": {Resource: ResourcePlayers, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/GetPlayerSkin":          {Resource: ResourcePlayers, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/RefreshPlayerSkin":      {Resource: ResourcePlayers, Action: ActionSkinRefresh, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/GetPlayerDeepData":      {Resource: ResourcePlayers, Action: ActionInventory, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/KickPlayer":             {Resource: ResourcePlayers, Action: ActionModerate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/BanPlayer":              {Resource: ResourcePlayers, Action: ActionModerate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/PardonPlayer":           {Resource: ResourcePlayers, Action: ActionModerate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/SetOperator":            {Resource: ResourcePlayers, Action: ActionModerateOp, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/SetWhitelistEntry":      {Resource: ResourcePlayers, Action: ActionAccessWrite, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/BulkPlayerAction":       {Resource: ResourcePlayers, Action: ActionBulk, ObjectIDField: "server_id"},
	"/carbonpanel.v1.PlayerService/UndoPlayerAction":       {Resource: ResourcePlayers, Action: ActionModerate, ObjectIDField: "server_id"},
}

func TestPlayerProcedureMappings(t *testing.T) {
	for proc, want := range playerProcedures {
		got, ok := ProcedurePermissions[proc]
		if !ok {
			t.Errorf("%s has no permission mapping (denied by default)", proc)
			continue
		}
		if got != want {
			t.Errorf("%s mapping = %+v, want %+v", proc, got, want)
		}
	}
	if len(ProcedurePermissions) < len(playerProcedures) {
		t.Fatal("permission map unexpectedly shrank")
	}
}

func TestPlayerResourceIsServerScoped(t *testing.T) {
	if got := ResourceScopeSource[ResourcePlayers]; got != ResourceServers {
		t.Fatalf("players scope source = %q, want %q", got, ResourceServers)
	}
}

// Reads, moderation and elevation are separate grants. A moderator who holds
// players:moderate must not inherit operator or bulk capability.
func TestPlayerActionsAreNarrowlySeparated(t *testing.T) {
	mustDiffer := [][2]string{
		{ActionModerate, ActionModerateOp},
		{ActionModerate, ActionBulk},
		{ActionAccessRead, ActionAccessWrite},
		{ActionRead, ActionInventory},
	}
	for _, pair := range mustDiffer {
		if pair[0] == pair[1] {
			t.Fatalf("player actions collapsed: %s", pair[0])
		}
	}
}

// Default low-privilege roles must not gain any player capability. Admin is
// intentionally excluded because its "*" policy is the break-glass grant.
func TestDefaultRolesHaveNoPlayerPermissions(t *testing.T) {
	for _, role := range []string{"user", "anonymous"} {
		for _, policy := range DefaultRolePolicies()[role] {
			if len(policy) < 3 {
				continue
			}
			if policy[1] == "*" && policy[2] == "*" {
				t.Fatalf("default role %q has wildcard policy %v", role, policy)
			}
			if policy[1] == ResourcePlayers {
				t.Fatalf("default role %q granted players:%s", role, policy[2])
			}
		}
	}
}

// The legacy SendCommand path must stay mapped to servers:command with "id":
// moderation is not permitted to fall back to that un-audited string path.
func TestLegacyCommandPathIsNotAPlayerPermission(t *testing.T) {
	perm, ok := ProcedurePermissions["/carbonpanel.v1.ServerService/SendCommand"]
	if !ok {
		t.Fatal("SendCommand has no permission mapping")
	}
	if perm.Resource != ResourceServers || perm.Action != ActionCommand || perm.ObjectIDField != "id" {
		t.Fatalf("SendCommand mapping = %+v, want servers/command with id scope", perm)
	}
}
