# ADR 0045: One version contract and immutable per-version docs

Status: Accepted

## Context

The published `/docs/` pages reported `v3.0.0-beta.4` at a commit
(`e1f23829ΓÇª`) that did not match the origin tag peel (`896ea7a1ΓÇª`). The label
was also misbound to an upstream name collision, the verified stable was
`1.0.40` at `f88748beΓÇª`, and the docs npm metadata claimed `2.0.0`. Free-form
label/revision fields in `docs/website-source.json` let a label and a commit
drift apart, and the version page inferred "prerelease" from a `v` prefix,
which misclassified source-only historical tags.

## Decision

`docs/website-source.json` is the single machine-readable build contract. It
declares the canonical stable tag, a current development identity, independent
artifact versions and compatibility bounds, and the documented versions. The
classified catalog `docs/release-history.json` is authoritative for
classification; channel, availability and download behaviour are derived from
that classification, never from the label. `scripts/version_contract.py`
generates one shared `docs/version-index.json`, validates every entry against
the catalog peel and the real Git tag, and fails closed on any disagreement.
The candidate commit is derived externally from the tag, not from a
self-referential source SHA.

Published docs keep the stable default at `/docs/`. Immutable routes
`/docs/versions/<display-version>/` are built for `1.0.40` (stable),
`3.0.0-alpha.1` (GHCR-only legacy preview, never sorted above stable) and
`1.0.39` (source-only, no download CTA). `v1.0.38` and `v3.0.0-beta.4` are
excluded. Each route is a separate build from the exact peeled commit for
authored content, examples, downloads and OpenAPI; current-branch code supplies
presentation only. A common switcher rendered from the generated index carries
the current page when the target has it, otherwise it lands on the target
home. The same index drives banners, direct URL validation, search scope,
sitemap/canonical URLs and Pages metadata.

One atomic `scripts/version_bump.py prepare`/`bump` workflow derives or
validates every affected surface on the same commit, including rendered and
packaged outputs, and has omission failure tests. It never creates a tag,
publishes a release, writes a registry or marks a candidate published.

## Consequences

A misbound label, mismatched peel, unsupported classification or stale index
now blocks packaging instead of reaching the live site. Consumers get
reproducible immutable version routes without retagging or republishing.
Staging, Pages and registry/GitHub writes remain separate deterministic steps;
there is no cross-provider atomic transaction, so reconciliation retries from
the verified exact-commit artifact. The proto declares the independent
Connect-OpenAPI **schema family** `2.x`; it is not a product version. Bundled
and website OpenAPI copies stamp `info.version` with the exact normalized
product version and are verified fail-closed, while the `carbonpanel.v1`
protocol identity stays unchanged. The served schema comes from that same
stamped input via the embedded build filesystem, so a running packaged panel
reports the exact version over HTTP. UI parity reads the authoritative
`build/version.json` emitted by the same version source as `__APP_VERSION__`,
not an incidental string in any build asset.
