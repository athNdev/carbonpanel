<!-- SPDX-License-Identifier: MPL-2.0 -->

# Current application captures

The README renders these images from the dedicated `showcase-assets` branch so
the URLs are stable:

`https://raw.githubusercontent.com/athNdev/carbonpanel/showcase-assets/assets/showcase/<name>.png`

The branch is seeded with the last known-good real captures before any README
link changes. The README no longer links to PNGs tracked on main; generated
captures are published on the asset branch. `manifest.json` there records the
exact source commit, version, data provenance, and a SHA256, byte count and
pixel size for every published image.

These are unretouched browser crops of the packaged application running in an
isolated disposable installation with seeded fixtures, not mockups or homepage
sample islands. The stack seeds a first admin, a local node, a running Vanilla
1.21.1 server, real Packwiz mods, one World Library revision, and a player session from a
headless Minecraft client connecting to the disposable server. The panel
creates the server-scoped roster row through its normal player observation
path. The review game port must be bound to loopback before the game starts. A compatibility verdict is
computed on demand by the read-only `CheckCompatibility` RPC; a panel that has
not run that check correctly shows "No compatibility report yet". The expanded roster capture shows the observed fixture identity and its provenance. Resource figures and logs are observed values, not
performance claims. No production account, world, log or private player profile
is used.

## Required and optional images

The canonical bundle contains `showcase-dashboard`, `showcase-dashboard-full`,
`showcase-server-detail`, `showcase-studio`, `showcase-modpacks`,
`showcase-library`, `showcase-library-revisions`, `showcase-player-allowlist`,
`showcase-worlds-list`, `showcase-world-tab`, and `showcase-world-view`.
The full dashboard is a native 2× capture of the 1440 CSS-pixel desktop
layout with navigation and shell.
The leading dashboard image captures the content at a narrower desktop width
for the README. Its link opens the original full-window PNG. The World View frame
uses a broad camera over the seeded Vanilla server's generated Overworld,
rendered by BlueMap through the authenticated panel route.

## Automated path

`.github/workflows/showcase.yml` runs on main pushes that affect the represented
UI, every Monday at 03:17 UTC as a backstop, and on manual dispatch. The default
dispatch captures the canonical set. To inspect a subset, disable the relevant
feature inputs. A partial or non-main dispatch uploads the validated artifact
without publishing. `playerdetail` is a manual opt-in and requires a decoded
skin preview.

The read-only capture job runs the production journey. A separate minimal
`contents:write` job publishes only `showcase-assets`, in one atomic commit,
after:

- re-validating the bundle against the plan and validator from an exact pinned
  `github.sha` checkout (the artifact is data only, never executable code),
- confirming both sources are in live main history and the new source advances
  the published revision. An older or repeated capture remains available in
  its run as a validated artifact,
- requiring every README-canonical image, and
- checking a content digest so an unchanged capture is a no-op.

Any failure leaves the previous good screenshots untouched.

## Regeneration

```sh
bun install --cwd scripts/review/fixtures/minecraft-player --frozen-lockfile --ignore-scripts

REVIEW_OBSERVED_PLAYER=1 \
REVIEW_STACK_NAME=carbon-readme-review \
REVIEW_PORT=18267 \
  bash scripts/review/devstack.sh up

session="$HOME/.cache/carbon-panel-review/sessions/carbon-readme-review"
export SHOWCASE_SERVER_ID="$(cat "$session/server-id")"

node scripts/review/journey.mjs \
  --plan scripts/review/plan-showcase.json \
  --base "http://127.0.0.1:$(cat "$session/port")" \
  --out /tmp/showcase \
  --features players,library,worldtab,worlds

python3 scripts/showcase_assets.py build \
  --images-dir /tmp/showcase --publish-dir /tmp/showcase-bundle \
  --plan scripts/review/plan-showcase.json \
  --seed-script scripts/review/devstack.sh \
  --features players,library,worldtab,worlds \
  --source-sha <sha> --source-ref refs/heads/main \
  --repository athNdev/carbonpanel --run-id local --run-url <url> \
  --captured-at <utc-iso> --app-version <version> \
  --driver-version <version> --browser-version <version>
python3 scripts/showcase_assets.py validate \
  --publish-dir /tmp/showcase-bundle \
  --plan scripts/review/plan-showcase.json --features players,library,worldtab,worlds \
  --expected-source-sha <sha>
```

Use a unique stack name and data directory; never use a shared installation or
real player data for public screenshots. The standard viewport is 1440 × 1000
at 2× device scale. Crops remove navigation and unrelated fields to keep
features readable at README width; no labels, metrics or logs are repainted.
