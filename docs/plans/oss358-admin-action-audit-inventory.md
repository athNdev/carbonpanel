# OSS-358 admin-action audit inventory

**Status:** source-backed inventory and bounded Nightly audit slice, 2026-11-28.

**Source snapshot:** Carbon Panel `a27e45c3493a60f5c9e2830d4b5695900dfe80cc`
plus the OSS-358 working tree. This is not live deployment evidence and does
not claim an end-to-end deployed console walkthrough.

This inventory answers a narrower question than the OSS-358 acceptance line
"each admin action writes an audit row": which admin-mutating RPCs and internal
admin actions currently have an activity row, where that row is written, and
which are still gaps. Read-only/list RPCs, authentication bootstrap and
high-frequency transport paths are listed separately and deliberately do not
get new audit rows in this slice.

## How to read this file

- **Added** means this change writes a new row through
  `internal/rpc/services/audit.go:writeAudit` and adds a focused test.
- **Covered** means source already writes an activity row before this change.
  The row cites the existing helper; `—` in the test column means no
  audit-specific test was found in this slice, not that the action is untested.
- **Deferred** is an explicit gap. The slice deliberately does not guess an
  event schema for these; they are follow-up work, not claimed coverage.
- **Internal** marks a non-admin, bootstrap or high-frequency path that must
  not receive a per-call admin audit row.

Every code span below is checked by `internal/docscheck`: entry-point and
audit-write paths/symbols must exist, every event literal in an event cell must
be declared, and every named test must exist in the cited `_test.go` file and
assert a stored activity row. An **Added** row must also cite an audit-write
function that references its event. A missing inventory file fails the check
instead of skipping it.

## Admin-mutating RPCs and actions

