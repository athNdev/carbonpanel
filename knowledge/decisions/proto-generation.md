---
type: Decision
title: Staged proto generation
description: Generate and format proto outputs before replacing owned trees, and keep dependency installation frozen.
status: stable
owners: [oss-runtime]
code_refs:
  - Makefile
  - scripts/proto-generate.mjs
  - scripts/proto-generate.test.mjs
  - buf.gen.yaml
  - toolchain-versions.json
  - scripts/check-toolchain-policy.mjs
sources:
  - title: Development and generation commands
    resource: ../../docs/development.md
---

# Choice

Generate all outputs into an owned temporary directory and format TypeScript
before replacing generated trees. Existing Go generator/license header ordering
is preserved. A check compares tracked Go and TypeScript outputs after normalizing
line endings. OpenAPI schemas are untracked build outputs, not part of that check.

# Consequences

Generator and formatter failures preserve existing outputs. Ordinary filesystem
replacement errors trigger rollback; if rollback itself fails, the staging
directory is retained for recovery. Separate output renames do not provide a
single crash-atomic transaction across all trees. The installer rejects unexpected
non-generated files in managed trees. Dependencies install from frozen lockfiles;
updates are separately named commands. Buf CLI defaults to 1.50.0, independent
of the pinned remote plugin versions. See the development guide for overrides.
`toolchain-versions.json` records the Go, Bun and Buf defaults; CI checks their
duplicated Docker, Go module and workflow consumers for drift. Version tags are
not immutable digests, and explicit image build arguments remain available.
