# OSS-378 LXC qualification status

## What is qualified here

- **Real upstream pins.** `config/provenance.json` pins immutable, dated Ubuntu
  24.04 root tarballs with SHA-256 values taken from a GPG-signed `SHA256SUMS`
  and locally verified. See [RETRIEVAL-EVIDENCE.md](RETRIEVAL-EVIDENCE.md).
- **Signed archive bytes verified end to end.** Both per-architecture root
  tarballs and LXD metadata tarballs were actually downloaded in an owned
  disposable path, rehashed against the GPG-signed `SHA256SUMS` and the pins,
  and inspected for canonical layout/`os-release`/`metadata.yaml` version and
  architecture. See [SIGNED-ARCHIVE-EVIDENCE.md](SIGNED-ARCHIVE-EVIDENCE.md).
  This is byte-level artifact evidence, not boot evidence.
- **Offline artifact behavior.** The disposable fixture suite covers signed
  build/verify, checksum/signature/SBOM/secret refusals, the nested-runtime
  capability gate, first-boot identity with digest-only enrollment, volume
  import/backup/restore and two-slot upgrade/rollback. The synthetic pins used
  by these tests live **only** in `config/provenance.fixture.json`.
- **Fail-closed profiles.** Artifact mode refuses synthetic profiles,
  placeholder digests and moving (undated) URLs; fixture mode is explicitly
  synthetic and cannot be used for artifact claims.

## Disposable Proxmox readiness

Run the read-only readiness check from the repository root with a new,
caller-owned work directory:

```sh
python3 packaging/node-lxc/node_lxc_artifact.py boot-qualify \
  --arch amd64 --work-dir "$work_dir" --fetch
```

The command verifies and inspects the signed upstream root and LXD metadata
archives. It checks for Linux, Proxmox tools and cgroup v2 without invoking
guest management tools. The report records `BOOT_QUALIFICATION_BLOCKED` and
`attempted: false` even if those prerequisites are present. A runner probe
cannot establish fixture ownership or authorize guest operations. The manual
`.github/workflows/lxc-boot-qualification.yml` uploads the same JSON report.

The available WSL and hosted environments lack a designated disposable
Proxmox fixture. Milestone M5 requires a real unprivileged import, two boots,
effective confinement, shared enrollment and nested runtime evidence from
such a fixture. See [the mode documentation](../../../docs/node-lxc-mode.md#disposable-proxmox-qualification).
