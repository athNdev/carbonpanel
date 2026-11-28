# Runbook: MCP artifact distribution smoke

This procedure qualifies the separately versioned Carbon Panel MCP artifact
before any public announcement. It verifies a **public, immutable GHCR image**
on a disposable owned fixture. It does not publish anything by itself.

Authorization boundary: no Carbon Panel release, no Docker Hub interaction, no
deployment, no live or shared panel, no user data, no Docker socket mount, no
secret output. A registry listing is a metadata write performed only by the
separately authorized dispatch in
[`.github/workflows/mcp-dist.yml`](../.github/workflows/mcp-dist.yml), never by
this runbook.

## 0. Static ownership values (approved, not operator input)

| Value | Required |
| --- | --- |
| Registry `server.json` `name` | `io.github.athNdev/carbon-panel` |
| OCI label on **every** platform image | `io.modelcontextprotocol.server.name=io.github.athNdev/carbon-panel` |
| `repository.id` | `1369132627` (`gh api repos/athNdev/carbonpanel --jq .id`) |
| GHCR image | `ghcr.io/athndev/carbonpanel-mcp` |
| Platforms | `linux/amd64` and `linux/arm64` |

Check locally before any remote step:

```bash
python3 scripts/validate_mcp_server_json.py --file docker/mcp.server.json
python3 -m unittest scripts.test_validate_mcp_server_json scripts.test_mcp_onboarding_fixture scripts.test_mcp_stdio_smoke scripts.test_mcp_ghcr_preflight
```

The validator checks the exact GHCR image and requires the OCI identifier's tag
to match the listing version. OCI package entries carry the registry, tag and
digest in `identifier`. The official Registry rejects the legacy package fields
`registryBaseUrl`, `version` and `fileSha256`. The checked-in manifest uses a
placeholder digest that is refused by `--require-real-digest`.

## 1. Resolve and pin the immutable artifact

```bash
docker buildx imagetools inspect ghcr.io/athndev/carbonpanel-mcp:<PINNED_STABLE_VERSION>
docker buildx imagetools inspect --raw \
  ghcr.io/athndev/carbonpanel-mcp@sha256:<MANIFEST_DIGEST> > manifest.json
```

Retain the manifest-list digest and full platform list. Reject anything that is
only `:latest`, a branch tag, a moving minor tag, or missing either Linux
platform. Confirm the tag is unique before a push: an existing immutable tag
requires either a different version or a verified identical digest reuse with
no destructive move (the workflow performs this preflight before every push).

Verify each platform's **config** label by child digest, not only the index
annotations:

```bash
for platform in amd64 arm64; do
  child=$(jq -r --arg p "$platform" \
    '.manifests[] | select(.platform.os=="linux" and .platform.architecture==$p) | .digest' \
    manifest.json | head -n 1)
  docker pull -q --platform "linux/$platform" "ghcr.io/athndev/carbonpanel-mcp@$child"
  docker inspect --format '{{index .Config.Labels "io.modelcontextprotocol.server.name"}}' \
    "ghcr.io/athndev/carbonpanel-mcp@$child"
done
```

Both lines must print `io.github.athNdev/carbon-panel`.

The build-only workflow exports both final root filesystems and checks each
entrypoint's ELF header. It runs the native amd64 image against the isolated
fixture in section 3 and retains the build and smoke evidence as Actions
artifacts. The publication dispatch requires that check before any package push.

### First-tag observation: authoritative read is required

A registry manifest lookup alone cannot prove that an uncreated GHCR package
is absent. Read-only probes on 2026-11-18 observed these response shapes for
`ghcr.io/athndev/carbonpanel-mcp:v0.1.0` (no package was created):

