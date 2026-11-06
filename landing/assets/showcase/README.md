<!-- SPDX-License-Identifier: MPL-2.0 -->
# Showcase screenshots (TODO)

No PNGs are committed yet: this environment cannot reach a live panel instance.

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

Expected files:

- `showcase-dashboard.png`
- `showcase-server-detail.png`
- `showcase-studio.png`
- `showcase-modpacks.png`

Re-run on UI changes to the dashboard, server detail, studio, or modpacks.
Static PNGs only: CSP `frame-ancestors 'self'` forbids live iframe embeds.