| ID | Action | Entry point | Authorization scope | Activity event | Audit write | Covering test | Disposition |
| --- | --- | --- | --- | --- | --- | --- | --- |
| A01 | Update auth settings | `internal/rpc/services/auth.go:UpdateAuthSettings` | `settings:update` global | `auth.settings_update` | `internal/rpc/services/auth.go:UpdateAuthSettings` | `internal/rpc/services/admin_audit_test.go:TestAuthAdminMutationsWriteAuditRows` | **Added** |
| A02 | Create registration invite | `internal/rpc/services/auth.go:CreateInvite` | `users:create` global | `auth.invite.create` | `internal/rpc/services/auth.go:CreateInvite` | `internal/rpc/services/admin_audit_test.go:TestAuthAdminMutationsWriteAuditRows` | **Added** |
| A03 | Delete registration invite | `internal/rpc/services/auth.go:DeleteInvite` | `users:delete` global | `auth.invite.delete` | `internal/rpc/services/auth.go:DeleteInvite` | `internal/rpc/services/admin_audit_test.go:TestAuthAdminMutationsWriteAuditRows` | **Added** |
| A04 | Create API token | `internal/rpc/services/auth.go:CreateAPIToken` | authenticated user, own token | `auth.api_token.create` | `internal/rpc/services/auth.go:CreateAPIToken` | `internal/rpc/services/admin_audit_test.go:TestAuthAdminMutationsWriteAuditRows` | **Added** |
| A05 | Delete API token (revoke) | `internal/rpc/services/auth.go:DeleteAPIToken` | authenticated user, own token | `auth.token_revoked` | `internal/rpc/services/auth.go:DeleteAPIToken` | `internal/rpc/services/admin_audit_test.go:TestAuthAdminMutationsWriteAuditRows` | **Added** |
| A06 | Create user | `internal/rpc/services/user.go:CreateUser` | `users:create` global; admin-role grant additionally `roles:manage_wildcard` | `user.create` | `internal/rpc/services/user.go:CreateUser` | `internal/rpc/services/admin_audit_test.go:TestUserAdminMutationsWriteAuditRows` | **Added** |
| A07 | Update user profile/roles | `internal/rpc/services/user.go:UpdateUser` | `users:update` global; admin-role grant additionally `roles:manage_wildcard` | `user.update` | `internal/rpc/services/user.go:UpdateUser` | `internal/rpc/services/admin_audit_test.go:TestUserAdminMutationsWriteAuditRows` | **Added** |
| A08 | Delete user | `internal/rpc/services/user.go:DeleteUser` | `users:delete` global | `user.delete` | `internal/rpc/services/user.go:DeleteUser` | `internal/rpc/services/admin_audit_test.go:TestUserAdminMutationsWriteAuditRows` | **Added** |
| A09 | Begin OIDC local linking | `internal/rpc/services/user.go:BeginOIDCLink` | `users:update` global, own account | `none` | `none` | `—` | **Deferred** |
| A10 | Set server subuser | `internal/rpc/services/subuser.go:SetSubuser` | `subusers:create` on server | `none` | `none` | `—` | **Deferred** |
| A11 | Remove server subuser | `internal/rpc/services/subuser.go:RemoveSubuser` | `subusers:delete` on server | `none` | `none` | `—` | **Deferred** |
| A12 | Create role | `internal/rpc/services/role.go:CreateRole` | `roles:create` global; wildcard/default additionally `roles:manage_wildcard` | `role.create` | `internal/rpc/services/role.go:CreateRole` | `internal/rpc/services/admin_audit_test.go:TestRoleAdminMutationsWriteAuditRows` | **Added** |
| A13 | Update role | `internal/rpc/services/role.go:UpdateRole` | `roles:update` on role | `role.update` | `internal/rpc/services/role.go:UpdateRole` | `internal/rpc/services/admin_audit_test.go:TestRoleAdminMutationsWriteAuditRows` | **Added** |
| A14 | Delete role | `internal/rpc/services/role.go:DeleteRole` | `roles:delete` on role | `role.delete` | `internal/rpc/services/role.go:DeleteRole` | `internal/rpc/services/admin_audit_test.go:TestRoleAdminMutationsWriteAuditRows` | **Added** |
| A15 | Replace role permissions | `internal/rpc/services/role.go:UpdatePermissions` | `roles:update`; wildcard additionally `roles:manage_wildcard` | `role.permissions_update` | `internal/rpc/services/role.go:UpdatePermissions` | `internal/rpc/services/admin_audit_test.go:TestRoleAdminMutationsWriteAuditRows` | **Added** |
| A16 | Assign role to user | `internal/rpc/services/role.go:AssignRole` | `roles:create`; admin role additionally `roles:manage_wildcard` | `role.assign` | `internal/rpc/services/role.go:AssignRole` | `internal/rpc/services/admin_audit_test.go:TestRoleAdminMutationsWriteAuditRows` | **Added** |
| A17 | Unassign role from user | `internal/rpc/services/role.go:UnassignRole` | `roles:delete` | `role.unassign` | `internal/rpc/services/role.go:UnassignRole` | `internal/rpc/services/admin_audit_test.go:TestRoleAdminMutationsWriteAuditRows` | **Added** |
| A18 | Create node | `internal/rpc/services/node.go:CreateNode` | `nodes:create` global | `node.create` | `internal/rpc/services/node.go:CreateNode` | `internal/rpc/services/admin_audit_test.go:TestNodeAdminMutationsWriteBoundedAuditRows` | **Added** |
| A19 | Update node / replace TLS credentials | `internal/rpc/services/node.go:UpdateNode` | `nodes:update` on node | `node.update` | `internal/rpc/services/node.go:UpdateNode` | `internal/rpc/services/admin_audit_test.go:TestNodeAdminMutationsWriteBoundedAuditRows` | **Added** |
| A20 | Delete node | `internal/rpc/services/node.go:DeleteNode` | `nodes:delete` on node | `node.delete` | `internal/rpc/services/node.go:DeleteNode` | `internal/rpc/services/admin_audit_test.go:TestNodeAdminMutationsWriteBoundedAuditRows` | **Added** |
| A21 | Clear node TLS credentials | `internal/rpc/services/node_tls_reset.go:ClearNodeTLSCredentials` | `nodes:update` on node plus named confirmation | `node.tls_credentials_clear` | `internal/rpc/services/node_tls_reset.go:ClearNodeTLSCredentials` | `internal/rpc/services/admin_audit_test.go:TestNodeTLSClearWritesBoundedAuditRow` | **Added** |
| A22 | Revoke node enrollment credential | `internal/rpc/node_control_enrollment.go:RevokeCredential` | `nodes:update` on exact node, operator TLS domain | `none` | `none` | `—` | **Deferred** |
| A23 | Update server config | `internal/rpc/services/config.go:UpdateServerConfig` | `server_config:update` on server | `none` | `none` | `—` | **Deferred** |
| A24 | Update global settings | `internal/rpc/services/config.go:UpdateGlobalSettings` | `settings:update` global | `none` | `none` | `—` | **Deferred** |
| A25 | Sync global settings to servers | `internal/rpc/services/config.go:SyncGlobalSettingsToServers` | `settings:update` global | `none` | `none` | `—` | **Deferred** |
| A26 | Discard update batch | `internal/rpc/services/update_batch.go:DiscardUpdateBatch` | `server_config:update` on server | `none` | `none` | `—` | **Deferred** |
| A27 | Stage config change | `internal/rpc/services/staged_config.go:StageConfigChange` | `server_config:create` on server | `none` | `none` | `—` | **Deferred** |
| A28 | Apply staged change | `internal/rpc/services/staged_config.go:ApplyStagedChange` | `server_config:create` on server | `none` | `none` | `—` | **Deferred** |
| A29 | Roll back staged change | `internal/rpc/services/staged_config.go:RollbackStagedChange` | `server_config:create` on server | `none` | `none` | `—` | **Deferred** |
| A30 | Discard staged change | `internal/rpc/services/staged_config.go:DiscardStagedChange` | `server_config:create` on server | `none` | `none` | `—` | **Deferred** |
| A31 | Create backup | `internal/rpc/services/backup.go:CreateBackup` | `backups:create` on server | `none` | `none` | `—` | **Deferred** |
| A32 | Delete backup | `internal/rpc/services/backup.go:DeleteBackup` | `backups:delete` global | `none` | `none` | `—` | **Deferred** |
| A33 | Restore backup | `internal/rpc/services/backup.go:RestoreBackup` | `backups:update` global | `none` | `none` | `—` | **Deferred** |
| A34 | Set backup lock | `internal/rpc/services/backup.go:SetBackupLocked` | `backups:update` global | `none` | `none` | `—` | **Deferred** |
| A35 | Create scheduled task | `internal/rpc/services/task.go:CreateTask` | `tasks:create` on server | `none` | `none` | `—` | **Deferred** |
| A36 | Update scheduled task | `internal/rpc/services/task.go:UpdateTask` | `tasks:update` on task | `none` | `none` | `—` | **Deferred** |
| A37 | Delete scheduled task | `internal/rpc/services/task.go:DeleteTask` | `tasks:delete` on task | `none` | `none` | `—` | **Deferred** |
| A38 | Toggle scheduled task | `internal/rpc/services/task.go:ToggleTask` | `tasks:update` on task | `none` | `none` | `—` | **Deferred** |
| A39 | Trigger scheduled task | `internal/rpc/services/task.go:TriggerTask` | `tasks:update` on task | `none` | `none` | `—` | **Deferred** |
| A40 | Cancel task execution | `internal/rpc/services/task.go:CancelExecution` | `tasks:update` on execution | `none` | `none` | `—` | **Deferred** |
| A41 | Create module template | `internal/rpc/services/module.go:CreateModuleTemplate` | `module_templates:create` global | `none` | `none` | `—` | **Deferred** |
| A42 | Update module template | `internal/rpc/services/module.go:UpdateModuleTemplate` | `module_templates:update` on template | `none` | `none` | `—` | **Deferred** |
| A43 | Delete module template | `internal/rpc/services/module.go:DeleteModuleTemplate` | `module_templates:delete` on template | `none` | `none` | `—` | **Deferred** |
| A44 | Create module | `internal/rpc/services/module.go:CreateModule` | `modules:create` on server | `none` | `none` | `—` | **Deferred** |
| A45 | Update module | `internal/rpc/services/module.go:UpdateModule` | `modules:update` on module | `none` | `none` | `—` | **Deferred** |
| A46 | Delete module | `internal/rpc/services/module.go:DeleteModule` | `modules:delete` on module | `none` | `none` | `—` | **Deferred** |
| A47 | Start module | `internal/rpc/services/module.go:StartModule` | `modules:start` on module | `none` | `none` | `—` | **Deferred** |
| A48 | Stop module | `internal/rpc/services/module.go:StopModule` | `modules:stop` on module | `none` | `none` | `—` | **Deferred** |
| A49 | Restart module | `internal/rpc/services/module.go:RestartModule` | `modules:restart` on module | `none` | `none` | `—` | **Deferred** |
| A50 | Recreate module | `internal/rpc/services/module.go:RecreateModule` | `modules:restart` on module | `none` | `none` | `—` | **Deferred** |
| A51 | Import uploaded mod | `internal/rpc/services/mod.go:ImportUploadedMod` | `mods:create` on server | `mod.install` | `internal/rpc/services/mod.go:auditMod` | `internal/rpc/services/admin_audit_mod_test.go:TestImportUploadedModWritesAuditRow` | **Covered** |
| A52 | Update mod enabled state | `internal/rpc/services/mod.go:UpdateMod` | `mods:update` on server | `mod.update` | `internal/rpc/services/mod.go:auditMod` | `internal/rpc/services/admin_audit_mod_test.go:TestUpdateModWritesAuditRow` | **Covered** |
| A53 | Delete mod | `internal/rpc/services/mod.go:DeleteMod` | `mods:delete` on server | `mod.remove` | `internal/rpc/services/mod.go:auditMod` | `—` | **Covered** |
| A54 | Install Fabric optimization stack | `internal/rpc/services/mod.go:InstallFabricOptimizationStack` | `mods:update` on server | `mod.install` | `internal/rpc/services/mod.go:auditMod` | `—` | **Covered** |
| A55 | Install mods from browse | `internal/rpc/services/mod_browse.go:InstallMods` | `mods:create` global | `mod.install` | `internal/rpc/handlers/mod_search.go:auditModInstall` | `internal/rpc/services/admin_audit_mod_test.go:TestModBrowseServiceInstallWritesAuditRow` | **Covered** |
| A56 | Install datapack | `internal/rpc/services/datapack.go:InstallDatapack` | `mods:create` on server | `none` | `none` | `—` | **Deferred** |
| A57 | Upload datapack | `internal/rpc/services/datapack.go:UploadDatapack` | `mods:create` on server | `none` | `none` | `—` | **Deferred** |
| A58 | Delete datapack | `internal/rpc/services/datapack.go:DeleteDatapack` | `mods:delete` on server | `none` | `none` | `—` | **Deferred** |
| A59 | Sync modpack catalog | `internal/rpc/services/modpack.go:SyncModpacks` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A60 | Import uploaded modpack | `internal/rpc/services/modpack.go:ImportUploadedModpack` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A61 | Import remote modpack | `internal/rpc/services/modpack.go:ImportRemoteModpack` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A62 | Delete modpack | `internal/rpc/services/modpack.go:DeleteModpack` | `modpacks:delete` on modpack | `none` | `none` | `—` | **Deferred** |
| A63 | Sync modpack files | `internal/rpc/services/modpack.go:SyncModpackFiles` | `modpacks:update` on modpack | `none` | `none` | `—` | **Deferred** |
| A64 | Clone pack | `internal/rpc/services/packwiz.go:ClonePack` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A65 | Refresh pack | `internal/rpc/services/packwiz.go:RefreshPack` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A66 | Delete pack | `internal/rpc/services/packwiz.go:DeletePack` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A67 | Add pack mod | `internal/rpc/services/packwiz.go:AddPackMod` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A68 | Add URL mod | `internal/rpc/services/packwiz.go:AddUrlMod` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A69 | Update pack mod | `internal/rpc/services/packwiz.go:UpdatePackMod` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A70 | Delete pack mod | `internal/rpc/services/packwiz.go:DeletePackMod` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A71 | Batch pack mods | `internal/rpc/services/packwiz.go:BatchPackMods` | `modpacks:create` global | `none` | `none` | `—` | **Deferred** |
| A72 | Create server | `internal/rpc/services/server.go:CreateServer` | `servers:create` global; privileged overrides additionally `servers:manage_docker_privileged` | `server.create` | `internal/rpc/services/server.go:auditServerLifecycle` | `internal/rpc/services/server_command_audit_test.go:TestAuditServerLifecycleRowShapeUnchanged` | **Covered** |
| A73 | Update server settings/image | `internal/rpc/services/server.go:UpdateServer` | `servers:update` on server; privileged overrides additionally `servers:manage_docker_privileged` | `server.update` | `internal/rpc/services/server.go:auditServerLifecycle` | `internal/rpc/services/server_command_audit_test.go:TestAuditServerLifecycleRowShapeUnchanged` | **Covered** |
| A74 | Delete server | `internal/rpc/services/server.go:DeleteServer` | `servers:delete` on server | `server.delete` | `internal/rpc/services/server.go:auditServerDelete` | `internal/rpc/services/server_delete_data_test.go:TestDeleteServerKeepDataLeavesTheWorldAndDropsTheRow` | **Covered** |
| A75 | Start server | `internal/rpc/services/server.go:StartServer` | `servers:start` on server | `server.start` | `internal/rpc/services/server.go:auditPower` | `internal/rpc/services/server_command_audit_test.go:TestAuditServerLifecycleRowShapeUnchanged` | **Covered** |
| A76 | Stop server | `internal/rpc/services/server.go:StopServer` | `servers:stop` on server | `server.stop` | `internal/rpc/services/server.go:auditPower` | `internal/rpc/services/server_command_audit_test.go:TestAuditServerLifecycleRowShapeUnchanged` | **Covered** |
| A77 | Restart server | `internal/rpc/services/server.go:RestartServer` | `servers:restart` on server | `server.restart` | `internal/rpc/services/server.go:auditPower` | `internal/rpc/services/server_command_audit_test.go:TestAuditServerLifecycleRowShapeUnchanged` | **Covered** |
| A78 | Recreate server | `internal/rpc/services/server.go:RecreateServer` | `servers:restart` on server | `server.restart` | `internal/rpc/services/server.go:auditPower` | `internal/rpc/services/server_command_audit_test.go:TestAuditServerLifecycleRowShapeUnchanged` | **Covered** |
| A79 | Send console command | `internal/rpc/services/server.go:SendCommand` | `servers:command` on server | `server.command` | `internal/rpc/services/server.go:auditServerCommand` | `internal/rpc/services/server_command_audit_test.go:TestSendCommandWritesOneBoundedAuditRow` | **Covered** |
| A80 | Migrate server | `internal/rpc/services/server.go:MigrateServer` | `servers:update` on server | `none` | `none` | `—` | **Deferred** |
| A81 | Export server archive | `internal/rpc/services/server_archive.go:ExportServerArchive` | `files:read` on server | `none` | `none` | `—` | **Deferred** |
| A82 | Clear server logs | `internal/rpc/services/server.go:ClearServerLogs` | `servers:update` on server | `none` | `none` | `—` | **Deferred** |
| A83 | Upload logs to MCLogs | `internal/rpc/services/server.go:UploadToMCLogs` | `servers:read` on server | `none` | `none` | `—` | **Deferred** |
| A84 | Export world file | `internal/rpc/services/file.go:ExportWorld` | `files:read` on server | `server.export` | `internal/rpc/services/file.go:auditWorldExport` | `internal/rpc/services/server_export_world_test.go:TestExportWorldLeavesTheServerAndTheWorld` | **Covered** |
| A85 | File write surface (SaveUploadedFile, UpdateFile, DeleteFile, RenameFile, ExtractArchive, CreateFolder, MoveFile, CopyFile, CreateArchive, DownloadRemoteArchive) | `internal/rpc/services/file.go:SaveUploadedFile` | `files:create/update/delete` on server | `none` | `none` | `—` | **Deferred** |
| A86 | Update proxy config | `internal/rpc/services/proxy.go:UpdateProxyConfig` | `proxy:update` global | `none` | `none` | `—` | **Deferred** |
| A87 | Create proxy listener | `internal/rpc/services/proxy.go:CreateProxyListener` | `proxy:create` global | `none` | `none` | `—` | **Deferred** |
| A88 | Update proxy listener | `internal/rpc/services/proxy.go:UpdateProxyListener` | `proxy:update` on listener | `none` | `none` | `—` | **Deferred** |
| A89 | Delete proxy listener | `internal/rpc/services/proxy.go:DeleteProxyListener` | `proxy:delete` on listener | `none` | `none` | `—` | **Deferred** |
| A90 | Update server routing | `internal/rpc/services/proxy.go:UpdateServerRouting` | `proxy:update` on server | `none` | `none` | `—` | **Deferred** |
| A91 | Rotate Velocity secret | `internal/rpc/services/proxy.go:RotateVelocitySecret` | `proxy:update` global | `none` | `none` | `—` | **Deferred** |
| A92 | Sync Velocity secret to server | `internal/rpc/services/proxy.go:SyncVelocitySecretToServer` | `proxy:update` on server | `none` | `none` | `—` | **Deferred** |
| A93 | Kick player | `internal/rpc/services/player.go:KickPlayer` | `players:moderate` on server | `player.kick` | `internal/rpc/services/player.go:auditPlayer` | `internal/rpc/services/player_test.go:TestKickIsAuditedAndIdempotent` | **Covered** |
| A94 | Ban player | `internal/rpc/services/player.go:BanPlayer` | `players:moderate` on server | `player.ban` | `internal/rpc/services/player.go:auditPlayer` | `—` | **Covered** |
| A95 | Pardon player | `internal/rpc/services/player.go:PardonPlayer` | `players:moderate` on server | `player.pardon` | `internal/rpc/services/player.go:auditPlayer` | `—` | **Covered** |
| A96 | Set player operator | `internal/rpc/services/player.go:SetOperator` | `players:moderate:op` on server | `player.op` / `player.deop` | `internal/rpc/services/player.go:auditPlayer` | `—` | **Covered** |
| A97 | Set whitelist entry | `internal/rpc/services/player.go:SetWhitelistEntry` | `players:access:write` on server | `player.whitelist.add` / `player.whitelist.remove` | `internal/rpc/services/player.go:auditPlayer` | `internal/rpc/services/player_lookup_test.go:TestSetWhitelistManualAddByNameBeforeFirstJoin` | **Covered** |
| A98 | Bulk player action | `internal/rpc/services/player.go:BulkPlayerAction` | `players:bulk` on server | `player.bulk` | `internal/rpc/services/player.go:auditPlayer` | `internal/rpc/services/player_test.go:TestBulkReplayDoesNotReapplyMutations` | **Covered** |
| A99 | Undo player action | `internal/rpc/services/player.go:UndoPlayerAction` | `players:moderate` on server | `player.undo` | `internal/rpc/services/player.go:auditPlayer` | `internal/rpc/services/player_test.go:TestUndoAuditSurvivesClientCancellationAfterMutation` | **Covered** |
| A100 | Refresh player skin | `internal/rpc/services/player.go:RefreshPlayerSkin` | `players:skin:refresh` on server | `player.skin.refresh` | `internal/rpc/services/player.go:auditPlayer` | `—` | **Covered** |
| A101 | Import world | `internal/rpc/services/world.go:ImportWorld` | `worlds:create` global | `none` | `none` | `—` | **Deferred** |
| A102 | Create world from config | `internal/rpc/services/world.go:CreateWorldFromConfig` | `worlds:create` global | `none` | `none` | `—` | **Deferred** |
| A103 | Bind world | `internal/rpc/services/world.go:BindWorld` | `worlds:update` on server | `world.bind` | `internal/rpc/services/world.go:auditWorldOperation` | `internal/rpc/services/world_bind_test.go:TestBindWorldEmitsExactlyOneEventOnSuccessAndReplay` | **Covered** |
| A104 | Unbind world | `internal/rpc/services/world.go:UnbindWorld` | `worlds:update` on server | `world.unbind` | `internal/rpc/services/world.go:auditWorldOperation` | `internal/rpc/services/world_bind_test.go:TestUnbindWorldEmitsExactlyOneEventOnSuccess` | **Covered** |
| A105 | Resolve world operation | `internal/rpc/services/world.go:ResolveWorldOperation` | `worlds:update` on server | `none` | `none` | `—` | **Deferred** |
| A106 | Set map overlay | `internal/worldview/service.go:SetMapOverlay` | `world_view:update` on server | `world_view.overlay.set` | `internal/worldview/service.go:SetMapOverlay` | `internal/worldview/service_test.go:TestServiceMutationsAreAuditedAndScopedFixture` | **Covered** |
| A107 | Set player visibility | `internal/worldview/service.go:SetPlayerVisibility` | `world_view:observe_players` on server | `world_view.observe_players` | `internal/worldview/service.go:SetPlayerVisibility` | `internal/worldview/service_test.go:TestServiceMutationsAreAuditedAndScopedFixture` | **Covered** |
| A108 | Resolve server operation | `internal/rpc/services/operation.go:ResolveOperation` | `servers:update` on server | `server.operation.resolve` | `internal/rpc/services/operation.go:recordResolution` | `internal/rpc/services/activity_test.go:TestResolveOperationRecordsActivityWithRequestID` | **Covered** |
| A109 | Generate support bundle | `internal/rpc/services/support.go:GenerateSupportBundle` | `support:create` global | `none` | `none` | `—` | **Deferred** |
| A110 | Upload support bundle | `internal/rpc/services/support.go:UploadSupportBundle` | `support:create` global | `none` | `none` | `—` | **Deferred** |

