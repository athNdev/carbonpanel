// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane: negative argument-mapping evidence.
//
// Positive typed-field assertions prove good arguments map exactly. These
// fixtures prove the inverse: a wrong field name, a missing required object
// id, a conflicting/ambiguous input or an out-of-range bound is REFUSED
// before any typed RPC is sent, and no controller handler is reached. If a
// mapping regresses into a silent default, these tests fail.
package mcp

import (
	"context"
	"encoding/json"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type negativeMappingCase struct {
	name  string
	tool  string
	args  map[string]any
	kind  string
	procedure string
}

func TestIntegrationNegativeArgumentMapping(t *testing.T) {
	f := newFixture(t, "", true)
	cases := []negativeMappingCase{
		// Wrong field name: a server id under the wrong key must not be
		// silently mapped to the exact-id field.
		{"wrong-field-server-id", "get_server", map[string]any{"server_id": "srv-1"}, ErrKindInvalidArguments, "/carbonpanel.v1.ServerService/GetServer"},
		{"wrong-field-world-id", "get_world_entry", map[string]any{"world_entry_id": "we-1"}, ErrKindInvalidArguments, "/carbonpanel.v1.WorldService/GetWorldEntry"},
		// Missing required object ids.
		{"missing-server-id", "list_backups", map[string]any{}, ErrKindInvalidArguments, "/carbonpanel.v1.BackupService/ListBackups"},
		{"missing-player-id", "get_player", map[string]any{"server_id": "srv-1"}, ErrKindInvalidArguments, "/carbonpanel.v1.PlayerService/GetPlayer"},
		{"missing-identity-key", "player_service_ban_player", map[string]any{"server_id": "srv-1", "confirm": true}, ErrKindInvalidArguments, "/carbonpanel.v1.PlayerService/BanPlayer"},
		// Conflicting / ambiguous identity inputs must not be accepted as a
		// different single target.
		{"conflicting-world-ref", "get_world_view_status", map[string]any{
			"server_id": "srv-1", "binding_id": "b-1", "snapshot_id": "s-1",
		}, ErrKindInvalidArguments, "/carbonpanel.v1.WorldViewService/GetWorldViewStatus"},
		{"empty-binding-and-level", "get_world_binding", map[string]any{"server_id": "srv-1", "level_name": ""}, ErrKindInvalidArguments, "/carbonpanel.v1.WorldService/GetBinding"},
		// Bounds and enum values must be rejected in Go, not silently capped.
		{"limit-above-cap", "list_players", map[string]any{"server_id": "srv-1", "limit": 101}, ErrKindInvalidArguments, "/carbonpanel.v1.PlayerService/ListPlayers"},
		{"unknown-mod-loader", "create_server", map[string]any{
			"name": "srv", "mc_version": "1.21.4", "mod_loader": "not-a-loader", "confirm": true,
		}, ErrKindInvalidArguments, "/carbonpanel.v1.ServerService/CreateServer"},
		{"operator-level-out-of-range", "player_service_set_operator", map[string]any{
			"server_id": "srv-1", "identity_key": "p-1", "level": 9, "confirm": true,
		}, ErrKindInvalidArguments, "/carbonpanel.v1.PlayerService/SetOperator"},
		{"invalid-chunk-data", "upload_chunk", map[string]any{
			"session_id": "up-1", "chunk_index": 0, "data": "not-base64!!", "confirm": true,
		}, ErrKindInvalidArguments, "/carbonpanel.v1.UploadService/UploadChunk"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := len(invocationRecorder.snapshot())
			// A bad argument is refused either by the MCP SDK JSON-schema gate
			// (protocol error, no structured content) or by the tool's Go
			// validation (structured error). Both are valid refusals; a silent
			// success is not.
			res, err := f.sessions["admin"].CallTool(context.Background(), &sdkmcp.CallToolParams{
				Name: tc.tool, Arguments: tc.args,
			})
			if err != nil {
				t.Errorf("%s: CallTool protocol error: %v", tc.name, err)
				return
			}
			if res.StructuredContent != nil {
				raw, _ := json.Marshal(res.StructuredContent)
				var out ToolOutput
				if jsonErr := json.Unmarshal(raw, &out); jsonErr != nil {
					t.Fatalf("%s: structured content: %v", tc.name, jsonErr)
				}
				_ = assertStructuredError(t, out, tc.tool, tc.kind)
			} else if !res.IsError {
				t.Errorf("%s: bad arguments were accepted without a refusal: %+v", tc.name, res)
			}
			// The refusal must happen before the typed method is reached.
			records := invocationRecorder.snapshot()[before:]
			for _, rec := range records {
				if rec.Procedure == tc.procedure {
					t.Errorf("%s: rejected input still reached typed method %s", tc.name, tc.procedure)
				}
			}
		})
	}
}
