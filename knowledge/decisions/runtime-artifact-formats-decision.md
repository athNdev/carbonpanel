---
type: Decision
title: "Runtime artifact format support — Decision"
description: Candidate artifact kinds and the evidence required before publication.
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: runtime-artifact-formats.md
---
# Decision

QCOW2, unprivileged LXC rootfs and the portable Linux installer are the
candidate artifact kinds. Additional disk formats require a clean import and
boot of the Carbon Panel artifact on the target hypervisor, with recorded
evidence. For other hypervisors, operators can use the portable guest tooling
on a supported Linux guest. LXC shares the host kernel and needs separate
nested-runtime evidence, even when the dedicated VM is qualified.

A vendor's documented support for a disk format is not import/boot evidence.
The publication gate refuses unproven formats; the format table and its primary
sources are authoritative in the docs.

The workflow resolves its build step through three allowlisted wrapper scripts
(`scripts/build-runtime-*.sh`) rather than pointing the allowlist directly at
the sibling lanes. The real entry points are heterogeneous: a Python offline
planner (`scripts/node_vm_build.py`), a bash `build` subcommand
(`packaging/node-lxc/build-rootfs.sh`) and a host-mutating systemd qualification
fixture (`scripts/qualify-node-installer-systemd.sh`). The workflow instead
uses one uniform `bash <script> --version --commit --output` contract; the
wrappers translate that contract and delegate to the real lanes, with the
installer delegating only to the fixture's non-mutating `--preflight` mode. Both
wrapper and delegate must exist on disk or the step fails closed; there is no
resolution outside `BUILD_SCRIPTS`/`BUILD_DELEGATES`. The offline fixture
`scripts/testdata/runtime-artifacts/build-delegates.fixture.json` and
`runtime_artifacts.py preflight-build` prove reachability by resolution only:
no QEMU, LXC, systemd, host provisioning or publication is executed. This lane
has built, signed and published no artifact; the announcement stays gated.

Because those wrappers produce offline plans/fixtures (`BUILD-PREFLIGHT.json`
with `live_build: false`), a fail-closed publication predicate
(`runtime_artifacts.py publication_predicate`) runs in the workflow before
checksumming, signing or any draft release, and is also inherited by
`verify_bundle`. It refuses any offline/fixture marker or fixture-only plan
record, so the rehearsal outputs stamped by these wrappers are refused before
checksumming, signing or publication.

