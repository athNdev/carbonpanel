# ADR 0042: SvelteKit 3 security migration with narrow compatibility shims

Status: Accepted (OSS-393)

## Context

Four open Dependabot alerts affect `@sveltejs/kit` in
`web/carbon-panel/package.json`: GHSA-3r8g-jcqq-3p5j (high, fixed in 3.1.1),
GHSA-6cf5-gc4q-fm3x (medium, fixed in 3.1.1), GHSA-pfhg-q3qv-9q6r (low, fixed
in 3.0.0) and GHSA-27mw-82g8-phjj (low, fixed in 3.0.0). No 2.x patch exists.
Kit 3 requires Vite 8, `@sveltejs/vite-plugin-svelte` 7 and Svelte 5.57.1 or
newer. The `typescript` and `@opentelemetry/api` peers are optional. The
security migration kept the repository's existing TypeScript 5.9.3.

## Decision

1. **Migrate only the required toolchain**: `@sveltejs/kit` 2.70.4 → 3.4.0,
   `vite` 7.3.8 → 8.3.8, `@sveltejs/vite-plugin-svelte` 6.2.4 → 7.3.1, and
   `svelte-check` 4.3.4 → 4.7.8. `svelte-check` is required because older
   versions cannot read the new Kit 3 config location and report spurious
   diagnostics. Svelte, TypeScript, adapter-static, Tailwind and Prettier are
   unchanged; all other ecosystems stay on their pinned toolchains.
2. **Keep Prettier 3.4.2.** Prettier 3.10.1 (OSS-390 PR #488) fails lint with
   `getVisitorKeys is not a function` across all Svelte files for every
   available `prettier-plugin-svelte` in range; no compatible resolution was
   demonstrated. The advisory lane does not adopt it.
3. **Move SvelteKit config out of `svelte.config.js`** into `sveltekit.config.js`,
   passed to `sveltekit(...)` in `vite.config.ts`. Kit 3 rejects a
   `svelte.config.js` file and no longer accepts the `kit` namespace.
4. **Retain the existing `$lib` import style** via the documented
   `alias: { $lib: 'src/lib' }` compatibility option and explicit tsconfig
   paths (the latter also let the Bun test runner resolve `$lib`). This avoids
   rewriting hundreds of imports to `#lib`. It emits a deprecation warning and
   a `tsconfig_invalid` advisory from Kit's validator; neither affects type
   checking (`svelte-check` reports 0 errors and 0 warnings).
5. **Rename `src/lib/components/server/` to `src/lib/components/server-ui/`.**
   Kit 3 treats any `/server/` directory in `src` as server-only and refuses to
   bundle those UI components into the browser. The rename keeps the components
   client-safe; no server capability or authorization behavior changes.
6. **Migrate source call sites to the new APIs**: `$app/environment` →
   `$app/env`, `$app/stores` → `$app/state`, `base` image URLs → `asset()`,
   typed `resolve('/route/[param]', { param })` route IDs, and `goto` options
   `noScroll`/`keepFocus` → `reset`. Query suffixes are appended to the
   resolved pathname and branded as `ResolvedPathname` because Kit 3's typed
   `resolve` does not accept dynamic query suffixes.

## Routine successor qualification (OSS-394)

The routine bundle in bot PR #581 combined compatible updates with toolchain
bumps that are not qualified. Its successor is qualified by splitting that
bundle: the action SHA-pin refreshes, Go module patches, docs Astro updates,
the Monaco 0.57 update with its worker subpath fix, and the compatible web
manifest bumps are retained. Prettier stays at 3.4.2 with its 3.x plugin,
TypeScript at 5.x, and the review driver at Playwright 1.61.0; 1.65.1's
Chromium no longer retains a failed import in the document module map, so the
real editor-dialog regression loses a required assertion. SvelteKit remains at
3.4.0 — the 3.2.0 hunk in the bot bundle is a downgrade and is discarded. No
test-script condition or `happy-dom` dependency is removed. The split is
verified with real `bun run lint`, `bun run check`, the Bun test suite and both
Chromium harnesses; advisories without a patched release or needing a
coordinated migration follow the recurring path in
`docs/dependency-security-review.md`. Per-directory security-update groups in
`.github/dependabot.yml` keep security fixes able to land independently of any
held routine bump.

### Formatter and TypeScript follow-up

Prettier 3.10.1 and the Tailwind formatter plugin 0.7.4 address the
`getVisitorKeys` crash from the older Tailwind adapter. The Svelte formatter
plugin stays on 3.x and loads before the Tailwind formatter plugin to preserve
class sorting.

TypeScript 6.0.3 satisfies the peer ranges of SvelteKit, svelte-check and
typescript-eslint. The frontend manifest uses `~6.0.3` to respect the lint
parser's `<6.1.0` limit. The compiler configuration keeps the relative
`$lib` paths and drops the deprecated `baseUrl`.

## Consequences

- Kit emits `config_option_deprecated_alias` and `tsconfig_invalid` warnings by
  design for the compatibility shims; migrating to `#lib` subpath imports is
  future work, not part of this security lane.
- `svelte.config.js` is deleted; the lint config and four e2e harnesses read
  `sveltekit.config.js` instead. The old-path reference in a historical
  `docs/research` permalink is intentionally left unchanged.
- `$lib`-based tests rely on explicit tsconfig paths; keep them in sync with
  the alias.
- No release, deployment, registry publication or general Carbon Panel
  release action is part of this decision.
