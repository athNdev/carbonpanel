// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import "fmt"

// Bounds are enforced in Go as well as described in the tool schema, so a
// client that ignores the schema cannot ask for an unbounded roster, file or
// page of results.
const (
	maxIDLen     = 128
	maxPathLen   = 512
	maxQueryLen  = 256
	maxLimit     = 100
	defaultLimit = 20
	maxFileBytes = 256 * 1024
)

type emptyArgs struct{}

type fullStatsArgs struct {
	FullStats bool `json:"full_stats,omitempty" jsonschema:"include full server statistics"`
}

type idArgs struct {
	ID string `json:"id" jsonschema:"exact resource id, 1 to 128 characters"`
}

type lifecycleArgs struct {
	ID        string `json:"id" jsonschema:"exact server id, 1 to 128 characters"`
	RequestID string `json:"request_id,omitempty" jsonschema:"idempotency key, at most 128 characters"`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"must be true to perform this mutation"`
}

func (a lifecycleArgs) Confirmation() bool { return a.Confirm }

type confirmIDArgs struct {
	ID      string `json:"id" jsonschema:"exact backup id, 1 to 128 characters"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to restore a backup"`
}

func (a confirmIDArgs) Confirmation() bool { return a.Confirm }

type serverArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
}

type serverLimitArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	Limit    int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type listPlayersArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	Query    string `json:"query,omitempty" jsonschema:"optional identity filter, at most 256 characters"`
	Limit    int32  `json:"limit,omitempty" jsonschema:"roster limit from 1 to 100, default 20"`
}

type playerArgs struct {
	ServerID    string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	IdentityKey string `json:"identity_key" jsonschema:"exact player identity key, 1 to 128 characters"`
}

type playerSkinArgs struct {
	ServerID    string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	IdentityKey string `json:"identity_key" jsonschema:"exact player identity key, 1 to 128 characters"`
	SkinRef     string `json:"skin_ref" jsonschema:"opaque skin cache key, never a URL or path"`
	Kind        string `json:"kind,omitempty" jsonschema:"optional skin kind, at most 32 characters"`
}

type worldQueryArgs struct {
	Query   string `json:"query,omitempty" jsonschema:"optional text filter, at most 256 characters"`
	Edition string `json:"edition,omitempty" jsonschema:"optional edition filter, at most 32 characters"`
	Limit   int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type idLimitArgs struct {
	ID    string `json:"id" jsonschema:"exact world entry id, 1 to 128 characters"`
	Limit int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type listBindingsArgs struct {
	ServerID   string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	RevisionID string `json:"revision_id,omitempty" jsonschema:"optional revision filter, at most 128 characters"`
}

type serverLevelArgs struct {
	ServerID  string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	LevelName string `json:"level_name" jsonschema:"exact configured level name, 1 to 128 characters"`
}

type compatArgs struct {
	RevisionID      string `json:"revision_id" jsonschema:"exact revision id, 1 to 128 characters"`
	ServerID        string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	TargetMCVersion string `json:"target_mc_version,omitempty" jsonschema:"optional Minecraft version, at most 32 characters"`
}

type worldConfigArgs struct {
	Edition     string `json:"edition,omitempty" jsonschema:"edition, at most 32 characters"`
	MCVersion   string `json:"mc_version,omitempty" jsonschema:"Minecraft version, at most 32 characters"`
	ModLoader   string `json:"mod_loader,omitempty" jsonschema:"mod loader name from the proto enum"`
	DataVersion string `json:"data_version,omitempty" jsonschema:"data version, at most 32 characters"`
}

type previewConfigArgs struct {
	Edition     string            `json:"edition,omitempty" jsonschema:"edition, at most 32 characters"`
	MCVersion   string            `json:"mc_version,omitempty" jsonschema:"Minecraft version, at most 32 characters"`
	ModLoader   string            `json:"mod_loader,omitempty" jsonschema:"mod loader name from the proto enum"`
	DataVersion string            `json:"data_version,omitempty" jsonschema:"data version, at most 32 characters"`
	Values      map[string]string `json:"values,omitempty" jsonschema:"at most 32 draft values"`
	Preset      string            `json:"preset,omitempty" jsonschema:"optional preset name, at most 64 characters"`
}

type searchArtifactsArgs struct {
	Provider    string `json:"provider,omitempty" jsonschema:"provider name, at most 32 characters"`
	Query       string `json:"query,omitempty" jsonschema:"search query, at most 256 characters"`
	MCVersion   string `json:"mc_version,omitempty" jsonschema:"Minecraft version, at most 32 characters"`
	ModLoader   string `json:"mod_loader,omitempty" jsonschema:"mod loader name from the proto enum"`
	ProjectType string `json:"project_type,omitempty" jsonschema:"artifact type, at most 32 characters"`
	Limit       int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
	Cursor      string `json:"cursor,omitempty" jsonschema:"opaque cursor, at most 256 characters"`
}

type worldviewArgs struct {
	ServerID     string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	WorldRefKind string `json:"world_ref_kind,omitempty" jsonschema:"kind from the WorldRef enum"`
	LevelName    string `json:"level_name,omitempty" jsonschema:"server-local level name, at most 128 characters"`
	BindingID    string `json:"binding_id,omitempty" jsonschema:"binding id, at most 128 characters"`
	SnapshotID   string `json:"snapshot_id,omitempty" jsonschema:"snapshot id, at most 128 characters"`
	Dimension    string `json:"dimension,omitempty" jsonschema:"dimension name, at most 64 characters"`
}

type listFilesArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	Path     string `json:"path,omitempty" jsonschema:"server-relative directory path, at most 512 characters"`
	Tree     bool   `json:"tree,omitempty" jsonschema:"include descendants"`
}

type fileArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	Path     string `json:"path" jsonschema:"server-relative file path, 1 to 512 characters"`
}

