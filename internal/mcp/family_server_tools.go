// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/proto"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// FamilyServerClients owns the typed generated Connect clients used by the
// coverage-gap tools. It is intentionally separate from Clients: the
// integration owner decides how to merge the two and regenerate the catalog.
type FamilyServerClients struct {
	Server    carbonpanelv1connect.ServerServiceClient
	Archive   carbonpanelv1connect.ServerServiceClient
	Config    carbonpanelv1connect.ConfigServiceClient
	Task      carbonpanelv1connect.TaskServiceClient
	Activity  carbonpanelv1connect.ActivityServiceClient
	Operation carbonpanelv1connect.OperationServiceClient
	Blueprint carbonpanelv1connect.BlueprintServiceClient
	Minecraft carbonpanelv1connect.MinecraftServiceClient
	Support   carbonpanelv1connect.SupportServiceClient
	Node      carbonpanelv1connect.NodeServiceClient
}

// NewFamilyServerClients builds every typed client with the same bearer
// interceptor and bounded HTTP timeout as the first-slice clients. An empty
// token is refused; there is no anonymous fallback.
func NewFamilyServerClients(httpClient connect.HTTPClient, baseURL, token string) (*FamilyServerClients, error) {
	if httpClient == nil {
		httpClient = newHTTPClient()
	}
	if token == "" {
		return nil, fmt.Errorf("API token is required")
	}
	opts := []connect.ClientOption{connect.WithInterceptors(bearerInterceptor(token))}
	archiveClient := httpClient
	if client, ok := httpClient.(*http.Client); ok && client.Timeout == DefaultRequestTimeout {
		longCallClient := *client
		longCallClient.Timeout = time.Hour
		archiveClient = &longCallClient
	}
	return &FamilyServerClients{
		Archive:   carbonpanelv1connect.NewServerServiceClient(archiveClient, baseURL, opts...),
		Server:    carbonpanelv1connect.NewServerServiceClient(httpClient, baseURL, opts...),
		Config:    carbonpanelv1connect.NewConfigServiceClient(httpClient, baseURL, opts...),
		Task:      carbonpanelv1connect.NewTaskServiceClient(httpClient, baseURL, opts...),
		Activity:  carbonpanelv1connect.NewActivityServiceClient(httpClient, baseURL, opts...),
		Operation: carbonpanelv1connect.NewOperationServiceClient(httpClient, baseURL, opts...),
		Blueprint: carbonpanelv1connect.NewBlueprintServiceClient(httpClient, baseURL, opts...),
		Minecraft: carbonpanelv1connect.NewMinecraftServiceClient(httpClient, baseURL, opts...),
		Support:   carbonpanelv1connect.NewSupportServiceClient(httpClient, baseURL, opts...),
		Node:      carbonpanelv1connect.NewNodeServiceClient(httpClient, baseURL, opts...),
	}, nil
}

var famSpecs = func() map[string]ToolSpec {
	m := make(map[string]ToolSpec, FamilyServerToolCount)
	for _, s := range FamilyServerToolSpecs() {
		m[s.Name] = s
	}
	return m
}()

func famSpec(name string) ToolSpec { return famSpecs[name] }

