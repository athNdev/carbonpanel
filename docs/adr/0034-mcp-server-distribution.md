<!--
SPDX-License-Identifier: MPL-2.0
This Source Code Form is subject to the terms of the Mozilla Public
License, v. 2.0. If a copy of the MPL was not distributed with this
file, You can obtain one at https://mozilla.org/MPL/2.0/.
-->

# ADR 0034: Independent MCP artifact distribution and build-bound capabilities

Status: Accepted (preparation only — no image push, registry submission or GitHub release has been performed).

## Context

Carbon Panel manages persistent Minecraft worlds and a controller database. The
existing release machinery ([ADR 0010](0010-versioned-release-artifacts.md),
[ADR 0033](0033-release-tag-and-publication-policy.md)) owns panel versions,
`latest`/minor aliases and Docker Hub / GHCR panel images. An assistant-facing
MCP server is a different artifact with a different owner, support boundary and
publication channel. Reusing the panel release workflow would couple two
independent version lines and could imply that a new MCP tool works on an older
panel build.

That risk is concrete. The latest stable panel build, `v1.0.40`
(`f88748be6a216b187e675bbb1f110df07e544148`), predates the player, world and
world-view work. The pinned post-stable build
`fa23a8d73c181f9907633024d9023aead928324e` contains those services. A client
package that installs successfully says nothing about which of those RPCs a
given panel build implements.

The original implementation decision was a deliberate **first slice of 30
tools**. The mounted-service/RPC inventory available at that time was
**25 services / 220 RPCs**. Both figures are the historical baseline of this
decision, not the current catalogue. Mounted-service/RPC inventory is not tool
coverage, and this ADR must not be read as a claim of full API coverage:
OSS-370 remains open for supported families still missing.

### Current catalogue (superseding note, derived from the generator)

The figures below are read from the checked-in generators and gates, not hand
counted. `go run ./internal/mcp/cataloggen` reports `entries=245 services=27`
and `combined tools=218`; `internal/mcp/family_audit_test.go` asserts 255
declared proto RPCs and a 255/245/10 (declared/mounted/unmounted) matrix split,
while `internal/mcp/drift_test.go` asserts 27 mounted services and 245 mounted
RPCs. `scripts/mcp-generated-artifacts-check.sh` reproduces the generated
artifacts byte-for-byte from checked-in source.

The gateway now mounts the **218-tool combined catalogue**: **255 declared
RPCs**, of which **245 are mounted/reachable** across **27 mounted services**.
The 30-tool first slice is retained as the highest-precedence pinned subset
inside that combined catalogue; it is not the total tool count. The three
counts stay deliberately distinct: declared RPCs include 10 `NodeControlService`
RPCs that are never mounted, and audit exclusions plus duplicate-name
resolution reduce the 245 mounted reachable RPCs to 218 exposed tools. A `tools/list`
result is therefore evidence of the exposed tool catalogue, not of RPC
coverage or of a panel build's capability (see the two compatibility concepts
below). This note supersedes the historical counts above for present-tense
claims; it does not change the decision, capability distinctions or publication
cautions recorded in this ADR.

## Decision

### Artifact and ownership

- The MCP server is a separately versioned, separately owned artifact. Its
  version line belongs to the MCP distribution lane, not to panel releases. It
  never moves panel `latest`, panel minor tags or any Docker Hub panel tag, and
  it is not published as a Carbon Panel GitHub Release. [ADR 0033](0033-release-tag-and-publication-policy.md)
  remains authoritative for the panel publication boundary.
- The official distribution is a public OCI image on GHCR plus one official MCP
  Registry listing. The OCI image carries
  `io.modelcontextprotocol.server.name=io.github.athNdev/carbon-panel` on every
  platform image; that value is exactly the registry `server.json` `name`.
  `github.com/athNdev/carbonpanel` is the canonical repository and its stable
  repository id (`1369132627`) is recorded in `server.json`. The OCI package
  identifier contains the registry, version tag and digest in one canonical
  reference, as required by the official Registry.
