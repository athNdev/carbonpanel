# Recurring review: advisories without a patched release

Some Dependabot alerts report no `first_patched_version`. Others need a
coordinated migration to adopt the patched release. For example, SvelteKit 3
requires Vite 8 and `@sveltejs/vite-plugin-svelte` 7, and its optional TypeScript
peer is `^6.0.0`. Both cases need maintainer review before attempting an update.

## Cadence and owner

- **Weekly**, during the routine dependency review that follows the
  `routine-dependencies` multi-ecosystem bundle in `.github/dependabot.yml`.
- Owner: the maintainer handling that week's dependency review (OSS-runtime).
- Trigger: any open Dependabot alert whose advisory reports no patched
  release, or whose only available fix requires a coordinated major migration
  (record the required peer/toolchain changes as the reason).

## Review steps

1. Query the open alerts and read `security_vulnerability.first_patched_version`:

   ```sh
   gh api --paginate 'repos/athNdev/carbonpanel/dependabot/alerts?state=open&per_page=100'
   ```

2. For each alert with **no** patched version, check the upstream advisory and
   the installed version. Record whether a fixed release now exists, whether a
   compatible upgrade path is available, and the exact reason if not.
3. Confirm the dependency still has a reachable code path before deciding
   whether to accept the risk, upgrade, or replace it. Do not dismiss a HIGH
   alert merely because it has no fix.
4. When a patched release appears, open or refresh the dependency bump through
   the normal Dependabot entry for that manifest directory and remove the alert
   from this review list after the fix is verified.
5. Keep the decision and the verification evidence in the tracker (Jira) and
   the aggregate dependency PR; do not keep an activity log in `knowledge/`.

## Resolved advisories

- GitHub withdrew the
  [picomatch advisory GHSA-fc5q-39xc-vjpf](https://github.com/advisories/GHSA-fc5q-39xc-vjpf).
  The docs lockfile still uses `picomatch` 4.0.7. Continue to review new alerts
  against the installed version during the weekly audit.
- `@sveltejs/kit` — **resolved**. The four 2.x alerts were fixed by the narrow
  Kit 3.4.0 migration in PR #565 (ADR 0042); no SvelteKit alert remains open.
  Keep at least Kit 3.4.0 when qualifying the routine bundle.
- `github.com/gorilla/websocket` — **resolved** by the routine bundle. GO-2026-6278
  (GHSA-w67g-5rqw-f597, weak PRNG for the client mask key) affects v1.5.3 and
  has no tagged fixed release; the first fixed version is the
  `v1.5.4-0.20240701034025-d67f41855da4` pseudo-version. That pseudo-version
  adds the `golang.org/x/net/proxy` requirement, so keep the indirect
  `golang.org/x/net` entry in `go.mod` until a tagged release supersedes it.

## Routine toolchain constraints

- Upgrade `prettier` and the Tailwind formatter plugin together. Bot PR #581 hit
  `getVisitorKeys is not a function` with Prettier 3.10.1 and the old Tailwind
  formatter plugin. [Tailwind formatter plugin 0.7.2](https://github.com/tailwindlabs/prettier-plugin-tailwindcss/releases/tag/v0.7.2)
  includes the fix for Prettier 3.7 and newer with the Svelte plugin. Keep the
  current plugin order to preserve class sorting. Qualify formatter changes
  with frontend lint and the generated-output check.
- Keep `typescript` on the 6.0.x line while the ESLint parser requires
  `<6.1.0`. Kit 3.4.0 and svelte-check 4.7.8 accept 6.0.x. The 7.0.2 bump in
  bot PR #581 failed with `typescript_compiler_api_missing` when loading the
  Vite configuration. Qualify a new compiler line with diagnostics, lint and
  the production and browser builds.
- `playwright` — the review driver stays at 1.61.0. The 1.65.1 bump in bot PR
  #581 passes the inventory harness but breaks the editor-dialog regression:
  its Chromium does not retain a failed bootstrap import in the document module
  map, so the required "Reload the page to try again" assertion never fires.
  Hold it until the whole real browser gate passes on the newer driver.

Security fixes continue to flow independently of these held version updates:
`.github/dependabot.yml` defines a per-directory `applies-to: security-updates`
group for GitHub Actions, Go, the console Bun manifest, the docs npm manifest
and the e2e Bun manifest. A held routine bundle therefore never blocks an
urgent security patch for a different manifest.
