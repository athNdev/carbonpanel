# ADR 0053: Disposable server-portability acceptance rehearsal in CI

- Status: Accepted
- Date: 2026-11-26
- Owners: oss-runtime
- Relates to: OSS-426 (native archive export), OSS-427 (archive preview and
  import), OSS-428 (create-from-archive wizard), ADR 0005 (node-owned storage),
  ADR 0044 (disposable CI rehearsals)

## Context

The server-archive feature ships a versioned native ZIP/tar.gz format and a
create-from-archive path. Its Go tests use fake Docker and synthetic archives;
they prove the format and RPC contracts but not a real controller, a second
Docker engine, a real Minecraft image or a full round trip. The acceptance
claims for OSS-426/427/428 therefore need a reproducible hosted job that can be
run at the exact candidate head without touching a live host, world, registry,
tag or release.

## Decision

`.github/workflows/server-archive-rehearsal.yml` runs one disposable
GitHub-hosted Linux job. It has only `contents: read`, no deployment
credentials and no publication step. The job checks out the exact head, builds
the `carbon-panel` and `carbon-storage` binaries and a controller image with
the commit revision label, then calls
`scripts/qualify-server-archive-hosted.sh`.

The hosted shell owns two isolated engines inside `$RUNNER_TEMP`:

1. the runner's Docker engine for the controller and the source server; and
2. a privileged Docker-in-Docker engine with a separate `/storage` mount for
   the registered remote node.

It starts the controller as a container with an exact same-path workspace
mount, a loopback listener and a dedicated managed network. The harness
(`scripts/server_archive_rehearsal_ci.py`) then creates a managed Vanilla source
without Docker overrides, proves a running export is refused, saves and
populates a deterministic fixture, exports both native formats, verifies each
archive's schema/runtime/per-file SHA-256 and expanded size, uploads and
previews each archive against the remote node, creates each import stopped,
verifies the destination bytes before start, and only then starts both imports
and joins them with the pinned Minecraft protocol client.

Every check is fail-closed and written to a JSON report. The wrapper removes its
containers and uploads only job evidence; it refuses to run outside a hosted
job or outside `$RUNNER_TEMP`.

## Consequences

- The archive round trip can be rerun at an exact head without a developer
  workstation, a live panel or a shared Docker daemon.
- The harness is deliberately narrower than a release qualification: it uses
  one Minecraft version, one managed Vanilla source, a synthetic fixture, a
  Docker-in-Docker node rather than a production remote host, and no operator
  ingress or proxy. Those limits are reported by every run.
- The first full hosted run is still required before claiming OSS-426/427/428
  live acceptance. Local work can verify the Python archive/lifecycle checks and
  shell/JavaScript syntax, but not Docker, Minecraft or hosted evidence.
- No release approval rule, registry alias or publication permission changes.
