<!-- SPDX-License-Identifier: MPL-2.0 -->
# UI preview screenshots

Seeded-stack captures, regenerated 2026-11-06 from the dashboard branch via devstack + journey (see commands below). Re-run on UI changes to dashboard, server detail, studio, or modpacks.

Regenerate from a disposable seeded stack:

```sh
scripts/review/devstack.sh up
node scripts/review/journey.mjs \
  --plan scripts/review/plan-showcase.json \
  --base http://127.0.0.1:<port-from-devstack> \
  --out /tmp/showcase
cp /tmp/showcase/showcase-*.png landing/assets/showcase/
cp /tmp/showcase/showcase-*.png assets/showcase/
```

Expected files (referenced by `README.md` and `landing/index.html`):

- `showcase-dashboard.png`
- `showcase-server-detail.png`
- `showcase-studio.png`
- `showcase-modpacks.png`

Re-run on UI changes to the dashboard, server detail, studio, or modpacks.
