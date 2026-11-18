// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370).
//
// This file adds only the 22 SUPPORTED operations that no family head ships a
// tool for, as asserted by internal/mcp/family_audit_matrix.json. It never
// edits a family file. Every entry is a ClassReview mutation with an explicit
// confirm gate, a typed generated Connect client from the shared Clients
// struct, and Go-side argument bounds. The exact source reason for each audit
// decision remains in the audit matrix; this file only supplies the missing
// callable path and its scoped fixture.
package mcp

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// integrationToolDecision is one audit-backed tool addition. These are the
// 22 SUPPORTED procedures absent from every family head's reachable set.
type integrationToolDecision struct {
	Procedure   string
	Tool        string
	Description string
}

var integrationToolDecisions = []integrationToolDecision{
	// AuthService: the matrix classifies Logout as a supported self-service
	// operation with a typed client; the admin family excluded browser-session
	// mechanics. This tool uses the API-token interceptor, not a session token.
	{"/carbonpanel.v1.AuthService/Logout", "auth_service_logout",
		"Request session logout for the presented bearer credential. This does not revoke a dp_ API token: the controller reports success but the token stays valid; revoke tokens out of band. Requires confirm=true."},

	// BackupService: bounded, server-scoped backup lifecycle.
	{"/carbonpanel.v1.BackupService/CreateBackup", "backup_service_create_backup",
		"Create one bounded, server-scoped backup. Requires confirm=true and an exact server id."},
	{"/carbonpanel.v1.BackupService/DeleteBackup", "backup_service_delete_backup",
		"Delete one backup by exact id. Destructive: requires confirm=true."},
	{"/carbonpanel.v1.BackupService/SetBackupLocked", "backup_service_set_backup_locked",
		"Set or clear the retention lock on one backup by exact id. Requires confirm=true."},

	// PlayerService: typed moderation/data reads replacing free-form console
	// commands (the audit's documented equivalent flow for SendCommand).
	// OSS-383: read-only exact-identity lookup. It never persists an identity.
	{"/carbonpanel.v1.PlayerService/LookupPlayer", "player_service_lookup_player",
		"Resolve one previously unseen account by exact UUID or current username. Read-only: FOUND/NOT_FOUND/AMBIGUOUS/UNAVAILABLE are explicit and no identity is persisted."},
	{"/carbonpanel.v1.PlayerService/BanPlayer", "player_service_ban_player",
		"Ban one player on one server by exact identity key. Requires confirm=true and a bounded reason."},
	{"/carbonpanel.v1.PlayerService/BulkPlayerAction", "player_service_bulk_player_action",
		"Apply one bounded typed action to a small list of exact identity keys. Requires confirm=true."},
	{"/carbonpanel.v1.PlayerService/GetPlayerDeepData", "player_service_get_player_deep_data",
		"Read bounded deep data for one exact player identity. Requires confirm=true and an exact server id."},
	{"/carbonpanel.v1.PlayerService/KickPlayer", "player_service_kick_player",
		"Kick one player on one server by exact identity key. Requires confirm=true and a bounded reason."},
	{"/carbonpanel.v1.PlayerService/ListPlayerAccess", "player_service_list_player_access",
		"List bounded access records for one exact player identity. Requires confirm=true."},
	{"/carbonpanel.v1.PlayerService/ListPlayerHistory", "player_service_list_player_history",
		"List bounded history for one exact player identity. Requires confirm=true and a limit of 1 to 100."},
	{"/carbonpanel.v1.PlayerService/ListPlayerSessions", "player_service_list_player_sessions",
		"List bounded sessions for one exact player identity. Requires confirm=true."},
	{"/carbonpanel.v1.PlayerService/PardonPlayer", "player_service_pardon_player",
		"Pardon one player on one server by exact identity key. Requires confirm=true."},
	{"/carbonpanel.v1.PlayerService/RefreshPlayerSkin", "player_service_refresh_player_skin",
		"Refresh the cached skin for one exact player identity. Requires confirm=true."},
	{"/carbonpanel.v1.PlayerService/SetOperator", "player_service_set_operator",
		"Grant or remove operator level for one exact player identity. Requires confirm=true; level is 0 to 4."},
	{"/carbonpanel.v1.PlayerService/SetWhitelistEntry", "player_service_set_whitelist_entry",
		"Add or remove one whitelist entry by exact identity key. Requires confirm=true."},
	{"/carbonpanel.v1.PlayerService/UndoPlayerAction", "player_service_undo_player_action",
		"Undo one prior typed player action by exact undo token. Requires confirm=true."},

	// WorldService: staged, bidirectionally scoped world workflows.
	{"/carbonpanel.v1.WorldService/BindWorld", "world_service_bind_world",
		"Bind one immutable world revision to one exact server level. Requires confirm=true."},
	{"/carbonpanel.v1.WorldService/CreateWorldFromConfig", "world_service_create_world_from_config",
		"Create one world entry from a bounded config draft. Requires confirm=true; at most 32 draft values."},
	{"/carbonpanel.v1.WorldService/ImportWorld", "world_service_import_world",
		"Import one world from a bounded source handle. Requires confirm=true; never accepts a raw URL as a path."},
	{"/carbonpanel.v1.WorldService/ResolveWorldOperation", "world_service_resolve_world_operation",
		"Approve or reject one staged world operation by exact id. Requires confirm=true."},
	{"/carbonpanel.v1.WorldService/UnbindWorld", "world_service_unbind_world",
		"Unbind one exact server level. Requires confirm=true."},

	// WorldViewService: typed overlay mutation replacing the raw map proxy,
	// plus the bounded session handle used by the supported viewer workflow.
	{"/carbonpanel.v1.WorldViewService/SetMapOverlay", "world_view_service_set_map_overlay",
		"Create or update one bounded map overlay on one exact server. Requires confirm=true."},
	{"/carbonpanel.v1.WorldViewService/GetWorldViewSession", "world_view_service_get_world_view_session",
		"Get the bounded pollable session for one exact server-local world. Requires confirm=true."},
}