| Read | Response | Classification |
| --- | --- | --- |
| Anonymous `GET /v2/.../manifests/v0.1.0` | `401 UNAUTHORIZED` (`{"errors":[{"code":"UNAUTHORIZED","message":"authentication required"}]}`) | ambiguous — refuse |
| Authenticated (GitHub token) manifest GET | `403`, empty body | ambiguous — refuse |
| Anonymous GHCR pull-token manifest GET | `403`, empty body | ambiguous — refuse |
| Authenticated `GET /users/athNdev/packages/container/carbonpanel-mcp/versions` | `404 {"message":"Package not found.","status":"404"}` | **definitive absence** |

`scripts/mcp_ghcr_preflight.py` therefore first calls the authenticated GitHub
Packages API: a definitive `404 Package not found.` is the only accepted proof
of first-tag absence; `200` means the package exists and the strict
`release.py` `observe_reference(strict=True)` classification is used for the
tag/digest. Any 401/403/5xx, empty or unrecognised response is `unknown` and
refuses. A requested resume digest against an absent package refuses; it never
falls back to a new push. These probes were read-only and created no package or
tag.

## 2. Verify package visibility separately from any registry listing

A successful authenticated push is not evidence that the image is publicly
pullable. Use a clean Docker config directory so the pull is anonymous:

```bash
mkdir -p "$RUNNER_TEMP/mcp-anon-docker"
docker --config "$RUNNER_TEMP/mcp-anon-docker" pull \
  ghcr.io/athndev/carbonpanel-mcp@sha256:<MANIFEST_DIGEST>
```

If this fails with `unauthorized`, the GHCR package needs its visibility set
and propagated. Record that as **package visibility pending**; do not claim a
registry listing or an announcement from a package push.

## 3. Verify the scoped artifact with a real stdio handshake and read-only call

The image entrypoint is the canonical `cmd/carbonpanel-mcp` binary. Its build
metadata is injected with the **real** main-package variables at core head
`7e6b1216`: `-X main.version -X main.commit -X main.buildDate`; the binary's
`-version` flag prints them. The same values are OCI config labels checked in
section 1.

The packaged container must be tested as a container. A host Python mock bound
to runner loopback is **not** reachable from a default-bridge MCP container, so
the fixture and the MCP image share one per-run isolated Docker network and the
MCP container uses the fixture's service DNS name (`panel-fixture:8080`). The
mock image is CI-only, built from `docker/Dockerfile.mcp-mock`, never published
and given no credentials or data.

```bash
network="mcp-smoke-${GITHUB_RUN_ID}"
fixture="mcp-fixture-${GITHUB_RUN_ID}"
docker network create "$network"
docker build -f docker/Dockerfile.mcp-mock -t "mcp-smoke-fixture:${GITHUB_RUN_ID}" .
docker run -d --name "$fixture" --network "$network" \
  --network-alias panel-fixture "mcp-smoke-fixture:${GITHUB_RUN_ID}"
# wait for GET /ready on 127.0.0.1:8080 inside the fixture container, then:
python3 scripts/mcp_stdio_smoke.py --expect-tool list_servers \
  --call-tool list_servers --url http://panel-fixture:8080 \
  --evidence mcp-stdio-smoke.json -- \
  docker run -i --rm --network "$network" \
  -e CARBONPANEL_URL -e CARBONPANEL_TOKEN -e CARBONPANEL_API_TOKEN \
  -e CARBONPANEL_MCP_TRANSPORT \
  ghcr.io/athndev/carbonpanel-mcp@sha256:<MANIFEST_DIGEST>
docker logs "$fixture" > mcp-container-fixture.log 2>&1
# assert the log names GetAuthStatus, GetCurrentUser and ListServers
docker rm -f "$fixture"; docker network rm "$network"
```

`docker logs` lines naming all three procedures are the evidence that the
packaged container actually reached the scoped read-only fixture by service
DNS. Retain the smoke JSON (tool list, `tool_call` with `isError: false`, exit
code, stderr bytes), the fixture log and a JSON record of the network/container
names and image digest. A host-installed binary can use
`python3 scripts/test_mcp_real_binary.py --binary <PATH>` for a separate
host-side check; it does not substitute for the packaged-container path.
Neither fixture contacts a live or shared panel.