- The image is multi-arch `linux/amd64` + `linux/arm64`, built from the pinned
  toolchain in `toolchain-versions.json` with `GOTOOLCHAIN=local` and
  `CGO_ENABLED=0`. The canonical command is `cmd/carbonpanel-mcp`; build
  metadata is injected into the real main-package variables
  `main.version`, `main.commit`, `main.buildDate` (verified at core head
  `7e6b1216`, printed by the binary's `-version` flag) and duplicated in OCI
  config labels. It runs as a non-root user, contains no world, database,
  backup or other persisted data, and receives no Docker socket. Liveness is the
  stdio session itself: the process serves JSON-RPC on stdin/stdout, writes no
  diagnostics to stdout, and exits on EOF. There is no HTTP healthcheck port.
- Publication is dispatch-only. `.github/workflows/mcp-dist.yml` has no `push`,
  `pull_request`, `schedule` or `workflow_run` trigger. An ordinary main merge
  never publishes. Registry publication, if later authorized, requires a second
  explicit opt-in, `id-token: write`, and a cosign-verified pinned
  `mcp-publisher` (v1.8.1); `latest`, branch tags and moving aliases are refused
  by the local validator. This slice prepares those artifacts and a smoke
  procedure only.
- Static ownership values are approved constants, not operator inputs: the
  registry `name` and the OCI `io.modelcontextprotocol.server.name` label are
  both `io.github.athNdev/carbon-panel`, and `repository.id` is `1369132627`.
  Both `linux/amd64` and `linux/arm64` are verified by manifest-list digest and
  each child image's **config** label; index annotations alone are not evidence.
- Announcement is limited to the root README, the published user guide and the
  registry entry. Discussions are disabled; no GitHub Discussion or GitHub
  release is part of this decision. The website source pin is advanced by the
  normal website publication process, not by this change.

### Publication gates

- No operator revision input authorizes a write. Every write job resolves the
  trusted pinned commit from the `refs/heads/main` dispatch SHA, checks out that
  exact SHA with full history, asserts `git rev-parse HEAD` equals it, and
  requires `scripts/release.py verify-main-ci` plus the shared
  `developer-channel-gate` to allow publication. Tags, feature refs and blank
  refs stage only. This is the same trusted-main pattern as `cd.yml`.
- The immutable version tag is checked before any push. An existing tag refuses
  a new write unless the operator supplies a verified identical digest, in
  which case the existing digest is reused with **no destructive move**. No
  alias is promoted and no existing tag is overwritten.
- The published image is verified by **anonymous pull** (package visibility is
  a separate claim from a successful authenticated push) and by a **real stdio
  handshake** (`scripts/mcp_stdio_smoke.py`: initialize, `tools/list`, stdout
  hygiene, clean EOF) before any registry advertisement. The MCP Registry entry
  is verified by a read-only GET for the exact version after publication.
  Verification requires an active record with the same OCI identifier and
  digest as the submitted manifest.
- The dispatch builds both platform filesystems and checks the ELF machine
  headers before publication. It also runs the built amd64 container over stdio
  against the owned isolated fixture. Packaging changes exercise the same
  read-only job in pull requests.
- The stdio MCP process must use a panel URL reachable from itself: a
  host-installed binary may use host loopback, while a container on the panel's
  Compose network must use the panel service DNS name and port. `localhost`
  inside the MCP container is never a fallback.

### Authorization and capability surface

- The MCP server reuses Carbon Panel's fail-closed authorization with a
  dedicated, expiring `dp_` API token supplied per client. The token inherits
  the grants of its issuing account, which should hold only the required
  permissions. The MCP server
  requires an authentication-enabled panel and verifies auth state through
  `AuthService/GetAuthStatus` and token identity; legacy no-auth installations,
  anonymous access and browser session tokens are refused with a credential
  error rather than an implicit fallback. It does not create a new privilege
  path and never writes a token to tracked config or stdout. Tool metadata marks
  secret inputs `isSecret: true`.
- Tools are read-only by default. Mutating capability is a separately reviewed
  decision and requires explicit per-action user approval; the documentation
  and smoke procedure exercise read-only calls only. No world, database or
  config write is a side effect of connecting a client.
- Data preservation is inherited from
  `knowledge/invariants/data-safety.md`: connecting, smoke-testing or
  upgrading the MCP artifact must never run `docker compose down -v`, prune
  volumes, delete worlds/databases or restart shared services. Switching an
  existing install from stable to nightly is never automatic; only an operator
  may choose a qualified panel build as a separate, backed-up change.

### Two distinct compatibility concepts

1. **Public package availability** means the official OCI artifact and registry
   entry exist, are immutable and install. It is evidence about the client
   artifact only.
2. **Compatible server capabilities** means the panel build behind
   `CARBONPANEL_URL` actually implements the RPC backing a tool. It is evidence
   about the server build only.

A successful install, `tools/list`, or client handshake is **not** proof that a
tool works against a panel. Capability is determined from a tested, pinned
panel build, not from the MCP client version or from a package being available.

The initial compatibility matrix is carried in the user guide and smoke
procedure and matches the implemented first-slice catalogue exactly. The
verified-by-contract minimal stable set is `list_servers` (`ServerService/ListServers`),
`get_server` (`ServerService/GetServer`), `list_files` (`FileService/ListFiles`),
`get_file` (`FileService/GetFile`) and `list_backups` (`BackupService/ListBackups`).
Tools backed by `PlayerService`, `WorldService` and `WorldViewService` are
unavailable on `v1.0.40`; they are qualified only against the pinned post-stable
build `fa23a8d73c181f9907633024d9023aead928324e` and only after smoke evidence
is recorded for that build. The documentation marks qualification pending until
that evidence exists. Full API coverage is explicitly out of scope for this
slice; OSS-370 remains open.

When a tool's RPC is absent or `Unimplemented`, the server returns a structured
MCP tool error. The result sets `isError: true` and carries
`structuredContent.kind: capability_unavailable`, the backing `procedure`
and the `minimum_panel_build`. Qualification evidence records the tested
controller revision alongside that result.

## Consequences

Two version lines and two distribution channels must be tracked: panel
releases under [ADR 0010](0010-versioned-release-artifacts.md) / [ADR 0033](0033-release-tag-and-publication-policy.md),
and MCP artifacts under this ADR. Documentation must always show both the
pinned MCP artifact version/digest and the panel build a tool was qualified
against. A green CI run or an installed client is not evidence that a tool
works on a user's panel; the smoke procedure retains the digest, platform list,
image id, tool list and capability responses as evidence. The first real digest
and the first tested qualification build replace the documented placeholders
before public announcement. Until then this ADR and the associated docs
describe preparation, not shipped behavior.
