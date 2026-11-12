# Development commands

Use the Go version in `go.mod` and the Bun version in CI. Install the console
dependencies with `bun install --frozen-lockfile` from `web/carbon-panel`.

`make deps` downloads Go modules and installs the frontend with its frozen lockfile.
Use `make deps-update-buf` or `make deps-update-frontend` for deliberate lockfile
updates. Proto generation uses Buf 1.50.0 and the pinned remote plugins in
`buf.gen.yaml`. After the frozen frontend install, `make gen` stages generation
and formatting before replacing generated output; `make proto-clean` is the
separately named removal operation. For native Windows or Linux with Buf 1.50.0
on PATH, `bun scripts/proto-generate.mjs --check` compares regenerated Go and
TypeScript with the checkout without modifying them; omit `--check` to install
the staged outputs. `BUF` and `BUF_VERSION`
select an explicit local executable and expected version; `BUF_IMAGE` selects
an explicit Docker image for Make. Review compatibility and regenerate after any
intentional tool or remote-plugin update. OpenAPI schemas are generated build
artifacts and are not part of the tracked output comparison.

`toolchain-versions.json` records the tested Go, Bun and Buf CLI defaults. Go
also appears in `go.mod`, Docker builder arguments and CI; Bun and Buf defaults
appear in Docker and workflow files. Run `bun scripts/check-toolchain-policy.mjs`
after updating the policy and all those consumers. Update pinned remote plugins
separately, regenerate and compare Go/TypeScript, then run frontend, backend,
proto and image checks. Image builds accept explicit `GO_IMAGE`, `BUN_IMAGE`
and `BUF_IMAGE` build arguments for a deliberate compatible alternative. Tags
select versions but are not immutable image digests.

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
process groups. Windows uses a native Job Object for each command: the operating
system assigns the suspended command to its job during creation, before the
helper resumes it. This also contains a command whose helper dies during startup.
The job owns descendants even after the original command exits. A held launcher handle
also ends the job if the JavaScript owner disappears. The helper's non-inherited
job handle makes abrupt helper termination end its descendants too. Do not
match process names across the machine: another worktree may use the same tools.
`make kill-dev` now explains this safe shutdown instead of killing every Bun,
Air, Vite or Panel process. Inspect an orphan's exact PID before stopping it.

On Windows, the launcher first builds the small `cmd/carbon-dev-supervisor`
developer tool when its source/dependency key is missing from `tmp`. This is a
nonprivileged tooling bootstrap, not a controller installation or firewall setup.
The content-keyed executable is reused rather than replacing a helper used by
another session. All subsequent command phases, including a fallback backend
build, run inside jobs. A failed job assignment refuses startup before the
command executes.

The helper preserves an existing terminal without opening a new console window.
Interactive stdin/stdout remain terminal handles. Ctrl+C reaches the JavaScript
owner, which stops the session's jobs together.

The initial compiler runs before that native helper is available. Normal stop
terminates its live compiler tree and starts no services. Abruptly killing the
JavaScript launcher during this first compilation is outside the job ownership
guarantee; the guarantee applies once the helper has been prepared. No claim of
durable arbitrary descendant ownership is made for that one bootstrap window.

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
native service-root exit/crash, abrupt Windows launcher/helper death, command
stdin/stdout, a real hidden Windows terminal with keyboard input and Ctrl+C,
initial bootstrap stop, path arguments, tool selection and
preservation of unrelated processes/data. Native fixtures compile with the
repository's Go toolchain, so Go must be on `PATH` for the launcher CI tests too.
Run the complete contributor checks for the affected Go/frontend surfaces too.

Module Dockerfiles are built by the `Developer and module build inputs` workflow
for affected PRs. These jobs build Geyser and status images for Linux amd64/arm64
without registry login or publication. Module-tag publication stays separate.
