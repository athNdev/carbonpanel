# Carbon Panel release history and migration catalog

This is the retrospective release history for the independent Carbon Panel
version line. The machine-readable companion is
[`release-history.json`](release-history.json); `scripts/release.py
verify-release-history` checks both against the first-party inventory and the
independent audits without network access. It is durable provenance, not task
state. Operational upgrade procedure lives in [releases.md](releases.md).

Basis: `carbon-panel-version-migration-2026-11-19.md`,
`migration-compatibility-audit.md`, `migration-provenance-audit.md`,
`git-tag-inventory.md` and `registry-downstream-inventory.md` in the release
evidence folder. All facts below are read-only verified; anything not verified
is explicitly marked **UNKNOWN**.

## UPGRADE NOTES — read before upgrading from v1.0.40

**The next stable is Carbon Panel 2.0.1 (`carbonpanel-v2.0.1`), a MAJOR
release.** `docs/releases.md:11` defines Major as *"incompatible operator or
API changes that require migration."* Three intentional changes mean an
unchanged v1.0.40 configuration does **not** upgrade cleanly. The Connect/HTTP
wire contract and the database schema are additive and compatible; the breaks
are in configuration and operating behavior. Plan a maintenance window, back
up the database (db + wal + shm) and preserve worlds and backups before upgrading.

### B1 — OIDC users with the documented v1.0.40 config lose all roles

v1.0.40's own example (`config.example.yaml:116-117`) told operators to leave
`role_mapping: {}` when IdP group names matched local role names; raw claim
values were assigned directly on every login. Current main uses raw claim
values only behind `auth.oidc.allow_raw_claim_roles` (default `false`) and
refuses privileged names (`admin`, `owner`, `root`, `superadmin`, `system`)
from raw claims. The default `absent_claim_policy: deny` then revokes all
OIDC-sourced grants.

**Migration steps:**

1. For every role a user must retain, add an explicit entry under
   `auth.oidc.role_mapping` mapping the IdP claim value to the local role. An
   empty `role_mapping` no longer grants anything.
2. Do **not** rely on `allow_raw_claim_roles: true` to restore an admin
   entitlement: privileged names from raw claims are always refused. The
   privileged role must be explicitly mapped (ADR 0025).
3. After upgrade, sign in with a test OIDC identity and confirm the expected
   roles are present before letting users in. Treat a zero-role session as a
   failed migration, not a successful login.

### B2 — No implicit first-user admin; explicit local admin linking required

v1.0.40 made the first OIDC user admin and granted roles to returning
role-less users. Current main has no implicit admin: new OIDC identities get
only configured defaults, and an existing subject whose issuer does not match
is refused (`ErrLinkRequired`).

**Migration steps:**

1. Before enabling OIDC on the upgraded build, ensure at least one local
   administrator account exists, or use the documented local bootstrap path.
2. Link each administrator's OIDC subject to that local account explicitly.
   Do not expect the first OIDC sign-in to create an administrator.
3. Verify a freshly linked identity can obtain admin, and that an unlinked
   identity cannot. Keep the local admin credential available for recovery.

### B3 — PROXY protocol v2 is trusted only from `trusted_proxy_cidrs`

v1.0.40 wrapped every listener connection when PROXY protocol was enabled,
with no source allowlist. Current main trusts PROXY v2 headers only from
`proxy.trusted_proxy_cidrs`; the default `[]` trusts nobody, so headers from an
unlisted source stay in the byte stream and the peer address is kept.

**Migration steps:**

1. Identify every load balancer or reverse proxy that sends PROXY v2 headers.
2. Add each source network to `proxy.trusted_proxy_cidrs` (CIDR notation).
3. Upgrade, then confirm real client IPs are preserved in the UI/handshake
   limiter and in backend-forwarded requests. If IPs become the proxy's own
   address, the CIDR list is incomplete.

