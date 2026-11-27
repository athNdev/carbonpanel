// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package rbac

// ProcedurePermission maps an RPC procedure to a resource and action.
type ProcedurePermission struct {
	Resource      string
	Action        string
	ObjectIDField string // Protobuf field name to extract for per-object RBAC (empty = "*")
}

// PublicProcedures lists RPC procedures that require no authentication.
var PublicProcedures = map[string]bool{
	"/carbonpanel.v1.AuthService/GetAuthStatus":   true,
	"/carbonpanel.v1.AuthService/Login":           true,
	"/carbonpanel.v1.AuthService/Register":        true,
	"/carbonpanel.v1.AuthService/GetOIDCLoginURL": true,
	"/carbonpanel.v1.AuthService/ValidateInvite":  true,
	"/carbonpanel.v1.AuthService/UseRecoveryKey":  true,
}

// AuthenticatedOnlyProcedures lists RPC procedures that require authentication
// but no specific resource permission.
var AuthenticatedOnlyProcedures = map[string]bool{
	// AuthService - authenticated user operations
	"/carbonpanel.v1.AuthService/GetCurrentUser": true,
	"/carbonpanel.v1.AuthService/Logout":         true,
	"/carbonpanel.v1.AuthService/ChangePassword": true,
	"/carbonpanel.v1.AuthService/CreateAPIToken": true,
	"/carbonpanel.v1.AuthService/ListAPITokens":  true,
	"/carbonpanel.v1.AuthService/DeleteAPIToken": true,

	// MinecraftService - reference data, no resource ownership
	"/carbonpanel.v1.MinecraftService/GetMinecraftVersions": true,
	"/carbonpanel.v1.MinecraftService/GetModLoaders":        true,
	"/carbonpanel.v1.MinecraftService/GetDockerImages":      true,
	"/carbonpanel.v1.MinecraftService/GetLoaderVersions":    true,
}

