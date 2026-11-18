# ADR 0033: Single auditable publication boundary and version domains

Status: Accepted

## Context

ADR 0010 defined the release bundle and its integrity checks, but a pushed `v*`
tag still started a run whose `publish` output was derived from the event. A tag
could therefore publish an image and a GitHub release without a deliberate
operator action. `mod-*` tags unconditionally pushed module images and moved the
`latest` alias. Those behaviors make a version/tag ref unsafe to stage and leave
two independent publication paths. Rehearsal `v0.0.0-verify.*` tags and
documentation tags had no explicit "never publish" rule. Registry names were
spelled inconsistently between the tooling, workflows and operations docs.

## Decision

There is one publication boundary, enforced by `publication_allowed()` in
`scripts/release.py`: only an operator `workflow_dispatch` with `publish=true`
may write to a registry or GitHub release. Branch pushes, tag pushes, pull
requests, `workflow_run` and every other automatic event only stage, build and
verify. A version/tag ref is always safe to stage. The workflow derives its
`publish` output from that function rather than from the event, and the publish
job is gated on that output.

Version domains are separate and immutable: panel releases use `vMAJOR.MINOR.PATCH`
with a full-version tag, `release-manifest.json` and an `image_digest`;
prereleases use their full version and move no stable alias; modules use
`mod-vMAJOR.MINOR.PATCH`; managed node-runtime artifacts reserve
`runtime-vMAJOR.MINOR.PATCH` without a workflow today; development builds use
per-commit `sha-<commit>` identity and the `edge` alias. Moving aliases
(`latest`, `MAJOR.MINOR`, module `latest`) move only under the matching explicit
promotion and are dropped only after a documented migration window. Publication
is phased: a read-only preflight classifies each registry reference as absent,
present or unknown; a conflict, unknown error or partial state refuses. Only the
immutable full-version tag is pushed first, then its tag→digest mapping and both
Linux architectures are verified in every intended registry. The durable
release record (draft/asset verification and published release) completes before
any alias moves, so an interruption cannot leave a newer alias unrecorded; a
retry reuses that record without re-upload or overwrite. Only after that
verification do separate, serialized steps promote aliases from the verified
digest, additionally checking each registry's actual alias digest against
durable digest-to-version evidence. Alias promotion is monotonic against published stable SemVer (no moving
backwards, no promotion from older versions), and prereleases promote nothing.
If a channel's ordering or immutability cannot be established, its automated
alias promotion is omitted with a recorded reason instead of guessed.
Deployments that need reproducible rollbacks pin the full version or digest.

The development channel adds one explicit alias, `nightly`, and no new immutable
reference: it reuses the verified `sha-<full-commit>` image of a pinned main
commit with exact successful CI. The shared sha tag is preflighted before any
build or push by both edge and nightly writers; a verified existing image is
reused, while a conflict or unknown lookup refuses with zero writes. Reuse
requires independent pinned-build evidence: exact successful CI for the commit,
the observed digest matching the recorded digest, both platform child manifests
and a matching `org.opencontainers.image.revision` in each digest-addressed child
config (descriptor annotations are not evidence); a bare expected digest is not
evidence.
Nightly and edge promotion bind the previously promoted commit to the observed
alias digest through that same evidence before checking ancestry, so a stale
operator string cannot authorize a move. Buildx writes only the sha reference;
edge is moved by an explicitly gated digest-based step so it survives sha reuse
in either publication order. Nightly never moves `latest` or a minor tag, and
all publication is serialized on the colliding destinations. Seeding a live
nightly remains a separate authorization; no scheduled write is enabled.
Deployments that need reproducible rollbacks pin the full version or digest.

Build and verification stay separate from publication. A fail-closed preflight
checks registry credential presence without reading values, credentialed steps
run only on an explicit publication, both registries must serve a complete
multi-arch index, and the GitHub release is exposed last. Repeated publication,
stale commits, conflicting versions and conflicting digests are refused. RESUME
is deliberately stronger than equal tag digests: it requires an operator-supplied
verified digest, an exact tag-to-commit identity, and a release manifest whose
version, commit, revision and digest all match; unknown or mismatched provenance
refuses.
Docker Hub is `atharvnaphade/carbonpanel`; GHCR is the lowercased GitHub
repository name, `ghcr.io/athndev/carbonpanel`.

## Consequences

Publishing now requires a deliberate dispatch with `publish=true`, so an accidental
tag push or a green CI run cannot publish. Staging a tag only means "no write";
a tag can target an old commit and a tag-triggered run reflects that target's
history, so the dispatch revision is never the trusted gate — the Python
boundary plus exact-commit main CI are. Main `:edge` and Pages channels now use
the same boundary: an ordinary verified main merge builds and verifies only,
while publication requires an explicit dispatch opt-in (`push=true` /
`deploy=true`), a read-only `verify-main-ci` check proving exact-commit main
ancestry and a successful completed push CI run, and a trusted `refs/heads/main`
ref. An operator checkbox is never CI evidence. Tags, feature refs and blank
refs are refused and the workflows are not disabled. Verification uses
per-SHA cancellable concurrency groups; publication uses a fixed channel-wide
non-cancelling group so different commits serialize on the moving `:edge` and
Pages destinations without cancelling an in-flight write.
IndexNow notification is deliberately deferred because changed URLs come from a
push diff while deployment happens on dispatch. These scoped gates need
release-auditor review before held product merges are unblocked. Consumers
relying on a moving alias must accept that it changes under explicit promotion;
pinned versions and digests remain the reproducible reference. The reserved
runtime naming creates no registry destination or workflow until that artifact
is implemented. See [release operations](../releases.md) for the procedure.
