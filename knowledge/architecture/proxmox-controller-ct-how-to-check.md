---
type: Architecture
title: "Proxmox controller CT — How to check"
description: "Verification entry points for the controller CT artifact"
status: stable
owners: [oss-runtime]
code_refs: [scripts/test_proxmox_controller_ct.py, .github/workflows/proxmox-controller-ct.yml]
sources:
  - title: Parent concept
    resource: proxmox-controller-ct.md
---
# Check

- `python3 -m unittest scripts.test_proxmox_controller_ct -v` covers archive
  root shape, ownership/modes/links, guest evidence, failed-pull retry,
  sticky success, refusals, pin drift and the publication gate.
- `proxmox_controller_ct.py verify-manifest` cross-checks the controller
  version/digest against `docs/version-index.json` Stable and the Debian base
  profile against the manifest.
- The PR workflow builds the real signed rootfs and uploads the verified
  artifact; the publish job re-verifies its digest with `--require-real` before
  a draft is promoted to a prerelease. Verification requires the enabled
  `multi-user.target.wants` first-boot symlink and a real receipt with signed
  Debian 13 InRelease digest, publishable flag and complete package hashes.
- Draft promotion uses the release-id API URL from `gh release view --json`,
  never `GET /releases/tags`; `ct_release_draft_gate.py` proves the view is
  still a draft for the exact tag and target commit and that attached asset
  names/digests match before the draft becomes a prerelease.
- A successful fixture run is not a live `pct` import/boot proof. No such
  runner exists in this lane; record the limitation rather than claiming one.
