# Manual review harness

Throwaway Carbon Panel instances for reviewing a branch end to end, with no
changes to the repository or your existing deployment.

## File editor browser regression

This focused check mounts the real file-editor dialog, Carbon controls, Bits UI
and Monaco in Chromium. Only the RPC boundary is replaced with deferred responses.
It needs no Docker daemon, server account or running Panel installation.

```sh
cd web/carbon-panel
bun install --frozen-lockfile
cd ../../scripts/manual-review/e2e
bun install --frozen-lockfile
bun run install-browser
bun run editor-dialog
```

The check covers stale loads and saves, same-turn file switches, close/reopen,
the same path on different servers, edits during save, and callbacks queued by a
disposed editor. Screenshots and results go to ignored `e2e/out/editor-dialog/`.
The Vite server binds only to loopback; the check closes its browser/server and
removes its own temporary dependency cache. Set
`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` to use an explicitly selected local Chromium
installation instead of Playwright's browser. CI runs this check separately from
the frontend unit and build gate.

## Quick start (WSL or Linux)

```sh
bash scripts/manual-review/review.sh up
```

This builds a production image (proto + frontend + backend) from `HEAD`, starts
**two** panels on the same Docker daemon, and creates an admin in each.

| Instance | URL | Purpose |
| --- | --- | --- |
| A | http://localhost:18081 | primary review |
| B | http://localhost:18082 | second installation sharing the daemon (ownership tests) |

Login: `review` / `CarbonReview123!`

## Commands

```sh
review.sh up [--ref origin/main] [--instances 1|2] [--no-build] [--recreate]
review.sh status
review.sh logs a|b
review.sh urls
review.sh down            # keeps database, worlds and review data
review.sh reset           # explicitly deletes only marked instance data, preserving source
```

`--ref` accepts any branch or commit. Building a new ref reuses Docker's layer
cache; a changed backend rebuilds in well under a minute once dependencies are
cached.

## Where things live

| Path | Contents |
| --- | --- |
| `~/.cache/carbon-review/<sha>/src` | detached git worktree used as the image build context |
| `~/.cache/carbon-review/<sha>/a`, `b` | per-instance `data`, `backups`, `tmp` |
| `~/.cache/carbon-review/<sha>/compose.yaml` | generated compose file |
| `~/.cache/carbon-review/current` | the ref currently running |

Nothing is written inside the repository. The source worktree is registered
with Git and is not automatically removed during teardown.

## Design notes

- **Isolation.** Each panel gets its own data directory, SQLite database,
  installation identity (`data/installation-id`) and host port. Both share
  `/var/run/docker.sock`, which is exactly what the ownership review needs.
- **Real UI.** The image embeds the compiled SvelteKit build, so what you review
  is the same binary shape as production, including `/healthz`.
- **No prompts.** Health is polled via Docker health checks; the admin is created
  through the public `AuthService.Register` RPC. The first registered user is
  admin by design. No credentials from 1Password are needed.
- **Safe teardown.** `down` keeps data. The separately named `reset` verifies
  the session marker, container labels and resolved instance paths before deleting
  only that session's instance data; its source checkout remains. Newly created
  sessions require a matching ownership marker and container labels; existing
  unmarked review data is not adopted automatically. Use `REVIEW_SESSION_SHA`
  to select a previously created session instead of the latest one.
- **Port discipline.** 18081/18082 are reserved for this harness. If either is
  busy, `up` fails with a clear message instead of binding something else.

## Limits

- Panels run in bridge mode; Minecraft game ports are published on the Docker
  host by the sibling server containers, not mapped through the panel container.
  Creating and starting a server works, but joining it from Windows may need the
  server's published host port (shown in the UI) rather than a panel-mapped port.
- One docker daemon, two panels: creating servers in both may collide on
  in-DB port allocation. Use B only for go-through-the-motion ownership checks.
- The review image is built from tracked files only. Uncommitted working-tree
  changes are not included; use a commit or a branch.
