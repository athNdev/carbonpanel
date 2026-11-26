# ADR 0053: Disposable server-portability acceptance rehearsal in CI

- Status: Accepted
- Date: 2026-11-26
- Owners: oss-runtime
- Relates to: OSS-426 (native archive export), OSS-427 (archive preview and
  RPC import), OSS-428 (create-from-archive browser workflow, tracked separately
  in #686), ADR 0005 (node-owned storage), ADR 0044 (disposable CI rehearsals)

## Context

The server-archive feature ships a versioned native ZIP/tar.gz format and a
create-from-archive RPC path. Its Go tests use fake Docker and synthetic
archives; they prove the format and RPC contracts but not a real controller, a
second Docker engine, a real Minecraft image or a full round trip. The backend
acceptance claims for OSS-426/427 therefore need a reproducible hosted job that
can be run at the exact candidate head without touching a live host, world,
registry, tag or release. The OSS-428 create-from-archive browser acceptance is
a separate UI claim owned by #686 and is not proven by this job.

## Decision

`.github/workflows/server-archive-rehearsal.yml` runs one disposable
GitHub-hosted Linux job for repository-owned pull requests or an explicit
dispatch. It has only `contents: read`, no deployment credentials and no
publication step. The job checks out the PR head SHA (the same value exported as
`STAGED_SHA`), builds the `carbon-panel` and `carbon-storage` binaries with an
empty frontend embed and a controller image with the commit revision label, then
calls `scripts/qualify-server-archive-hosted.sh`.

The hosted shell owns two isolated engines inside `$RUNNER_TEMP`:

1. the runner's Docker engine for the controller and the source server; and
2. a privileged Docker-in-Docker engine on an owned bridge with a separate
   `/storage` mount for the registered remote node.

The Docker-in-Docker engine does not use host networking and publishes no
management port. The controller reaches `tcp://<bridge-address>:2375`; that
literal address is non-loopback, so the node is classified as remote, and the
same dedicated bridge address is the probe host for nested game ports. Nested
pulls use the bridge's outbound networking. The engine image is pinned by a
digest verified against the primary Docker Hub registry metadata and recorded in
the fixture provenance.

The controller starts as a container with an exact same-path workspace mount, a
loopback listener and a dedicated managed network. Before any controller write,
`scripts/server_archive_rehearsal_ci.py` requires a GitHub-hosted caller, a fresh
`RUNNER_TEMP` subdirectory, a clean exact checkout and an exclusively created
fixture record that ties the checkout, controller container, loopback base URL
and remote endpoint together. It refuses redirects to other targets.

The harness then creates a managed Vanilla source without Docker overrides,
requires both the `failed_precondition` code and the specific running-export
refusal message, saves and populates a deterministic fixture, exports both
native formats, verifies each archive's schema/runtime/per-file SHA-256 and
expanded size, uploads and previews each archive against the remote node,
creates each import stopped, verifies the destination bytes before start, and
only then starts both imports and joins them with the pinned Minecraft protocol
client.

Every check is fail-closed and written to a JSON report. Cleanup is bounded and
verifies that only servers created by this run are gone; an uncleaned owned
resource fails an otherwise successful run and is reported. The wrapper removes
only containers and a bridge carrying its own rehearsal labels, then uploads job
evidence. It refuses to run outside a hosted job or outside `$RUNNER_TEMP`.

## Consequences

- The backend archive round trip can be rerun at an exact head without a
  developer workstation, a live panel or a shared Docker daemon.
- The harness is deliberately narrower than a release qualification: it uses
  one Minecraft version, one managed Vanilla source, a synthetic fixture, a
  Docker-in-Docker node rather than a production remote host, an empty frontend
  embed and no operator ingress or proxy. Those limits are reported by every
  run.
- OSS-428 browser acceptance remains with #686. A passing run here proves the
  RPC export/preview/import path, not the create-from-archive UI workflow.
- The first full hosted run is still required before claiming OSS-426/427 live
  acceptance. Offline unit tests validate archive parsing, fixture bindings and
  refusal paths only; they never record a round trip as verified.
- No release approval rule, registry alias or publication permission changes.