For a **local build-only** seam (no GHCR push, pull or registry write),
`scripts/mcp_build_only_packaged_smoke.py` builds `docker/Dockerfile.mcp` and
`docker/Dockerfile.mcp-mock` from the current checkout, starts the owned fixture
on an isolated Docker network, and runs the same packaged `stdio` smoke against
the locally built MCP image. It requires a local Docker daemon and does not
substitute for verification of a public immutable digest. Use `--dry-run` to
inspect the planned commands without Docker; when Docker is unavailable it
reports `blocked` with evidence instead of claiming success.

The build-only report records the local image ID used for the container and
includes the stdio driver's handshake, tool names and scoped call result under
`stdio_smoke`. The runner removes its fixture, network and automatically tagged
images. Cleanup failure fails the smoke and retains the report. Images selected
with `--image-tag`, `--mock-tag` or `--skip-build` remain available to the caller.

## 4. Read-only smoke on a disposable fixture

Use an owned mock panel or a freshly created, disposable panel install. Never a
live controller, shared service, existing world or database. Choose the URL
that matches where the MCP process runs:

- **Host-installed MCP binary:** `CARBONPANEL_URL=http://127.0.0.1:<published-port>`.
- **MCP container on the panel's Compose network:** use the panel service DNS
  name and port, e.g. `http://carbon-panel:8080`, and attach the MCP container
  to that network. `localhost` inside the MCP container is the MCP container
  itself and is a hard stop, never a fallback.

1. Check `GET /healthz`, `GET /readyz` and `GET /health/detail` **from the MCP
   process location**. Report `healthy`, `degraded`/`unavailable` and `unknown`
   separately; a stored `Running` status is not playable evidence.
2. Call `tools/list`; retain the tool list. It enumerates the generated
   combined catalogue of 218 tools (255 declared RPCs, 245 mounted/reachable).
3. On stable `v1.0.40` (`f88748be…`) call only the verified-by-contract
   read-only set matching the core lane's stable smoke path: `list_servers`,
   `get_server`, `list_files`, `get_file`, `list_backups`. Record request/response
   pairs with timestamps. The five player/world/world-view tools are mounted in
   the catalogue but their backing RPCs are absent from stable v1.0.40; OSS-370
   tracks the remaining catalogue gaps.
4. Do not call any mutating tool. No rollback is needed. Stop and report if a
   tool requests a write.
5. Remove the uniquely named container, temp volume, throwaway network and temp
   directory. Verify no leftovers with a namespace-scoped filter. Never run a
   fleet-wide prune.

### Capability-error check

Ask for `list_players` against a disposable panel fixture pinned to `v1.0.40`.
The expected result has `isError: true`, with
`structuredContent.kind: capability_unavailable`, the backing `procedure` and
the `minimum_panel_build` required by the tool. Record the fixture's pinned
controller revision alongside the response.

New tools (`PlayerService`, `WorldService`, `WorldViewService`) are qualified
only against the pinned post-stable build that contains them, with retained
smoke evidence. The current candidate is
`fa23a8d73c181f9907633024d9023aead928324e`; it is marked "qualification
pending" until that evidence exists. Switching an existing install to nightly
is never automatic: only an operator may choose it in a separate, backed-up
change. The fixture panel must have authentication enabled and the client must
use a real `dp_` API token; the core lane refuses legacy no-auth installs and
verifies `AuthService/GetAuthStatus` plus token identity. No anonymous fallback
is acceptable.

## 5. Verify the registry entry itself

A package push and a registry publication are different claims. After the
authorized registry dispatch, verify the listing with a read-only GET:

