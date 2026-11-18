# Deployment modes

Supported ways to run Carbon Panel, what the repository encodes, and what still
needs a live host. This is the honest matrix for OSS-246: a green CI job is not
a create→join after upgrade.

## Shipped in this repository

| Mode | How | In-repo check | Live host still required |
| --- | --- | --- | --- |
| Docker Compose, host network (default) | `docker-compose.yml` `network_mode: host`; data bind-mounted at `/opt/carbon-panel/data` | `internal/docscheck` parses bind mounts, refuses privileged mode, and requires `scripts/compose-upgrade.sh` to recreate with `up -d --pull always` (never `down --volumes`) | Install from a pinned image, create a disposable server, join it, upgrade the panel image, confirm the world and SQLite file still exist and the server is joinable |
| Docker Compose, bridge | Commented alternative in the same compose file | File still bind-mounts data; ports stay comments until an operator uncomments them | Same join path on a host that cannot use host network |
| Native binary | `make prod` / `go build` with `./data` | `make clean` / `make test` / `make build` must not delete `./data` (`upgrade_safety_test.go`) | Process upgrade (stop, replace binary, start) with the same data directory |
| Operator DB snapshot | `scripts/snapshot-panel-db.sh` / `make snapshot-panel-db` | Script writes beside the data dir; restore is copy-back | Snapshot on a running install before a real image bump |
| Compose DB rollback | `scripts/compose-upgrade-rollback.sh` / `make compose-upgrade-rollback` | Copy-back onto `carbon-panel.db`; never a volumes-deleting compose down; worlds and backups dirs stay | Interrupted upgrade and disk-full while a world stays joinable |

The compose upgrade path is:

```sh
scripts/compose-upgrade.sh --data-dir /opt/carbon-panel/data
# review the printed commands, then:
scripts/compose-upgrade.sh --data-dir /opt/carbon-panel/data --apply
```

`--apply` snapshots the controller SQLite file, then runs
`docker compose up -d --pull always`. If the snapshot fails (disk-full or an
interrupted copy), the script **stops before compose up**. It does not run
`docker compose down`, does not pass `--volumes`, and does not `docker volume rm`.
Bind-mounted worlds and backups stay on the host.

Copy-back rollback (panel stopped):

```sh
scripts/compose-upgrade-rollback.sh \
  --data-dir /opt/carbon-panel/data \
  --snapshot /opt/carbon-panel/carbon-panel-db-YYYYMMDDTHHMMSSZ.sqlite
# review the printed commands, then:
scripts/compose-upgrade-rollback.sh \
  --data-dir /opt/carbon-panel/data \
  --snapshot /opt/carbon-panel/carbon-panel-db-YYYYMMDDTHHMMSSZ.sqlite \
  --apply
```

That copies the snapshot onto `carbon-panel.db` and drops WAL sidecars. It does
not prune Docker volumes and does not delete world or backups directories.
See `docs/runbook-panel-db-restore.md`.

## Status sidecar module

The built-in Status Panel module runs `cmd/status`, whose `/`, `/api/status`
and `/health` endpoints have **no request authentication**. The controller
itself defaults `HOST=127.0.0.1`; the shipped module template explicitly sets
`HOST=0.0.0.0` inside its container so the panel's port proxy can reach the
published port. Deployments that run `cmd/status` outside that module must set
`HOST` deliberately. Restrict the published port to trusted networks and never
treat the status endpoints as an authenticated API.

## Geyser / Bedrock UDP binding

The `cmd/geyser` entrypoint generates `bedrock.address: 127.0.0.1` when
`BEDROCK_ADDRESS` is unset. A standalone entrypoint therefore listens only on
loopback until an operator opts in to a wider bind by setting `BEDROCK_ADDRESS`
explicitly (for example `0.0.0.0` or a private interface address). An explicit
wildcard bind prints a warning; the published UDP port must still be restricted
to trusted networks.

The built-in Geyser module keeps an explicit `BEDROCK_ADDRESS=0.0.0.0` in its
default environment. That is intentional: the panel's UDP port proxy dials the
container IP, so a loopback-only listener inside the container would be
unreachable. Module instances created before this change already carry that
explicit value, so their behavior is unchanged. Only direct entrypoint runs that
relied on the old implicit `0.0.0.0` default need a migration: set
`BEDROCK_ADDRESS=0.0.0.0` (or a specific interface address) in the container
environment, or accept loopback-only exposure. Existing generated
`/data/config.yml` files are never rewritten unless `OVERWRITE_CONFIG=true`, so
changing the default does not silently rewrite a running Geyser configuration.

## Not shipped

| Mode | Status |
| --- | --- |
| Helm | **Not shipped.** There is no chart in this repository. Do not document or test a Helm upgrade path until a chart exists. |
| Terraform / OpenTofu | **Not shipped.** There is no Terraform root module for OSS Carbon Panel. Managed Cloud provisioning belongs to the separate Cloud product; see the [documentation map](README.md#cloud-product-policy). |
| Kubernetes manifests | **Not shipped.** |
| Windows containers | **Not supported.** A Windows controller talking to Linux Docker (Desktop/WSL) is a different mode from running the panel image as a Windows container. |

`AGENTS.md` still says to update Compose, Docker, Helm, Terraform and release
docs when those artifacts change. That is a reminder for when they exist, not a
claim that Helm or Terraform is a supported OSS install.

## Live-host criteria (not claimed by CI)

These remain unqualified until run on an owned disposable host:

- Clean-machine install from a pinned release image with no sibling git
  checkout or workstation paths.
- Full user path in each claimed mode: create → join → edit → backup → restore
  → restart → export.
- Interrupted upgrade and disk-full rollback **on a live host** while a world
  stays joinable. In-repo policy: snapshot failure aborts before `up`; rollback
  is copy-back (`scripts/compose-upgrade-rollback.sh`), not a prune.
- Rootless Docker, Docker Desktop/WSL path translation, and remote Docker
  endpoints: engine capability is preflighted in `internal/docker`, but a real
  upgrade of the panel itself on those daemons is not CI.
- Effective container overrides after a panel image swap (no extra privilege
  compared with the previous image).
