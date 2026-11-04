---
type: Workflow
title: Modpack compatibility sweep
description: How to qualify pre-existing modpacks sequentially on the newest supported Minecraft version.
status: stable
owners: [oss-runtime]
code_refs:
  - docs/testing/modpack-compatibility-sweep.md
  - web/carbon-panel/src/routes/modpacks
sources:
  - title: Modpack compatibility sweep
    resource: ../../docs/testing/modpack-compatibility-sweep.md
  - title: Verification
    resource: verification.md
---

# Shape

One disposable server per pre-existing modpack, created from the library in a
fixed order. Record one Jira test case per pack. The matrix deliberately covers
Fabric, Forge/NeoForge, large content packs, a local upload, and client-only
packs as negative cases.

# Rules

- Run sequentially, never in parallel; a failure must not be masked by another
  server competing for RAM or ports.
- Use the newest game version the pack manifest advertises. Never force a
  version the pack does not publish.
- Probe Docker DNS and outbound TCP before the sweep. A container that resolves
  a name but cannot open a socket is a host-network failure, not a pack failure.
- Capture the container exit code and the last 200 console lines before
  deleting a failed server. Delete only after the evidence is attached.
- A panel-caused failure gets a separate tracked task and a knowledge update;
  a pack-caused failure stays attached to the pack test case.
- Never point the sweep at an operator world or a production database.

# Read next

- [Verification](verification.md)
- [Frontend change](../workflows/frontend-change.md)
