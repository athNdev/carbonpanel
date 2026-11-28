// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

// This file is the machine-readable coverage-gap declaration for the
// remaining supported service families (OSS-371, child of OSS-370). It is
// deliberately additive: it does NOT change the shipped first-slice catalog,
// tool count or server instructions. The integration owner consumes
// FamilyServerIncludedDescriptors() when generating the catalog and wires the
// handlers in family_server_tools.go into registration.

// FamilyServerToolCount is the number of executable coverage-gap tools. It is
// pinned by TestFamilyServerDescriptorInventory so the exported list cannot
// drift silently.
const FamilyServerToolCount = 55

// FamilyServerExcludedRPCCount is the single mounted RPC in these families that
// stays out of the tool set: automatic Docker daemon discovery. Its exact
// source reason and supported equivalent are recorded in the descriptor list.
const FamilyServerExcludedRPCCount = 1

// FamilyServerArgKind is the validation primitive for one tool argument.
type FamilyServerArgKind string

const (
	FamilyArgID   FamilyServerArgKind = "id"   // single resource id, 1..maxIDLen
	FamilyArgText FamilyServerArgKind = "text" // bounded free text
	FamilyArgInt  FamilyServerArgKind = "int"  // inclusive numeric range
	FamilyArgBool FamilyServerArgKind = "bool"
	FamilyArgEnum FamilyServerArgKind = "enum" // closed allowlist
	FamilyArgMap  FamilyServerArgKind = "map"  // bounded key/value map
)

// FamilyServerArg describes one bounded argument. Every mutation carries a
// single resource id (or, for create, an idempotency request_id) and every
// list/count argument is capped in Go, not only in the JSON schema.
type FamilyServerArg struct {
	Name        string              `json:"name"`
	Kind        FamilyServerArgKind `json:"kind"`
	Required    bool                `json:"required,omitempty"`
	MaxLen      int                 `json:"max_len,omitempty"`
	Min         int64               `json:"min,omitempty"`
	Max         int64               `json:"max,omitempty"`
	Default     int64               `json:"default,omitempty"`
	MaxEntries  int                 `json:"max_entries,omitempty"`
	MaxKeyLen   int                 `json:"max_key_len,omitempty"`
	MaxValueLen int                 `json:"max_value_len,omitempty"`
	Allowed     []string            `json:"allowed,omitempty"`
	Description string              `json:"description"`
}

// FamilyServerExclusion is a stable reason code for a deliberately unexposed
// procedure or non-RPC surface.
type FamilyServerExclusion string

const (
	ExcludePrivateProtocol  FamilyServerExclusion = "private_protocol"
	ExcludeDiscovery        FamilyServerExclusion = "discovery"
	ExcludeEnrollmentTLS    FamilyServerExclusion = "enrollment_or_tls"
	ExcludeSecretDump       FamilyServerExclusion = "secret_dump"
	ExcludeThirdPartyEgress FamilyServerExclusion = "third_party_egress"
	ExcludeIrreversible     FamilyServerExclusion = "irreversible_destruction"
	ExcludeMaintenance      FamilyServerExclusion = "cli_only_maintenance"
	ExcludeBrowserHandshake FamilyServerExclusion = "browser_handshake"
	ExcludeNoTypedClient    FamilyServerExclusion = "no_typed_client"
)

// FamilyServerToolDescriptor is one declared tool tuple plus the bounded
// argument contract and exposure decision. Scope strings are canonical and
// equal internal/rbac.expectedScope for the procedure on the pinned build.
type FamilyServerToolDescriptor struct {
	Name        string            `json:"name"`
	Procedure   string            `json:"procedure"`
	Service     string            `json:"service"`
	Method      string            `json:"method"`
	Client      string            `json:"client"`
	Scope       string            `json:"scope"`
	Mutation    bool              `json:"mutation"`
	Confirm     bool              `json:"confirm"`
	Class       Class             `json:"class"`
	Build       PanelBuild        `json:"build"`
	Args        []FamilyServerArg `json:"args"`
	Redaction   []string          `json:"redaction,omitempty"`
	Description string            `json:"description"`

	ExclusionReason FamilyServerExclusion `json:"exclusion_reason,omitempty"`
	ExclusionDetail string                `json:"exclusion_detail,omitempty"`

	// Source is the exact file:line provenance for the RPC declaration and its
	// live RBAC mapping on the pinned build. It is attached after declaration
	// from familyServerSources so every decision can be re-verified.
	Source string `json:"source,omitempty"`
}

// famDesc derives the procedure, service, typed constructor and build from a
// compact declaration so the exported list stays mechanically consistent.
func famDesc(service, name, method, scope string, class Class, mutation, confirm bool, description string, args ...FamilyServerArg) FamilyServerToolDescriptor {
	build := PanelBuildStableV140
	if service == "ServerService" && (method == "ExportServerArchive" || method == "PreviewServerArchive") {
		build = PanelBuildServerPortability
	}
	return FamilyServerToolDescriptor{
		Name: name, Procedure: "/carbonpanel.v1." + service + "/" + method,
		Service: "carbonpanel.v1." + service, Method: method,
		Client: "New" + service + "Client", Scope: scope,
		Mutation: mutation, Confirm: confirm, Class: class, Build: build,
		Args: args, Description: description,
	}
}

func famExcluded(d FamilyServerToolDescriptor, reason FamilyServerExclusion, detail string) FamilyServerToolDescriptor {
	d.Class = ClassExclude
	d.ExclusionReason = reason
	d.ExclusionDetail = detail
	return d
}

func famIDArg(name, desc string) FamilyServerArg {
	return FamilyServerArg{Name: name, Kind: FamilyArgID, Required: true, MaxLen: maxIDLen, Description: desc}
}

