<!-- SPDX-License-Identifier: MPL-2.0 -->

# Current application captures

The README renders these images from the dedicated `showcase-assets` branch so
the URLs are stable:

`https://raw.githubusercontent.com/athNdev/carbonpanel/showcase-assets/assets/showcase/<name>.png`

The branch is seeded with the last known-good real captures before any README
link changes. Main no longer tracks generated PNGs. `manifest.json` on the
asset branch records the exact source commit, version, data provenance, and a
SHA256, byte count and pixel size for every published image.

These are unretouched browser crops of the packaged application running in an
isolated disposable installation with seeded fixtures, not mockups or homepage
sample islands. The stack seeds a first admin, a local node, a running Vanilla
1.21.1 server, real Packwiz mods, one World Library revision with an honest
compatibility report, and one offline player identity through the real
allowlist RPC. Resource figures and logs are observed values, not performance
claims. No production account, world, log or private player profile is used.

## Required and optional images

`showcase-dashboard`, `showcase-server-detail`, `showcase-studio` and
`showcase-modpacks` are the README-canonical set. Publication fails closed if
any one is missing, corrupt, stale or inconsistent with the manifest.

The player, world library and world-view screenshots are feature-gated journey
steps (`--features players,library,worlds`). A feature that cannot be captured
is skipped rather than fabricated; it is only linked from the README after a
successful publication. World View is off by default: managed BlueMap discovery
requires a legacy-local node ID and a genuinely rendered tile set, which the
review stack's UUID node cannot provide (ADR 0040). Publishing a map image
without real tiles is not acceptable.

## Automated path

`.github/workflows/showcase.yml` runs on main pushes that touch the represented
UI, or on `workflow_dispatch` with explicit feature gates. The capture job is
read-only and runs the real production journey. A separate minimal
`contents:write` job publishes only `showcase-assets`, in one atomic commit,
after:

- re-validating the bundle against the plan and validator from an exact pinned
  `github.sha` checkout (the artifact is data only, never executable code),
- confirming the capture is live main and a descendant of the last published
  source commit, and refusing a divergent re-run,
- requiring every README-canonical image, and
- checking a content digest so an unchanged capture is a no-op.

Any failure leaves the previous good screenshots untouched.

## Regeneration

```sh
REVIEW_STACK_NAME=carbon-readme-review \
REVIEW_PORT=18267 \
REVIEW_DATA_DIR=/tmp/carbon-readme-review \
  bash scripts/review/devstack.sh up

node scripts/review/journey.mjs \
  --plan scripts/review/plan-showcase.json \
  --base http://127.0.0.1:<port-from-devstack> \
  --out /tmp/showcase \
  --features players,library

python3 scripts/showcase_assets.py build \
  --images-dir /tmp/showcase --publish-dir /tmp/showcase-bundle \
  --plan scripts/review/plan-showcase.json \
  --seed-script scripts/review/devstack.sh \
  --features players,library \
  --source-sha <sha> --source-ref refs/heads/main \
  --repository athNdev/carbonpanel --run-id local --run-url <url> \
  --captured-at <utc-iso> --app-version <version> \
  --driver-version <version> --browser-version <version>
python3 scripts/showcase_assets.py validate \
  --publish-dir /tmp/showcase-bundle \
  --plan scripts/review/plan-showcase.json --features players,library \
  --expected-source-sha <sha>
```

Use a unique stack name and data directory; never use a shared installation or
real player data for public screenshots. The standard viewport is 1440 × 1000
at 2× device scale. Crops remove navigation and unrelated fields to keep
features readable at README width; no labels, metrics or logs are repainted.