// IntegrationToolSpecs returns the integration-owned tool declarations. The
// catalog generator consumes these together with the first-slice and family
// descriptors; order is stable for reproducible generation.
func IntegrationToolSpecs() []ToolSpec {
	out := make([]ToolSpec, 0, len(integrationToolDecisions))
	for _, d := range integrationToolDecisions {
		class, confirm := ClassReview, true
		// OSS-383 LookupPlayer is explicitly read-only and ephemeral.
		if d.Procedure == "/carbonpanel.v1.PlayerService/LookupPlayer" {
			class, confirm = ClassSafe, false
		}
		out = append(out, ToolSpec{
			Name:        d.Tool,
			Procedure:   d.Procedure,
			Class:       class,
			Confirm:     confirm,
			Description: d.Description,
		})
	}
	return out
}

// integrationArgs is the single bounded argument envelope for the 22 additions.
// Every field is optional except those the individual handler requires, and
// all string fields are capped before they reach a generated request.
type integrationArgs struct {
	Confirm            bool              `json:"confirm,omitempty" jsonschema:"must be true to perform this mutation"`
	ID                 string            `json:"id,omitempty" jsonschema:"exact resource id, at most 128 characters"`
	ServerID           string            `json:"server_id,omitempty" jsonschema:"exact server id, at most 128 characters"`
	IdentityKey        string            `json:"identity_key,omitempty" jsonschema:"exact player identity key, at most 128 characters"`
	PlayerName         string            `json:"player_name,omitempty" jsonschema:"player name, at most 128 characters"`
	Query              string            `json:"query,omitempty" jsonschema:"exact canonical UUID or Minecraft username, 1 to 36 characters"`
	PlayerQuery        string            `json:"player_query,omitempty" jsonschema:"exact name or UUID for a manual allowlist add, at most 36 characters"`
	Reason             string            `json:"reason,omitempty" jsonschema:"bounded reason, at most 256 characters"`
	RequestID          string            `json:"request_id,omitempty" jsonschema:"idempotency key, at most 128 characters"`
	Kind               string            `json:"kind,omitempty" jsonschema:"closed action kind, at most 32 characters"`
	Limit              int32             `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
	BackupName         string            `json:"backup_name,omitempty" jsonschema:"backup name, at most 128 characters"`
	StopForBackup      bool              `json:"stop_for_backup,omitempty" jsonschema:"stop the server before taking the backup"`
	Locked             bool              `json:"locked,omitempty" jsonschema:"set or clear the backup retention lock"`
	Remove             bool              `json:"remove,omitempty" jsonschema:"remove rather than add the entry"`
	Level              int32             `json:"level,omitempty" jsonschema:"operator level from 0 to 4"`
	UndoToken          string            `json:"undo_token,omitempty" jsonschema:"exact undo token, at most 128 characters"`
	Window             string            `json:"window,omitempty" jsonschema:"optional bind window, at most 32 characters"`
	LevelName          string            `json:"level_name,omitempty" jsonschema:"exact server level name, at most 128 characters"`
	RevisionID         string            `json:"revision_id,omitempty" jsonschema:"exact revision id, at most 128 characters"`
	Name               string            `json:"name,omitempty" jsonschema:"world display name, at most 128 characters"`
	Edition            string            `json:"edition,omitempty" jsonschema:"world edition, at most 32 characters"`
	MCVersion          string            `json:"mc_version,omitempty" jsonschema:"Minecraft version, at most 32 characters"`
	ModLoader          string            `json:"mod_loader,omitempty" jsonschema:"mod loader name from the proto enum"`
	DataVersion        string            `json:"data_version,omitempty" jsonschema:"data version, at most 32 characters"`
	Values             map[string]string `json:"values,omitempty" jsonschema:"at most 32 bounded draft values"`
	Preset             string            `json:"preset,omitempty" jsonschema:"optional preset name, at most 64 characters"`
	LicenseAttestation string            `json:"license_attestation,omitempty" jsonschema:"license attestation, at most 128 characters"`
	Source             string            `json:"source,omitempty" jsonschema:"closed import source, at most 32 characters"`
	URL                string            `json:"url,omitempty" jsonschema:"import url, at most 512 characters; never a filesystem path"`
	UploadID           string            `json:"upload_id,omitempty" jsonschema:"opaque upload handle, at most 128 characters"`
	DisplayName        string            `json:"display_name,omitempty" jsonschema:"display name, at most 128 characters"`
	SourcePlatform     string            `json:"source_platform,omitempty" jsonschema:"source platform, at most 32 characters"`
	SHAChecksum        string            `json:"sha256_checksum,omitempty" jsonschema:"sha256 checksum, at most 128 characters"`
	OperationID        string            `json:"operation_id,omitempty" jsonschema:"exact staged operation id, at most 128 characters"`
	Note               string            `json:"note,omitempty" jsonschema:"bounded note, at most 256 characters"`
	Dimension          string            `json:"dimension,omitempty" jsonschema:"dimension name, at most 64 characters"`
	OverlayID          string            `json:"overlay_id,omitempty" jsonschema:"exact overlay id, at most 128 characters"`
	OverlayKind        string            `json:"overlay_kind,omitempty" jsonschema:"overlay kind, at most 32 characters"`
	WorldRefKind       string            `json:"world_ref_kind,omitempty" jsonschema:"kind from the WorldRef enum"`
	BindingID          string            `json:"binding_id,omitempty" jsonschema:"binding id, at most 128 characters"`
	SnapshotID         string            `json:"snapshot_id,omitempty" jsonschema:"snapshot id, at most 128 characters"`
	X                  float64           `json:"x,omitempty" jsonschema:"bounded x coordinate"`
	Z                  float64           `json:"z,omitempty" jsonschema:"bounded z coordinate"`
	Label              string            `json:"label,omitempty" jsonschema:"overlay label, at most 128 characters"`
	Style              string            `json:"style,omitempty" jsonschema:"overlay style, at most 128 characters"`
}

func (a integrationArgs) Confirmation() bool { return a.Confirm }

func integrationInvoke[Req, Res any](ctx context.Context,
	fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), req Req) (any, error) {
	return invoke(ctx, fn, req)
}

func intText(tool, field, value string, max int) error { return requireText(tool, field, value, max) }
func intID(tool, field, value string) error           { return requireID(tool, field, value) }
func intOptID(tool, field, value string) error        { return optionalID(tool, field, value) }

func intLimit(tool string, limit int32) (int32, error) { return limitOr(tool, limit) }

func intLoader(tool, value string) (v1.ModLoader, error) { return modLoader(tool, value) }

// RegisterIntegrationTools binds every integration-owned tool. The confirm
// gate is enforced by addTool before any request is sent.
func RegisterIntegrationTools(s *sdkmcp.Server, c *Clients) {
	for _, d := range integrationToolDecisions {
		d := d
		addTool(s, d.Tool, c, func(ctx context.Context, cl *Clients, in integrationArgs) (any, error) {
			return runIntegrationTool(ctx, cl, d.Procedure, in)
		})
	}
}

func runIntegrationTool(ctx context.Context, cl *Clients, procedure string, in integrationArgs) (any, error) {
	tool := integrationToolName(procedure)
	switch procedure {
	case "/carbonpanel.v1.AuthService/Logout":
		return integrationInvoke(ctx, cl.Auth.Logout, v1.LogoutRequest{})

	case "/carbonpanel.v1.BackupService/CreateBackup":
		if err := intID(tool, "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := intText(tool, "backup_name", in.BackupName, maxIDLen); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Backup.CreateBackup, v1.CreateBackupRequest{
			ServerId: in.ServerID, BackupName: in.BackupName,
			StopForBackup: in.StopForBackup,
		})

	case "/carbonpanel.v1.BackupService/DeleteBackup":
		if err := intID(tool, "id", in.ID); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Backup.DeleteBackup, v1.DeleteBackupRequest{Id: in.ID})

	case "/carbonpanel.v1.BackupService/SetBackupLocked":
		if err := intID(tool, "id", in.ID); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Backup.SetBackupLocked, v1.SetBackupLockedRequest{Id: in.ID, Locked: in.Locked})

	case "/carbonpanel.v1.PlayerService/LookupPlayer":
		if err := intID(tool, "query", in.Query); err != nil {
			return nil, err
		}
		if err := intText(tool, "query", in.Query, 36); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.LookupPlayer, v1.LookupPlayerRequest{Query: in.Query})

	case "/carbonpanel.v1.PlayerService/BanPlayer":
		if err := intPlayerTarget(tool, in, true); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.BanPlayer, v1.BanPlayerRequest{
			ServerId: in.ServerID, IdentityKey: in.IdentityKey, PlayerName: in.PlayerName,
			Reason: in.Reason, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.PlayerService/BulkPlayerAction":
		if err := intID(tool, "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := intText(tool, "kind", in.Kind, 32); err != nil {
			return nil, err
		}
		if len(in.IdentityKey) == 0 || len(in.IdentityKey) > maxIDLen*100 {
			return nil, invalidArgument(tool, "identity_keys", "a bounded comma-separated list is required")
		}
		keys := strings.Split(in.IdentityKey, ",")
		if len(keys) > 100 {
			return nil, invalidArgument(tool, "identity_keys", "at most 100 identity keys")
		}
		for i, key := range keys {
			keys[i] = strings.TrimSpace(key)
			if err := intID(tool, "identity_key", keys[i]); err != nil {
				return nil, err
			}
		}
		return integrationInvoke(ctx, cl.Player.BulkPlayerAction, v1.BulkPlayerActionRequest{
			ServerId: in.ServerID, Kind: in.Kind, IdentityKeys: keys,
			Reason: in.Reason, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.PlayerService/GetPlayerDeepData":
		if err := intPlayerTarget(tool, in, false); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.GetPlayerDeepData, v1.GetPlayerDeepDataRequest{
			IdentityKey: in.IdentityKey, ServerId: in.ServerID, Kind: in.Kind,
		})

	case "/carbonpanel.v1.PlayerService/KickPlayer":
		if err := intPlayerTarget(tool, in, true); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.KickPlayer, v1.KickPlayerRequest{
			ServerId: in.ServerID, IdentityKey: in.IdentityKey, PlayerName: in.PlayerName,
			Reason: in.Reason, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.PlayerService/ListPlayerAccess":
		if err := intPlayerTarget(tool, in, false); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.ListPlayerAccess, v1.ListPlayerAccessRequest{
			IdentityKey: in.IdentityKey, ServerId: in.ServerID,
		})

	case "/carbonpanel.v1.PlayerService/ListPlayerHistory":
		if err := intPlayerTarget(tool, in, false); err != nil {
			return nil, err
		}
		limit, err := intLimit(tool, in.Limit)
		if err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.ListPlayerHistory, v1.ListPlayerHistoryRequest{
			IdentityKey: in.IdentityKey, ServerId: in.ServerID, Limit: limit,
		})

	case "/carbonpanel.v1.PlayerService/ListPlayerSessions":
		if err := intPlayerTarget(tool, in, false); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.ListPlayerSessions, v1.ListPlayerSessionsRequest{
			IdentityKey: in.IdentityKey, ServerId: in.ServerID,
		})

	case "/carbonpanel.v1.PlayerService/PardonPlayer":
		if err := intPlayerTarget(tool, in, true); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.PardonPlayer, v1.PardonPlayerRequest{
			ServerId: in.ServerID, IdentityKey: in.IdentityKey, PlayerName: in.PlayerName,
			RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.PlayerService/RefreshPlayerSkin":
		if err := intPlayerTarget(tool, in, false); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.RefreshPlayerSkin, v1.RefreshPlayerSkinRequest{
			IdentityKey: in.IdentityKey, ServerId: in.ServerID,
		})

	case "/carbonpanel.v1.PlayerService/SetOperator":
		if err := intPlayerTarget(tool, in, true); err != nil {
			return nil, err
		}
		if in.Level < 0 || in.Level > 4 {
			return nil, invalidArgument(tool, "level", "must be between 0 and 4")
		}
		return integrationInvoke(ctx, cl.Player.SetOperator, v1.SetOperatorRequest{
			ServerId: in.ServerID, IdentityKey: in.IdentityKey, PlayerName: in.PlayerName,
			Remove: in.Remove, Level: in.Level, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.PlayerService/SetWhitelistEntry":
		if err := intPlayerTarget(tool, in, true); err != nil {
			return nil, err
		}
		if err := intText(tool, "player_query", in.PlayerQuery, 36); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.SetWhitelistEntry, v1.SetWhitelistEntryRequest{
			ServerId: in.ServerID, IdentityKey: in.IdentityKey, PlayerName: in.PlayerName,
			Remove: in.Remove, RequestId: in.RequestID, PlayerQuery: in.PlayerQuery,
		})

	case "/carbonpanel.v1.PlayerService/UndoPlayerAction":
		if err := intID(tool, "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := intID(tool, "undo_token", in.UndoToken); err != nil {
			return nil, err
		}
		if err := intOptID(tool, "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.Player.UndoPlayerAction, v1.UndoPlayerActionRequest{
			ServerId: in.ServerID, UndoToken: in.UndoToken, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.WorldService/BindWorld":
		if err := intID(tool, "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := intID(tool, "level_name", in.LevelName); err != nil {
			return nil, err
		}
		if err := intID(tool, "revision_id", in.RevisionID); err != nil {
			return nil, err
		}
		if err := intText(tool, "window", in.Window, 32); err != nil {
			return nil, err
		}
		if err := intOptID(tool, "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.World.BindWorld, v1.BindWorldRequest{
			ServerId: in.ServerID, LevelName: in.LevelName, RevisionId: in.RevisionID,
			Window: in.Window, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.WorldService/CreateWorldFromConfig":
		if err := intID(tool, "name", in.Name); err != nil {
			return nil, err
		}
		if err := intText(tool, "edition", in.Edition, 32); err != nil {
			return nil, err
		}
		if err := intText(tool, "mc_version", in.MCVersion, 32); err != nil {
			return nil, err
		}
		loader, err := intLoader(tool, in.ModLoader)
		if err != nil {
			return nil, err
		}
		if len(in.Values) > 32 {
			return nil, invalidArgument(tool, "values", "at most 32 draft values")
		}
		for k, v := range in.Values {
			if err := intText(tool, "values key", k, maxIDLen); err != nil {
				return nil, err
			}
			if err := intText(tool, "values value", v, maxQueryLen); err != nil {
				return nil, err
			}
		}
		if err := intText(tool, "license_attestation", in.LicenseAttestation, maxIDLen); err != nil {
			return nil, err
		}
		if err := intOptID(tool, "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.World.CreateWorldFromConfig, v1.CreateWorldFromConfigRequest{
			Name: in.Name, Edition: in.Edition, McVersion: in.MCVersion, ModLoader: loader,
			DataVersion: in.DataVersion, Values: in.Values, Preset: in.Preset,
			LicenseAttestation: in.LicenseAttestation, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.WorldService/ImportWorld":
		if err := intText(tool, "source", in.Source, 32); err != nil {
			return nil, err
		}
		if in.Source == "" {
			return nil, invalidArgument(tool, "source", "a bounded import source is required")
		}
		if err := intText(tool, "url", in.URL, maxPathLen); err != nil {
			return nil, err
		}
		if err := intOptID(tool, "upload_id", in.UploadID); err != nil {
			return nil, err
		}
		if err := intText(tool, "sha256_checksum", in.SHAChecksum, maxIDLen); err != nil {
			return nil, err
		}
		if err := intOptID(tool, "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.World.ImportWorld, v1.ImportWorldRequest{
			Source: in.Source, Url: in.URL, UploadId: in.UploadID, DisplayName: in.DisplayName,
			SourcePlatform: in.SourcePlatform, LicenseAttestation: in.LicenseAttestation,
			Sha256Checksum: in.SHAChecksum, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.WorldService/ResolveWorldOperation":
		if err := intID(tool, "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := intID(tool, "operation_id", in.OperationID); err != nil {
			return nil, err
		}
		if err := intText(tool, "action", in.Kind, 32); err != nil {
			return nil, err
		}
		if err := intText(tool, "note", in.Note, maxQueryLen); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.World.ResolveWorldOperation, v1.ResolveWorldOperationRequest{
			ServerId: in.ServerID, OperationId: in.OperationID, Action: in.Kind, Note: in.Note,
		})

	case "/carbonpanel.v1.WorldService/UnbindWorld":
		if err := intID(tool, "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := intID(tool, "level_name", in.LevelName); err != nil {
			return nil, err
		}
		if err := intOptID(tool, "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.World.UnbindWorld, v1.UnbindWorldRequest{
			ServerId: in.ServerID, LevelName: in.LevelName, RequestId: in.RequestID,
		})

	case "/carbonpanel.v1.WorldViewService/SetMapOverlay":
		if err := intID(tool, "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := intID(tool, "overlay_id", in.OverlayID); err != nil {
			return nil, err
		}
		if err := intText(tool, "dimension", in.Dimension, 64); err != nil {
			return nil, err
		}
		if err := intText(tool, "overlay_kind", in.OverlayKind, 32); err != nil {
			return nil, err
		}
		if err := intText(tool, "label", in.Label, maxIDLen); err != nil {
			return nil, err
		}
		if err := intText(tool, "style", in.Style, maxIDLen); err != nil {
			return nil, err
		}
		if in.X < -3e7 || in.X > 3e7 || in.Z < -3e7 || in.Z > 3e7 {
			return nil, invalidArgument(tool, "x/z", "coordinates must be within the world bound")
		}
		return integrationInvoke(ctx, cl.WorldView.SetMapOverlay, v1.SetMapOverlayRequest{
			ServerId: in.ServerID,
			Overlay: &v1.MapOverlay{
				Id: in.OverlayID, ServerId: in.ServerID, Dimension: in.Dimension,
				Kind: in.OverlayKind, X: in.X, Z: in.Z, Label: in.Label, Style: in.Style,
			},
		})

	case "/carbonpanel.v1.WorldViewService/GetWorldViewSession":
		if err := intID(tool, "server_id", in.ServerID); err != nil {
			return nil, err
		}
		ref := worldviewArgs{
			WorldRefKind: in.WorldRefKind, LevelName: in.LevelName,
			BindingID: in.BindingID, SnapshotID: in.SnapshotID,
		}
		world, err := worldRef(tool, ref)
		if err != nil {
			return nil, err
		}
		return integrationInvoke(ctx, cl.WorldView.GetWorldViewSession, v1.GetWorldViewSessionRequest{
			ServerId: in.ServerID, WorldRef: world,
		})

	default:
		return nil, invalidArgument(tool, "procedure", "not an integration-owned tool")
	}
}

func intPlayerTarget(tool string, in integrationArgs, name bool) error {
	if err := intID(tool, "server_id", in.ServerID); err != nil {
		return err
	}
	if err := intID(tool, "identity_key", in.IdentityKey); err != nil {
		return err
	}
	if name {
		if err := intText(tool, "player_name", in.PlayerName, maxIDLen); err != nil {
			return err
		}
	}
	if err := intText(tool, "reason", in.Reason, maxQueryLen); err != nil {
		return err
	}
	if err := intOptID(tool, "request_id", in.RequestID); err != nil {
		return err
	}
	return nil
}

func integrationToolName(procedure string) string {
	for _, d := range integrationToolDecisions {
		if d.Procedure == procedure {
			return d.Tool
		}
	}
	return "integration_tool"
}
