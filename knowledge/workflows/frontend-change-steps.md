---
type: Workflow
title: "Frontend change — Steps"
description: "Work in web/carbon-panel (SvelteKit 3, Svelte 5, IBM Carbon tokens)."
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: frontend-change.md
---
# Steps

1. Work in `web/carbon-panel` (SvelteKit 3, Svelte 5, IBM Carbon tokens).
2. Use the generated Connect client from `src/lib/proto`; do not add a second
   API layer.
3. Run:

```sh
cd web/carbon-panel
bun install --frozen-lockfile
bun run check
bun run test
```

4. Build the embedded assets with `bash scripts/web-build.sh` before a
   production or binary build.
