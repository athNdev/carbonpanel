// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"

	"connectrpc.com/connect"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

// ListStagedChanges has a filter but no server-side result limit. Bound the
// decoded response before projecting up to maxLimit records into the tool result.
const stagedConfigResponseLimit = 2 << 20

var stagedConfigListSpec = ToolSpec{
	Name:        "list_staged_config_changes",
	Procedure:   carbonpanelv1connect.StagedConfigServiceListStagedChangesProcedure,
	Class:       ClassSafe,
	Description: "List recent staged configuration changes for one server. Returns rollout metadata and status. Read-only.",
}

type stagedConfigListArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Status   string `json:"status,omitempty" jsonschema:"optional status filter: staged, applied, discarded or rolled_back"`
	Limit    int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type stagedChangeMetadata struct {
	ID           string `json:"id"`
	BatchID      string `json:"batch_id,omitempty"`
	Status       string `json:"status"`
	ApplyMode    string `json:"apply_mode"`
	ApplyAt      string `json:"apply_at,omitempty"`
	IdleMinutes  int32  `json:"idle_minutes,omitempty"`
	CreatedAt    string `json:"created_at"`
	AppliedAt    string `json:"applied_at,omitempty"`
	RolledBackAt string `json:"rolled_back_at,omitempty"`
}

type stagedConfigListResult struct {
	ServerID  string                 `json:"server_id"`
	Changes   []stagedChangeMetadata `json:"changes"`
	Matched   int                    `json:"matched"`
	Truncated bool                   `json:"truncated"`
}

func listStagedConfigChanges(ctx context.Context, client *Clients, args stagedConfigListArgs) (any, error) {
	tool := stagedConfigListSpec.Name
	if err := requireID(tool, "server_id", args.ServerID); err != nil {
		return nil, err
	}
	limit, err := limitOr(tool, args.Limit)
	if err != nil {
		return nil, err
	}
	switch args.Status {
	case "", "staged", "applied", "discarded", "rolled_back":
	default:
		return nil, invalidArgument(tool, "status", "use one of staged, applied, discarded or rolled_back")
	}
	resp, err := client.StagedConfig.ListStagedChanges(ctx, connect.NewRequest(&v1.ListStagedChangesRequest{
		ServerId: args.ServerID, Status: args.Status,
	}))
	if err != nil {
		return nil, err
	}
	changes := resp.Msg.GetChanges()
	count := min(len(changes), int(limit))
	out := stagedConfigListResult{
		ServerID:  args.ServerID,
		Changes:   make([]stagedChangeMetadata, 0, count),
		Matched:   len(changes),
		Truncated: count < len(changes),
	}
	for _, change := range changes[:count] {
		out.Changes = append(out.Changes, stagedChangeMetadata{
			ID: change.GetId(), BatchID: change.GetBatchId(), Status: change.GetStatus(),
			ApplyMode: change.GetApplyMode(), ApplyAt: change.GetApplyAt(), IdleMinutes: change.GetIdleMinutes(),
			CreatedAt: change.GetCreatedAt(), AppliedAt: change.GetAppliedAt(), RolledBackAt: change.GetRolledBackAt(),
		})
	}
	return out, nil
}
