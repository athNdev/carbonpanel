---
type: Architecture
title: "World Library — Rollback rules (part 2)"
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
- Compensation restores each retained `level.dat` and compares its digest with
  the value captured before mutation; a missing or changed family is ambiguous.
- The prior binding row is restored only after the bytes are proven, then read
  back and compared. A rolled-back binding is never reported before that proof.
- Ambiguous I/O deletes nothing: both trees stay for operator recovery, the
  journal keeps its lock, and the outcome is `unknown` (ADR 0049).
- Recovery restores a retained family only when the live family is missing,
  preserves staged and quarantined trees, and refuses ambiguity.
