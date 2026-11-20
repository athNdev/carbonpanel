---
type: Runbook
title: "Health and readiness — Why a runtime outage is not a `503` + How to check + Read next"
description: "/readyz gates traffic to the API. A remote Docker node that is down does not"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: health-checks.md
---
# Why a runtime outage is not a `503`

`/readyz` gates traffic to the API. A remote Docker node that is down does not
stop the API from serving the console, the catalogue or another node's servers.
Failing the probe would make the deployment restart a healthy process and take
the remaining nodes down with it. The layered endpoint reports the node as
unavailable while the process stays ready. See
[container privileges](../invariants/container-privileges.md) for the
per-container view.

# How to check

```sh
go test ./internal/health ./internal/rpc ./internal/rpc/handlers ./internal/docker ./internal/metrics ./internal/reconciler
```

# Read next

- [Runtime model](../architecture/runtime.md)
- [Lifecycle journal](lifecycle-journal.md)
- [Actionable recovery](actionable-recovery.md)
