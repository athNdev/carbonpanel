---
title: Changelog
description: Release history for Carbon Panel.
---

# Changelog

See the project [CHANGELOG.md](https://github.com/athNdev/carbonpanel/blob/main/CHANGELOG.md) for the full release history.

:::note[Versioned documentation]
Published `/docs/` and `/docs/versions/<version>/` pages are built from the exact
peeled commit named in the shared version index; this working-tree changelog is
a development preview only. `v3.0.0-beta.4` is an excluded upstream name collision (not a Carbon Panel release) and `v1.0.38` is excluded because its
local and authoritative remote tag ancestry differ. The verified stable is
`1.0.40` (`v1.0.40`).
:::

## 3.0.0-beta.4 - 2026-11-07

- Modpack Studio tabs for datapacks, resource packs and shader packs, with Modrinth search, direct URLs, validated uploads, and every packwiz metafile option.
- Server bakes install datapacks into the world and report client-only content; CurseForge export refuses a partial pack instead of dropping entries.
- Fullscreen server console fills the viewport and keeps its toolbar and command bar.
- Creating a server from a modpack keeps the RAM value you enter; the pack recommendation is a hint.
- Open Graph and Twitter previews on every website page, with dedicated cards for the main entry points.
- New guides for Modpack Studio, server datapacks, staged updates, sleep and wake, and proxy routing.
- Global error boundary with retry and home affordances, plus accessibility and robustness fixes.

## 3.0.0-beta.3

- Deep-sleep / wake-on-connect.
- Scheduler backup pruning and snapshot rollback safety.
- Remote node and proxy hardening.

## 2.0.15

- Multi-node clustering and remote Docker management.
- OIDC authentication.
- Modpack import/export.

## 1.0.38

- Initial single-node server management.
- Carbon Design System web UI.