// addFamilyTool mirrors addTool in tools.go but resolves its ToolSpec from the
// exported descriptor list. Confirmation is checked before any RPC is sent.
func addFamilyTool[In any](s *sdkmcp.Server, name string, clients *FamilyServerClients, run func(context.Context, *FamilyServerClients, In) (any, error)) {
	if !toolAllowedOn(s, name) {
		return
	}
	spec := famSpec(name)
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

// --- bounded argument validation -------------------------------------------

func famErr(tool, field, rule string) error {
	return &ToolError{
		Kind:      ErrKindInvalidArguments,
		Tool:      tool,
		Procedure: famSpec(tool).Procedure,
		Message:   fmt.Sprintf("%s for tool %q: %s", field, tool, rule),
	}
}

func famID(tool, field, value string) error {
	if len(value) == 0 || len(value) > maxIDLen {
		return famErr(tool, field, fmt.Sprintf("must be 1 to %d characters", maxIDLen))
	}
	return nil
}

func famOptID(tool, field, value string) error {
	if value != "" && len(value) > maxIDLen {
		return famErr(tool, field, fmt.Sprintf("must be at most %d characters", maxIDLen))
	}
	return nil
}

func famText(tool, field, value string, max int, required bool) error {
	if required && value == "" {
		return famErr(tool, field, "is required")
	}
	if len(value) > max {
		return famErr(tool, field, fmt.Sprintf("must be at most %d characters", max))
	}
	return nil
}

func famInt(tool, field string, value, min, max int32) error {
	if value < min || value > max {
		return famErr(tool, field, fmt.Sprintf("must be between %d and %d", min, max))
	}
	return nil
}

func famInt64(tool, field string, value, min, max int64) error {
	if value < min || value > max {
		return famErr(tool, field, fmt.Sprintf("must be between %d and %d", min, max))
	}
	return nil
}

func famLimit(tool string, value int32) (int32, error) {
	if value == 0 {
		return defaultLimit, nil
	}
	if value < 1 || value > maxLimit {
		return 0, famErr(tool, "limit", fmt.Sprintf("must be between 1 and %d", maxLimit))
	}
	return value, nil
}

func famTail(tool string, value int32) (int32, error) {
	if value == 0 {
		return 100, nil
	}
	if value < 1 || value > 1000 {
		return 0, famErr(tool, "tail", "must be between 1 and 1000")
	}
	return value, nil
}

func famEnum(tool, field, value string, allowed ...string) error {
	upper := strings.ToUpper(value)
	for _, a := range allowed {
		if upper == strings.ToUpper(a) {
			return nil
		}
	}
	return famErr(tool, field, "must be one of: "+strings.Join(allowed, ", "))
}

// famProtoEnum maps a short agent-facing name to a generated enum value whose
// declared name is PREFIX_NAME (e.g. "backup" -> TASK_TYPE_BACKUP).
func famProtoEnum(tool, field, value, prefix string, names map[string]int32) (int32, error) {
	upper := strings.ToUpper(value)
	if n, ok := names[upper]; ok {
		return n, nil
	}
	if n, ok := names[prefix+upper]; ok {
		return n, nil
	}
	return 0, famErr(tool, field, "unknown value")
}

func famIntValue(v *int32) int32 {
	if v == nil {
		return 0
	}
	return *v
}

func famStringPtr(v string) *string { return &v }
func famBoolPtr(v bool) *bool       { return &v }

// --- response redaction -----------------------------------------------------

// sensitiveConfigKey reports whether a config property or task config key is
// credential material that must never leave the gateway in clear text.
func sensitiveConfigKey(key string) bool {
	k := strings.ToLower(key)
	for _, marker := range []string{"password", "secret", "token", "api_key", "apikey", "credential", "private_key", "auth"} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return false
}

func redactConfigCategories(cats []*v1.ConfigCategory) []*v1.ConfigCategory {
	out := make([]*v1.ConfigCategory, 0, len(cats))
	for _, cat := range cats {
		if cat == nil {
			continue
		}
		c := proto.Clone(cat).(*v1.ConfigCategory)
		for _, p := range c.GetProperties() {
			if p == nil {
				continue
			}
			if strings.EqualFold(p.GetType(), "password") || sensitiveConfigKey(p.GetKey()) {
				p.Value = "[REDACTED]"
				if p.DefaultValue != nil {
					p.DefaultValue = famStringPtr("[REDACTED]")
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func redactConfigResponse(r *v1.GetServerConfigResponse) *v1.GetServerConfigResponse {
	out := proto.Clone(r).(*v1.GetServerConfigResponse)
	out.Categories = redactConfigCategories(out.GetCategories())
	return out
}

func redactGlobalSettingsResponse(r *v1.GetGlobalSettingsResponse) *v1.GetGlobalSettingsResponse {
	out := proto.Clone(r).(*v1.GetGlobalSettingsResponse)
	out.Categories = redactConfigCategories(out.GetCategories())
	return out
}

func redactConfigJSONValue(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, child := range value {
			if sensitiveConfigKey(k) {
				out[k] = "[REDACTED]"
				continue
			}
			out[k] = redactConfigJSONValue(child)
		}
		return out
	case []any:
		out := make([]any, 0, len(value))
		for _, child := range value {
			out = append(out, redactConfigJSONValue(child))
		}
		return out
	default:
		return v
	}
}

// redactTaskConfig removes webhook secrets and repository tokens from the
// serialized ScheduledTask.config JSON. An unparseable value is replaced
// wholesale rather than returned in clear text.
func redactTaskConfig(config string) string {
	if config == "" {
		return ""
	}
	var parsed any
	if err := json.Unmarshal([]byte(config), &parsed); err != nil {
		return "[REDACTED: unparseable task config]"
	}
	redacted, err := json.Marshal(redactConfigJSONValue(parsed))
	if err != nil {
		return "[REDACTED]"
	}
	return string(redacted)
}

func redactTask(t *v1.ScheduledTask) *v1.ScheduledTask {
	if t == nil {
		return nil
	}
	out := proto.Clone(t).(*v1.ScheduledTask)
	out.Config = redactTaskConfig(out.GetConfig())
	return out
}

func redactNode(n *v1.Node) *v1.Node {
	if n == nil {
		return nil
	}
	out := proto.Clone(n).(*v1.Node)
	out.TlsCaCert = ""
	out.TlsCert = ""
	out.TlsKey = ""
	return out
}

// --- argument shapes --------------------------------------------------------

type famIDArgs struct {
	ID string `json:"id" jsonschema:"exact resource id, 1 to 128 characters"`
}

type famConfirmIDArgs struct {
	ID      string `json:"id" jsonschema:"exact resource id, 1 to 128 characters"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to perform this mutation"`
}

func (a famConfirmIDArgs) Confirmation() bool { return a.Confirm }

type famLifecycleArgs struct {
	ID        string `json:"id" jsonschema:"exact server id, 1 to 128 characters"`
	RequestID string `json:"request_id,omitempty" jsonschema:"idempotency key, at most 128 characters"`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"must be true to perform this lifecycle mutation"`
}

func (a famLifecycleArgs) Confirmation() bool { return a.Confirm }

type famServerIDArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
}

type famServerLimitArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id, never empty; 1 to 128 characters"`
	Limit    int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type famLogsArgs struct {
	ID   string `json:"id" jsonschema:"exact server id"`
	Tail int32  `json:"tail,omitempty" jsonschema:"last N log lines, 1 to 1000, default 100"`
}

type famExportArchiveArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact stopped server id"`
	Format   string `json:"format" jsonschema:"archive format zip or tar.gz"`
}

type famPreviewArchiveArgs struct {
	UploadID string `json:"upload_id" jsonschema:"caller-owned completed archive upload id"`
	NodeID   string `json:"node_id,omitempty" jsonschema:"known destination node id"`
}

type famCreateServerArgs struct {
	ArchiveUploadID  string `json:"archive_upload_id,omitempty" jsonschema:"caller-owned completed server archive upload id"`
	Name             string `json:"name" jsonschema:"exact server name, 1 to 128 characters"`
	McVersion        string `json:"mc_version,omitempty" jsonschema:"Minecraft version, at most 32 characters"`
	ModLoader        string `json:"mod_loader,omitempty" jsonschema:"mod loader name"`
	Port             *int32 `json:"port,omitempty" jsonschema:"host port from 1 to 65535"`
	MaxPlayers       *int32 `json:"max_players,omitempty" jsonschema:"player cap from 1 to 200"`
	Memory           *int32 `json:"memory,omitempty" jsonschema:"memory in MiB from 256 to 65536"`
	NodeID           string `json:"node_id,omitempty" jsonschema:"known configured node id only"`
	RequestID        string `json:"request_id,omitempty" jsonschema:"idempotency key, at most 128 characters"`
	StartImmediately bool   `json:"start_immediately,omitempty" jsonschema:"start after creation"`
	Confirm          bool   `json:"confirm,omitempty" jsonschema:"must be true to create a server"`
}

func (a famCreateServerArgs) Confirmation() bool { return a.Confirm }

type famUpdateServerArgs struct {
	ID          string `json:"id" jsonschema:"exact server id"`
	Name        string `json:"name,omitempty" jsonschema:"new server name, at most 128 characters"`
	Description string `json:"description,omitempty" jsonschema:"new description, at most 256 characters"`
	Port        *int32 `json:"port,omitempty" jsonschema:"host port from 1 to 65535"`
	MaxPlayers  *int32 `json:"max_players,omitempty" jsonschema:"player cap from 1 to 200"`
	Memory      *int32 `json:"memory,omitempty" jsonschema:"memory in MiB from 256 to 65536"`
	AutoStart   *bool  `json:"auto_start,omitempty" jsonschema:"start on controller boot"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"must be true to update a server"`
}

func (a famUpdateServerArgs) Confirmation() bool { return a.Confirm }

type famUpdateConfigArgs struct {
	ServerID string            `json:"server_id" jsonschema:"exact server id"`
	Updates  map[string]string `json:"updates" jsonschema:"at most 16 bounded key/value updates"`
	Stage    bool              `json:"stage,omitempty" jsonschema:"record the diff without recreating the container"`
	Confirm  bool              `json:"confirm,omitempty" jsonschema:"must be true to change config"`
}

func (a famUpdateConfigArgs) Confirmation() bool { return a.Confirm }

type famDiscardBatchArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id"`
	ItemID   string `json:"item_id,omitempty" jsonschema:"empty discards the whole open batch"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to discard staged changes"`
}

func (a famDiscardBatchArgs) Confirmation() bool { return a.Confirm }

type famValidateKeyArgs struct {
	Provider  string `json:"provider" jsonschema:"curseforge or modrinth"`
	APIKey    string `json:"api_key,omitempty" jsonschema:"provider key; empty is an anonymous check; never echoed"`
	UserAgent string `json:"user_agent,omitempty" jsonschema:"optional user agent, at most 128 characters"`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"must be true to use a credential"`
}

func (a famValidateKeyArgs) Confirmation() bool { return a.Confirm }

type famCreateTaskArgs struct {
	ServerID     string `json:"server_id" jsonschema:"exact server id"`
	Name         string `json:"name" jsonschema:"exact task name"`
	TaskType     string `json:"task_type" jsonschema:"allowlisted task type"`
	Schedule     string `json:"schedule" jsonschema:"cron, interval, once or event"`
	CronExpr     string `json:"cron_expr,omitempty" jsonschema:"cron expression, at most 64 characters"`
	IntervalSecs int32  `json:"interval_secs,omitempty" jsonschema:"1 to 604800"`
	Timeout      int32  `json:"timeout,omitempty" jsonschema:"1 to 86400"`
	Config       string `json:"config,omitempty" jsonschema:"task config JSON, at most 4096 characters"`
	Confirm      bool   `json:"confirm,omitempty" jsonschema:"must be true to create a task"`
}

func (a famCreateTaskArgs) Confirmation() bool { return a.Confirm }

type famUpdateTaskArgs struct {
	ID       string `json:"id" jsonschema:"exact task id"`
	Name     string `json:"name,omitempty" jsonschema:"new task name, at most 128 characters"`
	TaskType string `json:"task_type,omitempty" jsonschema:"allowlisted task type"`
	Config   string `json:"config,omitempty" jsonschema:"task config JSON, at most 4096 characters"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to update a task"`
}

func (a famUpdateTaskArgs) Confirmation() bool { return a.Confirm }

type famToggleTaskArgs struct {
	ID      string `json:"id" jsonschema:"exact task id"`
	Status  string `json:"status" jsonschema:"enabled, disabled or paused"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to toggle a task"`
}

func (a famToggleTaskArgs) Confirmation() bool { return a.Confirm }

type famTaskExecArgs struct {
	TaskID string `json:"task_id" jsonschema:"exact task id"`
	Limit  int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type famActivityArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id"`
	Actor    string `json:"actor,omitempty" jsonschema:"optional actor filter, at most 128 characters"`
	Limit    int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type famListOperationsArgs struct {
	ServerID string `json:"server_id" jsonschema:"exact server id"`
	Outcome  string `json:"outcome,omitempty" jsonschema:"success, failed, running or unknown"`
	Limit    int32  `json:"limit,omitempty" jsonschema:"result limit from 1 to 100, default 20"`
}

type famGetOperationArgs struct {
	ServerID    string `json:"server_id" jsonschema:"exact server id"`
	OperationID string `json:"operation_id" jsonschema:"exact operation id"`
}

type famResolveOperationArgs struct {
	ServerID    string `json:"server_id" jsonschema:"exact server id"`
	OperationID string `json:"operation_id" jsonschema:"exact operation id"`
	Note        string `json:"note,omitempty" jsonschema:"operator note, at most 256 characters"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"must be true to release a lifecycle lock"`
}

func (a famResolveOperationArgs) Confirmation() bool { return a.Confirm }

type famLoaderVersionsArgs struct {
	Loader    string `json:"loader" jsonschema:"loader name, 1 to 32 characters"`
	MCVersion string `json:"mc_version" jsonschema:"Minecraft version, 1 to 32 characters"`
}

type famTailArgs struct {
	Tail int32 `json:"tail,omitempty" jsonschema:"last N log lines, 1 to 1000, default 100"`
}

type famAppLogsResult struct {
	Content   string `json:"content"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`
	Truncated bool   `json:"truncated"`
}

type famDeleteServerArgs struct {
	ID       string `json:"id" jsonschema:"exact server id"`
	KeepData bool   `json:"keep_data,omitempty" jsonschema:"retain the world directory"`
	Detach   bool   `json:"detach,omitempty" jsonschema:"retain container, world and reserved port"`
	Confirm  bool   `json:"confirm,omitempty" jsonschema:"must be true to delete a server"`
}

func (a famDeleteServerArgs) Confirmation() bool { return a.Confirm }

type famSendCommandArgs struct {
	ID      string `json:"id" jsonschema:"exact server id"`
	Command string `json:"command" jsonschema:"allowlisted operational command"`
	Confirm bool   `json:"confirm,omitempty" jsonschema:"must be true to send a command"`
}

func (a famSendCommandArgs) Confirmation() bool { return a.Confirm }

type famMigrateServerArgs struct {
	ID           string `json:"id" jsonschema:"exact server id"`
	TargetNodeID string `json:"target_node_id" jsonschema:"known configured node id"`
	RequestID    string `json:"request_id,omitempty" jsonschema:"idempotency key"`
	Force        bool   `json:"force,omitempty" jsonschema:"proceed despite preflight warnings"`
	Confirm      bool   `json:"confirm,omitempty" jsonschema:"must be true to migrate a server"`
}

func (a famMigrateServerArgs) Confirmation() bool { return a.Confirm }

type famUpdateGlobalArgs struct {
	Updates map[string]string `json:"updates" jsonschema:"at most 16 bounded key/value updates"`
	Confirm bool              `json:"confirm,omitempty" jsonschema:"must be true to change global settings"`
}

func (a famUpdateGlobalArgs) Confirmation() bool { return a.Confirm }

type famSyncGlobalArgs struct {
	OpsAndWhitelistOnly bool `json:"ops_and_whitelist_only,omitempty" jsonschema:"restrict to Ops and Whitelist"`
	Confirm             bool `json:"confirm,omitempty" jsonschema:"must be true to sync global settings"`
}

func (a famSyncGlobalArgs) Confirmation() bool { return a.Confirm }

type famGenerateBundleArgs struct {
	IncludeLogs       bool   `json:"include_logs,omitempty" jsonschema:"include application logs"`
	IncludeConfigs    bool   `json:"include_configs,omitempty" jsonschema:"include configuration"`
	IncludeSystemInfo bool   `json:"include_system_info,omitempty" jsonschema:"include system information"`
	ServerIDs         string `json:"server_ids,omitempty" jsonschema:"comma-separated known server ids; empty means all"`
	Confirm           bool   `json:"confirm,omitempty" jsonschema:"must be true to generate a bundle"`
}

func (a famGenerateBundleArgs) Confirmation() bool { return a.Confirm }

type famUploadBundleArgs struct {
	IncludeLogs       bool   `json:"include_logs,omitempty" jsonschema:"include application logs"`
	IncludeConfigs    bool   `json:"include_configs,omitempty" jsonschema:"include configuration"`
	IncludeSystemInfo bool   `json:"include_system_info,omitempty" jsonschema:"include system information"`
	IssueDescription  string `json:"issue_description" jsonschema:"description of the issue, at most 2048 characters"`
	Confirm           bool   `json:"confirm,omitempty" jsonschema:"must be true to upload a bundle"`
}

func (a famUploadBundleArgs) Confirmation() bool { return a.Confirm }

type famBundleIDArgs struct {
	BundleID string `json:"bundle_id" jsonschema:"exact bundle id, 1 to 128 characters"`
}

type famCreateNodeArgs struct {
	Name         string `json:"name" jsonschema:"node name"`
	Host         string `json:"host" jsonschema:"Docker daemon host, at most 512 characters"`
	AdvertisedIP string `json:"advertised_ip,omitempty" jsonschema:"advertised address, at most 128 characters"`
	MaxMemoryMB  *int64 `json:"max_memory_mb,omitempty" jsonschema:"memory budget from 256 to 1048576"`
	MaxServers   *int32 `json:"max_servers,omitempty" jsonschema:"server cap from 1 to 10000"`
	Enabled      bool   `json:"enabled,omitempty" jsonschema:"enable the node"`
	Confirm      bool   `json:"confirm,omitempty" jsonschema:"must be true to register a node"`
}

func (a famCreateNodeArgs) Confirmation() bool { return a.Confirm }

type famUpdateNodeArgs struct {
	ID          string `json:"id" jsonschema:"known configured node id"`
	Name        string `json:"name,omitempty" jsonschema:"new name"`
	Host        string `json:"host,omitempty" jsonschema:"new Docker daemon host"`
	MaxMemoryMB *int64 `json:"max_memory_mb,omitempty" jsonschema:"memory budget from 256 to 1048576"`
	MaxServers  *int32 `json:"max_servers,omitempty" jsonschema:"server cap from 1 to 10000"`
	Enabled     *bool  `json:"enabled,omitempty" jsonschema:"enable or disable the node"`
	Confirm     bool   `json:"confirm,omitempty" jsonschema:"must be true to update a node"`
}

func (a famUpdateNodeArgs) Confirmation() bool { return a.Confirm }

func famInt64Value(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func redactNodeListResponse(r *v1.ListNodesResponse) *v1.ListNodesResponse {
	out := proto.Clone(r).(*v1.ListNodesResponse)
	for i, n := range out.GetNodes() {
		out.Nodes[i] = redactNode(n)
	}
	return out
}

// --- registration -----------------------------------------------------------

// RegisterFamilyServerTools binds every executable descriptor to a typed
// handler. It panics if the handler set and the exported descriptor list ever
// disagree, so a coverage-gap tool cannot be declared but left unregistered.
func RegisterFamilyServerTools(s *sdkmcp.Server, c *FamilyServerClients) {
	if s == nil || c == nil {
		panic("mcp.RegisterFamilyServerTools: nil server or clients")
	}

	// --- ServerService --------------------------------------------------
	addFamilyTool(s, "get_server_logs", c, func(ctx context.Context, cl *FamilyServerClients, in famLogsArgs) (any, error) {
		if err := famID("get_server_logs", "id", in.ID); err != nil {
			return nil, err
		}
		tail, err := famTail("get_server_logs", in.Tail)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.GetServerLogs, v1.GetServerLogsRequest{Id: in.ID, Tail: tail})
	})
	addFamilyTool(s, "clear_server_logs", c, func(ctx context.Context, cl *FamilyServerClients, in famConfirmIDArgs) (any, error) {
		if err := famID("clear_server_logs", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.ClearServerLogs, v1.ClearServerLogsRequest{Id: in.ID})
	})
	addFamilyTool(s, "get_next_available_port", c, func(ctx context.Context, cl *FamilyServerClients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.Server.GetNextAvailablePort, v1.GetNextAvailablePortRequest{})
	})
	addFamilyTool(s, "export_server_archive", c, func(ctx context.Context, cl *FamilyServerClients, in famExportArchiveArgs) (any, error) {
		if err := famID("export_server_archive", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		var format v1.ServerArchiveFormat
		switch in.Format {
		case "zip":
			format = v1.ServerArchiveFormat_SERVER_ARCHIVE_FORMAT_ZIP
		case "tar.gz":
			format = v1.ServerArchiveFormat_SERVER_ARCHIVE_FORMAT_TAR_GZ
		default:
			return nil, famErr("export_server_archive", "format", "must be zip or tar.gz")
		}
		return invoke(ctx, cl.Archive.ExportServerArchive, v1.ExportServerArchiveRequest{
			ServerId:    in.ServerID,
			Format:      format,
			Consistency: v1.ServerArchiveConsistency_SERVER_ARCHIVE_CONSISTENCY_STOPPED,
		})
	})
	addFamilyTool(s, "preview_server_archive", c, func(ctx context.Context, cl *FamilyServerClients, in famPreviewArchiveArgs) (any, error) {
		if err := famID("preview_server_archive", "upload_id", in.UploadID); err != nil {
			return nil, err
		}
		if err := famOptID("preview_server_archive", "node_id", in.NodeID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Archive.PreviewServerArchive, v1.PreviewServerArchiveRequest{UploadId: in.UploadID, NodeId: in.NodeID})
	})
	addFamilyTool(s, "create_server", c, func(ctx context.Context, cl *FamilyServerClients, in famCreateServerArgs) (any, error) {
		if err := famText("create_server", "name", in.Name, maxIDLen, true); err != nil {
			return nil, err
		}
		if err := famText("create_server", "mc_version", in.McVersion, 32, false); err != nil {
			return nil, err
		}
		loader, err := famProtoEnum("create_server", "mod_loader", in.ModLoader, "MOD_LOADER_", v1.ModLoader_value)
		if err != nil && in.ModLoader != "" {
			return nil, err
		}
		if in.Port != nil {
			if err := famInt("create_server", "port", *in.Port, 1, 65535); err != nil {
				return nil, err
			}
		}
		if in.MaxPlayers != nil {
			if err := famInt("create_server", "max_players", *in.MaxPlayers, 1, 200); err != nil {
				return nil, err
			}
		}
		if in.Memory != nil {
			if err := famInt("create_server", "memory", *in.Memory, 256, 65536); err != nil {
				return nil, err
			}
		}
		if err := famOptID("create_server", "node_id", in.NodeID); err != nil {
			return nil, err
		}
		if err := famOptID("create_server", "archive_upload_id", in.ArchiveUploadID); err != nil {
			return nil, err
		}
		if in.ArchiveUploadID != "" && in.RequestID == "" {
			return nil, famErr("create_server", "request_id", "required when importing an archive")
		}
		if err := famOptID("create_server", "request_id", in.RequestID); err != nil {
			return nil, err
		}
		client := cl.Server
		if in.ArchiveUploadID != "" {
			client = cl.Archive
			preview, err := cl.Archive.PreviewServerArchive(ctx, connect.NewRequest(&v1.PreviewServerArchiveRequest{UploadId: in.ArchiveUploadID, NodeId: in.NodeID}))
			if err != nil {
				return nil, err
			}
			for _, finding := range preview.Msg.GetFindings() {
				if finding.GetSeverity() == v1.ServerArchiveFindingSeverity_SERVER_ARCHIVE_FINDING_SEVERITY_BLOCKER {
					return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("archive preview: %s", finding.GetMessage()))
				}
			}
		}
		return invoke(ctx, client.CreateServer, v1.CreateServerRequest{
			Name: in.Name, McVersion: in.McVersion, ModLoader: v1.ModLoader(loader),
			Port: famIntValue(in.Port), MaxPlayers: famIntValue(in.MaxPlayers),
			Memory: famIntValue(in.Memory), NodeId: in.NodeID, RequestId: in.RequestID,
			StartImmediately: in.StartImmediately, ArchiveUploadId: in.ArchiveUploadID,
		})
	})
	addFamilyTool(s, "update_server", c, func(ctx context.Context, cl *FamilyServerClients, in famUpdateServerArgs) (any, error) {
		if err := famID("update_server", "id", in.ID); err != nil {
			return nil, err
		}
		if err := famText("update_server", "name", in.Name, maxIDLen, false); err != nil {
			return nil, err
		}
		if err := famText("update_server", "description", in.Description, 256, false); err != nil {
			return nil, err
		}
		if in.Port != nil {
			if err := famInt("update_server", "port", *in.Port, 1, 65535); err != nil {
				return nil, err
			}
		}
		if in.MaxPlayers != nil {
			if err := famInt("update_server", "max_players", *in.MaxPlayers, 1, 200); err != nil {
				return nil, err
			}
		}
		if in.Memory != nil {
			if err := famInt("update_server", "memory", *in.Memory, 256, 65536); err != nil {
				return nil, err
			}
		}
		return invoke(ctx, cl.Server.UpdateServer, v1.UpdateServerRequest{
			Id: in.ID, Name: in.Name, Description: in.Description,
			Port: in.Port, MaxPlayers: famIntValue(in.MaxPlayers),
			Memory: famIntValue(in.Memory), AutoStart: in.AutoStart,
		})
	})
	addFamilyTool(s, "recreate_server", c, func(ctx context.Context, cl *FamilyServerClients, in famConfirmIDArgs) (any, error) {
		if err := famID("recreate_server", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.RecreateServer, v1.RecreateServerRequest{Id: in.ID})
	})
	addFamilyTool(s, "pause_server", c, func(ctx context.Context, cl *FamilyServerClients, in famLifecycleArgs) (any, error) {
		if err := famID("pause_server", "id", in.ID); err != nil {
			return nil, err
		}
		if err := famOptID("pause_server", "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.PauseServer, v1.PauseServerRequest{Id: in.ID, RequestId: in.RequestID})
	})
	addFamilyTool(s, "deep_sleep_server", c, func(ctx context.Context, cl *FamilyServerClients, in famLifecycleArgs) (any, error) {
		if err := famID("deep_sleep_server", "id", in.ID); err != nil {
			return nil, err
		}
		if err := famOptID("deep_sleep_server", "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.DeepSleepServer, v1.DeepSleepServerRequest{Id: in.ID, RequestId: in.RequestID})
	})
	addFamilyTool(s, "list_server_players", c, func(ctx context.Context, cl *FamilyServerClients, in famServerIDArgs) (any, error) {
		if err := famID("list_server_players", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.ListServerPlayers, v1.ListServerPlayersRequest{ServerId: in.ServerID})
	})
	addFamilyTool(s, "delete_server", c, func(ctx context.Context, cl *FamilyServerClients, in famDeleteServerArgs) (any, error) {
		if err := famID("delete_server", "id", in.ID); err != nil {
			return nil, err
		}
		// An unqualified delete would remove world data; require an explicit
		// data-preserving option so the confirm gate has real meaning.
		if !in.KeepData && !in.Detach {
			return nil, famErr("delete_server", "keep_data/detach", "set keep_data=true or detach=true to preserve world data")
		}
		return invoke(ctx, cl.Server.DeleteServer, v1.DeleteServerRequest{Id: in.ID, KeepData: in.KeepData, Detach: in.Detach})
	})
	addFamilyTool(s, "send_command", c, func(ctx context.Context, cl *FamilyServerClients, in famSendCommandArgs) (any, error) {
		if err := famID("send_command", "id", in.ID); err != nil {
			return nil, err
		}
		if err := famEnum("send_command", "command", in.Command, famSafeConsoleCommands...); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.SendCommand, v1.SendCommandRequest{Id: in.ID, Command: strings.ToLower(in.Command)})
	})
	addFamilyTool(s, "upload_to_mclogs", c, func(ctx context.Context, cl *FamilyServerClients, in famConfirmIDArgs) (any, error) {
		if err := famID("upload_to_mclogs", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.UploadToMCLogs, v1.UploadToMCLogsRequest{Id: in.ID})
	})
	addFamilyTool(s, "migrate_server", c, func(ctx context.Context, cl *FamilyServerClients, in famMigrateServerArgs) (any, error) {
		if err := famID("migrate_server", "id", in.ID); err != nil {
			return nil, err
		}
		if err := famID("migrate_server", "target_node_id", in.TargetNodeID); err != nil {
			return nil, err
		}
		if err := famOptID("migrate_server", "request_id", in.RequestID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Server.MigrateServer, v1.MigrateServerRequest{
			Id: in.ID, TargetNodeId: in.TargetNodeID, RequestId: in.RequestID, Force: famBoolPtr(in.Force),
		})
	})

	// --- ConfigService ---------------------------------------------------
	addFamilyTool(s, "get_server_config", c, func(ctx context.Context, cl *FamilyServerClients, in famServerIDArgs) (any, error) {
		if err := famID("get_server_config", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		res, err := cl.Config.GetServerConfig(ctx, connect.NewRequest(&v1.GetServerConfigRequest{ServerId: in.ServerID}))
		if err != nil {
			return nil, err
		}
		return redactConfigResponse(res.Msg), nil
	})
	addFamilyTool(s, "update_server_config", c, func(ctx context.Context, cl *FamilyServerClients, in famUpdateConfigArgs) (any, error) {
		if err := famID("update_server_config", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if len(in.Updates) == 0 || len(in.Updates) > 16 {
			return nil, famErr("update_server_config", "updates", "must contain 1 to 16 entries")
		}
		for k, v := range in.Updates {
			if err := famText("update_server_config", "updates.key", k, maxIDLen, true); err != nil {
				return nil, err
			}
			if err := famText("update_server_config", "updates.value", v, 1024, false); err != nil {
				return nil, err
			}
		}
		res, err := cl.Config.UpdateServerConfig(ctx, connect.NewRequest(&v1.UpdateServerConfigRequest{
			ServerId: in.ServerID, Updates: in.Updates, Stage: in.Stage,
		}))
		if err != nil {
			return nil, err
		}
		return &v1.UpdateServerConfigResponse{
			Categories: redactConfigCategories(res.Msg.GetCategories()), Staged: res.Msg.GetStaged(),
		}, nil
	})
	addFamilyTool(s, "get_update_batch", c, func(ctx context.Context, cl *FamilyServerClients, in famServerIDArgs) (any, error) {
		if err := famID("get_update_batch", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Config.GetUpdateBatch, v1.GetUpdateBatchRequest{ServerId: in.ServerID})
	})
	addFamilyTool(s, "discard_update_batch", c, func(ctx context.Context, cl *FamilyServerClients, in famDiscardBatchArgs) (any, error) {
		if err := famID("discard_update_batch", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := famOptID("discard_update_batch", "item_id", in.ItemID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Config.DiscardUpdateBatch, v1.DiscardUpdateBatchRequest{ServerId: in.ServerID, ItemId: in.ItemID})
	})
	addFamilyTool(s, "get_global_settings", c, func(ctx context.Context, cl *FamilyServerClients, _ emptyArgs) (any, error) {
		res, err := cl.Config.GetGlobalSettings(ctx, connect.NewRequest(&v1.GetGlobalSettingsRequest{}))
		if err != nil {
			return nil, err
		}
		return redactGlobalSettingsResponse(res.Msg), nil
	})
	addFamilyTool(s, "validate_provider_key", c, func(ctx context.Context, cl *FamilyServerClients, in famValidateKeyArgs) (any, error) {
		if err := famEnum("validate_provider_key", "provider", in.Provider, "curseforge", "modrinth"); err != nil {
			return nil, err
		}
		if err := famText("validate_provider_key", "api_key", in.APIKey, 512, false); err != nil {
			return nil, err
		}
		if err := famText("validate_provider_key", "user_agent", in.UserAgent, 128, false); err != nil {
			return nil, err
		}
		// The key is passed straight through and is never echoed, logged or
		// placed in the response. The soft result only exposes valid/message.
		res, err := cl.Config.ValidateProviderKey(ctx, connect.NewRequest(&v1.ValidateProviderKeyRequest{
			Provider: strings.ToLower(in.Provider), ApiKey: in.APIKey, UserAgent: in.UserAgent,
		}))
		if err != nil {
			return nil, err
		}
		message := res.Msg.GetMessage()
		if in.APIKey != "" {
			message = strings.ReplaceAll(message, in.APIKey, "[REDACTED]")
		}
		return &v1.ValidateProviderKeyResponse{Valid: res.Msg.GetValid(), Message: message}, nil
	})
	addFamilyTool(s, "update_global_settings", c, func(ctx context.Context, cl *FamilyServerClients, in famUpdateGlobalArgs) (any, error) {
		if len(in.Updates) == 0 || len(in.Updates) > 16 {
			return nil, famErr("update_global_settings", "updates", "must contain 1 to 16 entries")
		}
		for k, v := range in.Updates {
			if err := famText("update_global_settings", "updates.key", k, maxIDLen, true); err != nil {
				return nil, err
			}
			if err := famText("update_global_settings", "updates.value", v, 1024, false); err != nil {
				return nil, err
			}
		}
		res, err := cl.Config.UpdateGlobalSettings(ctx, connect.NewRequest(&v1.UpdateGlobalSettingsRequest{Updates: in.Updates}))
		if err != nil {
			return nil, err
		}
		return redactGlobalSettingsResponse(&v1.GetGlobalSettingsResponse{Categories: res.Msg.GetCategories()}), nil
	})
	addFamilyTool(s, "sync_global_settings_to_servers", c, func(ctx context.Context, cl *FamilyServerClients, in famSyncGlobalArgs) (any, error) {
		return invoke(ctx, cl.Config.SyncGlobalSettingsToServers, v1.SyncGlobalSettingsToServersRequest{
			OpsAndWhitelistOnly: in.OpsAndWhitelistOnly,
		})
	})

	// --- TaskService -----------------------------------------------------
	addFamilyTool(s, "list_tasks", c, func(ctx context.Context, cl *FamilyServerClients, in famServerIDArgs) (any, error) {
		if err := famID("list_tasks", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		res, err := cl.Task.ListTasks(ctx, connect.NewRequest(&v1.ListTasksRequest{ServerId: in.ServerID}))
		if err != nil {
			return nil, err
		}
		out := make([]*v1.ScheduledTask, 0, len(res.Msg.GetTasks()))
		for _, t := range res.Msg.GetTasks() {
			out = append(out, redactTask(t))
		}
		return &v1.ListTasksResponse{Tasks: out}, nil
	})
	addFamilyTool(s, "get_task", c, func(ctx context.Context, cl *FamilyServerClients, in famIDArgs) (any, error) {
		if err := famID("get_task", "id", in.ID); err != nil {
			return nil, err
		}
		res, err := cl.Task.GetTask(ctx, connect.NewRequest(&v1.GetTaskRequest{Id: in.ID}))
		if err != nil {
			return nil, err
		}
		return &v1.GetTaskResponse{Task: redactTask(res.Msg.GetTask())}, nil
	})
	addFamilyTool(s, "create_task", c, func(ctx context.Context, cl *FamilyServerClients, in famCreateTaskArgs) (any, error) {
		if err := famID("create_task", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := famText("create_task", "name", in.Name, maxIDLen, true); err != nil {
			return nil, err
		}
		if err := famEnum("create_task", "task_type", in.TaskType, famSafeTaskTypes...); err != nil {
			return nil, err
		}
		if err := famEnum("create_task", "schedule", in.Schedule, "cron", "interval", "once", "event"); err != nil {
			return nil, err
		}
		if err := famText("create_task", "cron_expr", in.CronExpr, 64, false); err != nil {
			return nil, err
		}
		if in.IntervalSecs != 0 {
			if err := famInt("create_task", "interval_secs", in.IntervalSecs, 1, 604800); err != nil {
				return nil, err
			}
		}
		if in.Timeout != 0 {
			if err := famInt("create_task", "timeout", in.Timeout, 1, 86400); err != nil {
				return nil, err
			}
		}
		if err := famText("create_task", "config", in.Config, 4096, false); err != nil {
			return nil, err
		}
		tt, _ := famProtoEnum("create_task", "task_type", in.TaskType, "TASK_TYPE_", v1.TaskType_value)
		sc, _ := famProtoEnum("create_task", "schedule", in.Schedule, "SCHEDULE_TYPE_", v1.ScheduleType_value)
		res, err := cl.Task.CreateTask(ctx, connect.NewRequest(&v1.CreateTaskRequest{
			ServerId: in.ServerID, Name: in.Name, TaskType: v1.TaskType(tt), Schedule: v1.ScheduleType(sc),
			CronExpr: in.CronExpr, IntervalSecs: in.IntervalSecs, Timeout: in.Timeout, Config: in.Config,
		}))
		if err != nil {
			return nil, err
		}
		return &v1.CreateTaskResponse{Task: redactTask(res.Msg.GetTask())}, nil
	})
	addFamilyTool(s, "update_task", c, func(ctx context.Context, cl *FamilyServerClients, in famUpdateTaskArgs) (any, error) {
		if err := famID("update_task", "id", in.ID); err != nil {
			return nil, err
		}
		if err := famText("update_task", "name", in.Name, maxIDLen, false); err != nil {
			return nil, err
		}
		if err := famText("update_task", "config", in.Config, 4096, false); err != nil {
			return nil, err
		}
		req := &v1.UpdateTaskRequest{Id: in.ID}
		if in.Name != "" {
			req.Name = famStringPtr(in.Name)
		}
		if in.Config != "" {
			req.Config = famStringPtr(in.Config)
		}
		if in.TaskType != "" {
			if err := famEnum("update_task", "task_type", in.TaskType, famSafeTaskTypes...); err != nil {
				return nil, err
			}
			tt, _ := famProtoEnum("update_task", "task_type", in.TaskType, "TASK_TYPE_", v1.TaskType_value)
			v := v1.TaskType(tt)
			req.TaskType = &v
		}
		res, err := cl.Task.UpdateTask(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return &v1.UpdateTaskResponse{Task: redactTask(res.Msg.GetTask())}, nil
	})
	addFamilyTool(s, "delete_task", c, func(ctx context.Context, cl *FamilyServerClients, in famConfirmIDArgs) (any, error) {
		if err := famID("delete_task", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Task.DeleteTask, v1.DeleteTaskRequest{Id: in.ID})
	})
	addFamilyTool(s, "toggle_task", c, func(ctx context.Context, cl *FamilyServerClients, in famToggleTaskArgs) (any, error) {
		if err := famID("toggle_task", "id", in.ID); err != nil {
			return nil, err
		}
		if err := famEnum("toggle_task", "status", in.Status, "enabled", "disabled", "paused"); err != nil {
			return nil, err
		}
		st, _ := famProtoEnum("toggle_task", "status", in.Status, "TASK_STATUS_", v1.TaskStatus_value)
		res, err := cl.Task.ToggleTask(ctx, connect.NewRequest(&v1.ToggleTaskRequest{Id: in.ID, Status: v1.TaskStatus(st)}))
		if err != nil {
			return nil, err
		}
		return &v1.ToggleTaskResponse{Task: redactTask(res.Msg.GetTask())}, nil
	})
	addFamilyTool(s, "trigger_task", c, func(ctx context.Context, cl *FamilyServerClients, in famConfirmIDArgs) (any, error) {
		if err := famID("trigger_task", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Task.TriggerTask, v1.TriggerTaskRequest{Id: in.ID})
	})
	addFamilyTool(s, "list_task_executions", c, func(ctx context.Context, cl *FamilyServerClients, in famTaskExecArgs) (any, error) {
		if err := famID("list_task_executions", "task_id", in.TaskID); err != nil {
			return nil, err
		}
		limit, err := famLimit("list_task_executions", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Task.ListTaskExecutions, v1.ListTaskExecutionsRequest{TaskId: in.TaskID, Limit: limit})
	})
	addFamilyTool(s, "list_server_executions", c, func(ctx context.Context, cl *FamilyServerClients, in famServerLimitArgs) (any, error) {
		if err := famID("list_server_executions", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		limit, err := famLimit("list_server_executions", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Task.ListServerExecutions, v1.ListServerExecutionsRequest{ServerId: in.ServerID, Limit: limit})
	})
	addFamilyTool(s, "get_task_execution", c, func(ctx context.Context, cl *FamilyServerClients, in famIDArgs) (any, error) {
		if err := famID("get_task_execution", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Task.GetTaskExecution, v1.GetTaskExecutionRequest{Id: in.ID})
	})
	addFamilyTool(s, "cancel_execution", c, func(ctx context.Context, cl *FamilyServerClients, in famConfirmIDArgs) (any, error) {
		if err := famID("cancel_execution", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Task.CancelExecution, v1.CancelExecutionRequest{Id: in.ID})
	})
	addFamilyTool(s, "get_scheduler_status", c, func(ctx context.Context, cl *FamilyServerClients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.Task.GetSchedulerStatus, v1.GetSchedulerStatusRequest{})
	})

	// --- ActivityService ---------------------------------------------------
	addFamilyTool(s, "list_activity_logs", c, func(ctx context.Context, cl *FamilyServerClients, in famActivityArgs) (any, error) {
		if err := famID("list_activity_logs", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := famText("list_activity_logs", "actor", in.Actor, maxIDLen, false); err != nil {
			return nil, err
		}
		limit, err := famLimit("list_activity_logs", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Activity.ListActivityLogs, v1.ListActivityLogsRequest{ServerId: in.ServerID, Actor: in.Actor, Limit: limit})
	})

	// --- OperationService --------------------------------------------------
	addFamilyTool(s, "list_operations", c, func(ctx context.Context, cl *FamilyServerClients, in famListOperationsArgs) (any, error) {
		if err := famID("list_operations", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if in.Outcome != "" {
			if err := famEnum("list_operations", "outcome", in.Outcome, "success", "failed", "running", "unknown"); err != nil {
				return nil, err
			}
		}
		limit, err := famLimit("list_operations", in.Limit)
		if err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Operation.ListOperations, v1.ListOperationsRequest{ServerId: in.ServerID, Outcome: strings.ToLower(in.Outcome), Limit: limit})
	})
	addFamilyTool(s, "get_operation", c, func(ctx context.Context, cl *FamilyServerClients, in famGetOperationArgs) (any, error) {
		if err := famID("get_operation", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := famID("get_operation", "operation_id", in.OperationID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Operation.GetOperation, v1.GetOperationRequest{ServerId: in.ServerID, OperationId: in.OperationID})
	})
	addFamilyTool(s, "resolve_operation", c, func(ctx context.Context, cl *FamilyServerClients, in famResolveOperationArgs) (any, error) {
		if err := famID("resolve_operation", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := famID("resolve_operation", "operation_id", in.OperationID); err != nil {
			return nil, err
		}
		if err := famText("resolve_operation", "note", in.Note, 256, false); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Operation.ResolveOperation, v1.ResolveOperationRequest{
			ServerId: in.ServerID, OperationId: in.OperationID, Note: in.Note,
		})
	})

	// --- BlueprintService --------------------------------------------------
	addFamilyTool(s, "list_blueprints", c, func(ctx context.Context, cl *FamilyServerClients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.Blueprint.ListBlueprints, v1.ListBlueprintsRequest{})
	})
	addFamilyTool(s, "get_blueprint", c, func(ctx context.Context, cl *FamilyServerClients, in famIDArgs) (any, error) {
		if err := famID("get_blueprint", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Blueprint.GetBlueprint, v1.GetBlueprintRequest{Id: in.ID})
	})

	// --- MinecraftService --------------------------------------------------
	addFamilyTool(s, "get_minecraft_versions", c, func(ctx context.Context, cl *FamilyServerClients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.Minecraft.GetMinecraftVersions, v1.GetMinecraftVersionsRequest{})
	})
	addFamilyTool(s, "get_mod_loaders", c, func(ctx context.Context, cl *FamilyServerClients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.Minecraft.GetModLoaders, v1.GetModLoadersRequest{})
	})
	addFamilyTool(s, "get_docker_images", c, func(ctx context.Context, cl *FamilyServerClients, _ emptyArgs) (any, error) {
		return invoke(ctx, cl.Minecraft.GetDockerImages, v1.GetDockerImagesRequest{})
	})
	addFamilyTool(s, "get_loader_versions", c, func(ctx context.Context, cl *FamilyServerClients, in famLoaderVersionsArgs) (any, error) {
		if err := famText("get_loader_versions", "loader", in.Loader, 32, true); err != nil {
			return nil, err
		}
		if err := famText("get_loader_versions", "mc_version", in.MCVersion, 32, true); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Minecraft.GetLoaderVersions, v1.GetLoaderVersionsRequest{Loader: in.Loader, McVersion: in.MCVersion})
	})

	// --- SupportService ----------------------------------------------------
	addFamilyTool(s, "get_application_logs", c, func(ctx context.Context, cl *FamilyServerClients, in famTailArgs) (any, error) {
		tail, err := famTail("get_application_logs", in.Tail)
		if err != nil {
			return nil, err
		}
		res, err := cl.Support.GetApplicationLogs(ctx, connect.NewRequest(&v1.GetApplicationLogsRequest{Tail: tail}))
		if err != nil {
			return nil, err
		}
		content := res.Msg.GetContent()
		truncated := len(content) > maxFileBytes
		if truncated {
			content = content[:maxFileBytes]
		}
		return famAppLogsResult{Content: content, Filename: res.Msg.GetFilename(), Size: res.Msg.GetSize(), Truncated: truncated}, nil
	})
	addFamilyTool(s, "generate_support_bundle", c, func(ctx context.Context, cl *FamilyServerClients, in famGenerateBundleArgs) (any, error) {
		var ids []string
		if in.ServerIDs != "" {
			for _, id := range strings.Split(in.ServerIDs, ",") {
				id = strings.TrimSpace(id)
				if err := famID("generate_support_bundle", "server_ids", id); err != nil {
					return nil, err
				}
				ids = append(ids, id)
			}
			if len(ids) > maxLimit {
				return nil, famErr("generate_support_bundle", "server_ids", "at most 100 server ids")
			}
		}
		return invoke(ctx, cl.Support.GenerateSupportBundle, v1.GenerateSupportBundleRequest{
			IncludeLogs: in.IncludeLogs, IncludeConfigs: in.IncludeConfigs,
			IncludeSystemInfo: in.IncludeSystemInfo, ServerIds: ids,
		})
	})
	addFamilyTool(s, "upload_support_bundle", c, func(ctx context.Context, cl *FamilyServerClients, in famUploadBundleArgs) (any, error) {
		if err := famText("upload_support_bundle", "issue_description", in.IssueDescription, 2048, true); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Support.UploadSupportBundle, v1.UploadSupportBundleRequest{
			IncludeLogs: in.IncludeLogs, IncludeConfigs: in.IncludeConfigs,
			IncludeSystemInfo: in.IncludeSystemInfo, IssueDescription: in.IssueDescription,
		})
	})
	addFamilyTool(s, "download_support_bundle", c, func(ctx context.Context, cl *FamilyServerClients, in famBundleIDArgs) (any, error) {
		if err := famID("download_support_bundle", "bundle_id", in.BundleID); err != nil {
			return nil, err
		}
		res, err := cl.Support.DownloadSupportBundle(ctx, connect.NewRequest(&v1.DownloadSupportBundleRequest{BundleId: in.BundleID}))
		if err != nil {
			return nil, err
		}
		content := res.Msg.GetContent()
		truncated := len(content) > maxFileBytes
		if truncated {
			content = content[:maxFileBytes]
		}
		return famAppLogsResult{Content: string(content), Filename: res.Msg.GetFilename(), Truncated: truncated}, nil
	})

	// --- NodeService (known configured node ids only) ----------------------
	addFamilyTool(s, "ping_node", c, func(ctx context.Context, cl *FamilyServerClients, in famIDArgs) (any, error) {
		if err := famID("ping_node", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Node.PingNode, v1.PingNodeRequest{Id: in.ID})
	})
	addFamilyTool(s, "get_node", c, func(ctx context.Context, cl *FamilyServerClients, in famIDArgs) (any, error) {
		if err := famID("get_node", "id", in.ID); err != nil {
			return nil, err
		}
		res, err := cl.Node.GetNode(ctx, connect.NewRequest(&v1.GetNodeRequest{Id: in.ID}))
		if err != nil {
			return nil, err
		}
		return &v1.GetNodeResponse{Node: redactNode(res.Msg.GetNode())}, nil
	})
	addFamilyTool(s, "list_nodes", c, func(ctx context.Context, cl *FamilyServerClients, _ emptyArgs) (any, error) {
		res, err := cl.Node.ListNodes(ctx, connect.NewRequest(&v1.ListNodesRequest{}))
		if err != nil {
			return nil, err
		}
		return redactNodeListResponse(res.Msg), nil
	})
	addFamilyTool(s, "create_node", c, func(ctx context.Context, cl *FamilyServerClients, in famCreateNodeArgs) (any, error) {
		if err := famText("create_node", "name", in.Name, maxIDLen, true); err != nil {
			return nil, err
		}
		if err := famText("create_node", "host", in.Host, 512, true); err != nil {
			return nil, err
		}
		if err := famText("create_node", "advertised_ip", in.AdvertisedIP, 128, false); err != nil {
			return nil, err
		}
		if in.MaxMemoryMB != nil {
			if err := famInt64("create_node", "max_memory_mb", *in.MaxMemoryMB, 256, 1048576); err != nil {
				return nil, err
			}
		}
		if in.MaxServers != nil {
			if err := famInt("create_node", "max_servers", *in.MaxServers, 1, 10000); err != nil {
				return nil, err
			}
		}
		res, err := cl.Node.CreateNode(ctx, connect.NewRequest(&v1.CreateNodeRequest{
			Name: in.Name, Host: in.Host, AdvertisedIp: in.AdvertisedIP,
			MaxMemoryMb: famInt64Value(in.MaxMemoryMB), MaxServers: famIntValue(in.MaxServers),
			Enabled: in.Enabled,
		}))
		if err != nil {
			return nil, err
		}
		return &v1.CreateNodeResponse{Node: redactNode(res.Msg.GetNode())}, nil
	})
	addFamilyTool(s, "update_node", c, func(ctx context.Context, cl *FamilyServerClients, in famUpdateNodeArgs) (any, error) {
		if err := famID("update_node", "id", in.ID); err != nil {
			return nil, err
		}
		if err := famText("update_node", "name", in.Name, maxIDLen, false); err != nil {
			return nil, err
		}
		if err := famText("update_node", "host", in.Host, 512, false); err != nil {
			return nil, err
		}
		if in.MaxMemoryMB != nil {
			if err := famInt64("update_node", "max_memory_mb", *in.MaxMemoryMB, 256, 1048576); err != nil {
				return nil, err
			}
		}
		if in.MaxServers != nil {
			if err := famInt("update_node", "max_servers", *in.MaxServers, 1, 10000); err != nil {
				return nil, err
			}
		}
		req := &v1.UpdateNodeRequest{Id: in.ID, MaxMemoryMb: in.MaxMemoryMB, MaxServers: in.MaxServers, Enabled: in.Enabled}
		if in.Name != "" {
			req.Name = famStringPtr(in.Name)
		}
		if in.Host != "" {
			req.Host = famStringPtr(in.Host)
		}
		res, err := cl.Node.UpdateNode(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		return &v1.UpdateNodeResponse{Node: redactNode(res.Msg.GetNode())}, nil
	})
	addFamilyTool(s, "delete_node", c, func(ctx context.Context, cl *FamilyServerClients, in famConfirmIDArgs) (any, error) {
		if err := famID("delete_node", "id", in.ID); err != nil {
			return nil, err
		}
		return invoke(ctx, cl.Node.DeleteNode, v1.DeleteNodeRequest{Id: in.ID})
	})
}
