# Carbon Panel OSS main reorganisation and implementation plan

**Reviewed:** 12 November 2026  
**Baseline:** [`b87190df731b8534ff08c12ed8b191f7760df40d`](https://github.com/athNdev/carbon-panel/commit/b87190df731b8534ff08c12ed8b191f7760df40d), latest fetched `origin/main` during review  
**Deliverable:** evidence-backed implementation plan; no production changes applied by this audit

## Recommended direction

Fix the demonstrated correctness and developer-experience problems first, remove code with no demonstrated consumer second, then consolidate repeated policy into modules that own the behavior. A broad directory rename would leave the important duplication intact.

The strongest findings are concrete:

- The full native Windows Go suite passed, but temporary module/proxy test executables triggered Windows Firewall Query User events. A diagnostic overlay restricting the implicated socket paths to loopback passed both complete packages with fresh binaries and zero new firewall events. Ordinary tests and development must run without recurring consent prompts while intentional production ingress remains available.
- Deferred file-editor requests can associate old file content with a new file path and save it there. A source-function probe reproduced that write with fake RPCs.
- The process storage adapter closes its output pipe prematurely. A direct roundtrip failed; removing that close in a diagnostic overlay passed. The existing purported remote test actually takes the local shared-storage adapter.
- Scheduled starts bypass the memory-admission rule enforced by other start paths. An overcommit fixture reproduced a successful scheduled start that should be rejected.
- Backup enumeration reads excluded files before applying exclusions and reads retained files twice. A counting adapter demonstrated both behaviors.
- Developer commands build a single `main.go` although the command now requires sibling files. Geyser's image recipe has the same package-input problem.
- **192 legacy frontend files, approximately 5,206 source lines**, are removal candidates after consumer/build checks. This is a maintenance-footprint estimate, not measured shipped-byte savings.

The plan contains 24 work packages. Small corrections should remain independently reviewable. Larger structural packages are sequences of small pull requests with stable interfaces and regression coverage. Jira is the current implementation tracker; historical tracker identifiers in source documents are not current status authority.

## Scope and evidence limits

The review used the immutable main revision above, governing knowledge concepts, direct source/reference analysis, normal validation commands, and targeted diagnostic probes. A clean checkout of the exact revision was reviewed; unrelated edits in the original checkout were preserved. Temporary source overlays and logs remained outside product source. Build-marker changes from verification were restored, leaving the analysis checkout clean.

The tracked tree contains 1,506 files and approximately 16.8 MB of working-tree file content. This excludes Git history and does not establish historical repository size. Generated protobuf output was excluded from judgments about handwritten complexity.

| Evidence label | Meaning |
| --- | --- |
| Reproduced | A focused executable probe demonstrated the behavior against the reviewed source; the seam and limits are stated. |
| Source-confirmed | Owning source establishes the mismatch or repeated behavior; the complete runtime scenario was not executed. |
| Design opportunity | A proposed improvement supported by concrete ownership/duplication evidence; not itself a defect. |
| Candidate / experiment | Requires further consumer tracing or measurement before adoption. |

Passing tests do not negate gaps the tests do not cover. This was not a security scan, live Docker deployment, production reliability qualification, or remote-node operational acceptance.

## Verification ledger

All outcomes refer to the reviewed revision and audit environment. Implementation must rerun checks for the surfaces it changes.

| Check | Observed outcome | Interpretation |
| --- | --- | --- |
| `go test ./...` | PASS, native Windows, Go 1.27.4 | Functional baseline passes; this original run also produced firewall Query User events for temporary test binaries. |
| `go vet ./...` | PASS, native Windows | Backend static baseline passes under the host toolchain. |
| Frozen frontend install | PASS | Lockfile resolved without intentional updates; 287 packages installed. |
| `bun run check` | PASS, 0 errors / 0 warnings | Svelte/TypeScript baseline is clean. |
| `bun run test` | PASS, 199 tests across 43 files | Existing suite passes; complex asynchronous workflows still lack necessary behavioral coverage. |
| Website, landing and design-system Python checks | PASS, 19 tests | Exercised documentation/website checks pass. |
| Brand asset verifier | PASS | Intentional deployable copies are consistent. |
| Buf 1.50.0 lint | PASS | Proto lint passes with this recorded version. |
| Knowledge fix, then lint | PASS, 42 concepts; 37/37 retrieval questions | Indexes current; 30 queries ranked the expected concept first. |
| Exact native `bun run lint` | FAIL: Prettier reported 498 files | Checkout CRLF policy conflicts with the default formatting invocation; not 498 independently reviewed style defects. |
| Prettier `--end-of-line auto` and separate ESLint | PASS | Supports line-ending policy as the failure mechanism; canonical command still needs repair. |
| Production Vite build via Node, 2,048 MB heap | PASS; static site emitted | Underlying frontend production build succeeds. |
| Canonical `bash scripts/web-build.sh` | BLOCKED by incomplete local Git Bash installation | Host could not launch its expected Bash executable. This is not a demonstrated repository build failure; rerun in a complete supported shell. |
| golangci-lint 2.13.2 with repository Go 1.26.10 | FAIL: 365 findings — 358 errcheck, 1 ineffassign, 6 staticcheck | Full debt is larger than stale CI comments. Triage meaningful failures by subsystem. |
| Same full linter with host Go 1.27.4 | INCOMPLETE: export-data decoder mismatch | An emitted “0 issues” does not establish success after typechecking failure. |
| Single-file Panel/Geyser build probes | FAIL: missing sibling helpers | Package-target corrections required. |
| Geyser Docker recipe | Source-confirmed invalid inputs; image not built | Clean-context Docker acceptance remains required after correction. |
| Direct process storage worker | FAIL; diagnostic premature-close removal PASS | Supports W06; separate production Docker transport was not shown broken. |
| Scheduled-start overcommit fixture | FAIL expectation; diagnostic guard PASS | Supports W07; restart parity needs its own executed case. |
| Backup counting adapter | FAIL read-count expectations | Excluded file read once, retained file twice. |
| File-editor deferred-RPC probe | Wrong-path write reproduced | Actual load/save bodies executed with fake RPC/state; no browser rendering or real user-file writes. |
| Corrected Windows module/proxy diagnostic | PASS, fresh binaries, zero new firewall events | One counterfactual run; repeated full-suite/dev acceptance remains outstanding. |

## Work package map

Estimates are focused engineering days, not delivery commitments. Risk means regression sensitivity. A dash means the package can begin independently after normal baseline/knowledge inspection.

| ID | Work package | Priority | Effort | Risk | Depends on |
| --- | --- | --- | --- | --- | --- |
| W01 | Eliminate recurring Windows test/development prompts | P1 | 2–4 | Medium | — |
| W02 | Repair package builds and own development processes | P1 | 1–2 | Medium | Coordinate W01 |
| W03 | Repair Geyser image inputs and build-only PR gates | P1 | 0.5–1 | Medium | — |
| W04 | Make validation failures visible; repair lint portability | P1 | 1–2 | Low/medium | — |
| W05 | Bind file-editor loads/saves to a session | P1 | 0.5–1.5 | Medium | — |
| W06 | Repair process transport ownership and test seam | P1 | 0.5–1 | Medium | — |
| W07 | Enforce scheduled-start admission parity | P1 | 0.5–1 | High | — |
| W08 | Apply backup exclusions before reading | P1 | 0.5–1 | Medium | — |
| W09 | Execute module hooks on their assigned node | P1 | 0.5–1 | Medium | — |
| W10 | Remove dead/scaffold code and exclusive dependencies | P2 | 0.5–1.5 | Low/medium | W04 helpful |
| W11 | Replace inherited ignore templates with tested policies | P2 | 0.5–1 | Medium | — |
| W12 | Remove ephemeral records; clarify documentation ownership | P1/P2 | 0.5–1 | Medium | Jira reconciliation |
| W13 | Pin toolchain policy; atomic reproducible generation | P2 | 1–2 | Medium | Coordinate W03/W04 |
| W14 | Unify review tooling with owned cleanup and profiles | P2 | 2–3 | Medium | W02/W11 |
| W15 | Share lifecycle admission and staged recreation policy | P2 | 4–7 | High | W07 |
| W16 | Inject a cancellable remote-storage resolver | P2 | 2–3 | High | W06 |
| W17 | Bounded storage streaming and actual size-limit parity | P2 | 4–7 | High | W08/W16 |
| W18 | Reconcile product REST with the proto contract | P2 | 3–5 | High | W13; coordinate W19/W22 |
| W19 | Consolidate provider HTTP policy | P2 | 2–3 | Medium | — |
| W20 | Share frontend inventory/lifecycle/status workflows | P2 | 2–4 | Medium | W05 test pattern useful |
| W21 | Generation-owned WebSocket lifetime | P1/P2 | 1–2 | Medium | — |
| W22 | Shared module codecs and cohesive studio workflows | P2 | 3–5 | Medium | W19; coordinate W18 |
| W23 | Authoritative display preferences; feature token migration | P2 | 2–4 | Medium | — |
| W24 | One lazy Monaco runtime owner | P2 | 1–2 | Medium | W05; coordinate W23 |

## Phase 0 — deterministic, unelevated development and reliable checks

### W01. Eliminate recurring Windows test/development prompts

**Priority / effort / risk:** P1; 2–4 days; medium. Coordinate development configuration with W02.

**Evidence.** The original full native suite generated Windows Firewall Query User block events **2097** for temporary `module.test.exe` / `proxy.test.exe`, followed by user-approved Allow changes **2099**. This identifies a firewall prompt mechanism; it does not independently explain every secure-desktop consent invocation.

Three Manager sites independently format wildcard `:<port>`: [Start at manager.go:229](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/proxy/manager.go#L229), [AddListener at :692](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/proxy/manager.go#L692), [module route at :811](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/proxy/manager.go#L811). Default-suite fixtures reach real wildcard TCP/UDP listeners through these paths. Separately, [UDP backend-session creation](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/proxy/udp.go#L250) passes a nil local address. Restricting only ingress does not constrain that separate socket purpose.

The agent launch path was unelevated (the Windows administrator-token check returned false). An external diagnostic overlay changed only those three Manager hosts and the UDP race-test's existing listener wrapper to loopback. The complete native module/proxy tests passed with newly built binaries. Firewall RecordId stayed **87896** throughout: **zero new firewall events**. No firewall/UAC policy was changed. One corrected package run is strong cause evidence, not final repeated acceptance.

**Change and ownership.**

- Supply `proxy.Manager` with one instance-owned ingress bind-host/address policy and route all three creation sites through it.
- Preserve current intentional production wildcard behavior; test instances use loopback and ephemeral listener-owned ports.
- Where a fixture only tests route restoration/selection, use a recording Proxier factory. Retain separate real TCP/UDP forwarding and race-cleanup tests.
- Inject UDP backend-session socket creation separately, with explicit local address policy. A rejecting test adapter must stop an unintended wildcard bind before the OS sees it.
- Avoid mutable package-global hooks and avoid hiding collisions by serialising the default suite.
- Default development explicitly selects loopback for Vite, controller, module/proxy ingress and direct knocks. [Vite wildcard is at line 60](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/vite.config.ts#L60); [controller defaults are public](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/config/config.go#L271). Make LAN/integration exposure a separately named opt-in profile.
- The existing direct-knock test already sets loopback. Do not repeat it as an unresolved root cause.

**Acceptance.**

1. Cover Manager Start/AddListener/module restoration, UDP backend sessions and direct-knock constructor paths with address-policy assertions.
2. Rebuild uncached module/proxy tests, then `go test -count=1 ./...`, repeatedly as a standard Windows user with fresh temporary paths and ordinary package parallelism.
3. Bracket runs with available firewall records and observe no new Query User events/consent; success status alone is insufficient.
4. Repeat build/vet, frontend check/test/build and default dev start/stop. Inspect actual bound addresses.
5. Validate on a clean disposable Windows profile so pre-existing broad allowances do not hide recurrence; do not change the user's rules automatically.
6. Ordinary tests require **zero initial or recurring elevation**. Any verified necessary LAN setup may use **at most one deliberate initial prompt**, for a stable exact application path and narrow ports/protocol/profile/scope.
7. Do not disable UAC/firewall, permit all temporary executables, register transient test paths, or move all verification to Linux to avoid native Windows coverage.

### W02. Repair package builds and own development processes

**Priority / effort / risk:** P1; 1–2 days; medium. Coordinate with W01.

**Evidence.** [Make builds main.go alone](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/Makefile#L42-L55), as do Air and several launchers, although sibling implementations are required. [dev.ts](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/scripts/dev.ts#L71-L113) lacks complete exit supervision; [kill-dev](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/Makefile#L136-L145) matches process names globally.

**Change.** Build `./cmd/carbon-panel` consistently. Make `scripts/dev.ts` the cross-platform process owner, with Make/Bash/PowerShell delegating. Derive repository root from the script, resolve tools on PATH before deliberate fallback, and terminate only owned process groups/session IDs. A failed child stops its peer and propagates failure. Pass W01's explicit development bind policy, ports and LAN opt-in through this owner.

**Acceptance.**

- Native/package and supported cross-compile builds pass; startup works with/without Air and paths containing spaces.
- SIGINT and early startup failure stop only owned children; another worktree's Bun/Panel process survives.
- Ordinary stop preserves worlds/databases and existing data-safety checks remain intact.

### W03. Repair Geyser image inputs and add build-only PR gates

**Priority / effort / risk:** P1; 0.5–1 day; medium. Independent.

**Evidence.** [Dockerfile.geyser](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/docker/Dockerfile.geyser#L1-L12) copies only main.go, while [the command](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/cmd/geyser/main.go#L10-L27) imports its local pin package and calls platform-specific sibling helpers. Single-file compilation failure was reproduced. Module image CI currently validates tags/manual dispatch rather than every affected PR.

**Change.** Supply module metadata and the complete command package, then build `./cmd/geyser`. Add reusable build-only PR jobs for Geyser and status image inputs; publication remains a separately credentialed/tag-triggered stage. Preserve pinned download/checksum policy, platform adapters, supported architectures and non-root execution.

**Acceptance.** Clean-context image builds pass without host caches/publish credentials; pin tests pass; supported architectures compile/build; PR jobs never push. The audit did not build the invalid Docker recipe, so actual image construction remains required.

### W04. Make validation failures observable; repair lint portability

**Priority / effort / risk:** P1; 1–2 days; low/medium. Independent; coordinate pins with W13.

**Evidence.** [govulncheck CI](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/.github/workflows/ci.yml#L133-L148) discards command failure and searches text for `Fixed in:`. Tool/typecheck/database failure can look successful. Native formatting reports 498 CRLF-related files while `--end-of-line auto` and ESLint pass. Compatible full lint reports 365 findings; incompatible export-data failure is not zero debt.

**Change.** Use a checked validator runner with structured completion/error states, retaining the current unfixed-finding policy unless separately changed. Keep the established line-ending policy and make the supported Windows invocation explicit (`--end-of-line auto`), or use a platform-aware lint entrypoint. Do not rewrite tracked files or change `.gitattributes` solely to silence a local-only formatter result. Publish full-lint artifacts with revision/toolchain and repair stale counts. Triage significant persistence/I/O/failure-path errors by owning subsystem rather than blanket `_ =` edits.

**Acceptance.**

- Fixtures distinguish clean completion, fixed/unfixed findings, malformed output, startup/typecheck/network failure; incomplete validation fails the job.
- Supported lint entrypoints pass on clean Linux and Windows checkouts, including the documented native formatter override, without a blanket line-ending rewrite.
- Compatible full debt is visible and new-code checks block regressions. Pay down owning paths with W08/W15–W19; do not hide historical debt behind a formatting sweep.

## Phase 1 — correctness fixes before extraction

### W05. Bind file-editor loads and saves to one session

**Priority / effort / risk:** P1; 0.5–1.5 days; medium. Independent.

**Evidence.** [loadFileContent/handleSave](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/files/file-editor-dialog.svelte#L130-L168) do not bind awaited results to current open/server/path identity. [The parent reuses the dialog](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/files/server-files.svelte#L1047-L1057). A probe extracted the actual function bodies: request A, close, open/request B, resolve A while B waits, edit/save. It wrote A content to B's path. Only fake RPC/state were used; browser rendering was not exercised.

**Change.** Introduce `lib/features/files/editor-session.ts` owning immutable `{generation, serverId, path}` and loaded/saved content identity. Close/navigation invalidates generation. Guard success, error and finally; stale errors cannot close a new dialog. Snapshot identity/content for save and mark only that saved revision clean. Keep modal restyling/editor replacement out of this correction.

**Acceptance.**

- Deferred calls complete in either order; close-before-success/error; same path across servers; navigation during save; edits during save.
- Obsolete calls cannot change B's loading/content, close B, or write A content to B.
- A short browser close/open case exercises the real dialog lifecycle and save-disabled state.

### W06. Repair process transport ownership and its test seam

**Priority / effort / risk:** P1; 0.5–1 day; medium. Precedes W16.

**Evidence.** [ProcessTransport.Open](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/nodestorage/transport.go#L86-L119) closes its pipe writer immediately after process start. A direct roundtrip failed with EOF; removing that close in an external diagnostic overlay passed. [The named remote roundtrip](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/nodestorage/transport_test.go#L51-L89) creates a locally visible root and selects `*nodestorage.local`; its green result does not cover process transport.

**Change.** Define one lifetime owner per pipe end; keep stdout writable until the pump exits. Cover the direct process adapter, then rename shared-storage tests or assert transport invocation. Clarify whether the adapter is a supported mode or a harness; do not remove it simply to avoid a failure.

**Acceptance.**

- Real subprocess Open/read/write/Close passes.
- Failed start, early exit, timeout and cancellation return bounded errors without child/goroutine leaks.
- A purported remote-selection test fails when bypassing transport. Do not claim the separate Docker adapter was reproduced broken.

### W07. Enforce scheduled-start admission parity

**Priority / effort / risk:** P1; 0.5–1 day; high. Land before W15.

**Evidence.** [RPC admission](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/rpc/services/server.go#L263-L301) and boot/wake reject overcommit, but [scheduled start](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/scheduler/scheduler.go#L882-L915) starts directly. A 6,144 MiB server on a fake 4 GiB daemon succeeded through this path. The existing guard in a diagnostic overlay made rejection pass. Scheduled restart is a similar statically inspected path requiring its own executed case.

**Change.** Land a small parity correction and coverage before structural extraction. Characterise restart accounting carefully: restarting an already allocated server and starting a stopped server need intentional treatment. Per-server transition locks do not establish atomic capacity reservations across several servers on one node.

**Acceptance.** Start/restart/boot/wake matrix with stopped/paused/deep-sleep and constrained nodes; rejected admission makes no Docker start call; explicit task error with unchanged retry semantics. Concurrent same-node starts need an explicit reservation test/design in W15.

### W08. Apply backup exclusions before reading

**Priority / effort / risk:** P1; 0.5–1 day; medium. Land before W17.

**Evidence.** [Backup enumeration](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/worldstore/accessor.go#L34-L83) lists/hashes before applying skip; [listing reads bodies](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/worldstore/accessor.go#L198-L245). A counting adapter observed one read of an excluded temporary file and two of a retained world file. An unreadable excluded file can therefore fail backup by visible error propagation; that failure was not separately injected.

**Change.** List metadata, prune excluded paths before body reads, and hash retained files while emitting archive entries. Preserve deterministic manifests/content. Keep bounded protocol redesign separate in W17.

**Acceptance.**

- Excluded files are never read; retained files are read once.
- Excluded unreadable/disappearing fixtures do not fail backup.
- Manifest ordering/checksums and restore roundtrip remain unchanged; cancellation/partial-archive errors stay explicit.

### W09. Execute module hooks on their assigned node

**Priority / effort / risk:** P1; 0.5–1 day; medium. Reconcile existing Jira work.

**Evidence.** [Module manager resolves assigned nodes](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/module/manager.go#L116-L132), but [hook execution calls local Docker directly](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/module/hooks.go#L208-L215). The gap is already present in historical knowledge; duplicate work should be avoided.

**Change.** Resolve hook execution through the existing Docker Locator/manager seam with operation context. Classify remaining direct local client uses by actual action ownership. Avoid adding another generic module for a one-call correction or repeating neighboring stale gap claims without checking current source.

**Acceptance.** Remote hook reaches a remote fake daemon; unavailable assigned node performs no local-sentinel request; valid remote resolution cannot panic because local Docker is nil; command semantics, concurrency limits and cancellation remain intact.

## Phase 2 — trim, reproducibility and documentation ownership

### W10. Remove dead/scaffold code and exclusive dependencies

**Priority / effort / risk:** P2; 0.5–1.5 days; low/medium. W04 is helpful, not a prerequisite for starting.

**Evidence.** Static frontend closure found 192 unreachable files across 24 legacy UI groups. [Unused cache](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/cache/cache.go#L8-L84) has only implementation-local tests. [Plugin database provisioning](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/plugindb/provision.go#L1-L25) is an uncalled placeholder, not an operational feature.

**Change.** Separate mechanical Go-scaffold removal from UI/dependency removal. Recheck build tags, generated scripts, registries, tests and documented interfaces. Remove only dependencies supported exclusively by removed code, then regenerate the lockfile normally. Keep future provisioning requirements in Jira instead of success-shaped executable placeholders.

**Acceptance.** Deletion matrix checks below; Go test/vet; frozen frontend install/check/test/build; retained dialogs/console/module templates functional. No persistent-data migration. Record actual source reduced and measure bundle changes separately if relevant.

### W11. Replace inherited ignore templates with tested policies

**Priority / effort / risk:** P2; 0.5–1 day; medium. Independent.

**Evidence.** [.gitignore](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/.gitignore#L10) and [.dockerignore](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/.dockerignore#L10) contain inherited templates with differing local-state exclusions. [Builder COPY](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/docker/Dockerfile.carbon-panel#L51) means inputs depend unnecessarily on checkout clutter. `node_modules/` is already excluded; do not report it missing.

**Change.** Write short grouped Git policy and separate Docker-context policy or explicit COPY inventory. Cover user data, local config, keys/certificates, local MCP overrides, caches, worktrees and generated output. Preserve source exceptions, including backupcrypt, plus worker/proto/web inputs. Do not delete local directories or run `git clean` as part of replacing policy.

**Acceptance.**

- `git check-ignore` sentinel matrix covers both required exclusions and required source exceptions.
- Context sentinel tests and clean-versus-cluttered disposable builds prove equivalent intended inputs.
- Ordinary cleanup remains non-destructive; required command/worker/config/example sources remain buildable.

### W12. Remove ephemeral records and clarify documentation ownership

**Priority / effort / risk:** P1 sanitisation / P2 organisation; 0.5–1 day; medium. Reconcile current Jira.

**Evidence.** [docs/agents README](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/docs/agents/README.md#L8-L10) describes private operational context although tracked in OSS. [state.md](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/docs/agents/state.md#L1-L14) records workstation/task state and claims contradicted by current worker/image wiring. The nine tracked files under `docs/agents` total roughly 105 KB and require individual review.

**Change.** Remove current ephemeral state after extracting still-valid decisions into governing ADR/knowledge concepts. Keep sanitised historical rationale only where useful and label it historical. Add a compact map: Starlight source for user guides, runbooks for operations, ADR/knowledge for durable design, Jira for work/status. Preserve deliberate commit-pinned website inputs and snapshot isolation.

**Acceptance.** Current tracked docs contain no private operational addresses, credentials or workstation paths; links resolve; rationale remains discoverable; docs/knowledge/site checks pass. No automatic history rewrite or speculative credential changes are included.

### W13. Pin toolchain policy and make generation atomic/reproducible

**Priority / effort / risk:** P2; 1–2 days; medium. Coordinate W03/W04.

**Evidence.** [Make uses floating Buf](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/Makefile#L15), while CI/docs/frontend metadata name different versions. Docker Go/Bun tags are broad; remote generator versions already have explicit pins. [Generation cleans before running](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/Makefile#L212-L223), so generator failure can leave tracked output deleted. Normal deps mixes installation and deliberate updates.

**Change.** Declare a tested policy consumed by Make/Docker/CI/release with explicit operator overrides. Pin image/tool versions with an update procedure; do not require generator/runtime equality without compatibility evidence. Generate in a temporary tree, apply established formatting/licensing, compare owned outputs, then replace only on success. Separate frozen install from named update commands.

**Acceptance.**

- Clean regeneration has no diff; deliberate proto edits fail comparison until output is refreshed.
- Failed generation preserves original output; normal install preserves lockfiles.
- Linux/Windows supported entrypoints use recorded compatible tools; deployment/release docs describe changed behavior.

### W14. Unify review tooling with explicit profiles and owned cleanup

**Priority / effort / risk:** P2; 2–3 days; medium. Depends on W02/W11.

**Evidence.** `scripts/manual-review` and `scripts/review` have useful but different dual-installation/seeded profiles, with duplicated session/RPC/browser/setup behavior. [Journey tool lookup](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/scripts/review/journey.mjs#L41-L70) depends on home-directory state. [Cleanup removes a configurable directory](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/scripts/review/devstack.sh#L181-L184).

**Change.** One `scripts/review` toolkit owns locked driver/browser setup, helpers and report schema. Retain `single-seeded` / `dual-ownership` profiles with isolated namespaces and ownership manifests. `down` preserves data; named reset resolves its path, verifies owned root/marker and refuses unrelated resources. Critical fixture setup failure must fail the journey instead of yielding success-shaped screenshots.

**Acceptance.**

- Both profiles retain coverage; concurrent sessions coexist; Windows/WSL/Linux use the same recorded driver.
- Stop preserves data; reset refuses root, unrelated directories and another session.
- Lifecycle/gameplay acceptance remains distinct from UI screenshots; no broad Docker-resource pruning.

## Phase 3 — deepen the modules that own repeated policy

### W15. Share lifecycle admission and staged recreation policy

**Priority / effort / risk:** P2; 4–7 days across several PRs; high. Depends on W07.

**Evidence.** [Server RPC lifecycle](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/rpc/services/server.go#L2232-L2509), [config recreation](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/rpc/services/config.go#L363-L430), [scheduler staging](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/scheduler/scheduler.go#L1029-L1158) and reconciler paths repeat admission, inheritance, staged apply/undo, replacement-ID persistence, status/events and rollback. W07 is a demonstrated policy divergence.

**Change.** Build focused `internal/serverruntime` behind a small intent-based start/restart/stop/reconfigure interface. Extract in-package first if necessary to avoid cycles, then move once ownership is clear. Accept owned transitions and actual Docker/SQLite seams; own admission, staged application and outcome persistence. RPC retains Connect decoding/error translation, scheduler retains cron/retries, reconciler retains observation. Design same-node capacity reservations explicitly if concurrent starts need atomic accounting.

**Migration order.** Characterise RPC behavior; extract admission without change; migrate scheduler/boot/wake; migrate config recreation; reuse only overlapping reconciler policy. Keep new behavior, such as reservations, separate from mechanical moves.

**Acceptance.**

- Stopped/paused/deep-sleep/partial-recreation behavior equivalent through intended entrypoints.
- Request idempotency/journal kinds stable; no local fallback; partial replacement IDs persisted.
- Staged rollback and LastStarted preserved; exactly one event; concurrent admission cases covered.
- Use real-store integration tests, not a giant Store interface plus hundreds of stubs. Record project-wide choice in an ADR/knowledge index.

### W16. Inject a cancellable remote-storage resolver

**Priority / effort / risk:** P2; 2–3 days; high. Depends on W06.

**Evidence.** [Transport is process-global](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/nodestorage/transport.go#L26-L44); [ForNode opens with Background](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/nodestorage/accessor.go#L319-L334). [Worker request and Close share a mutex](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/storageworker/client.go#L49-L98), so blocked decode can obstruct cleanup. A live Docker hang was not reproduced.

**Change.** Composition root owns `ForNode(ctx, node, dataRoot)` resolver with injected transport/capabilities. Carry operation context through startup/handshake. Cancellation closes underlying transport without waiting behind blocked request mutex. Migrate through a compatibility wrapper if needed, then remove global SetTransport. Preserve fail-closed selection; unreachable remote nodes never become local storage.

**Acceptance.** Blocked handshake/read/write cancels promptly; process/container/attach resources close; separate instances cannot change one another; explicit local/shared/worker selection; existing remote sentinels show zero controller-side fallback writes.

### W17. Add bounded storage streaming and actual size-limit parity

**Priority / effort / risk:** P2; 4–7 days across staged PRs; high. Depends on W08/W16.

**Evidence.** [Accessor uses whole-file slices](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/nodestorage/accessor.go#L38-L51); [CreateZip buffers output](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/nodestorage/createzip.go#L22-L46). [Worldstore allows 4 GiB/file and 64 GiB total](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/worldstore/stream.go#L26-L30), but [worker requests have an approximately 129 MiB encoded-line cap](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/storageworker/serve.go#L16-L40), including base64 expansion. Increasing that buffer does not bound whole-file/archive allocations.

**Change.** Version the worker protocol and add bounded streaming/chunks at the existing Accessor seam: fixed frames, backpressure, checksums, cancellation and temporary-file/atomic commit. Retain small text convenience operations with explicit limits. Migrate backup/restore/archive output before higher-level UI operations; metadata listing never fetches bodies.

**Acceptance.**

- A file larger than the old frame cap transfers with bounded memory; local/shared/process/Docker adapters match.
- Interrupted operations preserve old user data and recoverable staging.
- Hashes/manifests remain deterministic; documented limits match enforcement.
- Fault injection covers cancellation, partial write and rename/commit failure.

### W18. Reconcile product REST with the proto contract

**Priority / effort / risk:** P2; 3–5 days across incremental migrations; high. Depends on W13; coordinate W19/W22.

**Evidence.** [RPC server mounts product JSON handlers](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/rpc/server.go#L263-L288) for search/studio/staging and other control operations despite the proto-first knowledge contract. [Studio actively consumes them](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/routes/modpacks/studio/+page.svelte#L158-L183); they are not dead routes.

**Change.** Inventory consumers and record an ADR choosing administrative/product transport policy. Recommended: generated proto for control JSON, thin compatibility HTTP adapters through a stated deprecation window. Binary streaming/content delivery, OIDC callbacks and health remain justified HTTP. Shared application behavior and authorization mapping precede switching clients; avoid parallel policy implementations.

**Acceptance.** REST compatibility and generated clients cover identical behavior/errors/resource scope; negative authorization mapping exists before new RPCs ship; generated output reproduces; API docs match. Do not delete handlers or every `/api/v1` route as cosmetic cleanup.

### W19. Consolidate content-provider HTTP policy

**Priority / effort / risk:** P2; 2–3 days; medium. Independent; migrate before W18 client changes.

**Evidence.** [Existing HTTP client](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/indexers/httpclient.go#L19-L63) owns headers/timeouts/errors, while [mod search](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/rpc/handlers/mod_search.go#L286-L320) and [Packwiz update checks](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/internal/packwiz/content.go#L624-L661) reconstruct requests and hide failures as empty/no-update results.

**Change.** Extend existing concrete provider adapters; share a small search/versions/resolve interface across browse/authoring. Inject endpoint/client through construction. Centralise fallback, auth and timeout while preserving loader/side/pinning/hash differences. Review explicit degraded/failure results as a behavioral change after mechanical consolidation.

**Acceptance.** Fixture-only 401/403/404/429, malformed-data, cancellation and fallback cases; equal filtering/order/hash choice; no live provider calls in tests; one authoritative policy per provider.

### W20. Share frontend inventory, lifecycle and status workflows

**Priority / effort / risk:** P2; 2–4 days; medium. W05's deferred-call tests provide a useful pattern.

**Evidence.** [Servers store](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/stores/servers.ts#L17-L47) unconditionally commits responses. Layout/list/dashboard/detail overlap as writers with incomplete request sequencing. [Mutation/delete workflows](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/routes/servers/+page.svelte#L108-L197) and status switches repeat across views. A summary response can overwrite a richer object; field-level consequences require contract-specific coverage.

**Change.** `features/servers/inventory.svelte.ts` owns hydration/errors, generations, in-flight deduplication, explicit summary/detail merges and refresh subscriptions. Land stale guards before timer consolidation. `actions.ts` owns request IDs/payloads and returns outcomes; `presentation.ts` owns status label/tone. Views keep confirmation/navigation and genuinely specific copy. Use existing generated rpcClient, not a second transport layer.

**Acceptance.**

- Out-of-order responses cannot restore removed servers or overwrite post-action state; summary fetches do not silently erase richer observations.
- Direct/empty/error/login/logout paths work; timers/subscriptions close on unmount.
- Parameterised start/stop/restart/recreate/delete/keep/detach/export cases assert payloads, idempotency and non-destructive export.
- Shared ERROR/UNHEALTHY presentation retains distinct severity through each route.

### W21. Give WebSocket connections generation-owned lifetime

**Priority / effort / risk:** P1/P2; 1–2 days; medium. Independent.

**Evidence.** [Callbacks reference mutable socket state](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/stores/websocket.svelte.ts#L94-L133); [reconnect replaces it immediately](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/stores/websocket.svelte.ts#L152-L181). A late A close/error can clean up or close B. Subscription state loses tail options; [resubscribe hardcodes 500](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/stores/websocket.svelte.ts#L398-L408). Browser timing was not reproduced.

**Change.** Retain singleton-facing interface behind a factory accepting socket/timer/token access. Capture socket/generation in callbacks; stale events cannot affect current state. Timers belong to generations and subscription options survive reconnect. Preserve batching/backoff, fallback and subscriber isolation.

**Acceptance.** Fake sockets deliver A close/error/message after B authenticates without affecting B; only one reconnect timer; disconnect prevents queued reopen; non-default tail survives. Add a real browser reconnect smoke after seam coverage.

### W22. Extract module form codecs and cohesive studio workflows

**Priority / effort / risk:** P2; 3–5 days, staged; medium. Depends on W19; coordinate W18.

**Evidence.** [Module codecs](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/server/ModuleDialog.svelte#L139-L181) and [template codecs](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/server/ModuleTemplateCreateDialog.svelte#L125-L146) encode the same formats independently. Studio detail combines metadata/content/dependency/migration/file workflows. Repeated invariants and unrelated change paths, rather than a file-length threshold, justify extraction.

**Change.** First share pure `features/modules/form-codecs.ts`. Keep template defaults, instance overrides and staged scalar/topology updates distinct. Preserve parse behavior during mechanical extraction; later validation changes should report malformed persisted JSON explicitly. Move one cohesive studio workflow at a time into `features/modpacks/studio/`; stable route entries become composition shells. Avoid a generic form engine.

**Acceptance.** Round-trip whitespace/blank/duplicate rows, read-only/create-dir, metadata/numeric defaults; malformed input treatment explicit; outgoing generated requests equivalent; template-to-instance and scalar staging characterised. Tests move with behavior, not merely source filenames.

### W23. Make display preferences authoritative and migrate tokens by feature

**Priority / effort / risk:** P2; 2–4 days foundation/initial migrations; medium. Independent.

**Evidence.** [Preferences dialog](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/PreferencesDialog.svelte#L25-L62) and [shell](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/carbon/CarbonShell.svelte#L118-L138) hydrate/apply Carbon attributes separately; [toast reads ModeWatcher](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/ui/sonner/sonner.svelte#L1-L13). Editors hardcode dark. There are 2,490 six-digit hex occurrences in 73 Svelte files, including legitimate colors; a token edit alone is not full propagation.

**Change.** One `lib/display-preferences.svelte.ts` owns defaults, validation, storage, current values and document attributes. Shell/dialog/toast/editor selection consume it. Preserve storage keys, dark default, motion/density and `:where(:root)` specificity. Migrate surface/text/border tokens by feature with a narrow new-debt ratchet. Follow shared Carbon/Cloud pinned-snapshot rules for contract changes.

**Acceptance.** Fresh/stored light/dark, unavailable storage, login before shell, live switch, toast/editor/portal consistency, motion/density, dark/light visuals at 360/768/1440. Preserve purposeful third-party/status colors; no speculative Cloud edits or product-wide completion claims.

### W24. Give Monaco one lazy runtime owner

**Priority / effort / risk:** P2; 1–2 days; medium. Depends on W05; coordinate W23.

**Evidence.** [Code editor](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/ui/code-editor.svelte#L13-L40) and [file editor](https://github.com/athNdev/carbon-panel/blob/b87190df731b8534ff08c12ed8b191f7760df40d/web/carbon-panel/src/lib/components/files/file-editor-dialog.svelte#L27-L54) eagerly import Monaco/workers and configure global bootstrap independently; the former also registers JSON-template language. Byte savings are unmeasured.

**Change.** `features/editor/runtime.ts` exposes cached `loadEditorRuntime()` owning worker setup and one-time language registration. Views retain model/dirty/disposal. First-use load has explicit loading/error state and respects W05 session invalidation/W23 theme. Preserve large-file options and offline worker/CSP compatibility.

**Acceptance.** Concurrent opens initialise once; editors work in either first-open order; navigation during load leaks nothing; resize/close/reopen/save work; production worker serving passes. Compare emitted chunks/latency before claiming performance savings.

## Proposed ownership and repository layout

Retain top-level `cmd/`, `internal/`, `pkg/`, `proto/`, `web/`, `docker/`, `scripts/`, `docs/` and `knowledge/`. They already express useful ownership. Avoid another umbrella `core`, `common`, `services` or `utils` dumping ground.

```text
internal/
  serverruntime/             admission, staged apply/recreate outcomes (W15)
  nodestorage/               confinement, resolver, transport selection (W06/W16/W17)
  storageworker/             versioned bounded worker protocol (W16/W17)
  worldstore/                deterministic backup/restore policy (W08/W17)
  indexers/<provider>/       provider HTTP/auth/fallback behavior (W19)
  rpc/services/              generated-contract adapters, error/scope translation (W18)
  rpc/handlers/              HTTP content/streams + temporary compatibility (W18)
  scheduler/                 timing/retries, delegates lifecycle policy (W15)
  reconciler/                observation/recovery coordination (W15)
web/carbon-panel/src/lib/
  features/files/            editor-session identity (W05)
  features/servers/          inventory, actions, presentation (W20)
  features/modules/          codecs and domain form workflows (W22)
  features/modpacks/studio/  metadata/content/dependency/file workflows (W22)
  features/editor/           one lazy Monaco runtime (W24)
  display-preferences.svelte.ts  validated preference state (W23)
  components/carbon/         canonical shared UI; inventory preserved
  components/ui/             retained primitives with actual consumers
  api/                       current Connect transport/streams/HTTP exceptions
  proto/                     reproducible generated output
scripts/
  dev.ts                     owned cross-platform launcher (W02)
  review/                    locked single-seeded / dual-ownership profiles (W14)
docs/                        user/operations/design sources with a clear map (W12)
knowledge/                   durable concepts/invariants/decisions, never task logs
```

These are proposed destinations, not a requirement to move everything at once. An interface should reduce caller knowledge by concentrating policy. A helper used once can remain private beside its implementation. Actual variation justifies an adapter seam; a hypothetical future use does not.

## Deletion and consolidation matrix

| Candidate | Evidence / disposition | Proof before removal |
| --- | --- | --- |
| 24 legacy UI groups, 192 files / ~5,206 lines | W10 removal candidates, listed below. | Recompute closure after rebasing; inspect docs/tests/generation conventions; frozen check/test/build and retained journeys. Do not equate lines with shipped bytes. |
| `carbon-components-svelte`, `carbon-icons-svelte` | No source/config consumers outside metadata found. | Recheck current repository references, regenerate lockfile, clean install/build. |
| `@internationalized/date`, `embla-carousel-svelte`, `formsnap`, `sveltekit-superforms`, `vaul-svelte` | Only referenced in candidate UI islands. | Remove islands first; actual install/build graph proves the removable dependency closure. |
| `internal/cache` and its own tests | No production imports found. | Check build tags/scripts/generated inputs; Go test/vet. |
| `internal/plugindb/provision.go` | Uncalled provisioning placeholder. | Retain requirements in Jira; confirm no consumers; remove placeholder rather than claiming capability. |
| `components/files/file-toolbar.svelte`, orphan sidebar `is-mobile` hook | No route-entry runtime path. | Recheck references, remove with abandoned parent island. |
| `staged-config-view.svelte` | Intentionally unmounted and discussed in research. | Explicit feature decision: integrate with parity/acceptance or retire after reconciliation. Not automatic boilerplate deletion. |
| `docs/agents/state.md` / handoffs | Stale ephemeral operational context. | Extract valid decisions, resolve links, retain useful sanitised history intentionally. No automatic history rewrite. |
| Duplicate `scripts/manual-review` setup | Consolidate into retained dual-ownership profile. | Coverage parity, portable locked setup, owned cleanup. |
| Repeated lifecycle/provider/form/editor implementations | Consolidate through actual owning seams. | Characterisation and request/format/failure equivalence before old paths are deleted. |

The candidate UI groups are:

`accordion`, `aspect-ratio`, `avatar`, `calendar`, `carousel`, `collapsible`, `command`, `context-menu`, `drawer`, `form`, `hover-card`, `input-otp`, `menubar`, `navigation-menu`, `pagination`, `radio-group`, `range-calendar`, `scroll-area`, `sheet`, `sidebar`, `skeleton`, `slider`, `toggle`, `toggle-group`.

**Method.** Conservative static closure starts at every SvelteKit `+` route entry and client/server hook, resolving literal imports/re-exports through `$lib`, relative imports, indexes and `.js` to TypeScript equivalents. Production has no `import.meta.glob`; both dynamic imports are literal and included. The package is private with no exports, and `components.json` is generator configuration rather than runtime discovery. Barrel reachability is deliberately over-approximated. Type declarations and generated proto were not classed as dead runtime code. Absence of a textual import is insufficient for Docker binaries, reflected RPC names, runtime templates or embedded assets.

## Retain deliberately

- Worlds, databases, backups, migration history and recoverable staging. Ordinary clean/test/build/stop preserves them; destructive commands remain separately named and scoped.
- Docker Locator, owned Transition handles, pure proxy PlanFor, confined node Accessor and worldstore invariants. These already concentrate useful policy.
- Negative remote/local-sentinel tests, effective container-privilege/override checks, partial recreation/staged rollback and real SQLite integration coverage.
- Public `pkg/mcstatus` / `pkg/routing`, reproducible generated proto, actual worker modes and auxiliary images until consumers are traced.
- Live legacy UI primitives: select/dialog/table/dropdown/tooltip/resizable and required dependencies. Do not delete `ui/` wholesale.
- Current Carbon exports and their documentation inventory. An export change updates the design-system page and inventory test in the same PR.
- Active Lodash, bits-ui, paneforge, toast/theme dependencies and Monaco until a measured migration proves otherwise. A devDependency label alone does not prove a bundled import unused.
- Brand editable sources and intentional copies across independent deployable surfaces; licence files, inherited notices, third-party/generated exceptions and lockfiles.
- Commit-pinned documentation examples/snapshot isolation. Publishing must not silently consume an arbitrary dirty checkout.

## Verification and implementation protocol

Before editing, retrieve governing constraints with:

```sh
go run ./cmd/knowledge-lint -root . -for-path <path>
```

Search before reading additional concepts. Preserve unrelated edits/staging. Recheck candidates against then-current main and current Jira issues before implementation.

Each PR should contain one reviewable correction or mechanical extraction with regression coverage at the closest practical seam. Keep correctness fixes separate from visual redesign, mass moves and formatting sweeps. Preserve API/proto/data formats during extraction unless that migration is explicitly in scope. Update governing knowledge with behavior in the same commit; project-wide non-obvious choices require an ADR and knowledge ADR index entry. Task state and temporary diagnostic evidence do not belong in knowledge.

Run narrow checks while iterating, then checks for every affected surface before merge:

```sh
go test ./...
go vet ./...
# Frontend, from web/carbon-panel:
bun install --frozen-lockfile
bun run check
bun run test
# Repository root, complete supported shell:
bash scripts/web-build.sh
make proto-lint
# Final knowledge reconciliation:
go run ./cmd/knowledge-lint -root . -fix
go run ./cmd/knowledge-lint -root .
```

Add relevant website/design-system/brand checks and image builds for those paths. W01 requires observable repeated native Windows no-prompt acceptance; W16/W17 require bounded cancellation/memory and adapter equivalence; W18 requires negative scope cases. Live gameplay/outage/recovery acceptance remains a named operational lane when required by the changed capability.

### Test reorganisation

Frontend has 43 test files; 31 read source, and 28 of those contain neither render nor mount. Some are valuable policy checks. Three Carbon tests already compile and render real components. As workflows move, replace implementation-shaped strings with calls across actual exported interfaces, deferred RPC/fake socket/time adapters, rendered semantics and small browser journeys for lifecycle/focus/scroll.

Preserve prohibited-import, token-default, inventory, layout and reduced-motion policies. Do not delete tests simply to make moves pass. A harmless symbol/path rename should not invalidate a behavioral expectation, and an actual behavior change must be detected. Do not test a duplicated reimplementation of the new helper.

### Completion criteria for each package

- Intended behavior and failure paths are covered at the owning seam.
- Required checks pass under recorded compatible tools; failures are not converted into success-shaped output.
- Shared contracts, deployment/release docs and operating behavior are updated where affected.
- Knowledge indexes/retrieval pass, with new durable choices recorded correctly.
- No unowned process/resource cleanup or user-data loss; reversible staging remains recoverable.
- Outstanding operational acceptance is named accurately rather than hidden behind green CI.
- Current Jira issue links the PR and relevant evidence.

## Delivery sequence and scope controls

1. Start W01–W09 as narrow corrections. W01/W02 share bind/process decisions; W05/W06/W07/W08 have reproductions to retain as regressions. W09 follows current assigned-node semantics.
2. W10–W13 remove demonstrated debt and stabilise inputs/checks. W12 sanitisation can proceed as soon as durable facts/links are reviewed. Do not block small fixes behind the complete toolchain programme.
3. W14 improves repeatable review evidence before large migrations. W15–W19 concentrate lifecycle/storage/transport policy around actual adapter variation.
4. W20–W24 reorganise frontend behavior by feature. W22 coordinates provider/control-path changes; W24 follows session identity; W23 follows the published shared-token contract.

Summing the rough package ranges gives approximately **36–65 focused engineering days**, before overlap, operational qualification and optional experiments. This is a sizing aid, not elapsed schedule or commitment. Early correctness/mechanical packages are independently shippable. Runtime/data changes need more review than unused UI scaffolding; sequence by impact and evidence rather than line count.

### Optional experiments outside the 24-package scope

- Measure production dependencies/build time/binary size before replacing general Casbin GORM persistence with a SQLite-specific adapter. Other SQL drivers are in the production closure, but replacement adds authorization-sensitive maintenance. Preserve `casbin_rule`, current policies and fail-closed behavior; adopt only for measured benefit with equivalence tests.
- Measure DynamicIcon's live Lucide registry chunk before trying on-demand loading. Arbitrary operator-provided template icon names currently work; an allow-list changes that contract. No saving is claimed.
- Narrow runtime/build images only after checking supported architectures, CGO and required runtime libraries. Slimming follows actual executable requirements.

## Jira implementation mapping

Jira is the current work tracker. The authenticated review covered all 291 existing OSS issues and the board's 17 active items before adding this plan. [OSS-320](https://carbonpanel.atlassian.net/browse/OSS-320) is the new implementation epic. Its 23 new tasks are To Do and contain the source evidence, proposed change, risk/effort and acceptance. Existing [OSS-248](https://carbonpanel.atlassian.net/browse/OSS-248) was reopened from Done to To Do with the new Windows evidence; its original parent OSS-20 remains intact. Current states after this dated snapshot belong to Jira.

| Package | Jira issue | Scope |
| --- | --- | --- |
| W01 | [OSS-248](https://carbonpanel.atlassian.net/browse/OSS-248) | Eliminate recurring Windows test/development prompts |
| W02 | [OSS-321](https://carbonpanel.atlassian.net/browse/OSS-321) | Repair package builds and own development processes |
| W03 | [OSS-322](https://carbonpanel.atlassian.net/browse/OSS-322) | Repair Geyser image inputs and add build-only PR gates |
| W04 | [OSS-323](https://carbonpanel.atlassian.net/browse/OSS-323) | Make validation failures observable; repair lint portability |
| W05 | [OSS-324](https://carbonpanel.atlassian.net/browse/OSS-324) | Bind file-editor loads and saves to one session |
| W06 | [OSS-325](https://carbonpanel.atlassian.net/browse/OSS-325) | Repair process transport ownership and its test seam |
| W07 | [OSS-326](https://carbonpanel.atlassian.net/browse/OSS-326) | Enforce scheduled-start admission parity |
| W08 | [OSS-327](https://carbonpanel.atlassian.net/browse/OSS-327) | Apply backup exclusions before reading |
| W09 | [OSS-328](https://carbonpanel.atlassian.net/browse/OSS-328) | Execute module hooks on their assigned node |
| W10 | [OSS-329](https://carbonpanel.atlassian.net/browse/OSS-329) | Remove dead/scaffold code and exclusive dependencies |
| W11 | [OSS-330](https://carbonpanel.atlassian.net/browse/OSS-330) | Replace inherited ignore templates with tested policies |
| W12 | [OSS-331](https://carbonpanel.atlassian.net/browse/OSS-331) | Remove ephemeral records and clarify documentation ownership |
| W13 | [OSS-332](https://carbonpanel.atlassian.net/browse/OSS-332) | Pin toolchain policy and make generation atomic/reproducible |
| W14 | [OSS-333](https://carbonpanel.atlassian.net/browse/OSS-333) | Unify review tooling with explicit profiles and owned cleanup |
| W15 | [OSS-334](https://carbonpanel.atlassian.net/browse/OSS-334) | Share lifecycle admission and staged recreation policy |
| W16 | [OSS-335](https://carbonpanel.atlassian.net/browse/OSS-335) | Inject a cancellable remote-storage resolver |
| W17 | [OSS-336](https://carbonpanel.atlassian.net/browse/OSS-336) | Add bounded storage streaming and actual size-limit parity |
| W18 | [OSS-337](https://carbonpanel.atlassian.net/browse/OSS-337) | Reconcile product REST with the proto contract |
| W19 | [OSS-338](https://carbonpanel.atlassian.net/browse/OSS-338) | Consolidate content-provider HTTP policy |
| W20 | [OSS-339](https://carbonpanel.atlassian.net/browse/OSS-339) | Share frontend inventory, lifecycle and status workflows |
| W21 | [OSS-340](https://carbonpanel.atlassian.net/browse/OSS-340) | Give WebSocket connections generation-owned lifetime |
| W22 | [OSS-341](https://carbonpanel.atlassian.net/browse/OSS-341) | Extract module form codecs and cohesive studio workflows |
| W23 | [OSS-342](https://carbonpanel.atlassian.net/browse/OSS-342) | Make display preferences authoritative and migrate tokens by feature |
| W24 | [OSS-343](https://carbonpanel.atlassian.net/browse/OSS-343) | Give Monaco one lazy runtime owner |

Blocking dependencies follow the implementation sequence; coordination-only and historical relationships remain related links rather than artificial blockers. Related completed work retains its original scope. In particular, the internal storage changes coordinate with the still-open public-primitives release [OSS-288](https://carbonpanel.atlassian.net/browse/OSS-288), and frontend work relates to [OSS-318](https://carbonpanel.atlassian.net/browse/OSS-318); neither existing epic was repurposed.

The original completed architecture work is retained: this plan builds on its Locator, Transition, PlanFor and worldstore seams. The repeated Windows prompt is reopened because this review supplies new runtime evidence. Other source-confirmed refinements have distinct follow-up tasks rather than mass reopening historical issues. Cloud's equivalent Windows acceptance was not exercised by this OSS review.
