---
type: Workflow
title: Server portability archive acceptance rehearsal
description: Hosted exact-head ZIP and tar.gz round trip through a second Docker engine, including preview, import hash checks and Minecraft readiness.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/server-archive-rehearsal.yml
  - scripts/qualify-server-archive-hosted.sh
  - scripts/server_archive_rehearsal_ci.py
  - scripts/qualify-archive-player.mjs
  - scripts/test_server_archive_rehearsal_ci.py
sources:
  - title: Server portability archive format
    resource: ../../docs/server-portability.md
  - title: Architecture decisions — Disposable server-portability acceptance rehearsal
    resource: ../../docs/adr/0053-disposable-server-archive-acceptance.md
---

Run the `Server archive acceptance rehearsal` workflow at the exact candidate
head. It is CI-only: the wrapper refuses to run outside a GitHub-hosted job and
outside `$RUNNER_TEMP`, and it never publishes, deploys or contacts a live
panel, world or registry.

The job builds the controller from the checkout, starts it with an
exact same-path disposable workspace and a dedicated managed network, and
starts a second privileged Docker-in-Docker engine with a separate `/storage`
mount as the registered remote node. The Python harness creates a managed
Vanilla source without Docker overrides, refuses an export while it runs,
saves and writes a deterministic fixture, then exports both native formats.
It verifies each archive independently (schema, runtime, every file SHA-256,
expanded size, excluded credential fixture), uploads and previews against the
remote node, creates each import stopped, compares the destination bytes to the
manifest, and only then starts and joins both imports with the pinned
Minecraft protocol client.

Fail closed on every check. The JSON report records the candidate commit,
image revision label, archive hashes, preview capacities, destination
verification and readiness. A failed run still writes a failing report and the
wrapper still removes its containers and uploads evidence.

# Rules

- This rehearsal proves the native archive round trip and Minecraft readiness.
  It does not prove a production remote host, operator ingress, proxy routing,
  OIDC, a published release or a live deployment. State those limits in the PR
  receipt rather than upgrading the claim.
- The separate engine is a disposable Docker-in-Docker container, not a real
  node VM. A loopback Docker endpoint is invalid because the controller
  classifies loopback as its own local engine.
- Do not replace the exact-head build, the pinned readiness client or the
  expected ZIP and tar.gz formats with fixtures. Fake-Docker Go tests remain
  necessary but are not acceptance evidence.
- Keep the fixture deterministic and credential-bearing fixture files inside the
  archive's exclusion rules; never add a real secret or a live endpoint.
