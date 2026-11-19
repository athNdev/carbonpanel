# ADR 0042: SvelteKit 3 security migration with narrow compatibility shims

Status: Accepted (OSS-393)

## Context

Four open Dependabot alerts affect `@sveltejs/kit` in
`web/carbon-panel/package.json`: GHSA-3r8g-jcqq-3p5j (high, fixed in 3.1.1),
GHSA-6cf5-gc4q-fm3x (medium, fixed in 3.1.1), GHSA-pfhg-q3qv-9q6r (low, fixed
in 3.0.0) and GHSA-27mw-82g8-phjj (low, fixed in 3.0.0). No 2.x patch exists.
Kit 3 requires Vite 8, `@sveltejs/vite-plugin-svelte` 7 and Svelte 5.57.1 or
newer. The `typescript` and `@opentelemetry/api` peers are optional, and there
is no stable TypeScript 6 release, so TypeScript can remain on 5.9.3.

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
