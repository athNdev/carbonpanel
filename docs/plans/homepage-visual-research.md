<!-- SPDX-License-Identifier: MPL-2.0 -->
# Carbon Panel homepage: further visual research

Research date: 12 November 2026. This is a proposal for the existing homepage,
not an implementation approval or a change to the product design system.

The current direction is graphite, warm white, a stepped mineral landscape,
subtle grid structure and small pieces of the real panel UI. The user wants a
more striking, artistic result while retaining that identity. They specifically
rejected the grey halo around the hero. The opportunity is stronger authorship
and contrast in the composition, rather than replacing that halo with another
ambient effect.

## Evidence and limits

The references below are first-party product pages or accounts by their creators.
The findings in the first table come from their published text. They establish
design intent and documented techniques, not a fresh hands-on observation of
every animation. The historical case studies retain their original dates.
Recommendations are design judgments for Carbon; they are not measured claims
about conversion or user preference.

| Reference | What the source establishes | Application to Carbon |
| --- | --- | --- |
| [Teenage Engineering: OP–1 field](https://teenage.engineering/products/op-1) | The product story repeatedly links refinement to specific details: its lowered aluminium body, flush display, materials, graphics and physical controls. | Give the terrain and UI the attention of a carefully made object. A deliberate silhouette, material detail and useful control states are stronger craft signals than adding a decorative layer everywhere. |
| [Stripe: designing an interactive globe](https://stripe.com/blog/globe), 2020 | Stripe chose a single interactive object to express the actual scope of its business. The team prototyped the complete scene early and required a static fallback if performance was inadequate. | Carbon's signature object should express a Minecraft world being made and cared for. The lesson is a meaningful visual metaphor, not that Carbon needs a globe, WebGL or perpetual rotation. |
| [Rauno Freiberg: Crafting the Next.js Website](https://rauno.me/craft/nextjs), 2023 | The creator describes responsive grid lines with masked ends, consistent focus treatment, fluid typography, and small feature-specific animations. The static version shipped before the extra motion. | Finish the page's static composition first. Use occasional local grid details and precise interaction feedback; preserve open space. Do not turn every section into a moving demonstration. |
| [Rauno Freiberg: Designing Depth](https://rauno.me/craft/depth), 2024 | The essay explains layering, framing and choreography, including a marketing composition whose depth comes from its subject matter rather than unrelated objects. | Use a foreground terrace, a distant ledge or a cropped view of the same terrain to create depth. Keep actual UI readable and flat; perspective belongs to the artwork, not the controls. |
| [Vercel Geist: Grid](https://vercel.com/geist/grid) | The guide documents responsive tracks, clipped guides and solid cells that occlude the grid. It advises against excessive nesting and says the guides are decorative. | Let the grid stop, disappear or change density where the story needs it. Its role is to organize the page, with occasional chunk-like subdivision, rather than fill every dark area. |
| [Linear: Design is more than code](https://linear.app/now/design-is-more-than-code), 2025 | Linear's co-founder argues for defining the problem and preserving conceptual exploration instead of allowing implementation tools to determine the design too early. | Compare a few materially different compositions before another round of CSS embellishment. Judge the result by whether it feels specific to Carbon and communicates ownership and creativity. |

## Direct visual observations

The following pages were also inspected in a desktop browser on the research
date. These are observations of selected views, not whole-site audits. Their
artwork is reference material; any new Carbon artwork should remain original.

| Page | Observed composition | Transferable principle |
| --- | --- | --- |
| [Teenage Engineering OP–1](https://teenage.engineering/products/op-1) | A pale field, restrained type and a dramatically enlarged physical instrument, cropped by the viewport. Surface finish and the object's proportions carry much of the visual interest. | Let one carefully composed object dominate. Carbon's terrain can have a stronger silhouette and a more intentional crop without needing a larger surrounding glow. |
| [Lusion](https://lusion.co/) | A very large, contained scene of glossy blue, black and white forms beneath compact introductory text. The artwork has its own space. A loading screen was also observed during entry; no loading-time measurement was taken. | Reserve enough room for the artwork to be experienced. Borrow the confidence of the composition, while keeping Carbon's initial content available without a blocking intro. |
| [Linear](https://linear.app/) | A large, left-aligned white headline on black, subdued supporting copy and a broad application representation beneath it. The hierarchy relies on pronounced differences in scale and density. | Give each view one dominant element. Carbon should retain its requested small, usable UI fragments rather than adopt the reference's broad application view. |
| [Raycast](https://www.raycast.com/) | A focused Clipboard History window sits within a larger framed stage, with a contextual action menu and concentrated red artwork behind it. | Stage one understandable task with enough surrounding space to read it. Carbon can use this principle for Studio without copying Raycast's palette, framing or autonomous demonstration behavior. |

The common lesson is selective emphasis. A large object, a strong typographic
gesture or a working interface can each carry a section; giving every section
all three would dilute that hierarchy.

## Ranked shortlist

These priorities are comparative estimates for the existing implementation.
“Effort” includes design and visual verification, not only writing code.

| Priority | Proposal | Impact / fit / effort | Specific change and limit |
| --- | --- | --- | --- |
| 1 | Make the landscape tell a small story | High / high / medium | Develop the existing terrain into one original, recognizable place: a sheltered entrance, a small constructed threshold or bridge, and a deliberate route through the steps. Let one warm detail suggest habitation. Compose a clear silhouette and two complementary crops. Avoid a busy Minecraft screenshot, scattered cubes or an unrelated futuristic object. |
| 2 | Give the page an editorial rhythm | High / high / low–medium | Alternate a spacious, image-led moment with a tighter working demonstration and a quiet text transition. Make Studio the broadest product moment, without showing the whole application. Reduce repeated eyebrow/headline/paragraph/panel patterns. Use asymmetry, carefully chosen line breaks and size contrast before adding more sections. |
| 3 | Make Studio feel satisfying to operate | High / high / medium | When a sample mod brings a dependency, show the relationship locally; when it enters the pack, give the row and count clear feedback; when client-only mods are excluded, visibly explain the export result. Keep each response short and interruptible, preserve state between tabs, and retain a static indication of every result. Reuse production components and keep all sample behavior labelled. |
| 4 | Turn the grid into an intentional composition tool | Medium / high / low | Keep the new breathing room around outer rails. Remove some interior lines around reading areas, concentrate subdivisions beside art or a diagram, and use one local stepped boundary or registration mark. Avoid animated scans, pointer spotlights or coordinates that look like real telemetry. |
| 5 | Add one sharply different visual beat | Medium–high / medium / medium | Test either a tightly cropped mineral detail with generous typography or a warm, light editorial specimen around the design-system introduction. Keep the panel fragments in their real theme. This is an option to compare visually, not a reason to add a second palette across the whole site. |
| 6 | Add bounded depth only after the still composition works | Medium / medium / medium–high | A very small viewpoint response on the decorative terrain, or an explicit “Inspect the world” control, could be delightful. Prefer two or three prepared layers or views before a full 3D scene. Keep reading and normal scrolling independent; disable the effect for reduced motion and small/coarse-pointer contexts. |

The best next combination is **1 + 2 + 3**: a more authored world, stronger page
rhythm, and a demonstration that rewards a few clicks. These address identity,
composition and product appeal separately. Adding all six simultaneously would
make it harder to know which choices improve the result.

## A proposed sequence for Carbon

This is a composition study for the existing content, not a request to lengthen
the page. Keep concise explanations of hosting, maintenance and installation.

1. **A place worth looking after.** Open with the landscape, a clear statement
   about self-hosted Minecraft and the compact server control. Leave a visible
   interval between the most interesting part of the terrain and the UI.
2. **The act of making.** Give Studio the broadest interactive stage. Place its
   heading above or offset from the demo to break the repeated two-column
   pattern. Keep Discover, Your pack and Export as focused views.
3. **Quiet confidence.** Present staging and operational features at a calmer
   scale. Align the real interfaces precisely and let their behavior provide
   the detail, without another large decorative scene.
4. **A moment of craft.** Use the design-system introduction as an editorial
   pause: one oversized typographic specimen, a few real token swatches and the
   existing visible link. Compare the graphite treatment against one warm-white
   inset specimen; keep the actual application theme unchanged.
5. **Return to the place.** Use a second, closer crop of the same world near the
   final action. This gives the ending a relationship to the opening without
   introducing a collection of unrelated illustrations.

Keep the existing font families. The typography opportunity is optical spacing,
shorter deliberate line breaks, a clearer difference between headline and
annotation sizes, and fewer repeated uppercase labels. On mobile, compose a
specific terrain crop and shorten gaps deliberately instead of merely stacking
the desktop arrangement.

One optional signature interaction is worth prototyping: the warm light inside
the terrain's entrance could respond to the sample server's completed
start/stop state. It would connect the real control to the imagined world.
Keep the light localized inside the artwork, retain the textual status as the
authoritative signal, and make the association explicit as a sample. Do not
reintroduce the rejected halo or suggest that a real server is being operated.
This is a lower-priority experiment after Studio's useful feedback is polished.

## Concrete visual experiments

1. **The world as a crafted object.** Keep the graphite palette and existing
   terrain vocabulary. Recompose the hero around the landscape's silhouette and
   warm opening, with the server controls placed in a quiet foreground area.
   Carry one detail crop into the lower page so the opening and closing feel
   connected. This is the closest continuation of the current design.
2. **The maker's workbench.** Give Modpack Studio a wider, more prominent stage.
   Surround the real UI with a sparse editorial explanation of the current
   action rather than extra interface chrome. A local dependency relationship
   becomes the signature interaction. The terrain remains the emotional opening.
3. **The field journal.** Add a restrained editorial counterpoint: mineral
   studies, fine rules and unusually confident typographic scale. A single light
   specimen section can establish contrast with the surrounding dark page.
   Keep technical labels meaningful and avoid invented “specification” numbers.

These experiments need visual comparison at the same desktop and phone widths.
They do not require replacing the approved logo, introducing a new font family,
changing application tokens or rebuilding the whole page.

## What to avoid

- Another broad glow, blur cloud or moving light behind the hero. The user's
  feedback already establishes that this treatment is a poor fit here.
- Floating UI panels on several angles, fake browser windows, or the full
  dashboard squeezed into a marketing illustration. They weaken the current
  advantage of real, readable, usable fragments.
- Scroll hijacking, a cinematic loading intro, long pinned sections or a custom
  cursor that changes normal interaction. Any sense of spectacle should survive
  with those effects removed.
- Autoplaying demos that reset user choices, repeated marquees, particle fields,
  animated text that delays reading, or decorative sounds.
- Treating “cutting edge” as a requirement to add a new graphics dependency.
  A distinctive static composition and excellent interactions can do more for
  this page than an expensive generic effect.

## Motion and performance boundaries

These are implementation constraints for a future experiment, not claims that
the present page has passed a formal accessibility or field-performance audit.

- Non-essential interaction animation must have a reduced-motion path. W3C's
  [SC 2.3.3 guidance](https://www.w3.org/WAI/WCAG22/Understanding/animation-from-interactions.html)
  specifically discusses parallax and user control. This criterion is Level AAA;
  respecting the preference is a useful design baseline regardless of the
  selected conformance target.
- Automatically moving content lasting more than five seconds alongside other
  content generally needs a pause, stop or hide mechanism under
  [SC 2.2.2](https://www.w3.org/WAI/WCAG22/Understanding/pause-stop-hide.html).
  A local, user-triggered response is a better fit than an ambient loop here.
- Prefer transform and opacity for animation; inspect other properties for
  layout or paint cost. Do not add `will-change` everywhere. This follows
  [web.dev's animation guidance](https://web.dev/articles/animations-guide).
- Keep a useful still image and working text/links before optional effects load.
  Reserve scene dimensions and defer below-fold media. Assess changes against
  actual loading, responsiveness and stability measurements. Google's current
  good Core Web Vitals thresholds are LCP ≤2.5s, INP ≤200ms and CLS ≤0.1 at the
  75th percentile, segmented by device; a local preview is not field evidence.
  See [Web Vitals](https://web.dev/articles/vitals).

## Decision test

Compare each experiment with the current page and ask:

- Is it recognizable as Carbon before reading the name?
- Does the first view still explain Minecraft, self-hosting and the next action?
- Does Studio invite a useful interaction without needing instructions?
- Is the page still compelling with animation disabled?
- Does the phone composition look designed for the screen, not merely stacked?
- Does the added detail justify its loading cost and maintenance burden?

The first three priorities should be reviewed as visual studies before changing
the working homepage again.