A compatibility mode that preserves v1.0.40 semantics was considered and
rejected: silently keeping raw-claim admin grants and universal PROXY trust
would retain the exact insecure defaults these changes remove. Operators must
migrate explicitly. The compatibility audit's go/no-go gate for these three
behaviors remains part of first-stable qualification; see
[releases.md](releases.md#maintainer-workflow).

## Retrospective inventory

Counts (origin `athNdev/carbonpanel`): **84** tag refs — 62 `v*`, 9 `mod-*`,
13 other. Upstream `discohaus/discopanel` also has 84 tag names. Comparison:
80 shared names = **79** exact object matches + **1** name with divergent
objects (`v3.0.0-beta.4`); **4** origin-only (`v1.0.38`, `v1.0.39`, `v1.0.40`,
`v3.0.0-alpha.1`); **4** upstream-only (`v3.0.0-beta.5`–`.8`, outside the
origin inventory). 88 distinct names across both repositories.

| Classification (origin) | Count | Meaning |
| --- | ---: | --- |
| `upstream-identical` | 57 | Inherited product history (v1.0.0–v1.0.37 incl. two `-dev`, v2.0.0–v2.0.15, beta.1–.3). Exact object match; upstream releases exist, none are Carbon Panel releases. |
| `historical-component` | 22 | docs (11), modules (9), `origin` and `rm` (2). Non-product domains; retained outside channel discovery. |
| `upstream-name-collision` | 1 | `v3.0.0-beta.4` — origin annotated `e1fa3679…→896ea7a1…`, upstream lightweight `a550598d…`. Both retained; no release for the shared name. |
| `carbonpanel-published-stable` | 1 | `v1.0.40` — the only verified stable GitHub release in this repository. |
| `carbonpanel-source-only` | 2 | `v1.0.38`, `v1.0.39` — Go proxy versions with matching remote objects; no release or container version tag. |
| `carbonpanel-legacy-preview` | 1 | `v3.0.0-alpha.1` — GHCR-only preview. |

Domain and provenance are separate fields in the catalog. `upstream-identical`
never implies a Carbon Panel release: upstream discopanel has many released
stable tags with five assets each, and none are promoted by this migration.

### The verified stable

`v1.0.40` is release **#448304811**: annotated tag object
`85a0b2c7b63cfa60ab428abeb62ff931afffbdc1` peeling to commit
`f88748be6a216b187e675bbb1f110df07e544148`; `draft: false`, `prerelease:
false`. It has **8** assets: five native archives, `checksums.txt`,
`openapi.yaml` and `release-manifest.json`. `checksums.txt` matches every asset
digest (it does not list itself). `release-manifest.json` is the commit/digest
anchor: version `v1.0.40`, commit `f88748be…`, images
`atharvnaphade/carbonpanel:1.0.40` and `ghcr.io/athndev/carbonpanel:1.0.40`,
and `image_digest`
`sha256:ead260ef33c9a9fa8929097e4e50db02243e8889a74efeb95c8fda622862c001`.
Both registries serve that identical multi-arch index with the same two linux
children; OCI indexes also contain two `unknown/unknown` attestation children,
which are not platforms.

The planned canonical annotated alias `carbonpanel-v1.0.40` will be created at
the same peeled commit without duplicating the GitHub release. It is
**historical-only** at the publication policy level: it resolves to the
original release for provenance, recovery and rollback, but publication
refuses it before any registry preflight. The Docker Hub alias
`sha-f88748be6a216b187e675bbb1f110df07e544148` is a *different* pre-final
build (`sha256:ba734569…`, `version=edge`) of the same commit and must not be
confused with the published 1.0.40 image.

### Frozen and preserved identities

- `1.0` and `latest` (both registries) currently point at `ead260ef…`; `1.0`
  stays on the 1.0.40 line. This is today's state, not a permanent ban on
  future 1.0.x maintenance — a later patch may move `1.0` only through the
  ordinary explicit, monotonic promotion path.
- All **19** readable `build-YYYY-MM-DD-HHMMSS` aliases and their 19 matching
  `sha-<revision>` aliases are preserved at verified digests with both linux
  platform children and matching config revision labels. No historical digest
  lacks a readable alias and no alias is invented or moved during migration.
- `edge` = `sha256:4c772558…`; `nightly` = `sha256:ecc89ba9…` with config
  revision `d8597d4e…` but `version=sha-d8597d4e…` (the readable build alias,
  not the literal `nightly`). Developer channels are Docker Hub only.
- The GHCR legacy preview `3.0.0-alpha.1` is `sha256:5c909828…` with config
  revision `5bbc06fe…` (the tag). The additive alias
  `legacy-preview-3.0.0-alpha.1` preconditions verify and the alias is absent;
  creating it is a separate, later, digest-proven operation. No Hub alpha image
  is fabricated. Its number does not determine stable `latest`, and product
  tags may not reuse `3.0.0-alpha.1` or the beta collision numbers.
- Go proxy: 41 `v1.0.x` versions, `@latest = v1.0.40`. Downstream
  `athNdev/carboncloud` pins `v1.0.40` on `main`, `v1.0.39` on branch
  `codex/adopt-panel-v1.0.39`, and `v1.0.38` on `cloud-v0.2.0`. All three are
  preserved; Carbon Cloud `go.mod` is not edited by this migration.

### Upstream-only and UNKNOWN

`v3.0.0-beta.5`, `.6`, `.7` and `.8` exist only on discohaus/discopanel with
upstream prereleases and five `discopanel-*` assets each. They are recorded in
the machine catalog under `upstream_only_tags` so nobody mistakes them for
missing origin tags. They are never promoted.

These facts remain **UNKNOWN** and are recorded as such, not guessed:

- Why local `refs/tags/v1.0.38` is `68ce7f95…` while remote is `fed0ca10…`.
  Both are retained; neither is overwritten.
- The source git tag for 18 of the 19 historical image revisions (the commit
  revision, digest and alias pair are verified).
- Any Carbon Panel channel meaning for the upstream-only beta.5–.8 tags.
- Exact asset bytes of upstream inherited releases.
- Module image repository visibility (anonymous 404 is not emptiness).

## Next stable

The `carbonpanel-v2.0.0` Git tag already identifies commit
`69ddb46d596aea8ed117a3dba6a1f63521572664`. The next publishable product tag is
`carbonpanel-v2.0.1`, displayed and immutable image version `2.0.1`, minor
alias `2.0`, `latest` promoted only after both registries serve the same
verified multi-architecture digest and the release assets are complete. The
Git tag, displayed SemVer, internal v-form and archive filename remain separate
fields derived from `scripts/release.py`; no consumer infers a version from an
arbitrary Git tag. The `1.0` alias stays at 1.0.40 for rollback. See
[releases.md](releases.md) for the maintainer workflow and
[ADR 0033](adr/0033-release-tag-and-publication-policy.md) for the publication
boundary.
