---
type: Workflow
title: Development
description: The canonical Bun launcher owns development process trees and selects explicit loopback or LAN listener profiles.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/dev.ts
  - scripts/dev-processes.ts
  - scripts/dev.test.ts
  - scripts/dev.sh
  - scripts/dev.ps1
  - .air.toml
  - web/carbon-panel/vite.config.ts
sources:
  - title: Development commands
    resource: ../../docs/development.md
---

# Procedure

Use `bun run dev`; shell wrappers and Make delegate to the same launcher.
It builds the complete Go command package and owns only the process trees it
starts. Child failure stops its peer. Ctrl+C stops that session; name-based
machine-wide process cleanup is unsupported.

Windows live-PID tree cleanup does not establish durable ownership after a
service has already exited; full failed-parent orphan recovery remains an
explicit acceptance requirement. Do not infer it from normal-stop tests.

The ordinary development profile binds controller, proxy and Vite ingress to
loopback. LAN exposure requires `--lan`; production defaults are unaffected.
No launcher path changes firewall policy, elevates, or deletes persistent data.

# Verification

`bun test scripts/dev.test.ts` exercises actual temporary child/descendant
lifetimes, including failure and unrelated-process/data preservation. Run it on
Windows and Linux when changing supervision or process ownership.
