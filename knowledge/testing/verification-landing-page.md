---
type: Workflow
title: "Verification — Landing page + Proto + Knowledge + Local note"
description: "python3 -m unittest scripts.test_landing_page"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: verification.md
---
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