func invalidArgument(tool, field, rule string) error {
	return &ToolError{
		Kind:      ErrKindInvalidArguments,
		Tool:      tool,
		Procedure: toolProcedure(tool),
		Message:   fmt.Sprintf("%s for tool %q: %s", field, tool, rule),
	}
}

func toolProcedure(tool string) string {
	if e, ok := Declared(tool); ok {
		return e.Procedure
	}
	return ""
}

func requireID(tool, field, value string) error {
	if len(value) == 0 || len(value) > maxIDLen {
		return invalidArgument(tool, field, fmt.Sprintf("must be 1 to %d characters", maxIDLen))
	}
	return nil
}

func optionalID(tool, field, value string) error {
	if value != "" && len(value) > maxIDLen {
		return invalidArgument(tool, field, fmt.Sprintf("must be at most %d characters", maxIDLen))
	}
	return nil
}

func requireText(tool, field, value string, max int) error {
	if len(value) > max {
		return invalidArgument(tool, field, fmt.Sprintf("must be at most %d characters", max))
	}
	return nil
}

func limitOr(tool string, limit int32) (int32, error) {
	if limit == 0 {
		return defaultLimit, nil
	}
	if limit < 1 || limit > maxLimit {
		return 0, invalidArgument(tool, "limit", fmt.Sprintf("must be between 1 and %d", maxLimit))
	}
	return limit, nil
}

// validatePath rejects absolute paths and traversal segments before they
// reach the controller. The service still applies its own containment check.
func validatePath(tool, path string, required bool) error {
	if path == "" {
		if required {
			return invalidArgument(tool, "path", "a server-relative file path is required")
		}
		return nil
	}
	if len(path) > maxPathLen {
		return invalidArgument(tool, "path", fmt.Sprintf("must be at most %d characters", maxPathLen))
	}
	if path[0] == '/' || path[0] == '\\' {
		return invalidArgument(tool, "path", "must be server-relative, not absolute")
	}
	for _, part := range splitPathParts(path) {
		if part == ".." {
			return invalidArgument(tool, "path", "must not contain parent directory segments")
		}
	}
	return nil
}

func splitPathParts(path string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(path); i++ {
		if path[i] == '/' || path[i] == '\\' {
			parts = append(parts, path[start:i])
			start = i + 1
		}
	}
	return append(parts, path[start:])
}
