<!-- SPDX-License-Identifier: MPL-2.0 -->
# Carbon Panel OSS homepage design plan

Design proposal prepared on 12 November 2026. Scope: the public OSS homepage in
`landing/`, its imagery, copy, embedded interactive UI fragments and installation
handoff. Each fragment uses a small part of the panel's actual Svelte UI with
clearly labelled sample data and browser-local actions. Present one focused
piece alongside each relevant story, without embedding the entire panel.

Make Carbon Panel feel like a carefully made instrument for looking after
Minecraft worlds. Lead with a usable slice of the actual product, give its
creative tools a clear story, and use graphite materials and stepped landscape
geometry to connect the identity to Minecraft. The sense of craft should come
from composition, typography, imagery and small interaction details.

The recommended direction retains the approved lower-cut block, technical
wordmark and IBM Plex family. It calls for a substantial recomposition of the
homepage, with a smaller number of purposeful sections. This is a design plan;
it does not change application behavior or establish a new brand decision.

## Product understanding

Carbon Panel combines two valuable experiences: making a world worth playing
and operating the server that keeps it available. Modpack Studio and datapacks
support the first. Containers, console access, staging, backups, routing and
idle modes support the second. The homepage should connect these experiences
through one recognizable world, rather than present each subsystem as a
separate selling point.

The OSS product is a self-hosted, single-tenant controller. Its Go daemon owns
the API, console, scheduling, proxy and SQLite database, while Minecraft
servers run in Docker containers on compatible local or remote nodes. The
operator supplies infrastructure. Carbon Cloud's tenancy, purchasing and
managed-service story belong to a different product. See the
[product overview](../../knowledge/overview.md) and [README](../../README.md).

The following audience priorities are design hypotheses derived from those
capabilities, to be checked with users during the design review.

| Audience | What they are trying to do | What the homepage should establish |
| --- | --- | --- |
| The person hosting for friends | Keep a shared world running without constant administration | Recognizable servers, clear controls, an approachable installation path |
| Modpack author or community organizer | Assemble and share a particular Minecraft experience | Studio, dependency feedback, export formats and datapacks |
| Homelab operator | Use their own machines and understand resource and storage consequences | Docker support, node placement, sleep modes, backups and practical documentation |
| Contributor or technical evaluator | Inspect, extend and integrate the product | Public source, licence, release context, API and contribution links |

The emotional promise is ownership with less administrative friction. A world
represents people's time and shared experiences. The tone should be capable,
personal and direct. Large-fleet imagery should remain secondary to an
individual operator understanding their own servers.

### Capabilities that deserve prominence

| Capability | Useful user outcome | Copy boundary |
| --- | --- | --- |
| Modpack Studio | Create, import, edit and share a pack, including `.mrpack`, CurseForge and packwiz exports | Dependency resolution can report unresolved dependencies; avoid promising universal compatibility or every launcher format |
| Datapacks | Find and add changes to a world from inside the panel | Describe compatibility warnings as assistance, not proof that every pack will work |
| Staged updates | Prepare compatible changes together and choose when they apply | Staging leaves the live tree untouched; applying to a running server includes a restart |
| Idle modes | Choose how an empty server uses resources | Freeze retains memory; deep sleep releases JVM memory; wake can take time and depend on host/version/configuration |
| Server operations | See state, inspect logs, manage files and configure backups | The interactive demo demonstrates UI with simulated operations; it does not establish runtime or recovery guarantees |
| Node placement and routing | Run workloads on compatible machines under one controller | Do not imply automatic cloud purchasing, universal storage transport, controller HA or seamless migration |

The implementation basis includes the [Studio route](../../web/carbon-panel/src/routes/modpacks/studio/+page.svelte),
[export handlers](../../internal/rpc/handlers/packwiz.go),
[dependency reporting](../../internal/packwiz/dependencies.go),
[staging invariant](../../knowledge/invariants/staged-updates.md),
[sleep runbook](../runbook-sleep-baseline.md) and
[node-resolution rules](../../knowledge/architecture/node-resolution.md).
Verify public claims against the selected release when implementing; this
working branch contains ongoing runtime changes.

## Current homepage assessment

