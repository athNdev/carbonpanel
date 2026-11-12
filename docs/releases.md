# Releases and versioning

Carbon Panel uses Semantic Versioning with a `v` prefix: `vMAJOR.MINOR.PATCH`.
The current 3.0 line is a prerelease series (`v3.0.0-beta.4`,
`v3.0.0-beta.5`, then further betas or release candidates). Publishing a beta
does not mark it as a stable release.

- **Patch:** compatible fixes and maintenance.
- **Minor:** compatible new features.
- **Major:** incompatible operator or API changes that require migration.
- **Prerelease:** append `-beta.N` or `-rc.N`; increment N for each published build.

A tag identifies one source commit. Published releases and their assets are not
overwritten. Correct a bad release with a new version. The tag is the release
version; `web/carbon-panel/package.json` is a private workspace package, not a
second release-version counter.

## Downloads

Download an archive from [GitHub Releases](https://github.com/athNdev/carbonpanel/releases).
The five targets are Linux amd64/arm64, macOS Intel/Apple Silicon, and Windows
amd64. For example:

```text
carbon-panel_v3.0.0-beta.5_linux_amd64.tar.gz
carbon-panel_v3.0.0-beta.5_linux_arm64.tar.gz
carbon-panel_v3.0.0-beta.5_darwin_amd64.tar.gz
carbon-panel_v3.0.0-beta.5_darwin_arm64.tar.gz
carbon-panel_v3.0.0-beta.5_windows_amd64.zip
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
shasum -a 256 carbon-panel_v3.0.0-beta.5_darwin_arm64.tar.gz
```

```powershell
# Windows: compare this hash with the matching line in checksums.txt.
Get-FileHash .\carbon-panel_v3.0.0-beta.5_windows_amd64.zip -Algorithm SHA256
```

Checksums detect changed downloads; they are not a code-signing certificate.
Back up the database and configuration before upgrading. Preserve worlds and
data directories, and use the example configuration as a reference rather than
overwriting the existing configuration. See the
[installation guide](https://carbonpanel.dev/docs/getting-started/docker-compose/).

## Container channels

The controller repository is `atharvnaphade/carbon-panel` on Docker Hub.

| Tag | Meaning |
| --- | --- |
| `3.0.0-beta.5` | The exact prerelease, built from its tested Linux downloads |
| `3.0.0`, `3.0` | An exact stable release and its moving minor-release channel |
| `latest` | Stable releases only under this release workflow |
| `edge`, `sha-…` | Development builds from CI-verified main commits |

Betas never move `latest` or the stable minor tag. Main builds move `edge`.
An older `latest` image may predate this channel policy; pin an explicit
version for upgrades. The image digest in the release manifest is the immutable
reference for that release. Module images have their own publishing workflow.

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

Alternatively, push an annotated SemVer tag at a main commit whose CI has
succeeded. A `v*` tag starts the same guarded publishing pipeline. The main CD
workflow does not also publish tags. Publication refuses invalid versions,
unverified commits, moved tags and already-published versions.

The frontend and API schema are generated once with pinned tools. Native
runners build each target and exercise both binaries' version commands and the
database with release settings. Archive ordering, ownership and timestamps are
normalized; the commit timestamp supplies `SOURCE_DATE_EPOCH` and the embedded
build date. Cross-OS archives are assembled into a complete set before any
GitHub release is public. The image contains the exact tested Linux binaries;
both architectures must be retrievable from Docker Hub before publication.

Only after all of those steps pass does the workflow create or resume a draft,
upload and verify its complete local asset set, and publish it with generated
pull-request notes. A failure before publication leaves no public partial
GitHub release. Retrying an unpublished draft is supported; published versions
require a new tag. GitHub release publication does not send a Discord message.
