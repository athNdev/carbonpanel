# ADR 0057: Disposable server-portability acceptance rehearsal in CI

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
registry, tag or release. The OSS-428 create-from-archive and export browser
claim additionally needs the production SvelteKit routes, a real controller and
the same engine, because stubbed-RPC screenshots cannot prove either transport.

## Decision

`.github/workflows/server-archive-rehearsal.yml` runs disposable
GitHub-hosted Linux jobs for repository-owned pull requests or an explicit
dispatch. They have only `contents: read`, no deployment credentials and no
publication step. Each checks out the PR head SHA (the same value exported as
`STAGED_SHA`), builds the `carbon-panel` and `carbon-storage` binaries and a
controller image with the commit revision label, then calls
`scripts/qualify-server-archive-hosted.sh`.

The `rehearsal` job builds an empty frontend embed and runs the RPC round trip.
The `ui` job sets `ARCHIVE_UI=1`, builds the exact production frontend with
`APP_VERSION="$CANDIDATE_VERSION"`, and runs the same wrapper and Python
harness. The Python harness then executes three browser phases through
`scripts/qualify-archive-ui-hosted.mjs`:

1. `refusal` — open the real detail-route export modal while the source is
   running and prove the warning and both controls are disabled without calling
   `ExportServerArchive`.
2. `export` — prepare and download ZIP and tar.gz through the real export
   panel, including the disclosure acknowledgement and same-origin `HEAD`
   preflight, and hand the downloaded files to the Python verifier.
3. `import` — upload each verified download through the real
   `/servers/new` archive tile, inspect the rendered preview, choose the remote
   destination, confirm capacity, apply the archive runtime defaults, submit
   create, and record the created server id from the result link.

The browser driver records the exact head, browser version, plan, download
hashes, preflight status and screenshots; it receives no credential on its
command line. The Python harness refuses a record whose head, identifiers or
hashes disagree, re-verifies the downloaded archive independently, verifies the
created server's destination bytes through the server-scoped FileService and
the remote engine container state, and binds every created id to the same
ownership-gated cleanup as the RPC job. A partial browser record still
contributes its created ids so a mid-wizard failure cannot leak a server.

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
fixture record that identifies the exact checkout, controller and destination
engine. The harness verifies both containers through the local Docker socket,
including the destination bridge address and storage mount. It uses direct
connections to the fixture endpoints and refuses redirects.

The harness then creates a managed Vanilla source without Docker overrides,
requires both the `failed_precondition` code and the specific running-export
refusal message, saves and populates a deterministic fixture, and records both
native formats: over RPC in the RPC job, through the production panel in the UI
job. It verifies each archive's schema/runtime/per-file SHA-256 and expanded
size, previews each archive against the remote node, creates each import stopped,
checks the new server identity and actual assigned port, and verifies
destination bytes. The RPC job then starts and joins both imports; the UI job
stops after the stopped-bytes proof and leaves boot/join continuity to the RPC
receipt.

Every check is fail-closed and written to a JSON report. Cleanup is bounded and
verifies that only servers created by this run are gone; an uncleaned owned
resource fails an otherwise successful run. The wrapper matches the recorded
container and network IDs and the run label before collecting resource
diagnostics or removing anything. Each job uploads the final teardown status
alongside its receipt.

## Consequences

- The backend archive round trip can be rerun at an exact head without a
  developer workstation, a live panel or a shared Docker daemon.
- The OSS-428 production-UI export and create-from-archive workflow can be
  rerun at the same exact head against a real controller and remote engine
  without stubbing RPC or substituting synthetic downloads.
- The harnesses are deliberately narrower than a release qualification: they
  use one Minecraft version, one managed Vanilla source, a synthetic fixture, a
  Docker-in-Docker node rather than a production remote host, and no operator
  ingress or proxy. The RPC job uses an empty frontend embed; the UI job covers
  only the archive routes and does not start its imports. Those limits are
  reported by every run.
- The first full hosted run of each job is still required before claiming the
  corresponding acceptance. Offline unit tests validate archive parsing,
  fixture bindings, plan/evidence refusal and browser helper logic only; they
  never record a round trip as verified.
- No release approval rule, registry alias or publication permission changes.
