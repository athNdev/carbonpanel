---
type: Workflow
title: Verification
description: The commands that define a verified change in this repository.
status: stable
owners: [oss-runtime]
code_refs:
  - Makefile
  - .github/workflows/ci.yml
  - cmd/govulncheck-policy
  - scripts/check_lint_report.py
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
  - title: Validation policy and reports
    resource: ../../docs/testing/validation.md
---

# Backend

```sh
go build ./...
go vet ./...
go test ./...
```

`make test` is the same suite. CI also compiles every package for Windows,
and executes the disk, config, proxy and module tests on native Windows.

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

# Landing page

```sh
python3 -m unittest scripts.test_landing_page
python3 scripts/check_brand_assets.py
```

CI runs these checks for the standalone landing page and distributed brand assets.

# Proto

```sh
make proto-lint
```

# Knowledge

```sh
go run ./cmd/knowledge-lint -root . -fix
go run ./cmd/knowledge-lint -root .
node scripts/mcp-smoke.mjs
```

The linter runs the fixed queries in `knowledge/queries.yaml`. The MCP smoke
needs Node 24+ and network access on the first run.

# Local note

The lint script passes `--end-of-line auto` to Prettier so existing LF and CRLF
checkouts use the same entrypoint. It checks formatting without rewriting source
or changing the repository's tracked line-ending policy.
