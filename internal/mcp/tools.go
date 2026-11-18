// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"strings"

	"connectrpc.com/connect"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var toolSpecs = func() map[string]ToolSpec {
	m := make(map[string]ToolSpec, len(exposedTools))
	for _, s := range exposedTools {
		m[s.Name] = s
	}
	return m
}()

func specFor(name string) ToolSpec { return toolSpecs[name] }

// addTool binds a typed tool handler. Confirmation is checked before the RPC
// is attempted, and every failure is converted to a structured ToolError.
func addTool[In any](s *sdkmcp.Server, name string, clients *Clients, run func(context.Context, *Clients, In) (any, error)) {
	spec := toolSpecs[name]
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: spec.Name, Description: spec.Description},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, ToolOutput, error) {
			if spec.Confirm {
				if c, ok := any(in).(interface{ Confirmation() bool }); !ok || !c.Confirmation() {
					return failure(spec, &ToolError{
						Kind:      ErrKindConfirmationRequired,
						Tool:      spec.Name,
						Procedure: spec.Procedure,
						Message:   spec.Name + " is a mutation: set confirm=true with the exact target id to proceed",
					})
				}
			}
			result, err := run(ctx, clients, in)
			if err != nil {
				return failure(spec, err)
			}
			return nil, ToolOutput{Status: "ok", Result: result}, nil
		})
}

func failure(spec ToolSpec, err error) (*sdkmcp.CallToolResult, ToolOutput, error) {
	te := toToolError(spec, err)
	out := ToolOutput{Status: "error", Error: &te}
	return &sdkmcp.CallToolResult{
		IsError: true,
		Content: []sdkmcp.Content{&sdkmcp.TextContent{Text: te.Message}},
	}, out, nil
}

