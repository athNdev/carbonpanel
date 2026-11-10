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

The compose upgrade path is:

```sh
scripts/compose-upgrade.sh --data-dir /opt/carbon-panel/data
# review the printed commands, then:
scripts/compose-upgrade.sh --data-dir /opt/carbon-panel/data --apply
```

`--apply` snapshots the controller SQLite file, then runs
`docker compose up -d --pull always`. It does not run `docker compose down`,
does not pass `--volumes`, and does not `docker volume rm`. Bind-mounted worlds
and backups stay on the host. See `docs/runbook-panel-db-restore.md` for
copy-back rollback.

## Not shipped

| Mode | Status |
| --- | --- |
| Helm | **Not shipped.** There is no chart in this repository. Do not document or test a Helm upgrade path until a chart exists. |
| Terraform / OpenTofu | **Not shipped.** There is no Terraform root module for OSS Carbon Panel. Cloud provisioning notes in `docs/agents/` do not apply here. |
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
- Interrupted upgrade and disk-full rollback while a world stays joinable.
- Rootless Docker, Docker Desktop/WSL path translation, and remote Docker
  endpoints: engine capability is preflighted in `internal/docker`, but a real
  upgrade of the panel itself on those daemons is not CI.
- Effective container overrides after a panel image swap (no extra privilege
  compared with the previous image).