func famOptIDArg(name, desc string) FamilyServerArg {
	return FamilyServerArg{Name: name, Kind: FamilyArgID, MaxLen: maxIDLen, Description: desc}
}

func famTextArg(name string, max int, required bool, desc string) FamilyServerArg {
	return FamilyServerArg{Name: name, Kind: FamilyArgText, Required: required, MaxLen: max, Description: desc}
}

func famIntArg(name string, min, max int64, required bool, def int64, desc string) FamilyServerArg {
	return FamilyServerArg{Name: name, Kind: FamilyArgInt, Required: required, Min: min, Max: max, Default: def, Description: desc}
}

func famBoolArg(name, desc string) FamilyServerArg {
	return FamilyServerArg{Name: name, Kind: FamilyArgBool, Description: desc}
}

func famEnumArg(name string, required bool, allowed []string, desc string) FamilyServerArg {
	return FamilyServerArg{Name: name, Kind: FamilyArgEnum, Required: required, Allowed: allowed, Description: desc}
}

func famMapArg(name string, entries, keyLen, valueLen int, desc string) FamilyServerArg {
	return FamilyServerArg{Name: name, Kind: FamilyArgMap, MaxEntries: entries, MaxKeyLen: keyLen, MaxValueLen: valueLen, Description: desc}
}

// FamilyServerDescriptors is the complete machine-readable declaration for the
// family: executable tools and deliberately excluded procedures. Consult
// FamilyServerIncludedDescriptors() for the tools to add to the catalog.
var FamilyServerDescriptors = buildFamilyServerDescriptors()

