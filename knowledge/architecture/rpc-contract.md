---
type: Architecture
title: RPC contract
description: Proto definitions are the single API contract for the backend, console and CLI.
status: stable
owners: [oss-runtime]
code_refs: [proto/carbonpanel/v1, internal/rpc, buf.gen.yaml, web/carbon-panel/src/lib/proto, internal/nodescan]
sources:
  - title: Design specification
    resource: ../../DESIGN.md
---

# Parts

- [RPC contract — Rule](rpc-contract-rule.md) — Every frontend or CLI call goes through a service defined in
- [RPC contract — Rule (part 2)](rpc-contract-rule-2.md) — Packwiz pack control and staged config rollout are further compatibility
- [RPC contract — Flow + Invariants + Read next](rpc-contract-flow.md) — Regenerate with make proto (Docker + pinned Buf); generation and formatting
