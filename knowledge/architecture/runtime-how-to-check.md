---
type: Architecture
title: "Runtime model — How to check + Read next"
description: "Detailed guidance on how to check."
status: stable
owners: [oss-runtime]
code_refs: [cmd/carbon-panel, internal/rpc, internal/proxy, internal/db, internal/docker, internal/docker/resource_enforcement.go, pkg/runtimeprofile, web/carbon-panel/src/lib/utils/resource-enforcement.ts, pkg/files, internal/scheduler, internal/operation, internal/reconciler, web/carbon-panel]
sources:
  - title: Parent concept
    resource: runtime.md
---
# How to check

```sh
go build ./...
go vet ./...
go test ./...
```

# Read next

- [RPC contract](rpc-contract.md)
- [Persistence](persistence.md)
- [Scheduler](scheduler.md)
