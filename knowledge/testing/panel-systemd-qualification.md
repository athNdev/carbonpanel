---
type: Workflow
title: Packaged Linux systemd qualification
description: Qualify a controller archive with a fresh install and in-place upgrade on hosted Linux.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/qualify_panel_systemd.py
  - .github/workflows/qualify-panel-systemd.yml
sources:
  - title: Release bundle operator instructions
    resource: ../../docs/release-bundle.md
---

Dispatch **Qualify packaged Panel under systemd** with the Carbon Panel Release
run ID, full candidate commit, published Stable tag and full Stable commit.
Choose the same product commit and artifact intended for release. The workflow
checks the Actions artifact digest, release manifest, checksum file, native
package identity and running binary hash. The release workflow commit and
selected product commit are recorded separately. It builds no product code.

The hosted amd64 and arm64 jobs start each controller from the unpacked archive
in a transient systemd unit with real Docker and unique persistent paths.
They exercise first-admin setup, login, inventory, health, UI and service
restart. The upgrade backs up the database and configuration, swaps both
programs, and verifies the same account and fixture bytes. Use the report
and journals for the tracker. The scoped evidence covers the representative
Linux binary service; appliance boot and gameplay require separate runs.
