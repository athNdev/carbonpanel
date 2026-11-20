# Releases and versioning

Carbon Panel uses Semantic Versioning 2.0.0. A **product release tag** is
`carbonpanel-vMAJOR.MINOR.PATCH[-alpha.N|-beta.N|-rc.N]`. The displayed version
and the immutable Docker tag are `MAJOR.MINOR.PATCH` with the same optional
prerelease suffix. The Git tag, SemVer, image tag and archive filename are
separate fields; tooling never infers a release from an arbitrary `v*` tag or a
filename. Plain `v*`, `mod-v*`, `docs*` and inherited tags are preserved as
historical refs in their own domains (see
[release history](release-history.md)). `v1.0.40` is the only verified
historical stable; `v3.0.0-alpha.1` was a GHCR-only preview. Prereleases are for
early testing and never move a stable alias.

- **Patch:** compatible fixes and maintenance.
- **Minor:** compatible new features.
- **Major:** incompatible operator or API changes that require migration.
- **Prerelease:** append `-alpha.N`, `-beta.N` or `-rc.N`; increment N for each published build.

## UPGRADE NOTES (from v1.0.40)

**2.0.1 is a MAJOR release.** An unchanged v1.0.40 configuration does not
upgrade cleanly. The Connect/HTTP wire contract and database schema are additive
and compatible; the breaks are in configuration and operating behavior. Back up
the database (db + wal + shm), configuration, worlds and backups, and rehearse on
disposable copies, before upgrading. Full evidence and the rehearsal record are
in [release-history.md](release-history.md#upgrade-notes--read-before-upgrading-from-v1040).

A recurring, read-only rehearsal runs in CI (`upgrade-rehearsal.yml`): weekly,
on PRs that change migration/config/release surfaces and on manual dispatch. It
runs two fail-closed cases on seeded disposable DB/world copies: the explicit
`v1.0.40` legacy baseline (35 → 45 → 35 tables) and the current published
stable derived from the authoritative release record by the product version
contract, so the weekly check follows new stables without a hardcoded version.
Both use real `carbon-panel` binaries and assert integrity, additive table
growth, row/world preservation, forced-failure refusal and both restore paths.
Exact artifact versions and SHA-256 digests are emitted in the job's evidence
report. This check is not a release approval gate: it grants no write scope,
touches no live world, host, tag, release or registry, and does not replace the
OIDC/PROXY migration go/no-go steps above. See
[ADR 0044](adr/0044-disposable-upgrade-rehearsal-ci.md).

1. **B1 — OIDC role mapping.** v1.0.40's `role_mapping: {}` example granted raw
   IdP group names directly. Current main uses raw claims only behind
   `auth.oidc.allow_raw_claim_roles` (default `false`) and refuses privileged
   names even then; the default `absent_claim_policy: deny` revokes OIDC roles.
   Add an explicit `auth.oidc.role_mapping` entry for every retained role,
   including admin, and verify a test sign-in before opening access. Setting
   `allow_raw_claim_roles: true` cannot restore a privileged raw-claim role.
2. **B2 — Explicit admin linking.** There is no implicit first-user admin.
   Keep a local administrator, explicitly link each admin's OIDC subject, and
   verify that an unlinked identity cannot gain admin.
3. **B3 — PROXY protocol trust.** PROXY v2 headers are trusted only from
   `proxy.trusted_proxy_cidrs` (default `[]` trusts nobody). Add every load
   balancer source CIDR and confirm real client IPs are preserved after upgrade.

The 1.0 line stays at `1.0.40` as the current rollback path (not a permanent ban
on future 1.0.x maintenance). The next planned stable is
**`carbonpanel-v2.0.1` (MAJOR)**: the wire/Connect API and schema are additive,
but three intentional operator/upgrade changes break an unchanged v1.0.40
config, so a compatible MINOR would be dishonest. See
[UPGRADE NOTES](#upgrade-notes-from-v1040) before upgrading. Before changing a
version convention, evaluate the public API — the Connect-RPC contracts, the HTTP
compatibility surface, CLI flags, the configuration schema and documented
operator behavior — against SemVer. `1.0.0` already defines that API; existing
published versions and Go module endpoints are never renumbered, retargeted or
unpublished. A tag identifies one source commit. Published releases and their
assets are not overwritten. Correct a bad release with a new version.
`web/carbon-panel/package.json` is a private workspace package, not a second
release-version counter. The earlier `carbonpanel-v2.0.0` tag is occupied at
`69ddb46d596aea8ed117a3dba6a1f63521572664` and stays at that commit.

## Version domains and promotion

Each artifact family owns a separate version domain. A published version is
immutable: its Git tag, release assets and image manifest are never moved.
`scripts/release.py` is the single source of truth for these rules and verifies
them in `scripts/test_release.py`.

| Domain | Tag/naming | Immutable identity | Moving aliases |
| --- | --- | --- | --- |
| Panel release | `carbonpanel-vMAJOR.MINOR.PATCH` | Full SemVer, `release-manifest.json` and its `image_digest` | `MAJOR.MINOR`, `latest` |
| Panel prerelease | `carbonpanel-vMAJOR.MINOR.PATCH-<alpha\|beta\|rc>.N` | Full prerelease version | None |
| Plain Go version / inherited panel tag | `vMAJOR.MINOR.PATCH`, `origin`, `rm` | Existing Git/Go proxy history; never a new publication | None |
| Module image | `mod-vMAJOR.MINOR.PATCH` | `mod-` version tag | `latest` |
| Documentation version | `docsMAJOR.MINOR.PATCH` | Historical docs tag; no trigger | None |
| Managed node runtime (reserved) | `runtime-vMAJOR.MINOR.PATCH` | Full version and digest; no workflow yet | `MAJOR.MINOR` only on explicit promotion |
| Development build | no version tag | Per-commit `sha-<commit>` and OCI revision label | `edge`, `nightly` (Hub only) |

Prereleases keep their full immutable version and never move `latest` or a
stable minor tag. A stable release publishes its immutable full version first,
then its documented moving aliases. Tag names are derived in that promotion
order by `scripts/release.py image-tags`; the GitHub release is exposed only
after both registries serve a complete multi-architecture index.

An explicit product publication holds ONE global, non-cancelling
**workflow-level** concurrency group, `pages-deploy`, across the
**entire** flow — immutable image publish, GitHub release/`latest`, and moving
alias promotion. This is the SAME shared lock used by the CD channel transaction
and the docs-only publisher: every Pages write and every moving-alias write is
serialized through it, so a stable alias cannot move while a docs/CD writer holds
the Pages slot. The lock is not released between phases, so two versions cannot
interleave phases or strand a run after immutable publication. `cancel-in-progress:
false` protects the **running** run; with GitHub's default `queue: single` only
one pending run is retained, and a later request **replaces** that pending run —
this lane uses **single-operator dispatch**. Do not assume every queued publisher
waits: after a replacement, the operator must verify the resulting run before
retrying. Alias decisions are recomputed with live preflight inside the same
locked run. Build-only rehearsals, tag pushes, pull requests and ordinary main
CI get a distinct per-run key (`carbonpanel-stage-<run_id>`) and are not
serialized.

Moving aliases (`latest`, `MAJOR.MINOR`, module `latest`) are conveniences, not
reproducible references. Consumers that need reproducible rollbacks pin the full
version or the `sha256:` digest in `release-manifest.json`. Before dropping or
retargeting an established alias, announce a migration window and leave the old
alias in place for that window. Do not invent aliases or move them on a
speculative classification. The managed node-runtime naming is reserved for a
future artifact; no workflow or registry destination exists for it today.

## Downloads

Download an archive from [GitHub Releases](https://github.com/athNdev/carbonpanel/releases).
The five targets are Linux amd64/arm64, macOS Intel/Apple Silicon, and Windows
amd64. The current verified stable is `1.0.40` (Git tag `v1.0.40`); its fixed
archive set and digests are recorded in [release history](release-history.md).
A future, not-yet-published product release tagged `carbonpanel-v2.0.1` keeps
the same archive naming convention, derived from its SemVer field (`v2.0.1`
internally for archive names). For example:

```text
carbon-panel_v2.0.1_linux_amd64.tar.gz
carbon-panel_v2.0.1_linux_arm64.tar.gz
carbon-panel_v2.0.1_darwin_amd64.tar.gz
carbon-panel_v2.0.1_darwin_arm64.tar.gz
carbon-panel_v2.0.1_windows_amd64.zip
```

Every archive contains `carbon-panel`, `carbon-storage` (with `.exe` on
Windows), the embedded web interface, `config.example.yaml`, both license
files, a short installation guide and `BUILD-INFO.json`. The Go binaries use
the pure-Go SQLite driver and do not require a system SQLite library. Docker
is still required to run Minecraft servers. The archives are unsigned portable
programs, not installers.

The release also includes `openapi.yaml`, `release-manifest.json`, and
`checksums.txt`. The manifest records the source commit, archive hashes and
multi-architecture Docker image digest. Both command-line tools support
`--version` without starting the service or loading configuration.

Download `checksums.txt` beside the archive and verify before extracting. The
concrete commands below use the current stable 1.0.40 asset names — the files
you can actually download today. A future release uses the same pattern with
its own version:

```sh
# Linux; verifies whichever listed files you downloaded.
sha256sum --check --ignore-missing checksums.txt
# macOS: compare this output with the matching line in checksums.txt.
shasum -a 256 carbon-panel_v1.0.40_darwin_arm64.tar.gz
```

```powershell
# Windows: compare this hash with the matching line in checksums.txt.
Get-FileHash .\carbon-panel_v1.0.40_windows_amd64.zip -Algorithm SHA256
```

Checksums detect changed downloads; they are not a code-signing certificate.
Back up the database and configuration before upgrading. Preserve worlds and
data directories, and use the example configuration as a reference rather than
overwriting the existing configuration. See the
[installation guide](https://carbonpanel.dev/docs/getting-started/docker-compose/).

## Container channels

Release images are published to both `atharvnaphade/carbonpanel` on Docker Hub
and `ghcr.io/athndev/carbonpanel` in GitHub Packages. Both references use the
same multi-architecture image digest and tested Linux downloads. The manifest
records both references. Development `edge` images remain on Docker Hub; the
OSS-413 `nightly` channel is published to BOTH registries at one verified digest
(see below). No redundant rolling image is added: the existing `nightly`,
`sha-<commit>`, `edge` and release aliases already cover the rolling and pinned
roles.

The Docker Hub repository name and the GHCR name are normalized in one place:
`atharvnaphade/carbonpanel` and the lowercased GitHub owner/name,
`ghcr.io/athndev/carbonpanel`. The tool derives GHCR from `GITHUB_REPOSITORY`;
the GitHub owner spelling is never treated as a Docker Hub namespace. No
credential value appears in workflows, docs or fixtures. Docker Hub uses a
personal access token in repository secrets; GHCR uses the workflow's
short-lived `GITHUB_TOKEN` with package-write permission.

| Tag | Meaning |
| --- | --- |
| `1.0.40` | The verified historical stable release, immutable; `1.0` is its current rollback alias |
| `2.0.1-alpha.1` | An exact future prerelease; never moves a stable alias |
| `2.0.1` | An exact stable release, immutable |
| `2.0` | Moving minor channel, promoted only with a stable `2.0.x` release |
| `latest` | Moving stable channel, promoted only with a stable release |
| `edge`, `sha-…` | Docker Hub development builds from CI-verified main commits; not versions |
| `nightly` | Development channel promoted on every successful trusted main CI on BOTH registries at one verified multiarch digest; never a GitHub Release or a stable version |
| GHCR `3.0.0-alpha.1` | Historical GHCR-only preview; not a stable release and not on Docker Hub |

Prereleases never move `latest` or the stable minor tag. `edge` moves only under
an explicit developer-channel opt-in; an ordinary main merge builds and verifies
the immutable `sha-*` image, and the OSS-413 publisher advances `nightly` on
both registries only after the full qualified transaction below. An older
`latest` image may predate this channel
policy; pin an explicit version for upgrades. The image digest in the release
manifest is the immutable reference for that release. Module images have their
own publishing workflow.

Readable immutable per-build aliases (`build-YYYY-MM-DD-HHMMSS`, with `-N` only
for a publication-second collision) are documented separately in
[Docker readable build aliases](docker-readable-aliases.md). The UTC timestamp is
the Docker Hub tag-push/publication time, not a verified image build time; no
commit or SHA fragment appears in the alias. Full commit identity stays in the
checked-in mapping and the retained original `sha-<40hex>` ref. These aliases are
additions at existing digests and never move `latest`, a minor tag, `edge`,
`nightly` or a release tag.

## Nightly development channel

`nightly` is a development alias, not a release version or a GitHub release. It
points to the most recent **qualifying** nightly build of a pinned main commit.
The immutable identity is the existing `sha-<full-commit>` tag; no date alias or
second digest is created for a nightly. A nightly never moves `latest`, a stable
minor tag, or `edge`; those remain owned by their own explicit gates. Following
the OSS-413 correction, `nightly` is published on BOTH Docker Hub and GHCR at
the SAME digest and OCI revision; the earlier Hub-only wording is superseded.

Both `edge` and `nightly` writers share the `sha-<commit>` tag. Before any build
or push, the workflow reads that tag with strict lookup classification: only a
definitive "manifest unknown" for the queried reference (including the real
Buildx `ERROR: docker.io/<repo>:<tag>: not found` shape) is an absent tag;
unauthorized, network, transient and generic 404-style failures are **unknown**
and refuse. An absent tag may be
built, but supplying an expected digest while the tag is missing also refuses.

A present tag is reused only with **trusted pinned build evidence**, not with a
bare expected digest (which is merely a second copy of the observed digest). The
evidence requires: a successful completed push CI run for the exact pinned
commit; the observed index digest matching the digest recorded by that evidence;
both `linux/amd64` and `linux/arm64` platform child manifests; and the
`org.opencontainers.image.revision` label in **each child image config** equal to
the pinned commit. Every evidence fetch is anchored to the digest observed at
the immutable reference: the index is read from `repo@<observed-digest>` and each
config from `repo@<child-digest>`, never by re-reading the mutable tag. The
reported index digest must equal the observed digest or the gate refuses with
zero writes. The child configs are fetched by their exact platform digests from
the index; descriptor annotations are never evidence. A read-only
probe of the public `edge` index confirmed the descriptors carry a null revision
while both digest-addressed configs carry the full commit label. Any mismatch,
missing label, untrusted CI or unknown lookup refuses with zero buildx pushes and
zero alias moves. Moving `nightly` (or `edge`) over a different digest binds the
previous commit to the **observed** alias digest through that same config-based
sha evidence before the ancestry check; an operator-supplied commit string alone
is never trusted. Repeating a promotion at the same digest is idempotent.

Buildx writes only the `sha-<commit>` reference — there is no implicit early
`edge` or `nightly` move. After the immutable image is verified, `edge` is moved
by an explicitly gated, digest-based step (so it survives a sha reuse in either
publication order), and `nightly` by its own gated step. All publication uses one
fixed, non-cancellable channel concurrency group; verification builds stay
per-SHA and cancellable.

Two publication paths exist, and both require exact-commit CI. The manual path
is an explicit `workflow_dispatch` with `nightly=true` from `refs/heads/main`.
The unattended path is a native Actions `schedule` (`cd.yml`, 18:00 UTC daily,
ahead of the 06:30 Australia/Sydney local updater). A schedule **verifies by
default**: it resolves the CURRENT `main` tip read-only twice (a moving or
unavailable tip pins nothing), checks out that exact SHA, requires a completed
successful push CI run for that SHA, then re-reads the live tip and re-verifies
exact-commit CI again immediately before the buildx push, and a THIRD time
(fresh result, not the pre-push one) immediately before moving the alias. A
tip that moved after pinning, or a CI run that stopped being green between the
push and the alias move, blocks the affected write; verification-only runs
still build locally without pushing. It may
move only the `nightly` alias, reusing the verified
`sha-<commit>` image; a present-but-unverified or unknown sha tag is skipped
with zero writes. The unattended path alone derives the previous alias binding
from the alias's own digest-addressed platform configs rather than an operator
string; manual dispatch keeps its original explicit-binding rule and still
refuses an absent binding. Both paths refuse an unbound, non-ancestor or
same-commit-digest move.

A scheduled run writes to the registry only when the separately reviewed,
**default-off** repository variable `NIGHTLY_SCHEDULE_PUBLISH` is exactly
`true`. Leaving it unset or `false` keeps every schedule verification-only;
this repository does not set or mutate that variable. Push, pull_request,
workflow_run, tag and dispatch events remain verify-only through the schedule
gate even when the variable is enabled. The schedule joins the same fixed,
non-cancellable `cd-publish-edge` concurrency group as manual publishes, so
only one writer can touch the shared sha identity and aliases at a time.

Nightly never writes `latest`, a stable minor tag, `edge`, a GitHub release, a
release record or any MCP/runtime/module image; those lanes have no schedule
trigger. Its GHCR write is the verified dual-registry alias move described
under the OSS-413 gates below, not a separate release lane. Seeding a live
`nightly` tag is a distinct, separately authorized operation; the existing
`edge` image predates current main and is never relabelled as a fresh nightly.

## OSS-413 verified channel promotion

Every docs and OCI channel move is gated on an EXACT source revision pinned
read-only from trusted `refs/heads/main` (full 40-hex commit) plus a successful
exact-commit main CI result. OCI moves additionally require the immutable
`sha-<commit>` image at ONE verified multiarch digest with both Linux
architectures in BOTH registries (`atharvnaphade/carbonpanel` on Docker Hub and
`ghcr.io/athndev/carbonpanel`), with matching `org.opencontainers.image.revision`
in each digest-addressed child config. Docs moves additionally require the built
site's schema_version 3 two-channel contract (`Stable` and `Nightly` only, no
`1.0.39`/`3.0.0-alpha.1` doc snapshots, full-SHA `/commit/<sha>` links) and
exact site build validation, so stale docs cannot mask a candidate. A tag ref,
short SHA, unknown/single-arch/mismatched registry or stale contract fails
closed with zero writes.

A channel move is ONE qualified transaction, not an alias write: pin the exact
source revision and qualified stage, reject older/out-of-order candidates
against the active receipt, stage BOTH immutable registries and the exact docs
artifact, deploy a candidate docs build and confirm its PUBLIC marker over HTTPS
before moving aliases, then deploy the final docs and confirm both registry
aliases and the live marker/dropdown agree on the same revision and digest. A
full receipt records the revision, both digests, before/after alias state, docs
artifact hash, public marker URLs/status, run IDs and timestamps. Retry uses the
exact staged digest, never a rebuild or blind alias move; partial failure
reconciles or rolls back to the prior verified receipt.

`nightly` advances on each successful main on both registries at one digest and
produces NO GitHub Release. No redundant `rolling` channel is added — the user-
facing `nightly` alias and the existing supported release aliases are sufficient.
Nightly revision belongs in the published artifact/marker and the receipt only;
no mutable Nightly state is committed to main, which would self-trigger CI.
Canonical `carbonpanel-v*` builds stay immutable. A reconciliation run re-verifies
and repairs drift. `latest` and the `stable` alias move only from the verified
stable digest observed in both registries and bound to the public docs revision.

**Selecting the next Stable.** The classified catalog records the selected
`next_stable` identity, its minor alias and its MAJOR migration boundary.
The occupied product tag `carbonpanel-v2.0.0` stays at its recorded Git
commit. The inherited plain tags `v2.0.0` and `v2.0.1` retain their
historical provenance. For `carbonpanel-v2.0.1`, the workflow renders the
canonical and versioned guides and checks both OpenAPI documents against
2.0.1. It also checks the Nightly schema against the verified Nightly commit.
The private download draft is complete before moving channel aliases.
Rerunning failed jobs uses the original staged OCI archive and checks every
existing immutable reference before copying to an absent registry.

No legacy alias-write step remains in any workflow: Nightly and Stable aliases
move only inside the qualified transaction. A stable release binds its exact
target version, both registry digests and the deployed public docs marker
together before GitHub release creation. Rollback runs under `always()` with
explicit outcome checks, restoring both aliases and docs to the prior verified
receipt; retry resumes that exact digest. Offline fixtures inject a second-
registry write failure and a final-probe failure to prove the invariant.

Rollback uses the separately verified prior receipt captured before any write, never the candidate receipt, and redeploys the exact prior docs bytes from a distinct Pages artifact (byte hash checked). The stable gate consumes observed dual-registry digests and compensates a failed alias write; older release targets without the trusted gate script are refused explicitly.

The verified current Docker Hub nightly baseline (`sha256:ecc89ba9…`, commit
`d8597d4e…`) is a fact about today's registry, not a mandatory first revision.
It seeds nightly only through a bounded, recorded bootstrap that re-verifies
dual-registry parity and a matching docs marker; no shipped workflow supplies
`--bootstrap`. All channel and docs moves share ONE fixed non-cancelling
publication lock (`pages-deploy`, `cancel-in-progress: false`); no job nests a
second group and no workflow dispatches another publisher that takes the same
lock. A manual/site-only `landing.yml deploy=true` dispatch may publish ONLY an
already-active exact channel source from the receipt, never originate a new
promotion. Each move is digest-anchored and bound to the previously verified
digest and commit: an
ancestor check refuses stale/out-of-order moves, an alias already at the target
is an idempotent no-op, a partial move converges, and a candidate that loses
qualification rolls back to the previous verified digest. No immutable tag is
overwritten and no tag is deleted. See ADR 0052.

The trigger/tag-name patterns behind these channels follow the dated primary-
source research note `container-image-stable-rolling-channels-2026-11-20.md`:
Docker branch/tag/schedule examples and tag/label patterns, GitHub's
release-published and release-branch examples, and Grafana's moving `:main` tag
refreshed after each successful main build alongside immutable per-build tags.
`latest` is the omitted-tag default and a documentation convention, not an
implicit stability contract. The corrected Docker tags/labels guide URL is
`https://docs.docker.com/build/ci/github-actions/manage-tags-labels/`.

The GitHub container package links to the source repository, so it appears in
the repository's Packages sidebar. Verify visibility after first publication;
if the package is private, an owner can make it public in Package settings.
Later versions retain the package's visibility. This does not change repository
permissions or require a saved package token: publishing uses the workflow's
short-lived `GITHUB_TOKEN` with package-write permission.

GitHub omits prereleases from the repository homepage's latest-release card.
Alpha downloads are available on the Releases page and through the README's
Releases link; they remain correctly marked as prereleases.

## Maintainer workflow

1. Update the changelog and upgrade documentation for shipped changes. Merge the
   intended commit and wait for **Continuous Integration** on main to succeed.
2. Open **Actions → Carbon Panel Release → Run workflow**. Enter the canonical
   product tag (for example `carbonpanel-v2.0.1`) and the exact commit (or
   `main`). Leave **publish** off for a complete build and verification
   rehearsal. The rehearsal creates no tag or release and pushes no image; its
   downloads are retained as workflow artifacts.
3. Inspect all five native archive jobs, the image assembly job and the
   `verified-release-downloads` and `verified-release-oci` artifacts. For a
   Stable release, finish operator qualification with these retained bytes.
   Open `stage.json` in the `qualified-stable-stage` artifact. Set **staged_run**
   to its run ID and **expected_digest** to its digest. Dispatch the workflow
   on main with the same product tag and exact commit and enable **publish**.
   Stable uses those original archives and OCI index. Publication also requires a trusted `refs/heads/main` dispatch ref and exact-commit main CI,
   verified read-only by `verify-publication-ref`.

Pushing a `carbonpanel-v*` tag — like a branch push, a pull request, a CI run, a
plain `v*` tag, a documentation tag or a rehearsal tag — only stages, builds and
verifies. It never writes to a registry or a GitHub release. `mod-*` tags belong
to the separate module workflow. The publication decision is made by
`publication_allowed()` in `scripts/release.py` and requires an explicit
`workflow_dispatch` with **publish** on and a canonical product tag; inherited,
source-only, preview, name-collision and unclassified tags are rejected. Before
a requested publication, a fail-closed preflight confirms that Docker Hub and
GHCR credentials are present; staging and rehearsals neither need nor read them.

Publication refuses invalid versions, verification/test namespaces, unverified
or stale commits, moved tags, already-published versions and a second,
conflicting image digest for the same version. Module images use the same
boundary: a `mod-*` tag push builds without pushing, and publication requires an
explicit dispatch with the module version. Main CD keeps ownership of `edge`
and `sha-<commit>` development images.

The frontend and API schema are generated once with pinned tools. The checked-in
Go and TypeScript RPC sources are the same sources verified by main CI; schema
generation writes separately rather than replacing those licensed source files. Native
runners build each target and exercise both binaries' version commands and the
database with release settings. Archive ordering, ownership and timestamps are
normalized; the commit timestamp supplies `SOURCE_DATE_EPOCH` and the embedded
build date. Cross-OS archives are assembled into a complete set before any
GitHub release is public. The image contains the exact tested Linux binaries;
both architectures must be retrievable from both registries before publication.

Only after all of those steps pass does the workflow create or resume a draft,
upload and verify its complete local asset set, and publish it with generated
pull-request notes. A failure before publication leaves no public partial
GitHub release. Retrying an unpublished draft is supported; published versions
require a new tag. GitHub release publication does not send a Discord message.

## Scope of staged-tag safety and pending channel opt-in

Staging a tag guarantees no registry or release write, but it says nothing about
how current the target is. A `carbonpanel-v*`, `v*` or `mod-*` tag can point at
**old commits**; a tag-triggered workflow checks out that tag's target and
reflects that commit's history and configuration. A green staging run is not
proof that current `main` was verified, and the dispatch `revision` input is a
convenience, never the trusted publish gate. The authoritative gate is
`publication_allowed()` plus `verify-publication-ref` (trusted main ref) together
with a successful `ci.yml` run for the exact published commit.

Main `edge` publication and website/Pages effects belong to `cd.yml` and the
site workflows. An ordinary main merge now only builds and verifies: Docker Hub
`edge`/`sha-*` publication requires `workflow_dispatch` with `push=true`, and
Pages deployment requires `workflow_dispatch` with `deploy=true`. Both gates
also require a real successful push CI run for the exact dispatch commit (verified
read-only by `verify-main-ci`, including ancestry of `main`) and a trusted
`refs/heads/main` dispatch ref. An operator checkbox is never treated as CI
evidence; tags, feature refs, blank refs, PRs and dry runs are refused regardless
of any input. The CD job pins the checkout to the verified event SHA (or the CI
run's head SHA for `workflow_run`), fetches full history for ancestry, asserts the
actual HEAD matches, and derives tags and provenance from that same SHA.
Verification uses per-SHA cancellable concurrency groups, while opted-in
publication uses a fixed channel-wide, non-cancelling group (`cd-publish-edge`
and `pages-deploy`) with no SHA. Different commits therefore serialize on the
moving `:edge`/Pages destinations, and routine builds cannot cancel an
in-flight publication. The workflows are not disabled — they still build and
validate on their normal triggers.

IndexNow notification is deliberately deferred under that model:
`indexable-changes` derives URLs only from a push diff, while deployment now
occurs only on a dispatch, so a dispatch deploy produces no URLs and sends no
notification. This is a documented deferral, not a failed notification; a dry-run
fixture covers it. These scoped gates still need release-auditor review before
held product merges are unblocked; until that review, product merges stay held.

## Historical tags and migration

All 84 inherited tags are classified with first-party evidence in
[release history](release-history.md) and [release-history.json](release-history.json).
The product prefix and that catalog are the only channel discovery mechanism;
`upstream-identical`, `upstream-name-collision`, `carbonpanel-published-stable`,
`carbonpanel-source-only`, `carbonpanel-legacy-preview`, `historical-component`
and `unknown` are separate from the tag's version domain and provenance. The
1.0 rollback line stays at `1.0.40`. The planned `carbonpanel-v1.0.40` alias
points at the original peeled commit without a duplicate GitHub release; the
GHCR-only `3.0.0-alpha.1` preview and the `v3.0.0-beta.4` cross-repository name
collision are recorded, not republished. No registry alias, ref or release is
changed by the policy PR.

## Recover an interrupted publication

Open the original Carbon Panel Release run. Re-run failed jobs in that run
when GitHub retains its successful build jobs and artifacts. This preserves
the tested native archives and OCI index. The final job checks both registries,
the public docs and each download before reporting success.

If Stable channel changes started and the original run ended before the draft
became public, dispatch **Recover interrupted Stable publication** on `main`.
Set `release_run` to the original numeric run ID. The workflow uses the
transaction journal and both Pages archives retained for 30 days. It checks
the current draft, previous public release, exact main CI, both immutable OCI
indexes and the active Pages deployment. It recovers each owned alias and
re-deploys the retained prior site as needed, then verifies the public bytes.
A conflicting release, deployment or alias requires investigation. If the
release is already public, retry its failed verification from the original run.

The immutable product tag, image and exact draft downloads remain available
after recovery. Use the verified digest and original artifacts for subsequent
qualification and promotion.
