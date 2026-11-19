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

Version domains are separate and immutable: panel releases use the product tag
`carbonpanel-vMAJOR.MINOR.PATCH[-alpha.N|-beta.N|-rc.N]`; the displayed SemVer
and immutable image tag are `MAJOR.MINOR.PATCH` with the same suffix, and the
Git tag, SemVer and archive filename remain separate fields. A product release
must not depend on adding a plain `v*` tag. Plain `v*`, `mod-v*`, `docs*` and
inherited refs are historical domains, never publication candidates; only the
verified legacy stable `v1.0.40` is recognised for monotonic promotion and the
frozen 1.0 rollback line. `publication_allowed()` rejects inherited,
source-only, preview, name-collision and unclassified tags even with the
opt-in, before any immutable registry preflight. The historical-only alias
`carbonpanel-v1.0.40` resolves to the original commit for provenance and
rollback but is also refused as a new release candidate; reserved legacy
preview/collision version numbers cannot be reused by a product tag. The
current 1.0 rollback line is 1.0.40; this is today's published state, not a
permanent ban on future 1.0.x maintenance, which may move `1.0` only through
ordinary explicit monotonic promotion. The 84-tag classification with domain, provenance and evidence is
checked in at `docs/release-history.md` and `docs/release-history.json` and
validated by `verify-release-history`; this extends the existing framework
rather than creating a second one. Prereleases use their full version and move
no stable alias; modules use `mod-vMAJOR.MINOR.PATCH`; managed node-runtime
artifacts reserve `runtime-vMAJOR.MINOR.PATCH` without a workflow today;
development builds use per-commit `sha-<commit>` identity and the `edge` alias.
Moving aliases
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
GitHub `latest` selection uses the same product-only verified stable ordering
as registry alias promotion: prereleases never promote, an older maintenance
release does not displace a newer verified stable, and an undeterminable state
leaves `latest` unchanged. The three writing jobs share one global,
non-cancelling `carbonpanel-product-publish` concurrency group so different
versions cannot race on `latest` or the moving aliases; build-only rehearsals
and ordinary CI are not serialized.

Docker Hub is `atharvnaphade/carbonpanel`; GHCR is the lowercased GitHub
repository name, `ghcr.io/athndev/carbonpanel`.

## Product tag line and retrospective catalog

A publication additionally requires a trusted `refs/heads/main` dispatch ref
and a read-only exact-commit main CI check (`verify-publication-ref`); a tag,
feature ref, blank ref or operator checkbox can never substitute. The
retrospective catalog preserves all 84 origin tags: the only verified stable
`v1.0.40` is canonical history with five archives and matching Hub/GHCR digests;
the planned product alias `carbonpanel-v1.0.40` resolves to the same peeled
commit without a duplicate GitHub release; `v1.0.38`/`v1.0.39` remain
source/module history and `v3.0.0-alpha.1` remains a GHCR-only legacy preview.
The `v3.0.0-beta.4` name collision records both repository-qualified commits,
and `origin`/`rm` stay outside channel discovery. No ref deletion, image rebuild
or invented historical alias is part of this decision; ADR 0039 still governs
the 19 immutable readable build aliases.

The first new stable is deliberately `carbonpanel-v2.0.0` (MAJOR), not a
compatible `1.1.0`: the wire/Connect contract and database schema are additive,
but three operator-facing changes (OIDC raw-claim role mapping locked out by
default and no implicit first-user admin; explicit local admin linking;
PROXY v2 trusted only from `trusted_proxy_cidrs`) require migration from an
unchanged v1.0.40 configuration. The audit evidence and operator steps are in
`docs/release-history.md` and `docs/releases.md`. Using the correct SemVer
boundary here is part of this decision; a future compatible release remains a
MINOR under the same rules.

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
