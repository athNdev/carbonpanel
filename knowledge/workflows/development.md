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
  - cmd/carbon-dev-supervisor
  - scripts/testdata/dev-process
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

Windows commands use a native helper; Windows assigns the suspended root to a
kill-on-close Job Object during creation, before it resumes. It owns descendants
across root exit/crash and watches a held launcher handle; a reused PID cannot become the
owner. Helper death also closes the non-inherited job handle. A failed
assignment refuses startup. See the [ownership decision](../decisions/development-process-ownership.md).

The content-keyed helper is prepared by a nonprivileged Go build in `tmp`.
Normal stop owns that live compiler tree. Abrupt launcher death during this
first bootstrap remains outside the native job guarantee; ordinary service
and fallback-build commands start only after preparation and use jobs.

The ordinary development profile binds controller, proxy and Vite ingress to
loopback. LAN exposure requires `--lan`; production defaults are unaffected.
No launcher path changes firewall policy, elevates, or deletes persistent data.

# Verification

`bun test scripts/dev.test.ts` exercises actual temporary child/descendant
lifetimes, including failure and unrelated-process/data preservation. Run it on
Windows and Linux when changing supervision or process ownership. The fixture
uses a native process so runtime-specific JavaScript child cleanup cannot hide
an orphaned descendant. Windows also covers abrupt owner/helper death and
stdin/stdout inheritance, plus real hidden-terminal input, output and Ctrl+C.
Cold native Go setup has an explicit three-minute runner allowance, including
Bun's hook timeout behavior. Already-prepared service readiness remains bounded
at five seconds; setup time must not be mistaken for a service ownership failure.
