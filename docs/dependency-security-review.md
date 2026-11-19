# Recurring review: advisories without a patched release

Some Dependabot alerts name a vulnerable dependency for which the advisory has
no `first_patched_version` (for example `picomatch` 4.0.7,
GHSA-fc5q-39xc-vjpf). No Dependabot version update or security group can create
a fix PR in that case, so these alerts need an explicit human review path.

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
  (HIGH) has no `first_patched_version`; no safe fixed version available at the
  time of the OSS-390 aggregate. Re-check weekly.
- `@sveltejs/kit` 2.x in `web/carbon-panel/package.json` — four alerts whose fix
  is 3.1.1, which peer-requires vite 8, `@sveltejs/vite-plugin-svelte` 7 and
  TypeScript 6. Re-check weekly until a dedicated major migration is landed.
