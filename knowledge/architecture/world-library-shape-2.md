---
type: Architecture
title: "World Library â€” Rollback rules (part 2)"
description: "Failed multi-family swap compensation: quarantine, byte proof and binding proof."
status: stable
owners: [oss-runtime]
code_refs: [internal/worldlib/binding.go, docs/adr/0049-world-swap-data-preserving-rollback.md]
sources:
  - title: Parent concept
    resource: world-library.md
---
# Rollback rules

- A failed multi-family publish quarantines already-promoted world bytes in a
  separate tree instead of mixing them with the retained old family.
- Compensation restores each retained family and checks its path and any old
  `level.dat` against the pre-swap observation. File hashing uses a stream.
- Restoring the prior binding follows disk verification. Tree cleanup requires
  a terminal journal decision.
- Ambiguous I/O deletes nothing: both trees stay for operator recovery, the
  journal keeps its lock, and the outcome is `unknown` (ADR 0049).
- Active operations prevent retry and startup tree changes. Legacy recovery
  without an active operation restores only when the live path is confirmed
  missing and preserves staged and quarantined trees.
