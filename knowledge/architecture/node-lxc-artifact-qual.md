---
type: Architecture
title: "Unprivileged LXC node artifact lane — Qualification and boot evidence"
description: "Fail-closed boot qualification and runtime evidence boundaries for the LXC artifact lane."
status: draft
owners: [oss-runtime]
code_refs: [packaging/node-lxc, packaging/node-lxc/node_lxc_artifact.py, packaging/node-lxc/qualification/QUALIFICATION.md, packaging/node-lxc/qualification/SIGNED-ARCHIVE-EVIDENCE.md, packaging/node-lxc/tests/test_node_lxc.py, docs/node-lxc-mode.md, .github/workflows/lxc-boot-qualification.yml]
sources:
  - title: Parent concept
    resource: node-lxc-artifact.md
---
# Qualification and boot evidence

- `boot-qualify` is the fail-closed import/boot verification path: it verifies
  real signed upstream archive bytes in an owned disposable path, probes for a
  capable unprivileged LXC runner, and reports qualification as `status: ok`
  only when actual import and boot are performed and evidenced; blocked or
  failed runs are refused with explicit blockers and a JSON report. The manual
  workflow `.github/workflows/lxc-boot-qualification.yml` runs this on a
  disposable runner and uploads the report.
- Runtime behavior beyond signed-archive byte evidence is qualified only when a
  capable runner produces actual import/boot evidence. Without that evidence,
  live import/boot, effective kernel enforcement and gameplay compatibility are
  not claimed. Pin and signed-archive byte verification is evidence about
  upstream artifacts, not boot evidence.
