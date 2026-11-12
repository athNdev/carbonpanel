---
type: Decision
title: Homepage UI fragments
description: Render real panel feature components as isolated, browser-local homepage examples.
status: stable
owners: [oss-runtime]
code_refs:
  - web/carbon-panel/demo
  - web/carbon-panel/src/lib/components/fragments
  - landing/index.html
  - scripts/build_website.py
sources:
  - title: Website publishing and fragment build
    resource: ../../docs/website-publishing.md
---

# Decision

The homepage reuses feature components consumed by the installed panel. It
shows small server-control, Studio and update-batch examples rather than an
entire application shell. Each example has independent, labelled sample data
and in-memory actions; reset and reload restore the fixture.
The staging example collects illustrative configuration, mod, datapack, file,
module and modpack edits in the shared update-batch inventory. Its review step
is a local demonstration of preparing one server's batch, not a claim that
every panel action is automatically intercepted. Staging snapshots the reviewed
items and window; simulate-apply publishes that snapshot only. Removing items,
clearing a batch or revising it never changes the live fixture. Timing is
explicitly simulated; the example has no scheduler or connected server.
Studio's compact tabs connect a sample catalog, the shared mod table and an
export preview. Dependency additions are illustrative fixtures, and the preview
does not download or claim to resolve a working pack.

Studio's dependency relationship and export exclusions remain visible as text;
color and motion are supplementary feedback. The sample server may publish a
completed-state event across its shadow boundary to switch the decorative
terrain entrance light. The event is local to the fixture, carries no server
identity, and never replaces the status text in the actual control.

The dedicated Svelte build must not import application API clients, stores,
SvelteKit route modules or Monaco. Production wrappers continue to own auth,
requests and persistence. Never add a production auth bypass or a demo URL
parameter to make a homepage example run.

Prerendered declarative shadow roots contain the actual component HTML and
scoped application styles. Controls are disabled until hydration, while
installation and documentation links work independently. The website builder
publishes the compiled islands alongside the homepage and pinned docs.

The build scopes canonical token root selectors to `:host` in a content-hashed
stylesheet. This supplies the same font, control-size and motion roles inside
shadow roots without changing the shared source or Cloud's snapshot. Do not
assume a `:root` declaration inside a shadow stylesheet reaches its host.