func invoke[Req, Res any](ctx context.Context,
	fn func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error), req Req) (any, error) {
	res, err := fn(ctx, connect.NewRequest(&req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func modLoader(tool, value string) (v1.ModLoader, error) {
	if value == "" {
		return v1.ModLoader_MOD_LOADER_UNSPECIFIED, nil
	}
	n, ok := v1.ModLoader_value[strings.ToUpper(value)]
	if !ok {
		return 0, invalidArgument(tool, "mod_loader", "unknown mod loader name")
	}
	return v1.ModLoader(n), nil
}

func worldRef(tool string, in worldviewArgs) (*v1.WorldRef, error) {
	kind := v1.WorldRef_KIND_UNSPECIFIED
	if in.WorldRefKind != "" {
		n, ok := v1.WorldRef_Kind_value[strings.ToUpper(in.WorldRefKind)]
		if !ok {
			return nil, invalidArgument(tool, "world_ref_kind", "unknown WorldRef kind")
		}
		kind = v1.WorldRef_Kind(n)
	}
	if err := optionalID(tool, "level_name", in.LevelName); err != nil {
		return nil, err
	}
	if err := optionalID(tool, "binding_id", in.BindingID); err != nil {
		return nil, err
	}
	if err := optionalID(tool, "snapshot_id", in.SnapshotID); err != nil {
		return nil, err
	}
	return &v1.WorldRef{
		Kind:       kind,
		ServerId:   in.ServerID,
		LevelName:  in.LevelName,
		BindingId:  in.BindingID,
		SnapshotId: in.SnapshotID,
	}, nil
}

type fileResult struct {
	Content   []byte `json:"content"`
	MimeType  string `json:"mime_type"`
	Truncated bool   `json:"truncated"`
}

func registerTools(s *sdkmcp.Server, c *Clients) {
	// --- Server lifecycle -------------------------------------------------
	addTool(s, "list_servers", c, func(ctx context.Context, cl *Clients, in fullStatsArgs) (any, error) {
		return invoke(ctx, cl.Server.ListServers, v1.ListServersRequest{FullStats: in.FullStats})
	})
	addTool(s, "get_server", c, func(ctx context.Context, cl *Clients, in idArgs) (any, error) {
		if err := requireID("get_server", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.GetServer, v1.GetServerRequest{Id: in.ID})
	})
	addTool(s, "start_server", c, func(ctx context.Context, cl *Clients, in lifecycleArgs) (any, error) {
		if err := requireID("start_server", "id", in.ID); err != nil {
			return nil, err
		}
		if err := optionalID("start_server", "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.StartServer, v1.StartServerRequest{Id: in.ID, RequestId: in.RequestID})
	})
	addTool(s, "stop_server", c, func(ctx context.Context, cl *Clients, in lifecycleArgs) (any, error) {
		if err := requireID("stop_server", "id", in.ID); err != nil {
			return nil, err
		}
		if err := optionalID("stop_server", "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.StopServer, v1.StopServerRequest{Id: in.ID, RequestId: in.RequestID})
	})
	addTool(s, "restart_server", c, func(ctx context.Context, cl *Clients, in lifecycleArgs) (any, error) {
		if err := requireID("restart_server", "id", in.ID); err != nil {
			return nil, err
		}
		if err := optionalID("restart_server", "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.RestartServer, v1.RestartServerRequest{Id: in.ID, RequestId: in.RequestID})
	})

	// --- Player lookup ----------------------------------------------------
	addTool(s, "list_players", c, func(ctx context.Context, cl *Clients, in listPlayersArgs) (any, error) {
		if err := requireID("list_players", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireText("list_players", "query", in.Query, maxQueryLen); err != nil {
			return nil, err
		}
		limit, err := limitOr("list_players", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Player.ListPlayers, v1.ListPlayersRequest{ServerId: in.ServerID, Query: in.Query, Limit: limit})
	})
	addTool(s, "get_player", c, func(ctx context.Context, cl *Clients, in playerArgs) (any, error) {
		if err := requireID("get_player", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("get_player", "identity_key", in.IdentityKey); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Player.GetPlayer, v1.GetPlayerRequest{ServerId: in.ServerID, IdentityKey: in.IdentityKey})
	})
	addTool(s, "list_player_capabilities", c, func(ctx context.Context, cl *Clients, in serverArgs) (any, error) {
		if err := requireID("list_player_capabilities", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Player.ListPlayerCapabilities, v1.ListPlayerCapabilitiesRequest{ServerId: in.ServerID})
	})
	addTool(s, "get_player_skin", c, func(ctx context.Context, cl *Clients, in playerSkinArgs) (any, error) {
		if err := requireID("get_player_skin", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("get_player_skin", "identity_key", in.IdentityKey); err != nil {
			return nil, err
		}
		if err := requireID("get_player_skin", "skin_ref", in.SkinRef); err != nil {
			return nil, err
		}
		if err := requireText("get_player_skin", "kind", in.Kind, 32); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Player.GetPlayerSkin, v1.GetPlayerSkinRequest{
			ServerId: in.ServerID, IdentityKey: in.IdentityKey, SkinRef: in.SkinRef, Kind: in.Kind,
		})
	})

	// --- World library and bindings ---------------------------------------
	addTool(s, "list_world_entries", c, func(ctx context.Context, cl *Clients, in worldQueryArgs) (any, error) {
		if err := requireText("list_world_entries", "query", in.Query, maxQueryLen); err != nil {
			return nil, err
		}
		if err := requireText("list_world_entries", "edition", in.Edition, 32); err != nil {
			return nil, err
		}
		limit, err := limitOr("list_world_entries", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.ListWorldEntries, v1.ListWorldEntriesRequest{Query: in.Query, Edition: in.Edition, Limit: limit})
	})
	addTool(s, "get_world_entry", c, func(ctx context.Context, cl *Clients, in idArgs) (any, error) {
		if err := requireID("get_world_entry", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.GetWorldEntry, v1.GetWorldEntryRequest{WorldEntryId: in.ID})
	})
	addTool(s, "list_world_revisions", c, func(ctx context.Context, cl *Clients, in idArgs) (any, error) {
		if err := requireID("list_world_revisions", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.ListRevisions, v1.ListRevisionsRequest{WorldEntryId: in.ID})
	})
	addTool(s, "get_world_revision", c, func(ctx context.Context, cl *Clients, in idArgs) (any, error) {
		if err := requireID("get_world_revision", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.GetRevision, v1.GetRevisionRequest{RevisionId: in.ID})
	})
	addTool(s, "list_world_bindings", c, func(ctx context.Context, cl *Clients, in listBindingsArgs) (any, error) {
		if err := requireID("list_world_bindings", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := optionalID("list_world_bindings", "revision_id", in.RevisionID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.ListBindings, v1.ListBindingsRequest{ServerId: in.ServerID, RevisionId: in.RevisionID})
	})
	addTool(s, "get_world_binding", c, func(ctx context.Context, cl *Clients, in serverLevelArgs) (any, error) {
		if err := requireID("get_world_binding", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("get_world_binding", "level_name", in.LevelName); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.GetBinding, v1.GetBindingRequest{ServerId: in.ServerID, LevelName: in.LevelName})
	})
	addTool(s, "list_import_jobs", c, func(ctx context.Context, cl *Clients, in idLimitArgs) (any, error) {
		if err := requireID("list_import_jobs", "id", in.ID); err != nil {
			return nil, err
		}
		limit, err := limitOr("list_import_jobs", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.ListImportJobs, v1.ListImportJobsRequest{WorldEntryId: in.ID, Limit: limit})
	})
	addTool(s, "get_import_job", c, func(ctx context.Context, cl *Clients, in idArgs) (any, error) {
		if err := requireID("get_import_job", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.GetImportJob, v1.GetImportJobRequest{JobId: in.ID})
	})
	addTool(s, "check_world_compatibility", c, func(ctx context.Context, cl *Clients, in compatArgs) (any, error) {
		if err := requireID("check_world_compatibility", "revision_id", in.RevisionID); err != nil {
			return nil, err
		}
		if err := requireID("check_world_compatibility", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireText("check_world_compatibility", "target_mc_version", in.TargetMCVersion, 32); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.CheckCompatibility, v1.CheckCompatibilityRequest{
			RevisionId: in.RevisionID, ServerId: in.ServerID, TargetMcVersion: in.TargetMCVersion,
		})
	})
	addTool(s, "get_world_config_schema", c, func(ctx context.Context, cl *Clients, in worldConfigArgs) (any, error) {
		loader, err := modLoader("get_world_config_schema", in.ModLoader)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.GetWorldConfigSchema, v1.GetWorldConfigSchemaRequest{
			Edition: in.Edition, McVersion: in.MCVersion, ModLoader: loader, DataVersion: in.DataVersion,
		})
	})
	addTool(s, "preview_world_config", c, func(ctx context.Context, cl *Clients, in previewConfigArgs) (any, error) {
		loader, err := modLoader("preview_world_config", in.ModLoader)
		if err != nil {
			return nil, err
		}
		if len(in.Values) > 32 {
			return nil, invalidArgument("preview_world_config", "values", "at most 32 draft values")
		}
		return invoke(ctx, cl.World.PreviewWorldConfig, v1.PreviewWorldConfigRequest{
			Edition: in.Edition, McVersion: in.MCVersion, ModLoader: loader,
			DataVersion: in.DataVersion, Values: in.Values, Preset: in.Preset,
		})
	})
	addTool(s, "search_world_artifacts", c, func(ctx context.Context, cl *Clients, in searchArtifactsArgs) (any, error) {
		loader, err := modLoader("search_world_artifacts", in.ModLoader)
		if err != nil {
			return nil, err
		}
		limit, err := limitOr("search_world_artifacts", in.Limit)
		if err != nil {
			return nil, err
		}
		if err := requireText("search_world_artifacts", "query", in.Query, maxQueryLen); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.World.SearchWorldArtifacts, v1.SearchWorldArtifactsRequest{
			Provider: in.Provider, Query: in.Query, McVersion: in.MCVersion,
			ModLoader: loader, ProjectType: in.ProjectType, Limit: limit, Cursor: in.Cursor,
		})
	})
	addTool(s, "get_provider_capabilities", c, func(ctx context.Context, cl *Clients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.World.GetProviderCapabilities, v1.GetProviderCapabilitiesRequest{})
	})

	// --- World view (observation only) ------------------------------------
	addTool(s, "list_world_view_targets", c, func(ctx context.Context, cl *Clients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.WorldView.ListWorldViewTargets, v1.ListWorldViewTargetsRequest{})
	})
	addTool(s, "get_world_view_status", c, func(ctx context.Context, cl *Clients, in worldviewArgs) (any, error) {
		if err := requireID("get_world_view_status", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		ref, err := worldRef("get_world_view_status", in)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.WorldView.GetWorldViewStatus, v1.GetWorldViewStatusRequest{ServerId: in.ServerID, WorldRef: ref})
	})
	addTool(s, "list_world_view_capabilities", c, func(ctx context.Context, cl *Clients, in serverArgs) (any, error) {
		if err := requireID("list_world_view_capabilities", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.WorldView.ListCapabilities, v1.ListCapabilitiesRequest{ServerId: in.ServerID})
	})
	addTool(s, "list_map_overlays", c, func(ctx context.Context, cl *Clients, in worldviewArgs) (any, error) {
		if err := requireID("list_map_overlays", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireText("list_map_overlays", "dimension", in.Dimension, 64); err != nil {
			return nil, err
		}
		ref, err := worldRef("list_map_overlays", in)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.WorldView.ListMapOverlays, v1.ListMapOverlaysRequest{
			ServerId: in.ServerID, WorldRef: ref, Dimension: in.Dimension,
		})
	})

	// --- Backup ------------------------------------------------------------
	addTool(s, "list_backups", c, func(ctx context.Context, cl *Clients, in serverLimitArgs) (any, error) {
		if err := requireID("list_backups", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		limit, err := limitOr("list_backups", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Backup.ListBackups, v1.ListBackupsRequest{ServerId: in.ServerID, Limit: limit})
	})
	addTool(s, "restore_backup", c, func(ctx context.Context, cl *Clients, in confirmIDArgs) (any, error) {
		if err := requireID("restore_backup", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Backup.RestoreBackup, v1.RestoreBackupRequest{Id: in.ID})
	})

	// --- File browse/read (no writes) -------------------------------------
	addTool(s, "list_files", c, func(ctx context.Context, cl *Clients, in listFilesArgs) (any, error) {
		if err := requireID("list_files", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("list_files", in.Path, false); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.File.ListFiles, v1.ListFilesRequest{ServerId: in.ServerID, Path: in.Path, Tree: in.Tree})
	})
	addTool(s, "get_file", c, func(ctx context.Context, cl *Clients, in fileArgs) (any, error) {
		if err := requireID("get_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("get_file", in.Path, true); err != nil {
			return nil, err
		}
		res, err := cl.File.GetFile(ctx, connect.NewRequest(&v1.GetFileRequest{ServerId: in.ServerID, Path: in.Path}))
		if err != nil {
			return nil, err
		}
		content := res.Msg.GetContent()
		truncated := len(content) > maxFileBytes
		if truncated {
			content = content[:maxFileBytes]
		}
		return fileResult{Content: content, MimeType: res.Msg.GetMimeType(), Truncated: truncated}, nil
	})
}