The live page was reviewed at [carbonpanel.dev](https://carbonpanel.dev/),
including desktop views, a 390 by 844 mobile layout, navigation, the ownership
FAQ and the Get started destination. The old website address redirects there.

The approved identity, explicit Minecraft headline, restrained palette and
clear self-hosting message already work. Feature descriptions are more specific
than a generic server-panel pitch. Real application captures now exist, and
the mobile FAQ provides a useful explanation of data ownership.

The main design problems are hierarchy and repetition:

| Observation | Consequence | Planned response |
| --- | --- | --- |
| The first desktop viewport contains headline, badge, two CTAs, four feature links and a clone command, with no product view | The visitor has to imagine the tool; several secondary elements compete for attention | Bring actual UI into the opening composition and move setup commands into the installation section |
| In the initial 1280 by 720 layout, the real product-tour section began around 3,605 pixels down, after an illustrative interface | The strongest evidence arrives late, and two different interfaces complicate expectations | Use one truthful product-preview system and remove the competing concept dashboard |
| Feature cards, the platform inventory, architecture, fleet, comparison and OSS sections repeat benefits | The page feels accumulated and the reading pace stays flat | Tell one short story, then offer technical depth through documentation links |
| Many sections use similar centered headings, dark bordered cards, tags and repeated grid backgrounds | Individual capabilities have little visual distinction | Use changes in scale, alignment and whitespace; reserve containers for meaningful groups |
| The silver/graphite treatment is mostly abstract and industrial | Minecraft is identified mainly through the copy | Introduce one original stepped landscape motif and carry world/pack context through focused interactive fragments |
| The mobile header retains Star while hiding Get started; the clone line scrolls horizontally | The persistent navigation prioritizes project support over adoption | Keep Install and Menu in the mobile header; place GitHub in the menu |
| The architecture presentation is a long scroll sequence | A secondary explanation consumes substantial attention | Use a concise, readable diagram and a link to the architecture documentation |

The initial desktop document measured roughly 14,880 pixels tall. This is a
capture-specific observation, not a stable page-size benchmark: viewport and
late-loading images affect the total. Shorten by removing repetition, while
preserving readable text and generous spacing.

The mobile menu opened, and choosing a section closed it. Escape left it open
in the observed state. Add predictable Escape dismissal and focus restoration
as interaction polish; that observation alone does not establish an
accessibility violation. The FAQ expanded and the primary CTA reached the
Docker Compose guide. Screen-reader use, measured contrast, zoom, reduced
motion and field performance remain implementation acceptance work.

## Visual direction

### Composition and material

Use a quiet graphite base, warm white typography, crisp structural rules and
one original image of a stepped landscape. Imagine a small landscape model
made from dark mineral material, lit from one direction: visible terraces,
soft contact shadows, restrained surface grain and carefully framed negative
space. Its geometry should suggest a Minecraft world at a glance without
requiring a game-texture background.

The landscape belongs in a limited art area beside or behind the product
presentation. Keep interface text, body copy and controls on clean surfaces.
The illustration should have a deliberate silhouette at small sizes and feel
composed by a person. Repeat at most one small crop near the final invitation.

Retain the original mark as an untouched, flat asset. The landscape's openings
and steps may echo its spatial character, but must not redraw, extrude or
decorate the logo. Follow [ADR 0008](../adr/0008-carbon-panel-brand-identity.md)
and the [brand usage guide](../../assets/branding/README.md).

Two inspected references sharpen the direction. [Linear](https://linear.app/)
puts a large product surface immediately beneath strong, left-aligned type.
The [OP-1 field presentation](https://teenage.engineering/products/op-1) gives
a single object enough space for its material and construction to carry the
identity. Apply those principles to Carbon's own UI and artwork. These are
references for composition and craft, not templates to reproduce.

### Minecraft cues

| Cue | Where it belongs | Restraint |
| --- | --- | --- |
| Stepped terrain and square volumes | Hero artwork and one small closing crop | One cohesive scene; no repeated floating cube decorations |
| The approved lower opening | Existing brand mark and the spatial rhythm of the artwork | Preserve the master mark exactly |
| Chunk-like subdivisions | A few local subdivisions of the structural backdrop beside artwork or a UI fragment | Keep fine subdivisions out of text and controls; no decorative coordinates or implied telemetry |
| World names, loaders, mod lists and datapacks | Small fragments of actual panel components beside the relevant story | Label sample data and simulated operations; preserve genuine product interaction patterns |
| Warmth and signs of habitation | A restrained light or sheltered opening within the terrain image | Keep the site chrome neutral; avoid introducing a competing brand accent |

Use at most two decorative Minecraft cues in a viewport. The category remains
explicit in the eyebrow and supporting copy. Keep pixel fonts, item hotbars,
grass-block button textures and game sound effects out of the interface.

### Typography and geometry

The following are starting specifications for mockups, not global token changes.

| Element | Desktop starting point | Mobile starting point |
| --- | --- | --- |
| Content frame | 1,248 px maximum; 12 columns; 24 px gutters | 20 px outer gutters; 16 px at 320 px width |
| Hero heading | IBM Plex Sans, 76–88 px, 500 weight, 1.02–1.06 line height | 44–52 px, with tested natural wrapping |
| Section heading | 40–48 px, 500 weight, 1.1–1.15 line height | 30–34 px |
| Lead text | 18–20 px; 1.5–1.65 line height; about 44–52 characters per line | 17–18 px |
| Body text | 16–18 px; maximum about 65 characters per line | 16 px minimum |
| Small labels | IBM Plex Mono, 12–13 px; short labels only | 12–13 px; no tiny feature-link strip |
| Section spacing | 96–128 px around major chapters | 56–72 px |
| Actions | 48 px high; clear hover, press and focus states | 48 px high; full-width hero primary action |
| Corners | Square editorial panels; small 2–4 px radius only where justified | Same geometry; preserve actual application component styling |

Use the existing graphite and neutral roles as the base: `#161616`, `#262626`,
`#393939`, `#f4f4f4`, with readable secondary text. A restrained metallic edge
can define the primary action. Remove the continuous button glow. Apply
texture only to artwork, with flat text surfaces around it.

The marketing site's silver primary action can remain a documented local
choice. Preserve the application's actual blue actions and semantic status
colors inside the embedded UI fragments. Keep controls at normal product scale.
Use named landing-page tokens for local presentation; changes to shared roles
must follow the [design-system propagation workflow](../design-system/README.md).

### Refine the existing grid backdrop

Keep the grid as a recognizable part of Carbon's identity. Make it feel like
fine construction lines on a carefully composed page: visible in the open
space, precise at the edges of objects, and quiet wherever someone is reading
or interacting. Its craftsmanship should come from alignment and selective
detail, with a subtle connection to the geometry of a Minecraft world.

The current [stylesheet](../../landing/assets/styles.css) draws a fixed,
viewport-wide set of vertical hairlines with `.page-grid`. It derives their
spacing from the container width, subtracts 64 px and divides by 12, changing
to eight and four columns at narrower breakpoints. Section borders supply
the horizontal rules. A separate radial light pool drifts over 36 seconds.
This is a useful starting point, but a repeated background does not share the
actual track and gutter geometry of each content layout. Its fixed position
also keeps the same treatment behind every chapter.

The proposed treatment has three levels:

| Level | Appearance | Role |
| --- | --- | --- |
| Structural rails | Crisp 1 CSS px lines at the content frame and selected real column boundaries | Tie the headline, feature copy and UI fragments into one composition |
| Quiet construction lines | Fainter intermediate lines, selectively masked through open space | Preserve the existing grid character without making every section a ruled box |
| Local chunk detail | One small square subdivision patch with a short stepped edge or two small intersection marks | Reward a closer look and connect the backdrop to Minecraft's spatial character |

Use the same centered wrapper, responsive padding, track count and gutter
tokens for the layout and its decoration. The new 1,248 px content frame
means the old 1,312 px/64 px calculation must not survive independently.
Place a guide at an actual column edge or gutter center; do not make a second
percentage-based grid that merely looks close. Draw only the selected tracks
needed by the composition. The hero's five/seven-column division can supply
one prominent internal guide without requiring the rest of the page to use
that split. Let the outer rails extend a little into section spacing, then
fade them; avoid repeating identical full-width section boxes.

Starting values for visual exploration, to be judged in rendered layouts:

| Property | Starting specification |
| --- | --- |
| Minor line | Warm white at 2.5–3.5% opacity on the graphite base |
| Main rail | Warm white at 5–7% opacity; clearly quieter than component borders |
| Line thickness | 1 CSS px; inspect at normal and high pixel density and browser zoom |
| Horizontal rhythm | Lines at meaningful section/fragment boundaries; short local crossbars within artwork, rather than a square grid over the whole page |
| Fade | Roughly 80–160 px at the end of a section's decorative field; smaller on mobile |
| Fine detail | One patch per major art composition, at most two restrained intersection marks; avoid repeating a motif behind every fragment |
| Light | One static, broad directional pool associated with the hero artwork; no independently drifting glow |

These are decorative line values, not text or control contrast targets.
Functional borders and focus indicators keep their own accessible contrast.
Keep the grid neutral: the application's blue controls and semantic status
colors should remain easy to identify.

Give each chapter a deliberate treatment. In the **hero**, rails and a small
subdivision patch meet the terrain near the server-control fragment, with
negative space around the headline. In **Studio**, use a quieter field and a
short rule aligned with the mod list. In **staging and sleep**, keep mainly
the outer rails so denser state information can breathe. The **installation
and FAQ** sections should be mostly plain graphite, retaining occasional
structural lines in the margins. This changes the rhythm while preserving a
continuous visual identity.

The fragment's real surface should visibly cover the background, like a
precisely placed object. Keep grids, grain and terrain outside its controls;
do not introduce translucent application panels to expose the decoration.
Align the surrounding rules to the fragment's outer frame, not every row
inside it. Reserve a clean area around explanatory text instead of relying
on text shadows to fight the background. Any stepped terrain/grid transition
belongs to the artwork boundary, never the logo or interactive component.

At wide sizes use the 12-column layout, with only selected guides visible.
Reduce the underlying layout to eight or four tracks when the composition
changes; on a narrow phone show mainly the two outer rails and, if space
allows, a small local patch beside the artwork. Recalculate from the actual
mobile gutters. Simplify the decoration as content stacks; do not shrink a
desktop lattice into a dense mobile pattern.

Implement the backdrop with lightweight CSS and, only where useful for short
stepped details, a small decorative SVG. Keep the terrain itself a separate
image asset. Use section-local, document-scrolling layers with shared layout
geometry, `aria-hidden` and `pointer-events: none`. Masks should enhance a
quiet base treatment; without mask support, retain sparse rails or omit the
fine detail. Do not require JavaScript, a canvas, WebGL, pointer tracking,
scroll-linked transforms or continuous animation. Reduced motion remains
static and forced colors removes the decorative layer. Avoid per-cell DOM
elements and viewport-sized blur filters.

Compare three hero explorations using the same real UI fragment: the current
grid as a baseline, refined structural rails, and those rails with one local
chunk detail. Prefer the least decoration that makes Carbon recognizable.
Inspect the full page as well as close crops: no moire, doubled rules, seams
at section transitions, visible misalignment, or grid showing through inputs.
The intended result is a backdrop that becomes more rewarding when noticed,
while the product remains the first thing a visitor understands.

## Proposed page structure

The page should answer, in order: what is it, what does it look like, what can
I make and manage with it, how does it fit my machines, and how do I start?

| Order | Section and purpose | Composition and content |
| --- | --- | --- |
| 1 | Header and hero | Brand; Product, Features and Docs links; GitHub; one Install action. Asymmetric copy and terrain artwork with one actual server status/action fragment |
| 2 | Your worlds at a glance | A small real server-list fragment with search/filter and a few example rows; short context about supported server software |
| 3 | Build the world you want | A focused Studio mod-list/search fragment beside the creation story; demonstrate adding one mod and its dependency |
| 4 | Keep changes on your terms | A staged-batch fragment beside the update explanation, then a separately spaced idle-state fragment beside sleep/wake copy |
| 5 | Run it on your machines | Compact topology plus a quiet capabilities index for console, files, backups, access and API. Keep infrastructure depth here |
| 6 | Install and keep ownership | Docker prerequisites, persistent storage, first admin and first server; a clear link to the versioned installation guide; concise source/licence/data statement |
| 7 | FAQ and closing action | Five practical questions, final installation action, source/docs/community links and the existing identity |

Retain a compact, static supported-server line near the first product proof.
Group server software and compatibility tools accurately; a scrolling ticker
of unrelated names should not serve as the explanation of support.

### Hero copy and layout

Proposed copy for the first design round:

> SELF-HOSTED MINECRAFT SERVER MANAGEMENT
>
> Your worlds.
> Yours to run.
>
> Build and share modpacks, manage your servers, and prepare the next update
> from one open-source panel on infrastructure you control.
>
> Install Carbon Panel · Try the controls
>
> Free and open source · MPL-2.0 · Runs with Docker

At 1440 px, give copy approximately five columns and the art/UI composition
seven. The UI is a server status-and-action fragment, around 480–600 px wide,
showing one sample world, its state and the actual lifecycle controls. It fits
because only the relevant part of the product is present. Keep terrain behind
or beside its frame, clear of text and hit targets. The fragment and main
installation action should both appear within the first 900 px.

On mobile, place that same responsive fragment directly after the initial
action group. Use native text sizes and allow controls to reflow. The Try the
controls link focuses the example's entry point without executing an action.
No sidebar, global navigation or full server-detail page belongs in the hero.

The existing headline remains a useful comparison in the first mockup round.
Evaluate comprehension with the category line present; do not select copy
solely for its silhouette.

### Interactive fragments of the actual UI

Place small, working pieces of Carbon Panel beside the content they explain.
Reuse the actual feature components, styling, form validation and interaction
logic from the application. Each fragment should answer one question through
one short interaction. The page remains an editorial product story, with
interactive evidence encountered naturally while scrolling.

The intended sequence is: see a world's controls, filter a few server rows,
add a mod, review an update batch, then understand a sleeping server. There
is no complete dashboard, application shell or global demo navigation.

Display a persistent, quiet label: **“Interactive demo · Sample data”**, with
the adjacent explanation **“Changes stay in this browser. No server is
connected.”** The UI is real product code; server processes, log events and
telemetry in these examples are simulated. Keep a compact label beside each
fragment so visitors arriving at a deep link still understand its context.
Do not describe an example as a live server.

Each fragment owns a small example state and a Reset example action. Preserve
its edits when it leaves the viewport and when the page resizes; scrolling
back should not restart the example. Use distinct world/pack names where the
examples are independent, so an action in one is not expected to affect
another. Share fixture types and catalogue definitions, not mutable state
between unrelated demonstrations. Reset affects only the named example and
asks before discarding edits.

#### What visitors can try

Use valid product enums and stable example IDs. Keep names, status, log events
and pending changes internally consistent within each fragment.

| Placement and actual UI fragment | Visitor action | Visible result and scope |
| --- | --- | --- |
| Hero: one server's status and lifecycle actions | Restart the sample Grove world using the existing control and confirmation | A short, bounded simulated state transition returns to running. An optional status message explains the result; no full server page opens |
| Worlds section: server-list rows and filter | Search a three-world example or select Running | The same product rows and empty-result treatment respond; no fleet dashboard or table/tiles tour is needed |
| Studio section: mod search/list and dependency feedback | Add/remove a mod from a small bundled catalogue | The real list and dependency feedback update. Include a sample unresolved dependency using the product's warning pattern; no full Studio workspace or provider calls |
| Updates section: a staged field and batch summary | Change one supported setting, review it, choose next restart, then apply or discard | The example's current value stays unchanged until apply. Preserve the actual restart explanation, validation and confirmation; omit unrelated Configuration settings |
| Sleep section: idle setting and current state | Choose a supported example mode and press a separate demo-only Simulate player joining control | Actual state components show the sample transition. Explain retained versus released JVM memory and cold-start delay; do not present the simulation trigger as a product feature |

Prioritize the hero control, Studio list and staged batch. The filtered list
and sleep example can follow if each adds information without making the page
feel busy. The first implementation slice is just the shared server
status/action component, including its simulated restart and Reset example.

Use product labels inside the fragments. “Prepare an update” is an editorial
heading beside the existing Configuration batch components, not an invented
Updates tab. Homepage prompts and simulation-only controls sit outside the
product fragment in a distinct caption area.

An optional console example belongs beside an operations explanation: six to
eight readable log lines and the actual command field. A small command set
such as `help` or `list` responds entirely in memory. Unknown input receives
a clear demo limitation. Render input as text, never execute script or shell
commands. Stream only finite events after a visitor action. Include this
fragment only if console interaction merits its own story; it is not part of
the hero by default.

Every enabled control must have meaningful behavior. Omit unrelated fields,
tabs and destinations. When retaining a disabled control helps
explain the product, provide an accessible reason. Export controls should
explain the available formats without pretending to create a usable pack
containing fake mods. Avoid advertising an operational feature through a
success toast when it was not simulated coherently.

#### Placement and scale

Pair one primary fragment with each story. A desktop section can place concise
copy in four or five columns and the component in the remaining space. Target
roughly 480–640 px of available width and 240–420 px of initial height where
the actual controls fit. These are layout starting points, not fixed clipping
boxes: validation messages, confirmations and expanded dependencies must fit.
On mobile, stack copy and component with natural height and native text size.

Vary the composition around the fragment, not the product's typography or
controls. A server action strip, mod list and update batch naturally have
different shapes; let those differences create rhythm. Aim for one dominant
interactive fragment per viewport and never place several complete demos
side by side. Use a thin frame only where it helps distinguish the example.

The UI is live DOM with selectable text, genuine buttons and forms. Extract
the appropriate subcomponent; do not load a complete route and hide most of
it behind a CSS crop. Do not transform-scale a desktop screenshot-sized UI.
Use normal page scrolling, with a named local scroll area only for content
such as a longer console transcript. No Expand to full panel action is part
of this plan. A necessary dialog contains only the current task, with Escape
and focus restoration, and must not clip at the fragment's boundary.

Do not automatically advance the tour, steal keyboard focus, move a simulated
cursor or start/restart a world on hover. One short prompt, such as “Try a
restart” or “Add a mod and see its dependency,” is enough to invite exploration.

#### Implementation approach

**Recommend small Svelte islands inside the existing static homepage:** a
dedicated demo entry mounts or hydrates shared feature components at their
respective sections. Share the Svelte runtime and common component chunks
across islands. The rest of the page stays static. Each example receives a
local fixture-backed data/action implementation instead of production clients.

| Approach | Fit for this plan |
| --- | --- |
| Inline shared Svelte fragments | Preferred: native page layout, selectable content, one scroll flow and direct interaction. Requires feature-component extraction and careful CSS/provider isolation |
| Dedicated static fragment documents in iframes | Fallback only if a feasibility check finds styles or portals cannot be isolated cleanly. Each frame still contains only its named fragment; assess duplicated runtime cost, focus and sizing before adopting |
| An iframe of a running panel | Outside this proposal: it introduces authentication, shared mutable state, backend operation and availability dependencies |

Source inspection shows why directly importing the current routes would not
be enough:

- [Root layout](../../web/carbon-panel/src/routes/+layout.svelte) performs
  authentication checks and server polling.
- [Server list](../../web/carbon-panel/src/routes/servers/+page.svelte) and
  [server store](../../web/carbon-panel/src/lib/stores/servers.ts) call RPCs
  directly; [CarbonShell](../../web/carbon-panel/src/lib/components/carbon/CarbonShell.svelte)
  also reads authentication, routing and node data.
- [Studio](../../web/carbon-panel/src/routes/modpacks/studio/+page.svelte)
  uses the authenticated REST helper, and
  [Console](../../web/carbon-panel/src/lib/components/server-console.svelte)
  uses RPC and WebSocket state. Replacing only the Connect transport would
  leave other network paths active.
- [Application CSS](../../web/carbon-panel/src/app.css) contains global rules;
  tooltip/toast providers and overlay placement also need deliberate ownership.

Extract the demonstrated portions of screens into shared feature components.
Both the installed application and homepage must render those same components.
For example, extract the actual lifecycle action/status group, Studio mod-list
editor or staged-batch summary instead of rebuilding them for marketing.
Sharing only buttons and colors while rewriting the feature UI is insufficient.
Production route wrappers keep their existing authenticated
clients, stores and navigation; the demo wrapper supplies its own state and
actions. Reuse generated protobuf types and existing formatters/validators.
Do not introduce a second backend API or recreate the entire runtime engine.

Create small interfaces around the selected fragments' actual dependencies:
server reads/actions, pack edits, update batches and console events as needed.
Model loading, success, cancellation and failure for the supported actions.
Fixture errors must use the same visible error components as production.
Keep fixture selection and simulation code in the demo entry, with no
`?demo=true` authentication bypass or runtime switch in the deployed panel.

Use an explicit build-time import boundary: the demo must not include
`authStore`, the production RPC/REST clients, global server/WebSocket stores
or a configurable live API URL. Network verification should allow only the
demo's static assets and reject API, WebSocket and third-party service calls.
Store session changes in memory by default. Reset, reload or closing the page
returns to the seeded example. The production authentication and
[framing headers](../../internal/rpc/security_headers.go) stay intact.

Each island should own its state, providers, overlay container and scoped
keyboard handlers, using unique IDs/prefixes. Scope theme and generated styles
to demo roots; do not apply the full
application reset to the marketing page or switch the document's theme.
Keep tooltips/dialogs inside the correct themed scope and ensure portals,
focus rings and z-index behavior work together without clipping. Global
shortcuts must not activate while focus is elsewhere on the homepage.

Svelte supports [rendering components to HTML](https://svelte.dev/docs/svelte/svelte-server)
and [hydrating a specific page region](https://svelte.dev/docs/svelte/imperative-component-api).
Use those capabilities to prerender the sample fragments during the website
build. Hydrate the hero fragment early and later fragments when near the
viewport or requested by the visitor. Keep fixtures, per-island IDs and
timestamps deterministic. Before readiness, disable actions with a clear
loading status; enable them after hydration without an extra tour gate.
If a chunk fails, retain that fragment's prerendered UI and offer Retry plus
documentation. A failure in one example must not disable the others. No
screenshot fallback is needed.

Proposed ownership: a dedicated entry under `web/carbon-panel/demo/`, shared
feature components under `src/lib/`, and an explicit build command using the existing
locked frontend dependencies. The website builder imports the resulting HTML,
CSS and hashed JS into its combined artifact. Generated output stays in the
ignored build directory. No Docker daemon or application service is required
to build or serve the fragments. A standalone full-panel demo is outside this
homepage plan.

Record the full UI source commit, fixture version and build inputs in a demo
manifest. Build the fragments and wrappers from one selected revision; CI must
reject a missing or mixed source rather than quietly using a stale demo.
Display its release context near the demo label. If the UI comes from a newer
development revision than the documentation pin, say so and keep the two
versions distinct. Update screenshot CI for regression evidence as necessary,
but screenshots are no longer the homepage presentation format.

### Feature narratives

**Build:** lead with the pack being assembled, not a set of provider badges.
Use the focused Studio mod-list fragment with an example loader/version label
and dependency feedback. Explain exports in the adjacent copy without
generating a bogus pack. Place datapacks beside this story with a short explanation. Candidate
heading: “Build the pack you want to play.”

**Update:** show the sequence “Prepare changes → Review the batch → Choose
when to apply.” The supporting sentence must explain that applying to a
running server restarts it. Candidate heading: “Prepare now. Apply when it
suits your server.” Link to the guide for supported staged operations.

**Rest:** show the difference between freeze and deep sleep without a
fabricated before/after chart. Use state labels, a short explanation of memory
behavior and a connection-triggered wake diagram. Candidate heading:
“Let quiet servers rest.” Keep cold-start and support conditions nearby.

**Operate:** use one small topology: players connect through the configured
proxy to server containers on compatible nodes; the panel manages them and
backups use configured storage. Distinguish player traffic from management
connections. Show one local machine and one optional remote node. Do not
animate servers migrating or machines appearing automatically.

### Installation and ownership

Move the clone command out of the hero. It obtains source; it does not install
the application. The installation section should state the prerequisites and
link to the currently published guide. If a command is shown, label exactly
what it does and verify it from a clean environment against the chosen release.

Explain persistent storage before startup, then first administrator creation,
server creation and the player address. Keep those steps concise; the guide
owns the full procedure. Use the real source and licence links as trust
evidence. Keep release/beta context accurate. Avoid synthetic testimonials,
unverified customer counts or recovery promises.

Suggested FAQ topics: required infrastructure, supported server software,
where world data lives, what sleep/wake changes, and whether a hosted account
is needed. Link to exact documentation sections for the detail.

## Interaction and responsive design

Use motion to explain an action or state change. Start from the shared
[motion contract](../design-system/motion-and-elevation.md): 100–150 ms for
small controls, around 200 ms for a fragment's local state transition, and a
maximum 300 ms for an infrequent explanatory transition. A single subtle
entrance for the hero artwork is optional. Keep content immediately available.

Remove the dependency on scroll choreography for architecture comprehension.
Avoid autoplay carousels, looping counters, cursor spotlights and perpetual
parallax. Reduced motion should show the same information in stable form.
An optional sleep/wake demonstration needs a named Play/Replay control and
a complete static explanation.

| Width or condition | Required behavior |
| --- | --- |
| 1440 and 1280 desktop | Hero has deliberate asymmetry; each story has one focused fragment at readable product scale; navigation fits without compression |
| 1024 tablet or small desktop | Stack a story and its fragment when side-by-side controls become too narrow; compress navigation based on content fit |
| 768 tablet | One main content column; fragments reflow naturally; no sticky multi-screen story |
| 390 mobile | Brand, Install and Menu in the header; primary action visible early; one compact native-size fragment after its supporting text |
| 320 mobile and 200% zoom | No page-level horizontal overflow or clipped labels; buttons can stack; code scrolls only inside its own labelled region |
| Keyboard | Logical order, visible focus, operable demo controls, Escape dismissal where appropriate, focus returned from overlays; no homepage-wide demo shortcuts |
| No JavaScript or failed demo load | Prerendered real components remain visible with interaction unavailable; narrative, documentation and install links still work |

Treat the mobile menu as an explicit navigation disclosure. If the design
instead becomes a modal drawer, implement dialog focus and background behavior
fully. Fragment dialogs need Escape, focus restoration and enough space to
show the task. Resizing or scrolling must not discard example edits.

## Asset production brief

| Asset | Production brief | Acceptance |
| --- | --- | --- |
| Terrain artwork | One original mineral landscape with stepped geometry, soft directional light and generous negative space; separate desktop and mobile compositions | Recognizable at mobile size; no text baked into art; clean copy area; no accidental changes to the brand mark |
| Interactive UI fragments | Shared feature components for server actions, mod lists and staged batches; typed fixtures, scoped styling and small compiled entries | Same components used inside the installed panel; coherent local actions; a sample label per example; no backend dependency |
| Architecture figure | Minimal diagram using approved iconography and real topology; distinct management and player paths | Text equivalent, labelled connections, readable mobile composition |
| Social preview | Reuse the approved identity, terrain crop and concise category statement at 1200 by 630 | Title legible at small size; versioned asset URL; correct alt text and metadata |
| Fonts | Existing IBM Plex faces, with only the necessary weights | Prefer local WOFF2 assets, preserve licence, stable fallback metrics |

Generate or commission the terrain as an image asset. Do not manufacture it
from layers of CSS boxes or hand-drawn placeholder SVGs. Product UI must render
from shared application components. Keep artwork separate from the live DOM
so each can respond properly to screen size and the controls remain usable.

Use static responsive AVIF/WebP artwork; there is no requirement for a runtime
3D engine. Reserve dimensions for both art and the initial demo view. Load the
small hero fragment deliberately and lazy-load the remaining islands, sharing
their common runtime instead of shipping a separate full application per block.

## Implementation sequence

| Phase | Concrete deliverable | Exit condition |
| --- | --- | --- |
| 1. Reconcile the source | Confirm the implementation branch, deployed homepage, domain, UI revision and docs pin; map data, auth and styling dependencies of the selected UI fragments | Domain/publication work is preserved; every claim has an owning source; fragment dependencies are understood |
| 2. Prove actual UI reuse | Extract one server status/action group, use it in the installed panel and homepage, and complete a simulated restart with reset | Both consumers use the same feature component; no network/auth leakage; CSS, providers, responsive behavior and prerender/hydration work |
| 3. Resolve the visual design | Three hero compositions and a Studio-section composition at desktop/mobile sizes, based on actual component proportions; compare the existing grid with refined rails and a local chunk detail | Product, purpose and primary action are clear; each fragment demonstrates one task at readable scale; the backdrop aligns with content and stays quiet behind it |
| 4. Complete the fragments and artwork | Add the mod-list and staged-batch examples, optional server-list/idle examples, original terrain, scoped styles and version manifest | Advertised interactions work with truthful sample labels, independent example state, fallbacks and budgets |
| 5. Recompose the homepage | Static narrative, distributed component islands, refined section-local grid, exact copy, compact navigation and direct section links | Each story has its own relevant UI fragment; the grid changes density with the content; no full-panel embed or screenshot tour; all install actions work |
| 6. Verify and refine | Shared-view regression, demo interactions, network isolation, keyboard, responsive, reduced-motion, performance and combined-site checks | Acceptance criteria below pass; publication preserves the docs and design-system routes |

Keep the marketing shell in the existing static site and compile the embedded
Svelte experience separately. This extends the work into shared application
feature-component extraction and the website build; it is not solely a landing CSS change.
There is no need for an external shared-component package. Consolidate the
landing stylesheet by foundation and section instead of adding another
trailing layer of overrides. Preserve supported deep links, using meaningful
destinations for sections that are consolidated.

Likely source changes are `landing/index.html`, `landing/assets/styles.css`,
`landing/assets/script.js`, the approved new imagery, selected feature components
and their production wrappers, demo fixtures/entry/build configuration, and
`scripts/build_website.py`/`scripts/check_website.py`. Extend relevant CI path
filters and tests so shared-component changes rebuild and validate the fragments. Update
the publishing and frontend knowledge when implementing this ownership model.
Existing screenshot workflows remain useful for visual regression and README
assets. Keep shared-token changes and the documentation pin deliberate.

Before editing those paths, retrieve their governing concepts. Update the
governing knowledge when implementation changes durable behavior. Record a
project-wide brand or shared-token decision in an ADR and the ADR index only
if that decision is actually adopted.

### Source reconciliation required before implementation

This review used working branch `feat/oss-remaining-slices` at
`38cfd2263d20ac97bc9051be461201598ca932ad`. Its landing page predates the live
showcase and custom domain. The locally available `origin/main` snapshot
`b87190df731b8534ff08c12ed8b191f7760df40d` contains those additions.
Do not implement by replacing newer homepage work with this branch's older
HTML. Recheck live source state at implementation time.

The relevant existing work is MINE-336 and
[PR 354](https://github.com/athNdev/carbon-panel/pull/354). The tracker's text
still mentions missing showcase PNGs, while both the live page and that main
snapshot contain them. Preserve the useful capture pipeline for regression
and other surfaces while replacing homepage screenshots with the demo;
tracker completion alone does not establish screenshot freshness. The historical
[homepage brand rollout plan](homepage-brand-redesign.md) describes an earlier
baseline and should not supply current hosting facts.

## Acceptance criteria

**Design and comprehension.** A new visitor can identify Minecraft server
management, self-hosting and the installation path within five seconds. Use a
small review with five people from the audience hypotheses; aim for at least
four to answer all three correctly without prompting. Then ask them to locate
Studio and explain what happens when a staged update is applied. These are
proposed review criteria, not measured results.

**Visual craft.** The real product appears in the opening experience. Every
section has a clear purpose and a distinct composition. The mark, type and
spacing remain consistent at 320, 390, 768, 1024, 1280 and 1440 px. The terrain
feels connected to Minecraft while the interface stays calm and legible.
Grid rails align with the same content tracks at every breakpoint, soften
around reading areas and remain outside the actual UI surfaces. Check the
decorative treatment at 100%, 125% and 200% zoom for doubled or uneven lines,
seams and interference with focus indicators. It needs no animation or
JavaScript to look complete.
Review actual screenshots at each size; do not qualify the design from token
values alone.

**Accessibility.** Check WCAG 2.2 AA requirements, including text contrast,
visible focus, semantic headings, text alternatives and operability. Aim for
44–48 px touch controls. The AA target-size minimum is 24 by 24 CSS pixels
subject to its exceptions; evaluate spacing exceptions where relevant. Test
200% zoom, reduced motion, forced colors and a screen reader. See the
[W3C standard](https://www.w3.org/TR/WCAG22/) and
[target-size guidance](https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum.html).

**Performance.** Proposed starting budgets: at most 25 KB compressed marketing
JavaScript plus 150 KB compressed JS for the hero fragment, including
its Svelte/runtime dependencies; 40 KB marketing CSS plus 60 KB scoped demo
CSS; 150 KB shared fonts; roughly 1 MB initial mobile transfer including art,
the initial fragment HTML, fixtures and selected bundles. These are feasibility
targets, not measured bundle sizes. Load Studio/staging fragment chunks near
their sections and measure their incremental transfer separately; shared
runtime bytes count once. Exclude Monaco,
full-file editing and unrelated routes from the initial dependency graph.
Pause fixture timers and console replay when hidden; resume without inventing
elapsed server activity. Keep the install CTA usable while hydration runs.
Target LCP at or below 2.5 seconds, INP at or below 200 ms and
CLS at or below 0.1 at the 75th percentile where field data exists. Lab results
are useful before publication but do not establish field performance. See
[Google's metric thresholds](https://web.dev/articles/defining-core-web-vitals-thresholds).

**Content and behavior.** Each fragment uses the same feature components as production,
from its recorded source revision. Sample values and simulated outcomes are
identified throughout; they are not usage statistics or operational proof.
Search, selection, supported actions, errors and per-example reset work.
Verify scenarios through their user-visible outcomes and state transitions,
including discard preserving the original sample configuration. No-JavaScript
and failed-chunk states retain actual prerendered UI and the installation path.
Mobile Install remains easy to find. Copy feedback reports clipboard success.

**Isolation and regression.** Inspect the demo bundle dependency graph and
exercise every fragment while recording requests: only approved
static assets may load. No production auth storage, RPCs, REST APIs,
WebSockets or provider calls. Verify that interactions affect only the local
fixture instance and that repeat visits/reset are deterministic. Compare the
installed panel's feature component and its homepage instance with matching
fixtures and widths; extraction must
preserve production loading, errors, validation and authorization behavior.
The demo's accessible HTML and visible product controls must not be replaced
with images to meet a performance budget.

**Repository and publication.** Run the narrow landing checks, then build and
validate the combined website with the current publishing contract:

    python3 -m unittest scripts.test_website scripts.test_landing_page scripts.test_design_system_page
    python3 scripts/check_brand_assets.py
    python3 scripts/build_website.py
    python3 scripts/check_website.py
    go run ./cmd/knowledge-lint -root . -fix
    go run ./cmd/knowledge-lint -root .

The shared-component extraction requires the frontend frozen-lockfile install,
`bun run check`, `bun run test` and `bash scripts/web-build.sh`, plus the new
explicit demo build and interaction tests. Run targeted existing view tests
while iterating. Backend or proto changes are not expected for the fixture
demo; any such scope expansion needs its own affected-surface verification.
Review `/`, `/docs/` and `/design-system/`, the pinned documentation label,
canonical domain, social cards, local links and the mobile installation
handoff. Publication remains a subsequent action; this plan does not deploy.

## Recommended first design milestone

Prove one shared server status/action component and its local restart
interaction, then compose it into the hero at 1440 by 900 and 390 by 844.
Include the refined structural grid and compare one restrained chunk-detail
variation. Follow with one Studio section showing only its mod-list fragment
and a quieter continuation of the same backdrop. This tests
actual component reuse, reading size, responsive behavior and embedding cost
while establishing the page's rhythm: a clear idea, a useful piece of the
real interface, and room to appreciate both.
