# Installing verified managed-node runtime artifacts

This page describes the artifacts this project publishes for managed Carbon
Panel nodes, how to verify them, and the honest limits of the evidence behind
them. It is intentionally separate from the [application release
process](../releases.md): runtime artifacts use an **independent version
domain** and are **not** a Carbon Panel application or module release.

> Status of this lane: the isolated publication workflow
> (`.github/workflows/runtime-artifacts.yml`) and its fail-closed boundary are
> implemented, but this implementation lane performs **no live registry push,
> GitHub release, or host provisioning**. No artifact has been built or
> published by this lane. The announcement near the end is a template that must
> not be published until the exact assets have been built and verified.

## Version and ownership

* Version domain: `runtime-vMAJOR.MINOR.PATCH` (reserved by
  [ADR 0033](../adr/0033-release-tag-and-publication-policy.md)). Never a `v*`
  application version and never a `mod-*` module version.
* Owner: the existing project repository `athNdev/carbonpanel` (GHCR namespace
  `ghcr.io/athndev/carbonpanel`). No new owner or credential is introduced.
* Publication boundary: an explicit `workflow_dispatch` with `publish=true`,
  on the trusted `refs/heads/main` ref, with a full 40-hex source SHA whose
  exact commit has green main CI, and an identity assertion bound to that
  version, commit, artifact and owner. All other events only stage and verify.
* Publication target (when actually dispatched): a **draft** GitHub release
  under the `runtime-v...` tag. No container registry push and no deployment is
  performed by this workflow.

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

## Supported paths (and the unsupported ones)

| Goal | Supported artifact | Notes |
| --- | --- | --- |
| Proxmox VE / KVM / libvirt VM | QCOW2 | First-class; see the QCOW2 artifact lane. |
| Proxmox VE unprivileged container | LXC rootfs | First-class; see the LXC artifact lane. |
| Any supported Linux guest, including Hyper-V, VirtualBox, VMware, bhyve | Portable Linux installer | **The supported path for unqualified formats.** |
| Direct VHDX / VDI / VMDK / OVA / OVF / bhyve image | None | Not supported: no real clean import/boot evidence. See [the format decision](home-hypervisor-formats.md). |

Enrollment is scoped and happens at first boot: no approved key, challenge
claim or other secret is baked into an image or its provisioning metadata. Each
instance gets a unique identity, following the cloud-init `instance-id`
first-boot model. Persistent node data (worlds, database, node identity) stays
on a separate volume that is imported/backed up and recovered by the owning
artifact lane.

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
checksumming or signing: a staged bundle must carry a real payload of the
declared artifact kind and non-synthetic provenance. Any offline rehearsal
marker (`BUILD-PREFLIGHT.json` with `live_build: false`, or a fixture-only plan
record) refuses the bundle. Offline planning and `--preflight` fixtures remain
reachable for rehearsal, but rehearsal output can never be published as a
qualified artifact.

## Accurate announcement (publish only after verification)

The following text is a template, not a claim of completed testing. Publish it
only after the exact assets above have been built, signed, and verified.

> Carbon Panel now publishes independently versioned **managed-node runtime
> artifacts** under `runtime-vMAJOR.MINOR.PATCH`. The first family is the
> portable Linux installer, with QCOW2 and unprivileged LXC rootfs artifacts
> maintained by their own lanes. Downloads are attached to draft releases in
> `athNdev/carbonpanel`, each with a SHA-256 manifest, a cosign keyless
> signature and a CycloneDX SBOM. These are not Carbon Panel application
> releases, and this work performs no registry push or deployment. Home
> hypervisors without a verified disk image (Hyper-V VHDX, VirtualBox, VMware,
> bhyve) use the portable guest installer as the supported path. Verification
> steps and the format evidence table are in `docs/node-runtime/`.

Do not state that a specific home-hypervisor image was imported or booted
unless that exact import/boot evidence exists; the evidence table records which
formats are proven and which are deliberately unsupported.