func buildFamilyServerDescriptors() []FamilyServerToolDescriptor {
	list := []FamilyServerToolDescriptor{
		// --- ServerService: remaining supported operations -----------------
		famDesc("ServerService", "get_server_logs", "GetServerLogs", "servers/read[id]", ClassSafe, false, false,
			"Read the last N lines of one server's container log. Read-only; tail is capped.",
			famIDArg("id", "exact server id"), famIntArg("tail", 1, 1000, false, 100, "last N log lines")),
		famDesc("ServerService", "clear_server_logs", "ClearServerLogs", "servers/update[id]", ClassReview, true, true,
			"Clear one server's container log. Requires confirm=true and an exact server id.",
			famIDArg("id", "exact server id"), famBoolArg("confirm", "must be true to clear logs")),
		famDesc("ServerService", "get_next_available_port", "GetNextAvailablePort", "servers/read", ClassSafe, false, false,
			"Report the next free host port and current port usage. Read-only."),
		famDesc("ServerService", "export_server_archive", "ExportServerArchive", "files/read[server_id]", ClassReview, false, false,
			"Prepare a verified ZIP or tar.gz export of one stopped server. Returns a scoped download session.",
			famIDArg("server_id", "exact stopped server id"),
			FamilyServerArg{Name: "format", Kind: FamilyArgEnum, Required: true, Allowed: []string{"zip", "tar.gz"}}),
		famDesc("ServerService", "preview_server_archive", "PreviewServerArchive", "servers/create", ClassSafe, false, false,
			"Validate one caller-owned server archive and report destination requirements.",
			famIDArg("upload_id", "caller-owned completed archive upload"), famOptIDArg("node_id", "known destination node")),
		famDesc("ServerService", "create_server", "CreateServer", "servers/create", ClassReview, true, true,
			"Create one server from bounded scalar options. Requires confirm=true and a request_id.",
			famTextArg("name", maxIDLen, true, "exact server name"),
			famTextArg("mc_version", 32, false, "Minecraft version"),
			famTextArg("mod_loader", 32, false, "mod loader name from the proto enum"),
			famIntArg("port", 1, 65535, false, 0, "host port"),
			famIntArg("max_players", 1, 200, false, 0, "player cap"),
			famIntArg("memory", 256, 65536, false, 0, "memory in MiB"),
			famOptIDArg("node_id", "known configured node id only; no discovery"),
			famOptIDArg("request_id", "idempotency key, required with archive_upload_id"),
			famOptIDArg("archive_upload_id", "caller-owned completed archive upload"),
			famBoolArg("start_immediately", "start after creation"),
			famBoolArg("confirm", "must be true to create a server")),
		famDesc("ServerService", "update_server", "UpdateServer", "servers/update[id]", ClassReview, true, true,
			"Update bounded scalar settings of one server. Requires confirm=true and an exact id.",
			famIDArg("id", "exact server id"),
			famTextArg("name", maxIDLen, false, "new server name"),
			famTextArg("description", 256, false, "new description"),
			famIntArg("port", 1, 65535, false, 0, "host port"),
			famIntArg("max_players", 1, 200, false, 0, "player cap"),
			famIntArg("memory", 256, 65536, false, 0, "memory in MiB"),
			famBoolArg("auto_start", "start on controller boot"),
			famBoolArg("confirm", "must be true to update a server")),
		famDesc("ServerService", "recreate_server", "RecreateServer", "servers/restart[id]", ClassReview, true, true,
			"Destroy and recreate one server container from its retained data. Requires confirm=true.",
			famIDArg("id", "exact server id"), famBoolArg("confirm", "must be true to recreate a container")),
		famDesc("ServerService", "pause_server", "PauseServer", "servers/stop[id]", ClassReview, true, true,
			"Freeze one running server with the cgroup freezer while retaining its container and world. Requires confirm=true.",
			famIDArg("id", "exact server id"), famOptIDArg("request_id", "idempotency key"),
			famBoolArg("confirm", "must be true to freeze a server")),
		famDesc("ServerService", "deep_sleep_server", "DeepSleepServer", "servers/stop[id]", ClassReview, true, true,
			"Park one running server into deep sleep while retaining its world and proxy route. Requires confirm=true.",
			famIDArg("id", "exact server id"), famOptIDArg("request_id", "idempotency key"),
			famBoolArg("confirm", "must be true to put a server into deep sleep")),
		famDesc("ServerService", "list_server_players", "ListServerPlayers", "servers/read[server_id]", ClassSafe, false, false,
			"Read the live online player list for one server. Read-only metrics; no health endpoint.",
			famIDArg("server_id", "exact server id")),

		// Ordinary authenticated administration stays in scope. Each mutation
		// carries the exact live servers/* scope, a single server id and a
		// confirm gate; destructive variants require a data-preserving option.
		famDesc("ServerService", "delete_server", "DeleteServer", "servers/delete[id]", ClassReview, true, true,
			"Remove one server record. confirm=true plus keep_data=true or detach=true is required; an unqualified world-destroying delete is refused.",
			famIDArg("id", "exact server id"),
			famBoolArg("keep_data", "retain the world directory after deleting the server"),
			famBoolArg("detach", "retain container and world, keep the reserved port"),
			famBoolArg("confirm", "must be true to delete a server")),
		famDesc("ServerService", "send_command", "SendCommand", "servers/command[id]", ClassReview, true, true,
			"Run one bounded operational console command on a single server after confirm=true. This is not a general shell: the command is length-capped and must be an allowlisted read/maintenance verb.",
			famIDArg("id", "exact server id"),
			famEnumArg("command", true, famSafeConsoleCommands, "allowlisted operational command"),
			famBoolArg("confirm", "must be true to send a command")),
		famDesc("ServerService", "upload_to_mclogs", "UploadToMCLogs", "servers/read[id]", ClassReview, true, true,
			"Upload one server's log to the configured paste service after confirm=true. Equivalent supported flow: get_server_logs reads the same log locally without egress.",
			famIDArg("id", "exact server id"), famBoolArg("confirm", "must be true to upload logs")),
		famDesc("ServerService", "migrate_server", "MigrateServer", "servers/update[id]", ClassReview, true, true,
			"Move one server to a known configured node. Requires confirm=true, a request_id and an exact node id; no discovery fallback.",
			famIDArg("id", "exact server id"), famIDArg("target_node_id", "known configured node id"),
			famOptIDArg("request_id", "idempotency key"), famBoolArg("force", "proceed despite preflight warnings"),
			famBoolArg("confirm", "must be true to migrate a server")),

		// --- ConfigService (all 8 RPCs; secrets redacted on read) -----------
		famDesc("ConfigService", "get_server_config", "GetServerConfig", "server_config/read[server_id]", ClassReview, false, false,
			"Read one server's categorized config. password-typed and secret-named values are redacted.",
			famIDArg("server_id", "exact server id")),
		famDesc("ConfigService", "update_server_config", "UpdateServerConfig", "server_config/update[server_id]", ClassReview, true, true,
			"Apply bounded config updates to one server; prefer stage=true. Requires confirm=true.",
			famIDArg("server_id", "exact server id"),
			famMapArg("updates", 16, maxIDLen, 1024, "bounded key/value updates"),
			famBoolArg("stage", "record the diff without recreating the container"),
			famBoolArg("confirm", "must be true to change config")),
		famDesc("ConfigService", "get_update_batch", "GetUpdateBatch", "server_config/read[server_id]", ClassSafe, false, false,
			"Read one server's open staged batch. Read-only.", famIDArg("server_id", "exact server id")),
		famDesc("ConfigService", "discard_update_batch", "DiscardUpdateBatch", "server_config/update[server_id]", ClassReview, true, true,
			"Discard one staged item or the whole batch for one server. Requires confirm=true.",
			famIDArg("server_id", "exact server id"),
			famOptIDArg("item_id", "empty discards the whole open batch"),
			famBoolArg("confirm", "must be true to discard staged changes")),
		famDesc("ConfigService", "get_global_settings", "GetGlobalSettings", "settings/read", ClassReview, false, false,
			"Read global defaults. password-typed and secret-named values are redacted."),
		famDesc("ConfigService", "update_global_settings", "UpdateGlobalSettings", "settings/update", ClassReview, true, true,
			"Update bounded global defaults. Requires confirm=true and an allowlisted set of at most 16 scalar values.",
			famMapArg("updates", 16, maxIDLen, 1024, "bounded key/value updates"),
			famBoolArg("confirm", "must be true to change global settings")),
		famDesc("ConfigService", "sync_global_settings_to_servers", "SyncGlobalSettingsToServers", "settings/update", ClassReview, true, true,
			"Propagate global defaults to existing servers. Requires confirm=true; ops_and_whitelist_only narrows the fan-out.",
			famBoolArg("ops_and_whitelist_only", "restrict to Ops and Whitelist categories"),
			famBoolArg("confirm", "must be true to sync global settings")),
		famDesc("ConfigService", "validate_provider_key", "ValidateProviderKey", "settings/read", ClassReview, false, true,
			"Check a content-provider credential without storing it. Requires confirm=true; the key is never echoed or logged.",
			famEnumArg("provider", true, []string{"curseforge", "modrinth"}, "provider name"),
			famTextArg("api_key", 512, false, "provider key; empty performs an anonymous connectivity check"),
			famTextArg("user_agent", 128, false, "optional custom user agent"),
			famBoolArg("confirm", "must be true to use a credential")),

		// --- TaskService (all 12 RPCs; ScheduledTask.config redacted) -------
		famDesc("TaskService", "list_tasks", "ListTasks", "tasks/read[server_id]", ClassSafe, false, false,
			"List scheduled tasks for one server. Task config is redacted.", famIDArg("server_id", "exact server id")),
		famDesc("TaskService", "get_task", "GetTask", "tasks/read[id]", ClassSafe, false, false,
			"Get one scheduled task. Task config is redacted.", famIDArg("id", "exact task id")),
		famDesc("TaskService", "create_task", "CreateTask", "tasks/create[server_id]", ClassReview, true, true,
			"Create one bounded scheduled task. SCRIPT/WEBHOOK/MODPACK_UPDATE are refused. Requires confirm=true.",
			famIDArg("server_id", "exact server id"), famTextArg("name", maxIDLen, true, "task name"),
			famEnumArg("task_type", true, famSafeTaskTypes, "allowlisted task type"),
			famEnumArg("schedule", true, []string{"cron", "interval", "once", "event"}, "schedule type"),
			famTextArg("cron_expr", 64, false, "cron expression"),
			famIntArg("interval_secs", 1, 604800, false, 0, "fixed interval"),
			famIntArg("timeout", 1, 86400, false, 0, "execution timeout"),
			famTextArg("config", 4096, false, "task config JSON"),
			famBoolArg("confirm", "must be true to create a task")),
		famDesc("TaskService", "update_task", "UpdateTask", "tasks/update[id]", ClassReview, true, true,
			"Update bounded fields of one task. Dangerous task types are refused. Requires confirm=true.",
			famIDArg("id", "exact task id"),
			famTextArg("name", maxIDLen, false, "new task name"),
			famEnumArg("task_type", false, famSafeTaskTypes, "allowlisted task type"),
			famTextArg("config", 4096, false, "task config JSON"),
			famBoolArg("confirm", "must be true to update a task")),
		famDesc("TaskService", "delete_task", "DeleteTask", "tasks/delete[id]", ClassReview, true, true,
			"Delete one scheduled task. Requires confirm=true.", famIDArg("id", "exact task id"),
			famBoolArg("confirm", "must be true to delete a task")),
		famDesc("TaskService", "toggle_task", "ToggleTask", "tasks/update[id]", ClassReview, true, true,
			"Enable, disable or pause one task. Requires confirm=true.", famIDArg("id", "exact task id"),
			famEnumArg("status", true, []string{"enabled", "disabled", "paused"}, "target status"),
			famBoolArg("confirm", "must be true to toggle a task")),
		famDesc("TaskService", "trigger_task", "TriggerTask", "tasks/update[id]", ClassReview, true, true,
			"Run one task now. Requires confirm=true.", famIDArg("id", "exact task id"),
			famBoolArg("confirm", "must be true to trigger a task")),
		famDesc("TaskService", "list_task_executions", "ListTaskExecutions", "tasks/read[task_id]", ClassSafe, false, false,
			"List recent executions of one task with a bounded limit.",
			famIDArg("task_id", "exact task id"), famIntArg("limit", 1, maxLimit, false, defaultLimit, "result limit")),
		famDesc("TaskService", "list_server_executions", "ListServerExecutions", "tasks/read[server_id]", ClassSafe, false, false,
			"List recent executions for one server with a bounded limit.",
			famIDArg("server_id", "exact server id"), famIntArg("limit", 1, maxLimit, false, defaultLimit, "result limit")),
		famDesc("TaskService", "get_task_execution", "GetTaskExecution", "tasks/read[id]", ClassSafe, false, false,
			"Get one task execution record.", famIDArg("id", "exact execution id")),
		famDesc("TaskService", "cancel_execution", "CancelExecution", "tasks/update[id]", ClassReview, true, true,
			"Cancel one running execution. Requires confirm=true.", famIDArg("id", "exact execution id"),
			famBoolArg("confirm", "must be true to cancel an execution")),
		famDesc("TaskService", "get_scheduler_status", "GetSchedulerStatus", "tasks/read", ClassSafe, false, false,
			"Read scheduler running state and counters. Read-only."),

		// --- ActivityService -------------------------------------------------
		famDesc("ActivityService", "list_activity_logs", "ListActivityLogs", "activity/read[server_id]", ClassSafe, false, false,
			"Read the audit trail for one server with a bounded limit.",
			famIDArg("server_id", "exact server id"), famTextArg("actor", maxIDLen, false, "optional actor filter"),
			famIntArg("limit", 1, maxLimit, false, defaultLimit, "result limit")),

		// --- OperationService ------------------------------------------------
		famDesc("OperationService", "list_operations", "ListOperations", "servers/read[server_id]", ClassSafe, false, false,
			"Read one server's lifecycle journal. Responses are already redacted.",
			famIDArg("server_id", "exact server id"),
			famEnumArg("outcome", false, []string{"success", "failed", "running", "unknown"}, "outcome filter"),
			famIntArg("limit", 1, maxLimit, false, defaultLimit, "result limit")),
		famDesc("OperationService", "get_operation", "GetOperation", "servers/read[server_id]", ClassSafe, false, false,
			"Read one lifecycle operation by server and id.",
			famIDArg("server_id", "exact server id"), famIDArg("operation_id", "exact operation id")),
		famDesc("OperationService", "resolve_operation", "ResolveOperation", "servers/update[server_id]", ClassReview, true, true,
			"Release one lifecycle lock without touching containers. Requires confirm=true.",
			famIDArg("server_id", "exact server id"), famIDArg("operation_id", "exact operation id"),
			famTextArg("note", 256, false, "operator note"), famBoolArg("confirm", "must be true to release a lock")),

		// --- BlueprintService ------------------------------------------------
		famDesc("BlueprintService", "list_blueprints", "ListBlueprints", "servers/read", ClassSafe, false, false,
			"List server flavor blueprints. Read-only."),
		famDesc("BlueprintService", "get_blueprint", "GetBlueprint", "servers/read", ClassSafe, false, false,
			"Get one server flavor blueprint.", famIDArg("id", "exact blueprint id")),

		// --- MinecraftService (authenticated-only) ---------------------------
		famDesc("MinecraftService", "get_minecraft_versions", "GetMinecraftVersions", "authenticated", ClassSafe, false, false,
			"List Minecraft version metadata. Authenticated-only read."),
		famDesc("MinecraftService", "get_mod_loaders", "GetModLoaders", "authenticated", ClassSafe, false, false,
			"List supported mod loaders. Authenticated-only read."),
		famDesc("MinecraftService", "get_docker_images", "GetDockerImages", "authenticated", ClassSafe, false, false,
			"List available server container images. Authenticated-only read."),
		famDesc("MinecraftService", "get_loader_versions", "GetLoaderVersions", "authenticated", ClassSafe, false, false,
			"List exact loader versions for one loader and Minecraft version.",
			famTextArg("loader", 32, true, "loader name"), famTextArg("mc_version", 32, true, "Minecraft version")),

		// --- SupportService (bundle families excluded) -----------------------
		famDesc("SupportService", "get_application_logs", "GetApplicationLogs", "support/read", ClassReview, false, false,
			"Read a bounded tail of controller application logs. Logs may contain secrets; content is capped.",
			famIntArg("tail", 1, 1000, false, 100, "last N log lines")),
		famDesc("SupportService", "generate_support_bundle", "GenerateSupportBundle", "support/create", ClassReview, true, true,
			"Create a local diagnostic bundle with bounded server ids and flags. Requires confirm=true; nothing is transmitted.",
			famBoolArg("include_logs", "include application logs"),
			famBoolArg("include_configs", "include configuration"),
			famBoolArg("include_system_info", "include system information"),
			famOptIDArg("server_ids", "comma-separated known server ids; empty means all"),
			famBoolArg("confirm", "must be true to generate a bundle")),
		famDesc("SupportService", "upload_support_bundle", "UploadSupportBundle", "support/create", ClassReview, true, true,
			"Send a diagnostic bundle to the configured support destination after confirm=true. Equivalent supported flow: generate_support_bundle plus an operator-controlled transfer.",
			famBoolArg("include_logs", "include application logs"),
			famBoolArg("include_configs", "include configuration"),
			famBoolArg("include_system_info", "include system information"),
			famTextArg("issue_description", 2048, true, "description of the issue"),
			famBoolArg("confirm", "must be true to upload a bundle")),
		famDesc("SupportService", "download_support_bundle", "DownloadSupportBundle", "support/read", ClassReview, false, false,
			"Read one generated support bundle. Content is capped in Go before it is returned to the client.",
			famIDArg("bundle_id", "exact bundle id")),

		// --- NodeService (known configured node ids only; no discovery/TLS) --
		famDesc("NodeService", "ping_node", "PingNode", "nodes/read[id]", ClassSafe, false, false,
			"Ping one configured node by exact id for status and latency. No discovery, no TLS material.",
			famIDArg("id", "known configured node id")),
		famDesc("NodeService", "get_node", "GetNode", "nodes/read[id]", ClassReview, false, false,
			"Get status of one configured node by exact id. TLS certificate and key fields are removed.",
			famIDArg("id", "known configured node id")),
		famDesc("NodeService", "list_nodes", "ListNodes", "nodes/read", ClassReview, false, false,
			"List configured nodes with runtime status. TLS CA/cert/key fields are removed from every response before it leaves the gateway."),
		famExcluded(famDesc("NodeService", "scan_nodes", "ScanNodes", "nodes/read", ClassExclude, false, false,
			"Automatic local-interface Docker daemon discovery. Not an agent tool; use ping_node/get_node with a known configured id or the panel's node configuration screen."),
			ExcludeDiscovery, "node.proto:68-69 + internal/rbac/mapping.go:173 + internal/rpc/server.go:308 define a daemon discovery probe, not a scoped data operation."),
		famDesc("NodeService", "create_node", "CreateNode", "nodes/create", ClassReview, true, true,
			"Register one Docker node from bounded non-secret settings. Requires confirm=true. TLS enrollment material is configured in the panel UI, not through this tool.",
			famTextArg("name", maxIDLen, true, "node name"),
			famTextArg("host", 512, true, "Docker daemon host"),
			famTextArg("advertised_ip", 128, false, "advertised address"),
			famIntArg("max_memory_mb", 256, 1048576, false, 0, "memory budget"),
			famIntArg("max_servers", 1, 10000, false, 0, "server cap"),
			famBoolArg("enabled", "enable the node"),
			famBoolArg("confirm", "must be true to register a node")),
		famDesc("NodeService", "update_node", "UpdateNode", "nodes/update[id]", ClassReview, true, true,
			"Update bounded non-secret settings of one node. Requires confirm=true; TLS material is out of band.",
			famIDArg("id", "known configured node id"),
			famTextArg("name", maxIDLen, false, "new name"),
			famTextArg("host", 512, false, "new Docker daemon host"),
			famIntArg("max_memory_mb", 256, 1048576, false, 0, "memory budget"),
			famIntArg("max_servers", 1, 10000, false, 0, "server cap"),
			famBoolArg("enabled", "enable or disable the node"),
			famBoolArg("confirm", "must be true to update a node")),
		famDesc("NodeService", "delete_node", "DeleteNode", "nodes/delete[id]", ClassReview, true, true,
			"Remove one node registration by exact configured id. Requires confirm=true; world data and Docker state are untouched.",
			famIDArg("id", "known configured node id"), famBoolArg("confirm", "must be true to remove a node")),
	}
	for i := range list {
		if src, ok := familyServerSources[list[i].Procedure]; ok {
			list[i].Source = src
		}
	}
	return list
}

