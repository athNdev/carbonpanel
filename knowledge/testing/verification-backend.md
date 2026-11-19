---
type: Workflow
title: "Verification — Backend + MCP coverage gate + Frontend + Production frontend build"
description: "make test is the same suite. CI also compiles every package for Windows,"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: verification.md
---
# Backend

```sh
go build ./...
go vet ./...
go test ./...
```

`make test` is the same suite. CI also compiles every package for Windows,
and executes the disk, config, proxy and module tests on native Windows.

# MCP coverage gate

Ordinary CI runs the STRICT full-coverage gate on the Go version pinned by
`go.mod` (1.26.11), with a fail-closed negative fixture, a byte-reproducible
catalog check and a real-binary stdio `tools/list`/`tools/call` smoke. See
[MCP gate separation evidence](../../docs/evidence/mcp-gate-separation.md).

The pinned JSON vulnerability scanner must exit successfully and emit its
supported configuration and populated scan roots. Tool, database, typecheck
or malformed-output failure fails the gate. Reachable findings with fixes fail;
unfixed reachable findings are reported without blocking. CI retains raw scan
JSON and the policy result, plus a full lint report with revision/toolchain
alongside the changed-line lint gate. A full lint exit 1 is accepted only with
valid report JSON, no report error and no typecheck findings; mixed analyzer
errors still fail. See [validation](../../docs/testing/validation.md).

# Frontend

```sh
cd web/carbon-panel
bun install --frozen-lockfile
bun run check
bun run test
bun run lint
```

# Production frontend build

```sh
bash scripts/web-build.sh
```
