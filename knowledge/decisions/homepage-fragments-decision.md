---
type: Decision
title: "Homepage UI fragments — Decision"
description: "The homepage reuses feature components consumed by the installed panel. It"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: homepage-fragments.md
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

Demo cards use concise labels, grouped metrics and one primary action per state.
Results expose previous/current columns; waiting uses a read-only schedule and
applying uses a dedicated progress view. Scope notes live in the outer caption.
Action announcements remain accessible without visible narration bars. The
shared batch component keeps its operational help enabled by default; only the
homepage opts out of that help.

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