// famSafeTaskTypes is the allowlist for task creation and task-type edits.
// SCRIPT, WEBHOOK and MODPACK_UPDATE can execute arbitrary code, reach external
// endpoints or carry an auth_token, so they stay outside the agent catalog.
var famSafeTaskTypes = []string{"command", "backup", "restart", "start", "stop", "chunky_pregen"}

// famSafeConsoleCommands bounds ServerService/SendCommand to read-only and
// routine maintenance verbs. The RPC remains fully RBAC-scoped
// (servers/command[id]) and confirm-gated; the allowlist only removes the
// "free-form" property that made the original operation unsuitable for agents.
var famSafeConsoleCommands = []string{"list", "tps", "save-all", "say", "whitelist list"}

// familyServerSources pins the exact declaration and RBAC line for every RPC
// in this family on the build these descriptors were derived from. A scope
// change or a moved RPC must be reconciled against these references.
var familyServerSources = map[string]string{
	"/carbonpanel.v1.ServerService/GetServerLogs":               "internal/rbac/mapping.go:52; proto/carbonpanel/v1/server.proto:26",
	"/carbonpanel.v1.ServerService/ClearServerLogs":             "internal/rbac/mapping.go:53; proto/carbonpanel/v1/server.proto:28",
	"/carbonpanel.v1.ServerService/GetNextAvailablePort":        "internal/rbac/mapping.go:54; proto/carbonpanel/v1/server.proto:30",
	"/carbonpanel.v1.ServerService/ExportServerArchive":         "internal/rbac/mapping.go:67; proto/carbonpanel/v1/server.proto:34",
	"/carbonpanel.v1.ServerService/PreviewServerArchive":        "internal/rbac/mapping.go:68; proto/carbonpanel/v1/server.proto:36",
	"/carbonpanel.v1.ServerService/CreateServer":                "internal/rbac/mapping.go:55; proto/carbonpanel/v1/server.proto:32",
	"/carbonpanel.v1.ServerService/UpdateServer":                "internal/rbac/mapping.go:56; proto/carbonpanel/v1/server.proto:34",
	"/carbonpanel.v1.ServerService/DeleteServer":                "internal/rbac/mapping.go:57; proto/carbonpanel/v1/server.proto:36",
	"/carbonpanel.v1.ServerService/RecreateServer":              "internal/rbac/mapping.go:63; proto/carbonpanel/v1/server.proto:48",
	"/carbonpanel.v1.ServerService/PauseServer":                 "internal/rbac/mapping.go:60; proto/carbonpanel/v1/server.proto:42",
	"/carbonpanel.v1.ServerService/DeepSleepServer":             "internal/rbac/mapping.go:61; proto/carbonpanel/v1/server.proto:44",
	"/carbonpanel.v1.ServerService/SendCommand":                 "internal/rbac/mapping.go:64; proto/carbonpanel/v1/server.proto:50",
	"/carbonpanel.v1.ServerService/UploadToMCLogs":              "internal/rbac/mapping.go:65; proto/carbonpanel/v1/server.proto:52",
	"/carbonpanel.v1.ServerService/MigrateServer":               "internal/rbac/mapping.go:66; proto/carbonpanel/v1/server.proto:54",
	"/carbonpanel.v1.ServerService/ListServerPlayers":           "internal/rbac/mapping.go:67; proto/carbonpanel/v1/server.proto:56",
	"/carbonpanel.v1.ConfigService/GetServerConfig":             "internal/rbac/mapping.go:76; proto/carbonpanel/v1/config.proto:19",
	"/carbonpanel.v1.ConfigService/UpdateServerConfig":          "internal/rbac/mapping.go:77; proto/carbonpanel/v1/config.proto:21",
	"/carbonpanel.v1.ConfigService/GetUpdateBatch":              "internal/rbac/mapping.go:78; proto/carbonpanel/v1/config.proto:23",
	"/carbonpanel.v1.ConfigService/DiscardUpdateBatch":          "internal/rbac/mapping.go:79; proto/carbonpanel/v1/config.proto:25",
	"/carbonpanel.v1.ConfigService/GetGlobalSettings":           "internal/rbac/mapping.go:80; proto/carbonpanel/v1/config.proto:27",
	"/carbonpanel.v1.ConfigService/UpdateGlobalSettings":        "internal/rbac/mapping.go:81; proto/carbonpanel/v1/config.proto:29",
	"/carbonpanel.v1.ConfigService/SyncGlobalSettingsToServers": "internal/rbac/mapping.go:82; proto/carbonpanel/v1/config.proto:31",
	"/carbonpanel.v1.ConfigService/ValidateProviderKey":         "internal/rbac/mapping.go:83; proto/carbonpanel/v1/config.proto:35",
	"/carbonpanel.v1.TaskService/ListTasks":                     "internal/rbac/mapping.go:200; proto/carbonpanel/v1/task.proto:22",
	"/carbonpanel.v1.TaskService/GetTask":                       "internal/rbac/mapping.go:201; proto/carbonpanel/v1/task.proto:24",
	"/carbonpanel.v1.TaskService/CreateTask":                    "internal/rbac/mapping.go:202; proto/carbonpanel/v1/task.proto:26",
	"/carbonpanel.v1.TaskService/UpdateTask":                    "internal/rbac/mapping.go:203; proto/carbonpanel/v1/task.proto:28",
	"/carbonpanel.v1.TaskService/DeleteTask":                    "internal/rbac/mapping.go:204; proto/carbonpanel/v1/task.proto:30",
	"/carbonpanel.v1.TaskService/ToggleTask":                    "internal/rbac/mapping.go:205; proto/carbonpanel/v1/task.proto:32",
	"/carbonpanel.v1.TaskService/TriggerTask":                   "internal/rbac/mapping.go:206; proto/carbonpanel/v1/task.proto:34",
	"/carbonpanel.v1.TaskService/ListTaskExecutions":            "internal/rbac/mapping.go:207; proto/carbonpanel/v1/task.proto:36",
	"/carbonpanel.v1.TaskService/ListServerExecutions":          "internal/rbac/mapping.go:208; proto/carbonpanel/v1/task.proto:38",
	"/carbonpanel.v1.TaskService/GetTaskExecution":              "internal/rbac/mapping.go:209; proto/carbonpanel/v1/task.proto:40",
	"/carbonpanel.v1.TaskService/CancelExecution":               "internal/rbac/mapping.go:210; proto/carbonpanel/v1/task.proto:42",
	"/carbonpanel.v1.TaskService/GetSchedulerStatus":            "internal/rbac/mapping.go:211; proto/carbonpanel/v1/task.proto:44",
	"/carbonpanel.v1.ActivityService/ListActivityLogs":          "internal/rbac/mapping.go:214; proto/carbonpanel/v1/activity.proto:18",
	"/carbonpanel.v1.OperationService/ListOperations":           "internal/rbac/mapping.go:221; proto/carbonpanel/v1/operation.proto:25",
	"/carbonpanel.v1.OperationService/GetOperation":             "internal/rbac/mapping.go:222; proto/carbonpanel/v1/operation.proto:28",
	"/carbonpanel.v1.OperationService/ResolveOperation":         "internal/rbac/mapping.go:223; proto/carbonpanel/v1/operation.proto:30",
	"/carbonpanel.v1.BlueprintService/ListBlueprints":           "internal/rbac/mapping.go:231; proto/carbonpanel/v1/blueprint.proto:16",
	"/carbonpanel.v1.BlueprintService/GetBlueprint":             "internal/rbac/mapping.go:232; proto/carbonpanel/v1/blueprint.proto:18",
	"/carbonpanel.v1.MinecraftService/GetMinecraftVersions":     "internal/rbac/mapping.go:40; proto/carbonpanel/v1/minecraft.proto:19",
	"/carbonpanel.v1.MinecraftService/GetModLoaders":            "internal/rbac/mapping.go:41; proto/carbonpanel/v1/minecraft.proto:21",
	"/carbonpanel.v1.MinecraftService/GetDockerImages":          "internal/rbac/mapping.go:42; proto/carbonpanel/v1/minecraft.proto:23",
	"/carbonpanel.v1.MinecraftService/GetLoaderVersions":        "internal/rbac/mapping.go:43; proto/carbonpanel/v1/minecraft.proto:25",
	"/carbonpanel.v1.SupportService/GenerateSupportBundle":      "internal/rbac/mapping.go:261; proto/carbonpanel/v1/support.proto:21",
	"/carbonpanel.v1.SupportService/DownloadSupportBundle":      "internal/rbac/mapping.go:262; proto/carbonpanel/v1/support.proto:23",
	"/carbonpanel.v1.SupportService/UploadSupportBundle":        "internal/rbac/mapping.go:263; proto/carbonpanel/v1/support.proto:25",
	"/carbonpanel.v1.SupportService/GetApplicationLogs":         "internal/rbac/mapping.go:264; proto/carbonpanel/v1/support.proto:27",
	"/carbonpanel.v1.NodeService/ListNodes":                     "internal/rbac/mapping.go:172; proto/carbonpanel/v1/node.proto:57",
	"/carbonpanel.v1.NodeService/ScanNodes":                     "internal/rbac/mapping.go:173; proto/carbonpanel/v1/node.proto:69",
	"/carbonpanel.v1.NodeService/GetNode":                       "internal/rbac/mapping.go:174; proto/carbonpanel/v1/node.proto:59",
	"/carbonpanel.v1.NodeService/CreateNode":                    "internal/rbac/mapping.go:175; proto/carbonpanel/v1/node.proto:61",
	"/carbonpanel.v1.NodeService/UpdateNode":                    "internal/rbac/mapping.go:176; proto/carbonpanel/v1/node.proto:63",
	"/carbonpanel.v1.NodeService/DeleteNode":                    "internal/rbac/mapping.go:177; proto/carbonpanel/v1/node.proto:65",
	"/carbonpanel.v1.NodeService/PingNode":                      "internal/rbac/mapping.go:178; proto/carbonpanel/v1/node.proto:67",
}