## Internal, bootstrap and high-frequency surfaces (no admin audit added)

These are explicitly not admin-action gaps in this slice:

- Authentication bootstrap and session lifecycle: `Login`, `Logout`, `Register`,
  `UseRecoveryKey`, `ChangePassword`, `GetCurrentUser`, `GetAuthStatus`.
  Login/logout/failed-login/recovery already have dedicated events in
  `internal/activity/activity.go`; adding generic admin rows would duplicate
  them or expose credentials. Registration/first-user setup is bootstrap, not
  an authenticated admin action.
- Node enrollment bootstrap: `CreateEnrollment`, `BeginEnrollmentChallenge`,
  `ClaimEnrollment`, `NegotiateVersion`, `OpenControlSession`,
  `ReportPreflight`, `ReportHealth`, `AckOperation`, `DispatchOperation`.
  These run on the dedicated TLS enrollment/control channel; `RevokeCredential`
  is the one operator mutation and is listed as A22.
- Upload transport: `InitUpload`, `UploadChunk`, `CancelUpload`,
  `GetUploadStatus`. Chunk frequency makes per-chunk audit rows noise; the
  consuming admin actions (mod/datapack/module import) own the audit decision.
- Per-user preferences: `ModpackService/ToggleFavorite`, `ListFavorites`.
  This is user preference state, not panel administration.