```bash
curl --fail --silent --show-error \
  --output mcp-registry-verification.json \
  "https://registry.modelcontextprotocol.io/v0.1/servers/io.github.athNdev%2Fcarbon-panel/versions/<PINNED_MCP_VERSION>"
python3 scripts/validate_mcp_server_json.py --file <SUBMITTED_SERVER_JSON> \
  --listing mcp-registry-verification.json
```

Retain the raw response and the submitted manifest. The verifier requires an
active record for the exact name and version, and compares the canonical OCI
identifier with the one submitted using the anonymously pulled image digest.

## Evidence to retain

- Pinned version, manifest-list digest, per-platform child digests, image ids.
- Full `imagetools inspect` output and both platform config labels.
- Anonymous pull result (this is the package-visibility proof).
- `mcp-stdio-smoke.json`: tool list, exit code, stderr/stdout hygiene.
- Health responses, read-only request/response pairs with timestamps, and the
  capability-error payload for a main-only tool.
- Cleanup proof: unique fixture names, commands, exit codes, post-cleanup
  "no leftovers" output. State explicitly that no live service was contacted.
- `mcp-publisher validate` output and the registry GET response (after the
  separately authorized publication dispatch only).

## Hard stops

Stop and do not advertise or publish if the artifact is not public and
immutable; either platform or the ownership label is missing; the panel has
authentication disabled or the credential is not a real `dp_` token; anonymous
pull fails; the stdio handshake or stdout hygiene fails; the fixture would need a
live controller, shared service, real credentials or any world/database mount;
a mutating or unauthorized outbound call is observed; the capability error is
reported as success; cleanup cannot be proven; or the registry listing cannot
be verified. Discussions are disabled, so the only announcement surfaces are
the README, the published guide and the registry entry.

## Author-side fixture evidence

The copyable instructions were exercised on a disposable owned fixture without
a Docker daemon: an in-process mock panel bound to `127.0.0.1` on an ephemeral
port, with health endpoints, a read-only `ListServers` path and a shutdown
assertion. Evidence paths in this section are relative to the external audit
workspace (`$CARBONPANEL_AUDIT_DIR`, e.g. an out-of-tree
`carbonpanel-main-audit/` checkout); set that variable to locate them:
`$CARBONPANEL_AUDIT_DIR/mcp-plans/mcp-dist-fixture-evidence.json`.
It proves the host-installed URL case and prompt content; the Docker-network
container case remains a documented, unexecuted step in this authoring
environment because no Docker daemon is available. Do not upgrade that to a
claim of container smoke.

The stdio smoke driver was exercised against a separate disposable mock MCP
server (initialize, `tools/list`, one scoped read-only call, clean EOF,
stdout-noise rejection, missing-capability rejection). Evidence:
`$CARBONPANEL_AUDIT_DIR/mcp-plans/mcp-dist-stdio-smoke-evidence.json`.

The **actual compiled `cmd/carbonpanel-mcp` binary** from core head `7e6b1216`
was then driven against an owned Connect mock on `127.0.0.1`: auth readiness
(`GetAuthStatus` + `GetCurrentUser`), `tools/list`, and one real `list_servers`
call returning `isError: false`. Evidence:
`$CARBONPANEL_AUDIT_DIR/mcp-plans/mcp-dist-real-binary-scoped-smoke.json`.
No image was built or pushed, no live panel was contacted and no real credential
was used. The **packaged container entrypoint on the isolated fixture network**
(section 3) is unexecuted in this authoring environment: no Docker daemon is
available here. `scripts/mcp_build_only_packaged_smoke.py` provides the local
build-only seam for environments with Docker, but it does not substitute for
verification of a public immutable digest or for the authorized dispatch path.
Packaged-container smoke must pass before any registry listing or advertisement.
Do not substitute host-binary evidence for that container proof.

The read-only GHCR first-tag probes are recorded in
`$CARBONPANEL_AUDIT_DIR/mcp-plans/mcp-ghcr-first-tag-probe.json`.
They created no package, tag or release.
