# Releases and versioning

Carbon Panel uses Semantic Versioning 2.0.0 with a `v` prefix on Git tags:
`vMAJOR.MINOR.PATCH`. Current builds are alpha prereleases, starting with
`v3.0.0-alpha.1`. Earlier beta tags are retained as historical versions. Alpha
releases are for early testing and are not promoted to stable releases for
visibility.

- **Patch:** compatible fixes and maintenance.
- **Minor:** compatible new features.
- **Major:** incompatible operator or API changes that require migration.
- **Prerelease:** append `-alpha.N`, `-beta.N` or `-rc.N`; increment N for each published build.

Before changing a version convention, evaluate the public API — the Connect-RPC
contracts, the HTTP compatibility surface, CLI flags, the configuration schema
and documented operator behavior — against SemVer. `1.0.0` already defines that
API; existing published versions and Go module endpoints are never renumbered,
retargeted or unpublished. A tag identifies one source commit. Published
releases and their assets are not overwritten. Correct a bad release with a new
version. The tag is the release version; `web/carbon-panel/package.json` is a
private workspace package, not a second release-version counter.

## Version domains and promotion

Each artifact family owns a separate version domain. A published version is
immutable: its Git tag, release assets and image manifest are never moved.
`scripts/release.py` is the single source of truth for these rules and verifies
them in `scripts/test_release.py`.

| Domain | Tag/naming | Immutable identity | Moving aliases |
| --- | --- | --- | --- |
| Panel release | `vMAJOR.MINOR.PATCH` | Full version, `release-manifest.json` and its `image_digest` | `MAJOR.MINOR`, `latest` |
| Panel prerelease | `vMAJOR.MINOR.PATCH-<id>.N` | Full prerelease version | None |
| Module image | `mod-vMAJOR.MINOR.PATCH` | `mod-` version tag | `latest` |
| Managed node runtime (reserved) | `runtime-vMAJOR.MINOR.PATCH` | Full version and digest; no workflow yet | `MAJOR.MINOR` only on explicit promotion |
| Development build | no version tag | Per-commit `sha-<commit>` and OCI revision label | `edge` |

Prereleases keep their full immutable version and never move `latest` or a
stable minor tag. A stable release publishes its immutable full version first,
then its documented moving aliases. Tag names are derived in that promotion
order by `scripts/release.py image-tags`; the GitHub release is exposed only
after both registries serve a complete multi-architecture index.

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
amd64. For example:

```text
carbon-panel_v3.0.0-alpha.1_linux_amd64.tar.gz
carbon-panel_v3.0.0-alpha.1_linux_arm64.tar.gz
carbon-panel_v3.0.0-alpha.1_darwin_amd64.tar.gz
carbon-panel_v3.0.0-alpha.1_darwin_arm64.tar.gz
carbon-panel_v3.0.0-alpha.1_windows_amd64.zip
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

Download `checksums.txt` beside the archive and verify before extracting:

```sh
# Linux; verifies whichever listed files you downloaded.
sha256sum --check --ignore-missing checksums.txt
# macOS: compare this output with the matching line in checksums.txt.
shasum -a 256 carbon-panel_v3.0.0-alpha.1_darwin_arm64.tar.gz
```

```powershell
# Windows: compare this hash with the matching line in checksums.txt.
Get-FileHash .\carbon-panel_v3.0.0-alpha.1_windows_amd64.zip -Algorithm SHA256
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
records both references. Development `edge` images remain on Docker Hub.

The Docker Hub repository name and the GHCR name are normalized in one place:
`atharvnaphade/carbonpanel` and the lowercased GitHub owner/name,
`ghcr.io/athndev/carbonpanel`. The tool derives GHCR from `GITHUB_REPOSITORY`;
the GitHub owner spelling is never treated as a Docker Hub namespace. No
credential value appears in workflows, docs or fixtures. Docker Hub uses a
personal access token in repository secrets; GHCR uses the workflow's
short-lived `GITHUB_TOKEN` with package-write permission.

| Tag | Meaning |
| --- | --- |
| `3.0.0-alpha.1` | The exact prerelease; never moves a stable alias |
| `3.0.0` | The exact stable release, immutable |
| `3.0` | Moving minor channel, promoted only with a stable `3.0.x` release |
| `latest` | Moving stable channel, promoted only with a stable release |
| `edge`, `sha-…` | Development builds from CI-verified main commits; not versions |

Prereleases never move `latest` or the stable minor tag. Main builds move `edge`.
An older `latest` image may predate this channel policy; pin an explicit
version for upgrades. The image digest in the release manifest is the immutable
reference for that release. Module images have their own publishing workflow.

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
2. Open **Actions → Carbon Panel Release → Run workflow**. Enter the next
   version and the exact commit (or `main`). Leave **publish** off for a complete
   build and verification rehearsal. The rehearsal creates no tag or release
   and pushes no image; its downloads are retained as workflow artifacts.
3. Inspect all five native archive jobs, the image assembly job and the
   `verified-release-downloads` artifact. Run the workflow again with the same
   version and commit, enabling **publish** to publish it.

Pushing a `v*` tag — like a branch push, a pull request, a CI run, a documentation
tag or a rehearsal tag — only stages, builds and verifies. It never writes to a
registry or a GitHub release. The publication decision is made by
`publication_allowed()` in `scripts/release.py` and requires an explicit
`workflow_dispatch` with **publish** on. Before a requested publication, a
fail-closed preflight confirms that Docker Hub and GHCR credentials are present;
staging and rehearsals neither need nor read them.

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
how current the target is. A `v*` or `mod-*` tag can point at **old commits**; a
tag-triggered workflow checks out that tag's target and reflects that commit's
history and configuration. A green staging run is not proof that current `main`
was verified, and the dispatch `revision` input is a convenience, never the
trusted publish gate. The authoritative gate is `publication_allowed()` together
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

## Recover a staged release

If only the final publication job fails, keep the version and its built images.
Do not move the tag or rebuild a different image under that version.

1. Confirm that all five native archive jobs and the matching-image job succeeded
   in the original release run. Start with current `main` tooling if the failure
   required a publisher fix; re-running an old job uses its old tooling.
2. Download that run's `verified-release-downloads` artifact. Read the version
   and source commit from its `release-manifest.json`. Fetch the existing release
   tag into the local checkout without overwriting any other tag.
3. Run `python3 scripts/release.py publish --version VERSION --commit COMMIT --output DIRECTORY`,
   replacing the three values with that artifact's metadata and download directory.

The publisher verifies exact-commit CI, tag identity, the complete download set,
local checksums and GitHub's uploaded asset digests before exposing the release.
It resolves drafts by their stable release ID; GitHub's by-tag REST endpoint
does not return unpublished drafts. Existing public releases remain immutable
under the tool's policy and require a new version for further changes.

A **resume** is stricter than an identical tag digest. The operator must supply
the verified `sha256:` image digest, and the exact Git tag must resolve to the
intended source commit. The tool then reads the existing release's
`release-manifest.json` asset and requires its version, source commit, revision
and image digest to match the requested release. Equal registry digests and
platform counts alone are never sufficient; unknown or mismatched provenance
refuses before any alias moves.