- Read-only/list RPCs across every service (for example `ListServers`,
  `GetServer`, `ListNodes`, `ListUsers`, `ListRoles`, `ListBackups`,
  `ListTasks`, `ListModules`, `ListMods`, `ListWorldEntries`,
  `ListActivityLogs`, `GetPermissionMatrix`, `GetProxyStatus`). These do not
  receive audit rows.
- World-view reads and map rendering (`ListWorldViewTargets`,
  `GetWorldViewStatus`, `GetWorldViewSession`, `ListCapabilities`,
  `ListMapOverlays`). Only the two mutations A106/A107 are audited.

## Disposition summary

| Disposition | Count | Meaning |
| --- | ---: | --- |
| Added | 18 | New bounded activity row plus focused test in this slice |
| Covered | 27 | Existing activity row; inventory only |
| Deferred | 65 | Explicit gap; no event schema guessed in this bounded slice |
| **Total admin rows** | **110** | Mutation rows in the table above |

The 18 additions are: auth settings (1), invite create/delete (2), API-token
create/delete (2), user create/update/delete (3), role create/update/delete/
permissions/assign/unassign (6), node create/update/delete/TLS-clear (4).

## Bounded-slice limits

- This is source inspection plus in-process tests. It is not deployed-console,
  live Docker, external identity-provider or multi-node evidence.
- Rejected mutations do not write audit rows. `DeleteUser` and `DeleteInvite`
  return not-found for a missing id instead of recording a misleading deleted
  row; tests cover those no-op deletes and the other representative refusals.
- No new event was invented for the deferred classes. Backup, task, module,
  proxy, config, content-catalog and world-lifecycle rows need a property
  schema reviewed for secrets (for example provider URLs, repository
  credentials, world names and archive paths) before they are added safely.
- API-token plaintext, invite code/PIN, passwords, TLS material and node
  endpoint credentials never enter properties. The added tests assert that
  those values do not appear in the stored row.
- Audit writes remain best-effort in this slice: a failed activity insert is
  logged and does not change the operation result. The node test explicitly
  drops the activity table and proves the mutation still succeeds.
