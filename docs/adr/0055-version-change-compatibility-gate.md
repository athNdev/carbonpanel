<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

# ADR 0055 — Version changes are gated by installed-content compatibility

Status: Accepted (2026-11-23).

## Context

`UpdateServer` rewrote `mc_version`/loader and recreated the container with no
analysis of the content that survives recreation by design. Mods pinned to the
old version then boot-crashed, and the failure surfaced only afterwards through
log scanning, as a broken server.

## Decision

1. Before any mutation, the version change scans installed content for
   declared compatibility with the target: `fabric.mod.json` /
   `quilt.mod.json` `depends` for mods, `plugin.yml` / `paper-plugin.yml`
   `api-version` for plugin loaders, and datapack pack-format ranges for
   downgrades.
2. Any incompatible file refuses the whole change with `FailedPrecondition`,
   listing each offending file, its constraint and the target version. The
   stored row is not touched before this gate.
3. Unreadable files, missing metadata and unparseable constraints are
   "unknown" and fail open — the panel refuses only what it can prove is
   broken. Forge-style ranges are not yet parsed and report unknown. This
   fail-open applies to a readable location: when the server's storage cannot
   be inspected at all (`nodestorage.ErrUnavailable` — no storage adapter for
   the node, an inaccessible controller storage root, or storage reachable
   only through the node's transport, whose node-side path the controller
   cannot walk), the change is refused with `FailedPrecondition` rather than
   skipping the gate.
4. The gate is a pre-flight, not a wrapper: modpack-driven provisioning keeps
   its own path, and a failed recreation after a passed gate still uses the
   existing recovery flow.

## Consequences

- Version changes fail closed with actionable output instead of open with a
  crash loop; the operator deletes or updates the listed content first.
- The matcher must stay conservative: a false refusal blocks a legitimate
  upgrade, so unknown beats wrong.
