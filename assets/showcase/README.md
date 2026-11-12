<!-- SPDX-License-Identifier: MPL-2.0 -->

# Current application captures

Captured on 12 November 2026 from application sources at
`a30c385bbb560454d1ed31f350209f5a5c40655e`, compiled into a production Docker image.
No application source changed during this capture pass.

These are unretouched browser crops of an isolated installation, not mockups or
the homepage's sample islands. The installation contains a real running Vanilla
1.21.1 server and seeded Studio projects. Mod entries use versions returned by
the provider through the panel's API. Resource figures and logs are observed
values from that installation, not performance claims. Missing or stale metrics
are not replaced with invented readings.

| Image                        | Focus                                                                                    |
| ---------------------------- | ---------------------------------------------------------------------------------------- |
| `showcase-studio.png`        | Content tabs and the real mod table, including client/server sides and a pinned version. |
| `showcase-server-detail.png` | Running server console with its native log filter set to `Server thread`.                |
| `showcase-staging.png`       | A real `viewDistance` batch staged for the next restart, not applied.                    |
| `showcase-dashboard.png`     | Cluster overview and observed server state.                                              |
| `showcase-modpacks.png`      | The live Modrinth catalog as displayed by the review installation.                       |

The standard viewport is 1440 × 1000 at 2× device scale. The staged-batch crop
uses the actual responsive UI at 1100 × 1000, also at 2×. Crops remove navigation
and unrelated fields to keep features readable at README width. No UI elements,
labels, metrics or logs are repainted. Both this directory and
`landing/assets/showcase/` carry identical PNGs.

## Regeneration

Start a disposable stack with a unique name, port and data directory. The
bootstrap script requires Docker, Bash, curl and Node on its execution PATH.
Never use a shared installation or real player data for public screenshots.

```sh
REVIEW_STACK_NAME=carbon-readme-review \
REVIEW_PORT=18267 \
REVIEW_DATA_DIR=/tmp/carbon-readme-review \
  bash scripts/review/devstack.sh up

node scripts/review/journey.mjs \
  --plan scripts/review/plan-showcase.json \
  --base http://127.0.0.1:<port-from-devstack> \
  --out /tmp/showcase

cp /tmp/showcase/showcase-*.png assets/showcase/
cp /tmp/showcase/showcase-*.png landing/assets/showcase/
```

For a manual capture, use the same real routes and controls through a browser.
In Configuration → Game Settings, enable a custom View Distance, enter `12`,
and choose **Stage**. Keep **On next restart** selected. Use keyboard focus to
bring the batch controls into view, then crop the complete batch, including its
apply window, staged item, discard actions and explanatory text. Do not apply
the batch merely to obtain an image.

Inspect exports at README display size before replacing files. Record the new
application revision and date here, update both PNG directories together, and
re-render the README. Refresh captures whenever the represented UI changes.
`.github/workflows/showcase.yml` runs the four-image standard journey in CI;
the additional staged-batch image uses the manual steps above.
