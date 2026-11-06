---
title: Troubleshooting
description: Common issues and how to report bugs.
---
<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->


Start with the [FAQ](/faq/). This page covers the failures that have a known
cause and the details to collect before opening an issue.

## The panel does not start

Carbon Panel checks its data directory before it serves anything. Startup stops
with a message naming the directory when:

| Message | Cause | Fix |
| --- | --- | --- |
| No volume is mounted at the data directory | The container writes worlds into its own filesystem, which disappears on recreate. | Add the data volume to the compose file. |
| Volume is mounted read-only | The mount has `:ro`, or the host filesystem is read-only. | Remove the read-only flag and restart. |
| Data directory is a `tmpfs` | Worlds would be lost on restart. | Use a persistent volume instead. |
| `no write permission on the mount dir` | The container user cannot write to the host path. | Fix ownership or permissions on the host path. |
| `mount path should be absolute` | A legacy `CARBONPANEL_HOST_DATA_PATH` override is relative. | Use an absolute path or remove the override. |

The panel reads the host path from its own container mount table. There is no
path variable to keep in sync; see [Docker Compose](/getting-started/docker-compose/#volume-paths).

Other startup failures:

- **Port already in use.** Another process owns `8080`, or `25565` when the
  proxy is enabled. Change `server.port` or the proxy listener.
- **Cannot reach the Docker daemon.** The socket is not mounted, or the panel
  user cannot access it. Check the `/var/run/docker.sock` volume and its
  permissions.
- **Panel starts but the UI is unreachable.** With host networking, confirm the
  port is not firewalled. With bridge networking, confirm the port mapping.

## A server will not start

- **`failed_precondition` in the task or server error.** This is usually a
  permissions problem. Minecraft containers run as `PUID:GID 1000:1000` by
  default and need access to the panel's data directory. Check with `ls -la`,
  then fix ownership with `chown`.
- **Out of memory.** The server container limit and the JVM heap are linked.
  Raise the server's Memory value in the overview, not the JVM flags.
- **Mod or loader mismatch.** After changing the Minecraft version or loader,
  the pack can contain incompatible mods. Open the Studio's Migration tab to
  see which entries have a compatible release.
- **Port conflict.** A published additional port is already in use on the host.
  Change or remove it in Additional Ports.

## The server sleeps when it should not

An empty server can be paused by three independent mechanisms: Minecraft's
native `pause-when-empty-seconds`, the panel's **Freeze on Idle**, and the
panel's **Deep Sleep on Idle**. Check the server's Settings page for the two
panel toggles and `server.properties` for the native setting. See
[Sleep and wake](/guides/sleep-and-wake/).

## Modpack content

### An archive is rejected

Uploads are validated before they become pack entries:

- Datapacks need `pack.mcmeta` at the archive root.
- Resource packs need `pack.mcmeta` and `assets/` at the root.
- Shader packs need a `shaders/` directory.
- A zip wrapped in a single top-level folder is rejected. Re-pack it so those
  entries are at the root.

Uploads are capped at 512 MiB.

### CurseForge export refuses

CurseForge manifests need numeric project and file IDs. If any entry lacks
them, the export stops and lists every item it cannot carry. Remove those
entries, or export as `.mrpack`, packwiz or an instance archive.

### An entry never shows an update

Update checks only query Modrinth-tracked entries. Pinned entries are skipped,
and direct URLs and uploads are not tracked at all. Unpin the entry or add it
from a Modrinth project to track it.

### A datapack does not load in game

- The world has to reload. Restart the server after installing or deleting a
  datapack.
- Check the pack-format warning on the server's Datapacks tab. A datapack built
  for another Minecraft version is often ignored.
- If the datapack is part of a pack, confirm the deployment mode. Bake writes
  to `<world>/datapacks/`; sync delivers the pack and lets packwiz place it in
  the pack's datapack folder.

### Resource packs or shaders are missing on a server

They are client-side content. A server bake does not write them; deliver them
with packwiz sync or an export. See
[Deploy to a server](/guides/modpack-studio/#deploy-to-a-server).

### Deploy reports missing node storage

Bake needs the server's node storage to be reachable. A remote node with
unreachable storage returns `503` instead of writing a partial pack. Restore the
node's storage, then deploy again.

## Backups

- Backups live in the directory configured by `storage.backup_dir`, grouped per
  server. If the panel runs in Docker, mount it as a volume.
- Retention is scoped to the task's backup name. Two tasks with different names
  do not prune each other's archives.
- A restore is not a panel button: stop the server, replace the world folders
  from the archive, then start it. See
  [Restoring a backup](/guides/backups/#restoring-a-backup).

## Single sign-on

- **Redirect loop or `redirect_uri` error.** The identity provider's redirect
  URI must match the panel's public URL exactly, including scheme and path.
- **`no_mapped_roles` or access denied.** The provider authenticated the user
  but no role mapping matched. Give the user or group a role in the panel.
- **Login works but every action is forbidden.** Check the user's roles and
  server-scoped permissions before assuming an OIDC fault.

Provider-specific walkthroughs are under [OIDC guides](/guides/oidc/keycloak/).

## Collecting details

Before opening an issue, gather:

1. Panel version or the commit shown on [Documentation version](/versions/).
2. Docker and Compose versions.
3. The exact error, including the panel log line around it.
4. For a server failure, the container's last console lines and the task history
   entry.
5. What changed: a pack edit, a staged update, a panel update, a host change.

## Reporting issues

1. Check this page and the [FAQ](/faq/) first.
2. Try the latest version of Carbon Panel.
3. Open an issue at [github.com/athNdev/carbon-panel/issues](https://github.com/athNdev/carbon-panel/issues)
   with the details above.

For quicker help, ask in the [Discord](https://discord.gg/6Z9yKTbsrP).
