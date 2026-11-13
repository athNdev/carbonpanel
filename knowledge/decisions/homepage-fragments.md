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
Marketing link arrows use decorative SVGs rather than Unicode diagonal-arrow
characters so Safari cannot choose an emoji presentation for a control icon.
The staging example collects illustrative configuration, mod, datapack, file,
module and modpack edits in the shared update-batch inventory. Its review step
is a local demonstration of preparing one server's batch, not a claim that
every panel action is automatically intercepted. Staging snapshots the reviewed
items and window; simulate-apply publishes that snapshot only. Removing items,
clearing a batch or revising it never changes the live fixture. Timing is
explicitly simulated; the example has no scheduler or connected server. Visitors
explicitly simulate a restart, player departures/joins and idle time, or reaching
a selected timestamp. Only the reviewed snapshot publishes after that window;
the result shows each applied before/after value. Reset cancels pending work.
Studio connects a twelve-entry sample catalog, six seeded mods, the shared mod
table with bulk actions, three inspectable override files and an export builder.
Sample dependency additions are idempotent; removing a library removes its
dependants, and exports reject missing dependencies after side filtering.
Sample updates respect pins. Changing a pack or export option invalidates the
previous result. The download is explicitly a JSON recipe with `playable: false`,
not a launcher archive or real mod binaries; no provider request is made.
The shared tab strip keeps the selected tab horizontally visible on resize or
programmatic selection without scrolling the surrounding page.

Studio's dependency relationship and export exclusions remain visible as text;
color and motion are supplementary feedback. The sample server may publish a
state event across its shadow boundary to switch the decorative terrain entrance
light, including an unlit transition during restart. Studio and staging publish
allowlisted presentation phases for localized light and progress motifs. These
events carry no identities and never change another fixture's data or replace
text feedback. Reduced motion and forced colors remain usable without effects.

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