// FamilyServerListedCount is every declared item: mounted RPC descriptors in
// these families (including the one deliberate exclusion) plus documented
// scoped REST/streaming and private-protocol surfaces.
func FamilyServerListedCount() int {
	return len(FamilyServerDescriptors) + len(FamilyServerExcludedSurfaces)
}

// FamilyServerReachableCount is the number of tools backed by a mounted RPC
// and a typed generated client. VERIFIED is reported separately by the fixture
// tests, which exercise each reachable tool end to end.
func FamilyServerReachableCount() int { return len(FamilyServerIncludedDescriptors()) }

// FamilyServerIncludedDescriptors returns only the executable tools the
// integration owner should add to the catalog.
func FamilyServerIncludedDescriptors() []FamilyServerToolDescriptor {
	out := make([]FamilyServerToolDescriptor, 0, FamilyServerToolCount)
	for _, d := range FamilyServerDescriptors {
		if d.Class != ClassExclude {
			out = append(out, d)
		}
	}
	return out
}

// FamilyServerExcludedDescriptors returns deliberately unexposed procedures
// together with their stable reason codes.
func FamilyServerExcludedDescriptors() []FamilyServerToolDescriptor {
	var out []FamilyServerToolDescriptor
	for _, d := range FamilyServerDescriptors {
		if d.Class == ClassExclude {
			out = append(out, d)
		}
	}
	return out
}

