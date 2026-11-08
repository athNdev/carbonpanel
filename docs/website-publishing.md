<!-- SPDX-License-Identifier: MPL-2.0 -->
# Website and pinned documentation

The Pages deployment contains the marketing homepage at `/` and
Starlight documentation at `/docs/`, and the standalone shared
design system at `/design-system/`. Only
`.github/workflows/landing.yml` publishes Pages. The docs workflow validates the
combined site on pull requests; it cannot replace the homepage.

## Source contract

`docs/website-source.json` records a full application commit SHA, its public
repository URL and a human-readable label. The label is the release tag when the
pin is a published release; any other commit is labelled a development snapshot.

The builder exports the authored guides and all imported Compose, configuration,
OIDC and protobuf sources from that commit into a temporary directory. It
generates the API schema using Buf 1.50.0 and the pinned OpenAPI plugin.
The current branch supplies the website presentation, docs entry page and
version explanation. No build reads application examples from a floating branch.

A source pin does not make old prose correct. Authors must review guidance against
the pinned implementation, especially deployment commands, external integrations,
sleep modes, permissions, destructive actions and recovery. Open gaps are tracked
in the [documentation backlog plan](plans/website-documentation-backlog.md).

## Build and verify

Requirements: Git with the pinned commit available, Python 3.12+, Node.js 24.22.1,
npm and Buf 1.50.0. Network access is needed for locked npm packages and the
versioned Buf generator. No application, Docker daemon or database is required.

```sh
python3 -m unittest scripts.test_website scripts.test_landing_page scripts.test_design_system_page
python3 scripts/check_brand_assets.py
python3 scripts/build_website.py
python3 scripts/check_website.py
```

Generated output stays in ignored `.website-build/site/`. The builder excludes
landing QA pages. The link check validates local page and asset paths, including
the API iframe, schema and design-system assets, under the deployment base.
The design-system page and tokens come from the current branch, independently
of the application documentation pin. Its source remains usable from a checkout.

For a local preview rooted at the server's root:

```sh
python3 scripts/build_website.py --base /
python3 scripts/check_website.py --base /
python3 -m http.server 18765 --directory .website-build/site
```

Open `http://localhost:18765/` and `http://localhost:18765/docs/`.
Use the default base when preparing a Pages artifact. Test search in a built
preview; Starlight's production search index is generated at build time.

The docs project's normal development server previews working-tree content and
labels its banner accordingly. Use the combined build to verify the publication
pin and generated API schema.

## Social previews

Every published page declares the full Open Graph and Twitter set: type, site
name, title, description, URL, a versioned absolute 1200 by 630 PNG and its alt
text. The homepage, the documentation home and any page without a dedicated
card share `landing/assets/og.png`.

The documentation home, introduction, Docker Compose, API reference and the
design system have dedicated cards in `landing/assets/og-*.png`. Regenerate
them with `python3 scripts/generate_social_cards.py`; the script renders the
shared layout with the vendored Inter and JetBrains Mono faces and needs a
Chromium-family browser. Advance a card's `?v=` query when its artwork changes.
`scripts/check_website.py` fails when a page is missing metadata, points at a
relative or missing image, or lets a prominent route fall back to the shared
card. Keep preview titles, descriptions and alt text free of em dashes.

## Advance the pin

1. Select and verify the intended full application commit. Prefer a published
   release tag and use the tag as the label; label any other commit as a
   development snapshot. Keep the referenced commit reachable.
2. Review authored docs, imported examples and API changes against that commit.
   Update the source docs first, commit them, then pin to the resulting commit.
3. Build and check the site. Verify the version banner and source link, install
   guide, search, API reference, mobile navigation, and a link back to the homepage.
4. Merge the reviewed change. The one combined Pages artifact deploys automatically.

Neither publishing a docs pin nor publishing the website creates an application
release. Pinned documentation must not claim live runtime acceptance that has
not been performed.
