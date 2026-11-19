---
type: Decision
title: Image digest provenance
description: Resolved registry digests are recorded for every image; strict pinning is an opt-in runtime preflight, never a default.
status: stable
owners: [oss-runtime]
code_refs:
  - pkg/runtimeprofile/profile.go
  - pkg/runtimeprofile/contract.go
  - internal/docker/image_ensure.go
  - internal/docker/runtime_profile.go
  - internal/docker/client.go
  - internal/config/config.go
  - cmd/carbon-panel/main.go
  - config.example.yaml
sources:
  - title: ADR 0046
    resource: ../../docs/adr/0046-image-digest-provenance.md
  - title: Container privileges
    resource: ../invariants/container-privileges.md
---

# Choice

`ensureImageWithDigest` records the registry repository digest from the same
inspect that resolves the image, and `ServerImageIdentity` exposes it alongside
the daemon image ID. A pinned reference is reported as `ImagePinned` in the
shared runtime profile. `docker.require_pinned_digests` (default `false`) is the
only thing that makes an unpinned Minecraft image fatal: the shared
`pkg/runtimeprofile` preflight raises `image_unpinned_strict` in that mode and
keeps the ordinary `image_unpinned` warning otherwise.

# Alternatives

Default-on strict pinning would break documented mutable-tag version selection;
recording a digest without any enforcement would leave operators no way to
require it. Inventing a digest for a tag would misreport identity.

# Consequence

Panel passes the operator flag through `ClientConfig.RequirePinnedImages` into
`runtimeSpecFor`; the Cloud node agent uses the same shared contract. An
operator enabling the flag must repin existing mutable-tag servers before their
next container create. Digest recording itself never changes and worlds,
backups and databases are untouched.
