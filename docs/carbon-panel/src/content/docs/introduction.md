---
title: Introduction
description: What Carbon Panel is and what it does.
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


Carbon Panel is a self-hosted control plane for Minecraft servers. It runs each
server in its own Docker container and gives you one web interface for creating
servers, routing players, building modpacks, backing up worlds, and controlling
who can do what.

## What you can do

**Run servers your way.** Vanilla, Paper, Purpur, Fabric, Quilt, Forge, NeoForge
and more, with per-server memory, CPU, ports, JVM flags and Docker overrides.
Servers can run on the local Docker host or on a remote node managed by the same
panel.

**Give every server a hostname.** The built-in proxy routes players by hostname
on a single port, so `survival.mc.example.com` and `creative.mc.example.com` can
share one address. See [Proxy routing](/guides/proxy-routing/).

**Build and deploy modpacks.** Modpack Studio edits packwiz projects: search
Modrinth and CurseForge for mods, add datapacks, resource packs and shader
packs, set every packwiz option, and export for launchers or deploy to a server.
See [Modpack Studio](/guides/modpack-studio/).

**Install datapacks directly.** The server's Datapacks tab browses Modrinth,
uploads archives, and manages the world's `datapacks/` folder. See
[Server datapacks](/guides/server-datapacks/).

**Prepare changes, not surprises.** Staged updates collect configuration, mods,
datapacks and module changes and apply them together at the next restart, a UTC
time, or an idle window. See [Staged updates](/guides/staged-updates/).

**Let idle servers rest.** Native pause, cgroup freeze and deep sleep trade
startup time for CPU and memory, and wake on player join. See
[Sleep and wake](/guides/sleep-and-wake/).

**Back up and restore.** World-aware backups run on a schedule, pruned by
retention rules, with a documented restore path. See
[Server backups](/guides/backups/).

**Control access.** Local accounts, roles and server-scoped permissions, OIDC
single sign-on, API tokens and an audit log. See
[Configure sign-in](/guides/oidc/keycloak/).

**Automate it.** Every operation is available over Connect RPC, with an
interactive API reference generated from the same application snapshot as these
docs. See [API reference](/api/).

## Where to go next

| You want to... | Start here |
| --- | --- |
| Install the panel | [Docker Compose](/getting-started/docker-compose/) |
| Configure it | [Configuration](/configuration/) |
| Build a modpack | [Modpack Studio](/guides/modpack-studio/) |
| Add datapacks to one server | [Server datapacks](/guides/server-datapacks/) |
| Stage updates | [Staged updates](/guides/staged-updates/) |
| Save CPU or memory when idle | [Sleep and wake](/guides/sleep-and-wake/) |
| Route players | [Proxy routing](/guides/proxy-routing/) |
| Back up a server | [Server backups](/guides/backups/) |
| Set up single sign-on | [OIDC guides](/guides/oidc/keycloak/) |
| Call the API | [API reference](/api/) |

Carbon Panel is under active development. The docs display the exact application
commit they describe on [Documentation version](/versions/). Check it against
your installation when following a guide.
