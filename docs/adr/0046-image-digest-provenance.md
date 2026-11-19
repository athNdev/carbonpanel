# ADR 0046: Opt-in strict image digest provenance

Status: accepted (OSS-369 B-09 closeout).

The panel records the registry repository digest that a pulled image resolves
to (`Client.ServerImageIdentity`, `RecreateContainer.RepoDigest`) and the shared
runtime profile marks a pinned reference (`name@sha256:<digest>`) as
`ImagePinned`. Before this decision an unpinned Minecraft tag produced only a
warning (`image_unpinned`): a rebuilt tag could silently change what the next
recreate ran, and an operator had no fail-closed mode.

## Decision

`docker.require_pinned_digests` is a new opt-in config flag (default `false`).
When enabled, the resolved runtime spec carries `RequirePinnedImage`, and the
shared `pkg/runtimeprofile` preflight turns the unpinned-Minecraft finding into
a fatal `image_unpinned_strict` issue instead of a warning. Digest recording is
unconditional and unchanged; strict mode only changes the severity of the
moving-tag finding. Pinning an image reference satisfies the gate.

Because the check lives in the shared runtime profile contract, Panel and the
Cloud node agent evaluate the same rule. The default stays permissive because
version selection in existing deployments relies on mutable tags; enabling the
flag is an explicit operator choice with a visible failure at container create
time rather than a silent image change.

## Alternatives

- Making strict mode the default would break documented tag-based upgrades and
  force every existing server to be repinned at once. Rejected.
- Recording a digest but only warning leaves no way for an operator to enforce
  provenance. Rejected.
- A registry-side allowlist change would not pin the resolved content and is
  outside this boundary. Rejected.

## Consequence

An operator who enables the flag must repin any server still using a mutable
tag before its next container create. Backups, worlds and databases are not
touched by the check. The digest value is never fabricated: an absent daemon
repo digest stays empty, and strict mode rejects a plain tag rather than
inventing an identity.
