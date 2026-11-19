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

## Real blocker — guest boot NOT qualified (external)

The originating host has **no `/dev/kvm`** and **no LXC tooling**
(`lxc`, `lxc-create`, `lxc-copy`, `pct`, `systemd-nspawn`), and no privileged
access to provision one. The task-owned WSL 2 Ubuntu distribution likewise has
no `/dev/kvm` and no LXC tools; installing them would require elevation and
host/WSL configuration changes, which are out of scope. A real Ubuntu 24.04 LXC
rootfs therefore cannot be imported and booted in this lane. This is an
**external qualification blocker**, not a code defect and not a pass:

- No live Proxmox/LXC import or boot was performed.
- No nested Docker daemon was started inside a real guest.
- No effective kernel enforcement (user namespaces, cgroup delegation, seccomp,
  AppArmor, `/dev` nesting) was observed on a real guest.
- No gameplay compatibility or performance evidence exists.

`node_lxc_artifact.py boot-qualify` provides the fail-closed import/boot
verification path: it verifies real signed upstream archive bytes in an owned
disposable path (or consumes a staged verified archive) and then probes for an
unprivileged LXC-capable disposable runner. It records explicit prerequisite
blockers and writes a JSON report; it returns `status: ok` only when an actual
unprivileged import and boot are performed and evidenced. The manual workflow
`.github/workflows/lxc-boot-qualification.yml` runs this command on a disposable
runner and uploads the report as evidence (fail-closed on blocked/unqualified
runs). Real byte-level archive evidence remains in
[SIGNED-ARCHIVE-EVIDENCE.md](SIGNED-ARCHIVE-EVIDENCE.md). Live clean
import/boot evidence (epic milestone M5) remains outstanding until a capable
runner produces it.

## Not claimed

No live host provisioning, global host configuration change, image publication,
registry push, release, or Carbon Panel application release is part of this
lane. No merge is authorized by this qualification record.
