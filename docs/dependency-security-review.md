# Recurring review: advisories without a patched release

Some Dependabot alerts name a vulnerable dependency for which the advisory has
no `first_patched_version` (for example `picomatch` 4.0.7,
GHSA-fc5q-39xc-vjpf). Others have a patched release whose only viable upgrade
path is a coordinated major migration of peer tooling (for example SvelteKit 3
requires Vite 8, `@sveltejs/vite-plugin-svelte` 7 and TypeScript 6). Neither
case produces a Dependabot version update that can be merged on its own, so
these alerts need an explicit human review path.

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
   gh api repos/athNdev/carbon-panel/dependabot/alerts --paginate
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

## Current entries

- `picomatch` 4.0.7 in `docs/carbon-panel/package-lock.json` — GHSA-fc5q-39xc-vjpf
  (HIGH) had no `first_patched_version` at the OSS-390 aggregate. No alert is
  open at the OSS-394 successor qualification (`gh api
  repos/athNdev/carbon-panel/dependabot/alerts?state=open` returns 0); re-check
  weekly and restore the entry if it reappears.
- `@sveltejs/kit` — **resolved**. The four 2.x alerts were fixed by the narrow
  Kit 3.4.0 migration in PR #565 (ADR 0042); no SvelteKit alert remains open.
  Kit 3.4.0 must not be downgraded to the 3.2.0 offered by the bot bundle.
- `prettier` — held at 3.4.2 with `prettier-plugin-svelte` 3.x (ADR 0042,
  decision 2). The 3.10.1 bump in bot PR #581 fails real `bun run lint` with
  `getVisitorKeys is not a function` for every plugin version in range; hold it
  until a verified compatible formatter stack passes lint.
- `typescript` — held on the 5.x toolchain. The 7.0.2 bump in bot PR #581 fails
  `bun run check` with `typescript_compiler_api_missing` (TypeScript 7 removed
  the JS compiler API that SvelteKit's config loader uses; the supported range
  is `typescript@^6`) and prevents the real Chromium harness from loading Vite
  config. Hold it until `check` and the browser checks are green together.
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
