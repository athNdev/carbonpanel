# Development commands

Use the Go version in `go.mod` and the Bun version in CI. Install the console
dependencies with `bun install --frozen-lockfile` from `web/carbon-panel`.

From any working directory, the Bash or PowerShell wrapper locates this checkout:

```sh
bun run dev
# Equivalent from the repository root: make dev
# Bash: ./scripts/dev.sh
# PowerShell: ./scripts/dev.ps1
```

`scripts/dev.ts` owns both development processes. It uses Air on `PATH`, then
`GOBIN` or `GOPATH/bin`, otherwise builds `./cmd/carbon-panel` into a stable `tmp/carbon-panel-dev` binary
(`.exe` on Windows) and launches it. Go commands build
the whole command package, including its operations, recovery and backup files.
Air and fallback launches share the same repository root and configuration.

The default profile explicitly binds the controller and proxy listeners to
`127.0.0.1`, and passes the same loopback host to Vite. The frontend runs on port
5174; the controller uses `CARBONPANEL_SERVER_PORT` or port 8080. Set
`CARBONPANEL_API_TARGET` to use an intentional alternate API target. Running
Vite directly also defaults to loopback; `CARBONPANEL_DEV_HOST` is its explicit
host override.

For intentional LAN exposure, select the profile explicitly:

```sh
bun run dev --lan
# Wrappers accept --lan as well.
```

This selects `0.0.0.0` for development listeners. It may require a deliberate
firewall rule for the stable development executable. Normal development does
not change firewall/UAC policy, install software, or request elevation. The
production controller and public proxy bind defaults are unchanged.

Press Ctrl+C in the owning session to stop its children and process trees. A
child's unexpected exit stops the peer and returns failure. POSIX uses owned
process groups; Windows stops the owned PID tree with `taskkill /T /F`. Do not
match process names across the machine: another worktree may use the same tools.
`make kill-dev` now explains this safe shutdown instead of killing every Bun,
Air, Vite or Panel process. Inspect an orphan's exact PID before stopping it.

Windows cleanup currently follows live direct-child PID trees. Descendants of
a service that has already exited are not covered by a durable ownership
guarantee. Keep that failure scenario as an explicit follow-up before claiming
complete Windows orphan recovery; ordinary stop and peer cleanup are tested.

Start, stop and build preserve worlds, databases and backups. The launcher only
creates missing `data` and `tmp` directories; it does not reset them.

## Verification

```sh
bun test scripts/dev.test.ts
# Also runnable with Node 24+ TypeScript support:
node --test scripts/dev.test.ts
```

These tests run real temporary child processes and descendants, without opening
network listeners. They cover startup failure, peer shutdown, explicit stop,
path arguments, tool selection and preservation of unrelated processes/data.
Run the complete contributor checks for the affected Go/frontend surfaces too.

Module Dockerfiles are built by the `Developer and module build inputs` workflow
for affected PRs. These jobs build Geyser and status images for Linux amd64/arm64
without registry login or publication. Module-tag publication stays separate.
