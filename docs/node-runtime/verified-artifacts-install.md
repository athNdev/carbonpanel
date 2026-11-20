# Installing verified managed-node runtime artifacts

Managed-node artifacts use an independent version domain. The publication
workflow is currently in rehearsal. Its builders produce plans or fixtures,
and there are no qualified runtime downloads. The VM and unprivileged LXC
paths are previews. The [portable Linux installer](../node-installer.md)
describes the guest-side tooling and its verification status.

## Version and ownership

* Version domain: `runtime-vMAJOR.MINOR.PATCH` (reserved by
  [ADR 0033](../adr/0033-release-tag-and-publication-policy.md)). Never a `v*`
  application version and never a `mod-*` module version.
* Owner: the existing project repository `athNdev/carbonpanel` (GHCR namespace
  `ghcr.io/athndev/carbonpanel`). No new owner or credential is introduced.
* Publication boundary: an explicit `workflow_dispatch` with `publish=true`,
  on the trusted `refs/heads/main` ref, with a full 40-hex source SHA whose
  exact commit has green main CI, and an identity assertion bound to that
  version, commit, artifact and owner. Main must also contain a reviewed
  qualification record that matches the version, kind and source commit. Other
  dispatches are refused before the build and signing job.
* Publication target (when actually dispatched): a **draft** GitHub release
  under the runtime tag, with the tag pinned to the qualified source commit.
  The workflow refuses a pre-existing tag that resolves to a different commit.
  The workflow writes the draft and its GitHub release files.

## What gets published

For each artifact kind the workflow stages and, on explicit opt-in, attaches:

| File | Purpose |
| --- | --- |
| `<artifact>` | The QCOW2 image, LXC rootfs archive, or portable installer bundle |
| `SHA256SUMS` | Deterministic SHA-256 manifest (`sha256sum -c` compatible) |
| `signature.sig` + `signature.bundle` | cosign keyless signature and bundle for `SHA256SUMS` |
| `signature.json` | Bound signer identity and manifest digest (fail-closed precondition record) |
| `sbom.cdx.json` | Deterministic CycloneDX 1.5 software bill of materials |

The build entry points are allowlisted per kind (`scripts/build-runtime-qcow2.sh`,
`scripts/build-runtime-lxc.sh`, `scripts/build-runtime-installer.sh`) and are
owned by their respective artifact lanes. `scripts/runtime_artifacts.py`
generates the integrity records and checks them before any write.

## Deployment paths

| Mode | Status | Isolation |
| --- | --- | --- |
| [QCOW2 for Proxmox VE, KVM and libvirt](../../packaging/node-vm/README.md) | Preview | A dedicated VM has its own kernel, disk, network and node identity. |
| [Unprivileged LXC for Proxmox VE](../node-lxc-mode.md) | Preview | The container shares the host kernel and requires verified nested-runtime capabilities. |
| [Portable Linux installer](../node-installer.md) | Guest-side tooling for supported Linux guests, including when no qualified disk image is available | Inherits the isolation of the VM or host chosen by the operator. |

See the [format evidence table](home-hypervisor-formats.md) for direct disk
images and per-hypervisor coverage.

## Add qualification evidence before signing

The artifact lane tests the exact payload in its intended deployment. Record
the platform and version, first and second boot for VM or LXC, scoped
enrollment, heartbeat, captive daemon isolation and workload provisioning.
Include persistence and recovery results for the mode under test.

After review, add a row to
[`scripts/runtime_artifact_qualification.json`](../../scripts/runtime_artifact_qualification.json)
on main with `version`, `artifact`, `commit`, `manifest_sha256` and an HTTPS
`evidence` link to the qualification results. `manifest_sha256` is the digest
of the complete `SHA256SUMS` file for the tested bundle, including its payload
and provenance. The workflow compares the rebuilt manifest with this digest
before signing. A new version, source commit or changed manifest needs a
matching qualification record.

## Verify before use

Substitution example; replace the version and asset names with the exact ones
you downloaded.

```sh
VERSION=runtime-v0.1.0
ARTIFACT=installer

# 1. Confirm the tag is the independent runtime domain, not an app release.
python3 scripts/runtime_artifacts.py assert-no-app-release --version "$VERSION" --artifact "$ARTIFACT"

# 2. Check file integrity (run in the directory containing the assets).
sha256sum -c SHA256SUMS

# 3. Verify the keyless signature. The certificate identity is the GitHub
#    Actions WORKFLOW REF, not any operator label. For this repository it is
#    the exact value below (derived from trusted repo + trusted main ref):
CERT_IDENTITY="https://github.com/athndev/carbonpanel/.github/workflows/runtime-artifacts.yml@refs/heads/main"
OIDC_ISSUER="https://token.actions.githubusercontent.com"
cosign verify-blob \
  --bundle signature.bundle \
  --certificate-identity "$CERT_IDENTITY" \
  --certificate-oidc-issuer "$OIDC_ISSUER" \
  SHA256SUMS

#    Optional corroboration of the GitHub-specific OIDC claims:
#      --certificate-github-workflow-repository athNdev/carbonpanel
#      --certificate-github-workflow-ref refs/heads/main

# 4. Inspect the SBOM and confirm it names the same version and source commit.
python3 -m json.tool sbom.cdx.json | head -40
```

The identity string above is the one cosign documents for GitHub Actions
keyless signing (`https://github.com/ORG/REPO/.github/workflows/WORKFLOW@refs/heads/main`,
per the sigstore/cosign README) and matches the `workflow_ref` OIDC claim
described in GitHub's OpenID Connect reference. The `publisher` field in the
identity assertion is **operator metadata only**; it is stored in
`signature.json` as `publisher_label` and is deliberately **not** a certificate
identity. Do not pass `publisher_label` to `--certificate-identity`: it cannot
verify a genuine artifact. `signature.json`'s `signer_identity` field holds the
same canonical workflow-ref identity shown above and can be cross-checked, but
the value you pass to cosign should be the documented constant.

If any step fails, or the assets are not attached to a draft release under the
exact `runtime-v...` tag, **do not use them**. Cryptographic verification is
this consumer-side `cosign verify-blob` step; the Python checks and
`verify-bundle` are only offline integrity/binding preconditions, not a substitute
for it.

The publication lane also enforces a fail-closed publication predicate before
checksumming or signing: a staged bundle must carry a payload of the declared
artifact kind and a non-synthetic provenance record. Any offline rehearsal
marker (`BUILD-PREFLIGHT.json` with `live_build: false`, or a fixture-only plan
record, including re-encoded, nested or case-variant forms) refuses the bundle.
`verify-bundle` now inherits this predicate, so verifying a rehearsal bundle
fails too. Offline planning and `--preflight` fixtures remain reachable for
rehearsal, but their stamped output is refused by the publication lane.

A filename suffix or byte count is never a deployable-artifact claim. The
predicate qualifies the payload through its native verifier — QCOW2 header and
`qemu-img` format inspection, the real xz rootfs/layout inspector for LXC, or
the installer package manifest contract — and requires the provenance pin to
match the payload's exact SHA-256. Symlinks and non-regular files are refused.
If a native verifier is unavailable on the runner (for example no `qemu-img`),
publication stops and the lane remains REHEARSAL-ONLY. These are preconditions,
not a substitute for the cryptographic step above.

## Announcement

Publish an announcement after the exact downloads have passed fresh
installation and signature verification. Name only the modes and hypervisor
versions covered by their qualification evidence. Report preview paths
separately so readers can tell which deployment was tested.
