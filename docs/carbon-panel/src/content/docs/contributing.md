---
title: Contributing
description: Build, test and submit changes to Carbon Panel.
---
<!--
SPDX-License-Identifier: MIT AND MPL-2.0
This file includes DiscoPanel material under the MIT License,
Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
and still applies to that inherited material.
Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
If a copy of the MPL was not distributed with this file, You can obtain
one at https://mozilla.org/MPL/2.0/.
-->


Carbon Panel is an open-source project. Contributions, bug reports and
documentation fixes are welcome.

## Prerequisites

- Go 1.26 for the backend.
- Node.js and Bun 1.4.2 for the console.
- Docker for anything that starts a server container or exercises the daemon.
- Buf 1.50.0 for protobuf changes.

## Repository layout

| Path | Contents |
| --- | --- |
| `cmd/`, `internal/`, `pkg/` | Go backend: RPC services, Docker runtime, storage, scheduler, proxy. |
| `proto/` | Protobuf definitions and the generated Connect surface. |
| `web/carbon-panel/` | SvelteKit console (Svelte 5, Bun, Carbon design tokens). |
| `landing/`, `docs/` | Website and documentation sources. |
| `knowledge/` | Agent-facing context base. |
| `scripts/` | Build, review and verification tooling. |

## Build and verify

Run the narrowest check while iterating, then every affected surface before
opening a pull request.

```bash
go build ./...
go vet ./...
make test

cd web/carbon-panel
bun install --frozen-lockfile
bun run check
bun run test
```

Build the embedded console before producing a binary:

```bash
bash scripts/web-build.sh
go build -o carbon-panel ./cmd/carbon-panel
```

Protobuf changes also need `make proto` and `make proto-lint`. The knowledge
base has its own lint:

```bash
make knowledge-index
make knowledge-lint
```

## Development loop

`make dev` (or `bun run dev`, `scripts/dev.sh`, `scripts/dev.ps1`) starts the
backend and the console together. The frontend runs at
<http://localhost:5174> and proxies API and RPC requests to the backend at
<http://localhost:8080>.

## What a change needs

- A regression test at the closest practical seam for any fixed bug.
- Authentication and authorization fail closed. A new RPC without a permission
  mapping is incomplete.
- No secrets, credentials, generated state, customer data, server worlds or
  workstation paths in tracked source.
- Container privileges stay at what the server image needs, including operator
  overrides.
- Configuration instead of machine-specific paths or addresses.
- Compose, Docker, Helm, Terraform and release documentation updated when their
  behavior changes.

## Documentation

- Stable architectural decisions go in `docs/adr/`; user-facing operations go in
  `docs/`.
- Agent-facing context lives in `knowledge/`. Update the governing concept in the
  same commit as a behavior change, then run the knowledge lint.
- The published documentation under `docs/carbon-panel/src/content/docs/` is
  pinned to an application commit in `docs/website-source.json`. The site build
  takes most documentation content from that commit and the presentation from
  the current branch, so a content change needs the pin advanced by a
  maintainer. Website changes are validated with:

```bash
python3 -m unittest scripts.test_website scripts.test_landing_page scripts.test_design_system_page
python3 scripts/build_website.py
python3 scripts/check_website.py
```

## Pull requests

- Keep commits focused and use an imperative subject line.
- Say what changed for users and how you verified it.
- Let CI finish; backend, frontend, prototype and website checks all gate the
  merge.

## License

Carbon Panel is licensed under the Mozilla Public License 2.0. Parts inherited
from the DiscoPanel project remain under the MIT License; see `LICENSE` and
`LICENSING.md`. New source files carry an SPDX header.
