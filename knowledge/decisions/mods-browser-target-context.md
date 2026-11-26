---
type: Decision
title: Mods browser target context
description: Opening the mods browser uses the same loader, Minecraft version and compatible-side context as later searches; providers without sides omit the side filter.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/src/lib/mods/mods-browser-store.svelte.ts
  - web/carbon-panel/src/lib/components/mod-browser-dialog.svelte
  - web/carbon-panel/src/lib/mods/mods-browser-store.test.ts
  - scripts/review/wizard-datapack-upload-check.mjs
sources:
  - title: Mods UI unification plan
    resource: ../../docs/plans/mods-ui-unification.md
---

# Choice

- The dialog's opening catalog request uses the target's loader, Minecraft
  version and `side`. A create-server wizard target therefore sends
  `side=server` on the first request even though its `serverId` is empty; the
  backend only defaults the side when a saved server ID exists.
- Plugin loaders (the Bukkit/Paper family served by Hangar) omit the side
  filter and the default `side:` chip, because that provider has no
  client/server sides. Other mod loaders keep the server-compatible default.
- An explicit manual search always overrides the target default, including
  removing a filter.
- The shared store owns this context; hosts pass it through `setTarget` rather
  than duplicating wizard-specific search code.

# Consequences

The opening list and later searches agree, so a wizard does not briefly render
client-only projects that the next search would hide. Provider-specific modes
remain separate: plugin browsing never claims side support, and back navigation
or filtered searches keep their own request context.

# Verification

`mods-browser-store.test.ts` covers the first wizard request, a side-less
provider and an explicit manual override. The checked-in Create Server browser
harness asserts the opening `searchMods` payload and the queued release/version
state without weakening its selectors.
