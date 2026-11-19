---
type: Decision
title: Named egress classes
description: Provider, webhook and archive outbound requests use class-specific policy objects; loopback is a test-only opt-in and remote saves are staged.
status: stable
owners: [oss-runtime]
code_refs: [pkg/utils/ssrf.go, pkg/utils/ssrf_egress_test.go, pkg/files/atomic.go, internal/rpc/services/file.go, internal/rpc/services/file_remote_test.go, internal/webhook/webhook.go, internal/minecraft/datapack.go, internal/rpc/handlers/packwiz_datapacks.go, internal/rpc/services/modpack.go, internal/rpc/services/modpack_egress_test.go, internal/config/config.go, config.example.yaml]
sources:
  - title: ADR 0043
    resource: ../../docs/adr/0043-egress-policy-enforcement.md
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Parts

- [Named egress classes — Choice + Alternatives](egress-class-policy-choice.md) — pkg/utils exposes three immutable EgressPolicy values, one per outbound
- [Named egress classes — Consequence](egress-class-policy-consequence.md) — New outbound families must construct a named policy rather than pass a bare
