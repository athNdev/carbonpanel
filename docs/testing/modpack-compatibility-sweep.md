# Modpack compatibility sweep

This is the repeatable qualification pass for deploying pre-existing modpacks on
the newest supported Minecraft version. It is intentionally sequential: one
server at a time, one result recorded per pack, so a failure cannot be masked by
another server competing for RAM or ports.

## Prerequisites

- Carbon Panel is healthy at the local dev URL and the `local-docker` node is
  online.
- The Docker daemon and its containers have working outbound DNS and TCP.
  Probe from a disposable container before starting:
  `docker run --rm alpine:3.20 sh -c 'getent ahosts maven.neoforged.net && wget -q -T 10 -O /dev/null https://maven.neoforged.net/'`
- At least 3 GB of free RAM beyond the panel itself, and free host ports.
- The modpack library has been synced (`Modpacks -> Sync`). Do not re-import a
  pack that is already present.
- Use disposable server names (`sweep-<pack-slug>`) and non-production ports.

## Test matrix

Run in this order, from small/fast packs to large/modded packs. Use the newest
game version the pack manifest advertises; do not force a version the pack does
not publish. A client-only pack is included deliberately to verify that the
panel rejects it instead of silently creating an unplayable server.

| # | Pack | Source | Loader (manifest) | Why it is in the matrix |
| --- | --- | --- | --- | --- |
| 1 | Kreatopolis-server | local upload | NeoForge 1.20.1 | Local pack path; already-created server used for the environment baseline |
| 2 | Haste | Modrinth | Fabric | Small, fast smoke test of the Modrinth install path |
| 3 | Cobblemon Official Modpack [NeoForge] | Modrinth | NeoForge | Large NeoForge pack with many client/server splits |
| 4 | Performium | Modrinth | Fabric | Performance pack; checks client-only mod rejection |
| 5 | Homestead | Modrinth | Fabric/Forge | World-generation and datapack-heavy pack |
| 6 | The Dwellers | Modrinth | Forge/NeoForge | 1.20.1 Forge-family regression coverage |
| 7 | ArdaCraft | Modrinth | Forge | Large 1.20.1 content pack and long startup path |
| 8 | Fabulously Optimized | Modrinth | Fabric | Client-only pack; expected outcome is a clear rejection or warning |
| 9 | Distant Horizons & Iris Shaders | Modrinth | Fabric | Client-only rendering pack; second negative case |

If a pack is not in the indexed library, import it from Modrinth by slug and
record the import result before creating the server. Never substitute a
different pack silently; update this table in the same change if the matrix
changes.

## Procedure per pack

1. Open `Modpacks`, select the pack, and deploy it with the recommended loader
   and version. Use 2 GB RAM unless the pack manifest requires more.
2. Start the server and record the start timestamp.
3. Wait up to 15 minutes for `RUNNING`. Watch the console for `Done` and for
   mod-resolution, mixin, or missing-dependency errors.
4. Once running, wait 5 minutes for the first full TPS sample. Run `list` and
   record online/max players and TPS.
5. Join the server once from the same machine (or verify the port is accepting
   connections if a client is unavailable). Disconnect immediately.
6. Stop the server through the panel and confirm it reaches `STOPPED` without a
   forced kill.
7. Capture: server ID, container ID, exit code (if any), last 200 console
   lines, status history, and the panel version/commit.
8. Delete the disposable server only after the evidence is attached to the Jira
   test case.

## Pass / fail criteria

Pass when all of the following hold:

- Container starts and remains up for 5 minutes after `Done`.
- No crash-loop, OOM, or mod-resolution error in the console.
- Player join succeeds (or the port accepts a Minecraft handshake when no client
  is available).
- TPS is at least 18 after warmup, with no sustained drop below 15.
- Stop completes cleanly and the panel reports `STOPPED`.

Fail when any of the following occurs:

- Container exits non-zero before `Done`.
- Server does not reach `RUNNING` within 15 minutes.
- Console reports unresolved mods, incompatible loader/version, or a world
  corruption error.
- Join fails while the panel reports `RUNNING`.
- TPS stays below 15, or the JVM is OOM-killed.
- The panel reports a state that contradicts the Docker state.

## Recording results

One Jira test case per pack, labelled `modpack-sweep` and linked to the parent
epic. The case records the pack, manifest version, result, evidence links, and
the follow-up issue when it fails. A failure that is caused by the panel, not
the pack, must also get a separate `Task` with the captured evidence and the
governing concept updated.

## Known environment blocker (2026-11-05)

The local WSL Docker daemon had DNS servers `8.8.8.8`/`1.1.1.1` configured but
no outbound route from the WSL NAT interface. Containers resolved names only
after switching Docker to the host resolver, then failed TCP connections with
`No route to host`. Kreatopolis therefore failed at `install-neoforge` with
`UnknownHostException` / `NoRouteToHostException` and exited 1.

This is an environment failure, not a panel bug. The panel correctly recorded
`ERROR` and the container exit code. Recover the host network before running
this sweep:

```powershell
# Elevated PowerShell / Command Prompt
wsl --shutdown
wsl -d Ubuntu_general
```

If the WSL NAT interface stays disconnected after that, reboot Windows or
re-enable the WSL Hyper-V network adapter. Do not "fix" this by adding a
workstation-specific DNS address to tracked configuration.
