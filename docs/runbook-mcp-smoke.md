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
python3 -m unittest scripts.test_validate_mcp_server_json scripts.test_mcp_onboarding_fixture scripts.test_mcp_stdio_smoke
```

The validator refuses `latest`, version ranges, a non-OCI package, a missing
secret marker on `CARBONPANEL_TOKEN`, a mismatched ownership label and an
all-zero digest when `--require-real-digest` is passed. The checked-in manifest
intentionally still has a placeholder digest.

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

## 3. Verify the scoped artifact with a real stdio handshake

Run the pinned image by digest with no world/database mount, no Docker socket,
a temporary filesystem only, throwaway network and placeholder credentials.
Drive a real initialize + `tools/list` exchange and assert stdout is JSON-RPC
only:

```bash
python3 scripts/mcp_stdio_smoke.py --expect-tool list_servers \
  --evidence mcp-stdio-smoke.json -- \
  docker run -i --rm -e CARBONPANEL_URL -e CARBONPANEL_TOKEN \
  ghcr.io/athndev/carbonpanel-mcp@sha256:<MANIFEST_DIGEST>
```

Retain the JSON report: command, resolved tool names, exit code, stderr byte
count. This verifies the artifact starts and speaks MCP; it does not prove a
tool works against a panel.

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
2. Call `tools/list`; retain the tool list.
3. On stable `v1.0.40` (`f88748be…`) call only the verified-by-contract
   read-only set: `list_servers`, `get_server`, `get_server_logs`,
   `list_server_players`, `list_nodes`, `get_node`. Record request/response
   pairs with timestamps.
4. Do not call any mutating tool. No rollback is needed. Stop and report if a
   tool requests a write.
5. Remove the uniquely named container, temp volume, throwaway network and temp
   directory. Verify no leftovers with a namespace-scoped filter. Never run a
   fleet-wide prune.

### Capability-error check

Ask for `list_players` against the stable fixture. `PlayerService` is absent
from `v1.0.40`; the expected result is an MCP tool error (`isError: true`) with
`code: CAPABILITY_UNAVAILABLE`, the tool and RPC names, the required qualified
build and the observed build. A success, an empty omission or a client-version
guess is a failure. The exact field names are owned by the core implementation
lane; the semantics are fixed by ADR 0034.

New tools (`PlayerService`, `WorldService`, `WorldViewService`,
`ModBrowseService`, `NodeControlService`) are qualified only against a pinned
build that contains them, with retained smoke evidence. The current candidate
is `d8597d4e953522d2eb2fa6d4764965ae8e58686f`; it is marked "qualification
pending" until that evidence exists. Switching an existing install to nightly
is never automatic: only an operator may choose it in a separate, backed-up
change.

## 5. Verify the registry entry itself

A package push and a registry publication are different claims. After the
authorized registry dispatch, verify the listing with a read-only GET:

```bash
curl --fail --silent --show-error \
  "https://registry.modelcontextprotocol.io/v0.1/servers?search=io.github.athNdev%2Fcarbon-panel"
```

Confirm the exact `name` and pinned `version` appear, and that the `packages`
entry identifier carries the same digest as the image. Retain the raw response.
Do not announce until this succeeds.

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
immutable; either platform or the ownership label is missing; anonymous pull
fails; the stdio handshake or stdout hygiene fails; the fixture would need a
live controller, shared service, real credentials or any world/database mount;
a mutating or unauthorized outbound call is observed; the capability error is
reported as success; cleanup cannot be proven; or the registry listing cannot
be verified. Discussions are disabled, so the only announcement surfaces are
the README, the published guide and the registry entry.

## Author-side fixture evidence

The copyable instructions were exercised on a disposable owned fixture without
a Docker daemon: an in-process mock panel bound to `127.0.0.1` on an ephemeral
port, with health endpoints, a read-only `ListServers` path and a shutdown
assertion. Evidence:
`C:/Users/Daybreak8458/Documents/Codex/2026-11-12/carbonpanel-main-audit/mcp-plans/mcp-dist-fixture-evidence.json`.
It proves the host-installed URL case and prompt content; the Docker-network
container case remains a documented, unexecuted step in this authoring
environment because no Docker daemon is available. Do not upgrade that to a
claim of container smoke.

The stdio smoke driver was exercised against a separate disposable mock MCP
server (initialize, `tools/list`, clean EOF, stdout-noise rejection, missing
capability rejection). Evidence:
`C:/Users/Daybreak8458/Documents/Codex/2026-11-12/carbonpanel-main-audit/mcp-plans/mcp-dist-stdio-smoke-evidence.json`.
This validates the **driver and procedure only**: no product MCP binary exists
in this worktree, no image was built or pushed, and no live panel was
contacted. The real image smoke in section 3 runs only after the core lane
ships `cmd/carbon-panel-mcp` and an authorized publication provides a digest.