// FamilyServerExcludedSurface records a non-RPC surface that stays outside the
// MCP catalog. It makes the EXCLUDE decisions machine-readable for the
// integration owner without pretending a typed client exists.
type FamilyServerExcludedSurface struct {
	Surface    string                `json:"surface"`
	Service    string                `json:"service"`
	Scope      string                `json:"scope,omitempty"`
	Reason     FamilyServerExclusion `json:"reason"`
	Source     string                `json:"source"`
	Equivalent string                `json:"equivalent"`
	Detail     string                `json:"detail"`
}

// FamilyServerExcludedSurfaces documents protocol/HTTP surfaces that are not
// tools. There is intentionally no health-endpoint tool: healthz/readyz are
// public probes. The servers/read tools give server status, logs and cached operational metrics,
// while Prometheus monitoring uses the panel metrics endpoints.
var FamilyServerExcludedSurfaces = []FamilyServerExcludedSurface{
	{
		Surface: "/api/v2/metrics/*", Service: "ServerService", Scope: "servers/read",
		Reason: ExcludeNoTypedClient, Source: "internal/rpc/server.go:326-332 (mux.Handle at :326); handler internal/rpc/handlers/metrics.go:75",
		Equivalent: "authorized Prometheus scrape",
		Detail:     "Host metrics require a global servers/read grant. Per-server metrics require servers/read on that server.",
	},
	{
		Surface: "/api/v1/settings/validate-key", Service: "ConfigService", Scope: "settings/read",
		Reason: ExcludeThirdPartyEgress, Source: "internal/rpc/server.go:300; handler internal/rpc/handlers/key_validator.go:45",
		Equivalent: "authorized provider validation in panel settings",
		Detail:     "The REST adapter and generated RPC delegate to the same settings/read operation. It handles provider credentials and contacts the provider.",
	},
	{
		Surface: "/api/v1/staged-config/*", Service: "StagedConfigService", Scope: "server_config/read|create",
		Reason: ExcludeMaintenance, Source: "internal/rpc/server.go:317; handler internal/rpc/handlers/staged_config.go:87,101",
		Equivalent: "panel staged changes workflow",
		Detail:     "The legacy adapter and generated StagedConfigService share staged changes, their server_config scopes and mutation owner checks.",
	},
	{
		Surface: "/api/v1/nodes/scan", Service: "NodeService", Scope: "nodes/read",
		Reason: ExcludeDiscovery, Source: "internal/rpc/server.go:308; handler internal/rpc/handlers/node_scan.go:50",
		Equivalent: "ping_node, get_node (known configured node id), list_nodes",
		Detail:     "Automatic daemon discovery is a private probe, not a scoped data operation. Operators use configured node ids.",
	},
	{
		Surface: "/carbonpanel.v1.NodeControlService/*", Service: "NodeControlService",
		Reason: ExcludePrivateProtocol, Source: "proto/carbonpanel/v1/node_control.proto:206-246; absent from internal/rpc/server.go:468-541; no mapping.go entry",
		Equivalent: "NodeService ping_node/get_node for status; panel UI for enrollment",
		Detail:     "Generated but unmounted enrollment/control protocol (10 RPCs). Genuine private protocol mechanics; never reachable on the pinned build.",
	},
	{Surface: "/ws", Service: "WebSocket hub", Reason: ExcludeBrowserHandshake, Source: "internal/rpc/server.go:278", Equivalent: "none (browser private handshake)", Detail: "Browser console/log/event hub."},
	{Surface: "/api/v1/auth/oidc/login, /callback, /exchange", Service: "AuthService", Reason: ExcludeBrowserHandshake, Source: "internal/rpc/server.go:287-294; internal/auth/oidc.go:596-627", Equivalent: "none (browser session bootstrap)", Detail: "IdP handshake; exchange redeems a one-time code for a session token."},
	{Surface: "/healthz, /readyz, /health/detail", Service: "platform", Reason: ExcludeNoTypedClient, Source: "internal/rpc/server.go:282-284", Equivalent: "none (public probes)", Detail: "Public read-only liveness probes; deliberately no health-endpoint tool."},
	{Surface: "gRPC reflection v1/v1alpha", Service: "platform", Reason: ExcludePrivateProtocol, Source: "internal/rpc/server.go:252-274", Equivalent: "tools/list for MCP tool discovery", Detail: "Reflection enumerates the registered Connect services."},
	{Surface: "CLI maintenance (list/ports/release-port/resolve)", Service: "platform", Reason: ExcludeMaintenance, Source: "cmd/carbon-panel/operations.go:19-30", Equivalent: "resolve_operation (mounted RPC)", Detail: "Operator-only maintenance path; the UI-facing RPC equivalent is listed above."},
	{Surface: "/api/v1/upload/, /api/v1/download/", Service: "UploadService/FileService", Scope: "uploads/*, files/*", Reason: ExcludeNoTypedClient, Source: "internal/rpc/server.go:296-300", Equivalent: "none in this family (byte transport primitives owned by other families)", Detail: "Streaming byte transport, out of family scope."},
}