// ProcedurePermissions maps each RPC procedure path to the resource and action
// required to invoke it, plus an optional ObjectIDField for per-object scoping.
var ProcedurePermissions = map[string]ProcedurePermission{
	// â”€â”€ ServerService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.ServerService/ListServers":          {Resource: ResourceServers, Action: ActionRead},
	"/carbonpanel.v1.ServerService/GetServer":            {Resource: ResourceServers, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/GetServerLogs":        {Resource: ResourceServers, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/ClearServerLogs":      {Resource: ResourceServers, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/GetNextAvailablePort": {Resource: ResourceServers, Action: ActionRead},
	"/carbonpanel.v1.ServerService/CreateServer":         {Resource: ResourceServers, Action: ActionCreate},
	"/carbonpanel.v1.ServerService/UpdateServer":         {Resource: ResourceServers, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/DeleteServer":         {Resource: ResourceServers, Action: ActionDelete, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/StartServer":          {Resource: ResourceServers, Action: ActionStart, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/StopServer":           {Resource: ResourceServers, Action: ActionStop, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/PauseServer":          {Resource: ResourceServers, Action: ActionStop, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/DeepSleepServer":      {Resource: ResourceServers, Action: ActionStop, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/RestartServer":        {Resource: ResourceServers, Action: ActionRestart, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/RecreateServer":       {Resource: ResourceServers, Action: ActionRestart, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/SendCommand":          {Resource: ResourceServers, Action: ActionCommand, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/UploadToMCLogs":       {Resource: ResourceServers, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/MigrateServer":        {Resource: ResourceServers, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.ServerService/ListServerPlayers":    {Resource: ResourceServers, Action: ActionRead, ObjectIDField: "server_id"},

	"/carbonpanel.v1.ServerService/ExportServerArchive":  {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ServerService/PreviewServerArchive": {Resource: ResourceServers, Action: ActionCreate},

	// â”€â”€ AuthService (admin) â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.AuthService/GetAuthConfig":      {Resource: ResourceSettings, Action: ActionRead},
	"/carbonpanel.v1.AuthService/UpdateAuthSettings": {Resource: ResourceSettings, Action: ActionUpdate},
	"/carbonpanel.v1.AuthService/CreateInvite":       {Resource: ResourceUsers, Action: ActionCreate},
	"/carbonpanel.v1.AuthService/ListInvites":        {Resource: ResourceUsers, Action: ActionRead},
	"/carbonpanel.v1.AuthService/GetInvite":          {Resource: ResourceUsers, Action: ActionRead},
	"/carbonpanel.v1.AuthService/DeleteInvite":       {Resource: ResourceUsers, Action: ActionDelete},

	// â”€â”€ ConfigService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.ConfigService/GetServerConfig":    {Resource: ResourceServerConfig, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ConfigService/UpdateServerConfig": {Resource: ResourceServerConfig, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ConfigService/GetUpdateBatch":     {Resource: ResourceServerConfig, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ConfigService/DiscardUpdateBatch": {Resource: ResourceServerConfig, Action: ActionUpdate, ObjectIDField: "server_id"},

	// StagedConfigService mirrors the legacy /api/v1/staged-config adapter:
	// reads need server config read on the request's server, mutations need
	// create. Mutations re-authorize the staged record's real owner in the
	// service before storage access.
	"/carbonpanel.v1.StagedConfigService/ListStagedChanges":    {Resource: ResourceServerConfig, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.StagedConfigService/StageConfigChange":    {Resource: ResourceServerConfig, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.StagedConfigService/ApplyStagedChange":    {Resource: ResourceServerConfig, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.StagedConfigService/RollbackStagedChange": {Resource: ResourceServerConfig, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.StagedConfigService/DiscardStagedChange":  {Resource: ResourceServerConfig, Action: ActionCreate, ObjectIDField: "server_id"},

	// PackwizService mirrors the legacy /api/v1/packwiz adapter: list and get
	// need modpacks/read, clone/refresh/delete and every mod mutation need
	// modpacks/create. The legacy handler checked the global "*" object for all
	// of these routes; no ObjectIDField keeps that exact scope.
	"/carbonpanel.v1.PackwizService/ListPacks":                  {Resource: ResourceModpacks, Action: ActionRead},
	"/carbonpanel.v1.PackwizService/GetPack":                    {Resource: ResourceModpacks, Action: ActionRead},
	"/carbonpanel.v1.PackwizService/ClonePack":                  {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.PackwizService/RefreshPack":                {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.PackwizService/CheckUpdates":               {Resource: ResourceModpacks, Action: ActionRead},
	"/carbonpanel.v1.PackwizService/DeletePack":                 {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.PackwizService/AddPackMod":                 {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.PackwizService/AddUrlMod":                  {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.PackwizService/UpdatePackMod":              {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.PackwizService/DeletePackMod":              {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.PackwizService/BatchPackMods":              {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.ConfigService/GetGlobalSettings":           {Resource: ResourceSettings, Action: ActionRead},
	"/carbonpanel.v1.ConfigService/UpdateGlobalSettings":        {Resource: ResourceSettings, Action: ActionUpdate},
	"/carbonpanel.v1.ConfigService/SyncGlobalSettingsToServers": {Resource: ResourceSettings, Action: ActionUpdate},
	"/carbonpanel.v1.ConfigService/ValidateProviderKey":         {Resource: ResourceSettings, Action: ActionRead},

	// â”€â”€ FileService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.FileService/ListFiles":                {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/GetFile":                  {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/SaveUploadedFile":         {Resource: ResourceFiles, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/UpdateFile":               {Resource: ResourceFiles, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/DeleteFile":               {Resource: ResourceFiles, Action: ActionDelete, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/RenameFile":               {Resource: ResourceFiles, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/ExtractArchive":           {Resource: ResourceFiles, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/CreateFolder":             {Resource: ResourceFiles, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/MoveFile":                 {Resource: ResourceFiles, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/CopyFile":                 {Resource: ResourceFiles, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/CreateArchive":            {Resource: ResourceFiles, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/DownloadArchive":          {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/ExportWorld":              {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/InitFileDownload":         {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/GetExtractionStatus":      {Resource: ResourceFiles, Action: ActionRead},
	"/carbonpanel.v1.FileService/DownloadRemoteArchive":    {Resource: ResourceFiles, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/GetRemoteArchiveProgress": {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	// OSS-387 controller-owned bounded stream transport.
	"/carbonpanel.v1.FileService/OpenStream": {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	// Every frame and release carries server_id so the interceptor enforces the
	// same per-server grant as open, and the service binds the handle to that
	// exact server. A token scoped to server A cannot read/close server B.
	"/carbonpanel.v1.FileService/ReadStreamFrame": {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.FileService/CloseStream":     {Resource: ResourceFiles, Action: ActionRead, ObjectIDField: "server_id"},

	// â”€â”€ ModService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.ModService/ListMods":                       {Resource: ResourceMods, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ModService/GetMod":                         {Resource: ResourceMods, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ModService/ImportUploadedMod":              {Resource: ResourceMods, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ModService/UpdateMod":                      {Resource: ResourceMods, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ModService/DeleteMod":                      {Resource: ResourceMods, Action: ActionDelete, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ModService/GetFabricOptimizationStack":     {Resource: ResourceMods, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ModService/InstallFabricOptimizationStack": {Resource: ResourceMods, Action: ActionUpdate, ObjectIDField: "server_id"},

	// ModBrowseService mirrors the legacy /api/v1/servers/{id}/mods/* adapter:
	// mods/read for browse and version lookup, mods/create for install. The
	// legacy handler enforced these actions against the global "*" object;
	// keeping no ObjectIDField preserves that exact scope and lets Studio's
	// explicit "none" server browse mode through the same permission check.
	"/carbonpanel.v1.ModBrowseService/SearchMods":     {Resource: ResourceMods, Action: ActionRead},
	"/carbonpanel.v1.ModBrowseService/GetModVersions": {Resource: ResourceMods, Action: ActionRead},
	"/carbonpanel.v1.ModBrowseService/InstallMods":    {Resource: ResourceMods, Action: ActionCreate},

	// ─── DatapackService (world datapacks, scoped like mods) ─────────────────────
	"/carbonpanel.v1.DatapackService/SearchDatapacks": {Resource: ResourceMods, Action: ActionRead},
	"/carbonpanel.v1.DatapackService/InstallDatapack": {Resource: ResourceMods, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.DatapackService/UploadDatapack":  {Resource: ResourceMods, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.DatapackService/ListDatapacks":   {Resource: ResourceMods, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.DatapackService/DeleteDatapack":  {Resource: ResourceMods, Action: ActionDelete, ObjectIDField: "server_id"},

	// â”€â”€ ModpackService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.ModpackService/SearchModpacks":        {Resource: ResourceModpacks, Action: ActionRead},
	"/carbonpanel.v1.ModpackService/GetModpack":            {Resource: ResourceModpacks, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ModpackService/GetModpackBySlug":      {Resource: ResourceModpacks, Action: ActionRead},
	"/carbonpanel.v1.ModpackService/GetModpackByURL":       {Resource: ResourceModpacks, Action: ActionRead},
	"/carbonpanel.v1.ModpackService/SyncModpacks":          {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.ModpackService/ImportUploadedModpack": {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.ModpackService/ImportRemoteModpack":   {Resource: ResourceModpacks, Action: ActionCreate},
	"/carbonpanel.v1.ModpackService/DeleteModpack":         {Resource: ResourceModpacks, Action: ActionDelete, ObjectIDField: "id"},
	"/carbonpanel.v1.ModpackService/ToggleFavorite":        {Resource: ResourceModpacks, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.ModpackService/ListFavorites":         {Resource: ResourceModpacks, Action: ActionRead},
	"/carbonpanel.v1.ModpackService/GetIndexerStatus":      {Resource: ResourceModpacks, Action: ActionRead},
	"/carbonpanel.v1.ModpackService/GetModpackConfig":      {Resource: ResourceModpacks, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ModpackService/GetModpackFiles":       {Resource: ResourceModpacks, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ModpackService/GetModpackVersions":    {Resource: ResourceModpacks, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ModpackService/SyncModpackFiles":      {Resource: ResourceModpacks, Action: ActionUpdate, ObjectIDField: "id"},

	// â”€â”€ ModuleService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.ModuleService/ListModuleTemplates":        {Resource: ResourceModuleTemplates, Action: ActionRead},
	"/carbonpanel.v1.ModuleService/GetModuleTemplate":          {Resource: ResourceModuleTemplates, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/CreateModuleTemplate":       {Resource: ResourceModuleTemplates, Action: ActionCreate},
	"/carbonpanel.v1.ModuleService/UpdateModuleTemplate":       {Resource: ResourceModuleTemplates, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/DeleteModuleTemplate":       {Resource: ResourceModuleTemplates, Action: ActionDelete, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/ListModules":                {Resource: ResourceModules, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ModuleService/GetModule":                  {Resource: ResourceModules, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/CreateModule":               {Resource: ResourceModules, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ModuleService/UpdateModule":               {Resource: ResourceModules, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/DeleteModule":               {Resource: ResourceModules, Action: ActionDelete, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/StartModule":                {Resource: ResourceModules, Action: ActionStart, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/StopModule":                 {Resource: ResourceModules, Action: ActionStop, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/RestartModule":              {Resource: ResourceModules, Action: ActionRestart, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/RecreateModule":             {Resource: ResourceModules, Action: ActionRestart, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/GetModuleLogs":              {Resource: ResourceModules, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.ModuleService/GetNextAvailableModulePort": {Resource: ResourceModules, Action: ActionRead},
	"/carbonpanel.v1.ModuleService/GetAvailableAliases":        {Resource: ResourceModules, Action: ActionRead},
	"/carbonpanel.v1.ModuleService/GetResolvedAliases":         {Resource: ResourceModules, Action: ActionRead},

	// â”€â”€ NodeService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	// Node responses embed the Docker daemon host and TLS client key/cert
	// used to reach it, and Create/Update/Delete let a caller point the
	// panel at an arbitrary Docker host. These were previously absent from
	// this table, which meant any authenticated user fell through the
	// interceptor's "no mapping found" path with zero RBAC enforcement.
	"/carbonpanel.v1.NodeService/ListNodes":  {Resource: ResourceNodes, Action: ActionRead},
	"/carbonpanel.v1.NodeService/ScanNodes":  {Resource: ResourceNodes, Action: ActionRead},
	"/carbonpanel.v1.NodeService/GetNode":    {Resource: ResourceNodes, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.NodeService/CreateNode": {Resource: ResourceNodes, Action: ActionCreate},
	"/carbonpanel.v1.NodeService/UpdateNode": {Resource: ResourceNodes, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.NodeService/DeleteNode": {Resource: ResourceNodes, Action: ActionDelete, ObjectIDField: "id"},
	"/carbonpanel.v1.NodeService/PingNode":   {Resource: ResourceNodes, Action: ActionRead, ObjectIDField: "id"},

	// â”€â”€ ProxyService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.ProxyService/GetProxyRoutes":      {Resource: ResourceProxy, Action: ActionRead},
	"/carbonpanel.v1.ProxyService/GetProxyStatus":      {Resource: ResourceProxy, Action: ActionRead},
	"/carbonpanel.v1.ProxyService/UpdateProxyConfig":   {Resource: ResourceProxy, Action: ActionUpdate},
	"/carbonpanel.v1.ProxyService/GetProxyListeners":   {Resource: ResourceProxy, Action: ActionRead},
	"/carbonpanel.v1.ProxyService/CreateProxyListener": {Resource: ResourceProxy, Action: ActionCreate},
	"/carbonpanel.v1.ProxyService/UpdateProxyListener": {Resource: ResourceProxy, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.ProxyService/DeleteProxyListener": {Resource: ResourceProxy, Action: ActionDelete, ObjectIDField: "id"},
	"/carbonpanel.v1.ProxyService/GetServerRouting":    {Resource: ResourceProxy, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.ProxyService/UpdateServerRouting": {Resource: ResourceProxy, Action: ActionUpdate, ObjectIDField: "server_id"},
	// GetVelocitySecret returns credential material (the modern-forwarding
	// secret). It is deliberately gated on settings rather than proxy read:
	// the default "user" and "anonymous" roles hold proxy read, and must not
	// be able to retrieve a secret that lets them spoof player identity to
	// backend servers.
	"/carbonpanel.v1.ProxyService/GetVelocitySecret":          {Resource: ResourceSettings, Action: ActionRead},
	"/carbonpanel.v1.ProxyService/RotateVelocitySecret":       {Resource: ResourceProxy, Action: ActionUpdate},
	"/carbonpanel.v1.ProxyService/SyncVelocitySecretToServer": {Resource: ResourceProxy, Action: ActionUpdate, ObjectIDField: "server_id"},

	// â”€â”€ TaskService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.TaskService/ListTasks":            {Resource: ResourceTasks, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.TaskService/GetTask":              {Resource: ResourceTasks, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.TaskService/CreateTask":           {Resource: ResourceTasks, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.TaskService/UpdateTask":           {Resource: ResourceTasks, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.TaskService/DeleteTask":           {Resource: ResourceTasks, Action: ActionDelete, ObjectIDField: "id"},
	"/carbonpanel.v1.TaskService/ToggleTask":           {Resource: ResourceTasks, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.TaskService/TriggerTask":          {Resource: ResourceTasks, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.TaskService/ListTaskExecutions":   {Resource: ResourceTasks, Action: ActionRead, ObjectIDField: "task_id"},
	"/carbonpanel.v1.TaskService/ListServerExecutions": {Resource: ResourceTasks, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.TaskService/GetTaskExecution":     {Resource: ResourceTasks, Action: ActionRead, ObjectIDField: "id"},
	"/carbonpanel.v1.TaskService/CancelExecution":      {Resource: ResourceTasks, Action: ActionUpdate, ObjectIDField: "id"},
	"/carbonpanel.v1.TaskService/GetSchedulerStatus":   {Resource: ResourceTasks, Action: ActionRead},

	// ── ActivityService ─────────────────────────────────────────────
	"/carbonpanel.v1.ActivityService/ListActivityLogs": {Resource: ResourceActivity, Action: ActionRead, ObjectIDField: "server_id"},

	// ── OperationService (OSS-245) ──────────────────────────────────
	// Every call is scoped to a server, so a scoped grant can read one
	// server's journal without gaining access to the whole panel. An empty
	// server_id becomes "*" in the interceptor, which only an unscoped grant
	// matches - listing every server's operations stays an admin action.
	"/carbonpanel.v1.OperationService/ListOperations":   {Resource: ResourceServers, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.OperationService/GetOperation":     {Resource: ResourceServers, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.OperationService/ResolveOperation": {Resource: ResourceServers, Action: ActionUpdate, ObjectIDField: "server_id"},

	// ── SubuserService ──────────────────────────────────────────────
	"/carbonpanel.v1.SubuserService/ListSubusers":  {Resource: ResourceSubusers, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.SubuserService/SetSubuser":    {Resource: ResourceSubusers, Action: ActionCreate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.SubuserService/RemoveSubuser": {Resource: ResourceSubusers, Action: ActionDelete, ObjectIDField: "server_id"},

	// ── BlueprintService ────────────────────────────────────────────
	"/carbonpanel.v1.BlueprintService/ListBlueprints": {Resource: ResourceServers, Action: ActionRead},
	"/carbonpanel.v1.BlueprintService/GetBlueprint":   {Resource: ResourceServers, Action: ActionRead},

	// ── BackupService ───────────────────────────────────────────────
	"/carbonpanel.v1.BackupService/ListBackups":     {Resource: ResourceBackups, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.BackupService/DeleteBackup":    {Resource: ResourceBackups, Action: ActionDelete},
	"/carbonpanel.v1.BackupService/RestoreBackup":   {Resource: ResourceBackups, Action: ActionUpdate},
	"/carbonpanel.v1.BackupService/SetBackupLocked": {Resource: ResourceBackups, Action: ActionUpdate},
	"/carbonpanel.v1.BackupService/CreateBackup":    {Resource: ResourceBackups, Action: ActionCreate, ObjectIDField: "server_id"},

	// â”€â”€ UserService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.UserService/ListUsers":     {Resource: ResourceUsers, Action: ActionRead},
	"/carbonpanel.v1.UserService/GetUser":       {Resource: ResourceUsers, Action: ActionRead},
	"/carbonpanel.v1.UserService/CreateUser":    {Resource: ResourceUsers, Action: ActionCreate},
	"/carbonpanel.v1.UserService/UpdateUser":    {Resource: ResourceUsers, Action: ActionUpdate},
	"/carbonpanel.v1.UserService/BeginOIDCLink": {Resource: ResourceUsers, Action: ActionUpdate},
	"/carbonpanel.v1.UserService/DeleteUser":    {Resource: ResourceUsers, Action: ActionDelete},

	// â”€â”€ RoleService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.RoleService/ListRoles":           {Resource: ResourceRoles, Action: ActionRead},
	"/carbonpanel.v1.RoleService/GetRole":             {Resource: ResourceRoles, Action: ActionRead},
	"/carbonpanel.v1.RoleService/CreateRole":          {Resource: ResourceRoles, Action: ActionCreate},
	"/carbonpanel.v1.RoleService/UpdateRole":          {Resource: ResourceRoles, Action: ActionUpdate},
	"/carbonpanel.v1.RoleService/DeleteRole":          {Resource: ResourceRoles, Action: ActionDelete},
	"/carbonpanel.v1.RoleService/GetPermissionMatrix": {Resource: ResourceRoles, Action: ActionRead},
	"/carbonpanel.v1.RoleService/UpdatePermissions":   {Resource: ResourceRoles, Action: ActionUpdate},
	"/carbonpanel.v1.RoleService/AssignRole":          {Resource: ResourceRoles, Action: ActionCreate},
	"/carbonpanel.v1.RoleService/UnassignRole":        {Resource: ResourceRoles, Action: ActionDelete},
	"/carbonpanel.v1.RoleService/GetUserRoles":        {Resource: ResourceRoles, Action: ActionRead},

	// â”€â”€ SupportService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.SupportService/GenerateSupportBundle": {Resource: ResourceSupport, Action: ActionCreate},
	"/carbonpanel.v1.SupportService/DownloadSupportBundle": {Resource: ResourceSupport, Action: ActionRead},
	"/carbonpanel.v1.SupportService/UploadSupportBundle":   {Resource: ResourceSupport, Action: ActionCreate},
	"/carbonpanel.v1.SupportService/GetApplicationLogs":    {Resource: ResourceSupport, Action: ActionRead},

	// â”€â”€ UploadService â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€â”€
	"/carbonpanel.v1.UploadService/GetUploadStatus": {Resource: ResourceUploads, Action: ActionRead},
	"/carbonpanel.v1.UploadService/InitUpload":      {Resource: ResourceUploads, Action: ActionCreate},
	"/carbonpanel.v1.UploadService/UploadChunk":     {Resource: ResourceUploads, Action: ActionCreate},
	"/carbonpanel.v1.UploadService/CancelUpload":    {Resource: ResourceUploads, Action: ActionDelete},

	// ── PlayerService (OSS-345) ──────────────────────────────────────
	// Server-scoped procedures use "server_id" so SubuserPermissions apply.
	// The skin lookup carries an optional server_id for the same reason; its
	// skin_ref is an opaque cache key and never an arbitrary URL.
	// Exact-identity lookup has no server scope: it resolves an account the
	// panel has not observed yet. It stays fail-closed behind players:read on
	// the global "*" scope; no subuser per-server grant implies it.
	"/carbonpanel.v1.PlayerService/LookupPlayer":           {Resource: ResourcePlayers, Action: ActionRead},
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

	// ── WorldViewService (OSS-354/355/357) ──────────────────────────
	// World viewing is observation-only. ListWorldViewTargets is global and
	// filtered per server in the service; every other procedure is scoped to a
	// concrete server_id. Map reads never imply player observation or overlay
	// edits: those actions are mapped separately and are admin-only by default.
	"/carbonpanel.v1.WorldViewService/ListWorldViewTargets": {Resource: ResourceWorldView, Action: ActionRead},
	"/carbonpanel.v1.WorldViewService/GetWorldViewStatus":   {Resource: ResourceWorldView, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldViewService/GetWorldViewSession":  {Resource: ResourceWorldView, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldViewService/ListCapabilities":     {Resource: ResourceWorldView, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldViewService/ListMapOverlays":      {Resource: ResourceWorldView, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldViewService/SetMapOverlay":        {Resource: ResourceWorldView, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldViewService/SetPlayerVisibility":  {Resource: ResourceWorldView, Action: ActionObservePlayers, ObjectIDField: "server_id"},

	// ── WorldService ────────────────────────────────────────────────
	// Library metadata is not server-scoped. Listing bindings and every
	// mutation that changes a server level use "server_id" so subuser grants
	// and the fail-closed interceptor apply to the server being changed.
	"/carbonpanel.v1.WorldService/ListWorldEntries":        {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/GetWorldEntry":           {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/ListRevisions":           {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/GetRevision":             {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/ListBindings":            {Resource: ResourceWorlds, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldService/GetBinding":              {Resource: ResourceWorlds, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldService/ListImportJobs":          {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/GetImportJob":            {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/ImportWorld":             {Resource: ResourceWorlds, Action: ActionCreate},
	"/carbonpanel.v1.WorldService/CheckCompatibility":      {Resource: ResourceWorlds, Action: ActionRead, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldService/BindWorld":               {Resource: ResourceWorlds, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldService/UnbindWorld":             {Resource: ResourceWorlds, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldService/ResolveWorldOperation":   {Resource: ResourceWorlds, Action: ActionUpdate, ObjectIDField: "server_id"},
	"/carbonpanel.v1.WorldService/GetWorldConfigSchema":    {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/PreviewWorldConfig":      {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/CreateWorldFromConfig":   {Resource: ResourceWorlds, Action: ActionCreate},
	"/carbonpanel.v1.WorldService/SearchWorldArtifacts":    {Resource: ResourceWorlds, Action: ActionRead},
	"/carbonpanel.v1.WorldService/GetProviderCapabilities": {Resource: ResourceWorlds, Action: ActionRead},
}
