# Controller CT artifact (OSS-415)

Traditional **amd64 unprivileged Proxmox system CT** rootfs for the Carbon
Panel **controller**. It is not a managed-node LXC, not an OCI application CT,
not a live host provisioning tool, and not a Carbon Panel product release.

## What is shipped

`carbonpanel-controller-ct.tar.zst` is a Debian 13 (`trixie`) minbase rootfs:
signed `debootstrap --force-check-gpg`, Docker Engine/Compose v2 from the signed
Docker apt repository, the first-party controller installer, and a first-boot
systemd unit. Upstream `/usr/share/doc/*/copyright` metadata is retained and a
checksum-bound package/receipt is embedded under
`/usr/share/carbon-panel/`. No database, world, secret, SSH host key or machine
ID is baked in. The controller image is pinned by immutable digest from the
canonical Stable contract (`docs/version-index.json`).

## Import contract (run on a Proxmox host you control)

```
CT_STORAGE=local-lvm   # storage holding the CT root disk and template cache
pct create <VMID> "${CT_STORAGE}:vztmpl/carbonpanel-controller-ct.tar.zst" \
  --arch amd64 --ostype debian --unprivileged 1 --features nesting=1,keyctl=1 \
  --net0 name=eth0,bridge=<bridge>,ip=dhcp --memory 2048 --cores 2
pct start <VMID>
```

- Keep the default AppArmor profile. Do **not** set `privileged=1`,
  `unrestricted`, or a host Docker socket mount. No `/dev/kvm` or device
  passthrough is required. `nesting=1,keyctl=1` are the minimal Proxmox
  features for unprivileged nested Docker; no host security setting is changed.
- `carbon-panel-ct-firstboot.service` runs once after `network-online.target`
  and `docker.service`, retries on failure with a 30 s backoff and a 900 s
  bound, and delegates all existing-state decisions to the bundled installer.
  A failed pull leaves resume state and is never reported as success; an
  operator's existing compose/data is never replaced.
- Nested Docker and controller health run inside the guest only. No live
  enrollment, public port, registry push or host edit occurs in this lane.

## Build / verification / publication

PR and branch qualification runs build the real rootfs, archive it with GNU tar
(`--numeric-owner --acls --xattrs`, standard zstd frames), verify root shape
(`./etc`, `./usr`, `./sbin`), ownership/modes/links, the enabled first-boot
`multi-user.target.wants` symlink, guest OS/init/Docker evidence, pin agreement
and first-boot success/refusal/idempotence, then upload the exact verified
artifact.

Publication is a separate `controller-ct-v*` release from exact `main` only,
gated on the live main API SHA, successful exact-commit main CI, `publish=true`,
and re-verification of the same-run artifact digest with `--require-real`. The
write job first creates a **draft** with the archive, its `.sha256` and the
package receipt, then checks each attached asset's exact name and GitHub digest;
only after that verification does it publish the release as a **prerelease**
with `latest=false`. Before coordinator approval the release may exist only as
a draft; no public prerelease is created automatically. It never updates
product releases, aliases or images.

## Receipt

Each preview release attaches `package-receipt.json` alongside the archive and
its `.sha256`. Read what actually shipped with the exact digest-bound asset:

```
gh release download controller-ct-vX.Y.Z -p package-receipt.json
python3 -m json.tool package-receipt.json
```

It records the signed base `InRelease` digest, Docker upstream key fingerprint,
exact package names, versions and per-file SHA-256 values.

`pct` import/boot qualification is **not run** in this repository lane: no safe
suitable Proxmox runner is available. Archive/fixture gates are evidence of
artifact shape and first-boot contract only, not of a live Proxmox import.