// FamilyServerDescriptor returns the full descriptor for a canonical tool name.
func FamilyServerDescriptor(name string) (FamilyServerToolDescriptor, bool) {
	for _, d := range FamilyServerDescriptors {
		if d.Name == name {
			return d, true
		}
	}
	return FamilyServerToolDescriptor{}, false
}

// FamilyServerProcedure returns the descriptor for a Connect procedure path.
func FamilyServerProcedure(procedure string) (FamilyServerToolDescriptor, bool) {
	for _, d := range FamilyServerDescriptors {
		if d.Procedure == procedure {
			return d, true
		}
	}
	return FamilyServerToolDescriptor{}, false
}

// FamilyServerToolNames returns the canonical names of all executable tools in
// declaration order.
func FamilyServerToolNames() []string {
	included := FamilyServerIncludedDescriptors()
	out := make([]string, 0, len(included))
	for _, d := range included {
		out = append(out, d.Name)
	}
	return out
}

// FamilyServerToolSpecs projects the executable descriptors into the shared
// ToolSpec type so catalog generation can reuse the existing drift gate.
func FamilyServerToolSpecs() []ToolSpec {
	included := FamilyServerIncludedDescriptors()
	out := make([]ToolSpec, 0, len(included))
	for _, d := range included {
		out = append(out, ToolSpec{
			Name: d.Name, Procedure: d.Procedure, Class: d.Class,
			Confirm: d.Confirm, Description: d.Description,
		})
	}
	return out
}
