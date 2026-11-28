---
type: Runbook
title: Deep sleep and wake
description: A player knock must not start an intentionally stopped server. Hibernation and deep sleep share one idle-power module.
status: stable
owners: [oss-runtime]
code_refs: [internal/docker/freezer.go, internal/docker/sleep.go, internal/docker/idle.go, internal/docker/sleep_wake_policy_test.go, internal/rpc/services/server.go, internal/rbac/mapping.go, docs/runbook-sleep-baseline.md, docs/deep-sleep-tier-b.md]
sources:
  - title: Sleep baseline runbook
    resource: ../../docs/runbook-sleep-baseline.md
  - title: Deep sleep tier B
    resource: ../../docs/deep-sleep-tier-b.md
  - title: In-process start admission gate
    resource: ../../docs/adr/0014-in-process-admission.md
---

# Parts

- [Deep sleep and wake — Use this when + Procedure + Player knock versus intentional stop + Known gap + Read next](deep-sleep-use-this-when.md) — A server should hibernate or wake, or a sleep-related change is being reviewed.

# Key facts

- A player knock must not start an intentionally stopped server. `WakeServer`
- Hibernation and deep sleep share `internal/docker/idle.go`. Each manager still
- A server should hibernate or wake, or a sleep-related change is being reviewed.
- `docs/deep-sleep-tier-b.md` for the tier-B behavior. Do not infer the state
- # Player knock versus intentional stop
- starts only recorded deep sleep. AutoDeepSleep remaining enabled after an
- Follow `docs/runbook-sleep-baseline.md` for the baseline and
- The reconciler does not recreate a stopped container as a substitute wake.
- wake cannot overcommit a node against another start (ADR 0014). The preparer
