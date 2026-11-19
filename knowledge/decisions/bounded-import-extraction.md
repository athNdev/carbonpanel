---
type: Decision
title: Bounded modpack import extraction
description: All modpack imports use the shared bounded extractor in a staging directory; unsafe entries are rejected, never silently skipped.
status: stable
owners: [oss-runtime]
code_refs:
  - pkg/files/files.go
  - internal/packwiz/importer.go
  - internal/rpc/handlers/packwiz.go
sources:
  - title: Contributor guide
    resource: ../../AGENTS.md
---

# Choice

Every Zip/mrpack/CurseForge/Packwiz-native import spills the uploaded reader to
a temporary `.zip` and extracts into a staging directory through
`files.ExtractArchiveWithLimits`. That is the same primitive used by generic
archive extraction: one aggregate uncompressed budget, a per-file budget, an
entry-count ceiling, refusal of symlink/device entries, and a
symlink-evaluating containment check. The importer copies only the selected
prefix from staging into the pack directory after extraction succeeds; an
oversized entry, traversal name or symlink fails the whole import before any
pack files are written. `downloads[]` URLs are validated at import time with the
provider static policy; userinfo and non-http(s) schemes are rejected and no
raw URL is reflected in an error. The multipart handler bounds the whole request
with `MaxBytesReader` before `ParseMultipartForm` and caps the `io.ReadAll`.

# Alternatives

Keeping per-importer `files.Within` plus `io.Copy` under a per-file limit is
cheaper but gives no aggregate cap, silently skips traversal entries, and lets
a small upload expand without bound. Adding those checks independently in each
importer would fork the policy. Validating download URLs only when the mod is
later fetched lets a hostile index be persisted first.

# Consequence

New import formats must route through the shared bounded extractor rather than
opening archive entries directly. Import caps are configurable through
`ManagerOptions.ImportLimits`; the zero value keeps the documented 20 GiB
generic-archive defaults. Generous defaults keep legitimate large packs working;
lowering them is an explicit operator decision, not an automatic behaviour
change. Import-time URL validation is static (no DNS): a hostname that later
resolves into a private network is still stopped by the guarded download client.
