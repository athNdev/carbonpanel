# Manual review harness

A small browser driver for reviewing the console the way a person would: it
logs in, clicks, asserts what is on screen, and writes one screenshot per named
step plus a Markdown report. The report and images are the evidence a review
quote can point at.

This is deliberately not an end-to-end test suite. It exists for the things a
unit test cannot see — a table showing `Invalid Date`, a tab indicator sitting
on the wrong tab, a scrollbar appearing under the pointer.

## Requirements

The disposable stack stores each named session under
`~/.cache/carbon-panel-review/sessions/<name>`. `REVIEW_STACK_NAME` selects a
session; a custom `REVIEW_DATA_DIR` must resolve to that exact owned location.
An ownership marker and a matching Docker container label are required before
stopping or cleaning a session. Existing unmarked data and containers are not
automatically adopted. `down` preserves the session's data; the explicitly
named `clean` command deletes only a matching owned session. The selected port
is in the session's `port` file. Listeners bind to loopback.
Seeding waits for asynchronous starter-container creation before starting it;
error/unhealthy state or readiness timeout fails setup. The bounded timeout is
180 seconds by default (`REVIEW_SEED_TIMEOUT_SECONDS`, range 1–600).
The starter uses the first free game port from 25585 through 25605, keeping
the ordinary 25565 development/test port available. `REVIEW_GAME_PORT` changes
the start of that 21-port range for a fresh session.

- Node 24.
- Bun and the locked Playwright package used by both review profiles.
- A running panel with a local account. Credentials are read from the
  environment and never written to the report.

```sh
node scripts/review/setup-browser.mjs
node scripts/review/browser-check.mjs
```

## Run

The shared entrypoint selects an explicit profile. Docker stack commands run in
WSL or Linux; the locked browser driver is the same on Windows and Linux.

```sh
node scripts/review/run.mjs driver setup
node scripts/review/run.mjs single-seeded up
node scripts/review/run.mjs dual-ownership up --instances 2
node scripts/review/run.mjs single-seeded review --plan scripts/review/plan-console.json --base http://127.0.0.1:8080 --out /tmp/review
node scripts/review/run.mjs dual-ownership review
```

Both profiles write `review.json`, schema version 1, alongside their detailed
Markdown/findings. Its `scope` is `ui-review`; `passed`, `failed`, `incomplete`
and `review-required` describe the recorded checks, not gameplay acceptance.
URL credentials/query parameters are excluded. A critical login/setup failure
returns failure rather than continuing through screenshots of the login page.
Default artifacts stay in the checkout's ignored `tmp/review-output/` tree;
single review accepts `--out`, and dual review accepts the `REVIEW_OUT` parent.

```sh
export PANEL_REVIEW_USER=you
export PANEL_REVIEW_PASSWORD=...

node scripts/review/journey.mjs \
  --plan scripts/review/plan-console.json \
  --base http://127.0.0.1:8080 \
  --out /tmp/review
```

Exit code is 0 when recorded steps passed, 1 when a step failed, 2 on a setup
error or empty plan. Screenshots of failed steps are written as `FAILED-<name>.png` with the
full page, so a clipped element is still visible.

Environment overrides:

| Variable                                      | Purpose                                |
| --------------------------------------------- | -------------------------------------- |
| `PANEL_REVIEW_USER` / `PANEL_REVIEW_PASSWORD` | login (names configurable per plan)    |
| `REVIEW_CHROME`                               | explicit Chromium executable path      |

Both profiles use the checked-in `scripts/manual-review/e2e` package and frozen
lockfile through `scripts/review/browser.mjs`. They emit `browser.json` with the
driver version, actual browser version, executable path and override status.
Setup installs the exact driver's Chromium build into the checkout's ignored
`tmp/review-browsers/<driver-version>` directory, without scanning or garbage
collecting shared home-directory browser caches. `PLAYWRIGHT_BROWSERS_PATH`
is an explicit cache override. On disposable Linux CI, `--with-deps` also installs OS
browser dependencies; ordinary setup does not request that system change.

## Plan format

```json
{
  "name": "console-smoke",
  "title": "Console smoke",
  "viewport": { "width": 1440, "height": 900 },
  "login": {
    "path": "/login",
    "usernameSelector": "input[placeholder=\"Enter your username\"]",
    "passwordSelector": "input[placeholder=\"Enter your password\"]",
    "submitSelector": "button[type=\"submit\"]",
    "successSelector": "a[href^=\"/servers/\"]"
  },
  "steps": [
    {
      "goto": "/servers",
      "waitFor": "a[href^=\"/servers/\"]",
      "screenshot": "01-list"
    }
  ]
}
```

Steps run in order. Each step names exactly one action; `name` labels it in the
report and `screenshot` captures the page after it succeeds. Screenshots settle
CSS animations first, so a report never argues about a state nobody can see.

A `screenshot` step may add `"element": "<selector>"` to crop to that box (the
shot is a viewport clip, so the themed page renders exactly as a person sees
it). Crops scroll the element into view first; set `"scroll": false` for a
full-height page column so it is captured from the top instead of being
aligned under the fixed app header.
Use `"maxHeight": 528` on an element screenshot to end a crop after a focused
region, such as the first complete row of library cards. This caps the native
viewport crop; it does not resize or repaint the UI.

| Action                           | Meaning                                    |
| -------------------------------- | ------------------------------------------ |
| `goto`                           | navigate to a path under the base URL      |
| `click` / `fill` / `press`       | act on a selector                          |
| `waitFor`                        | wait for a selector to be visible          |
| `wait`                           | sleep, in milliseconds                     |
| `assertVisible` / `assertHidden` | assert a selector's visibility             |
| `assertText`                     | `{ "assertText": sel, "contains": "..." }` |
| `assertNoScrollbar`              | fail if the element scrolls vertically     |

## Plans in this directory

- `plan-console.json` — login, server list, server detail, every major tab,
  and the tab strip's scrollbar.
- `plan-lifecycle.json` — start a real server, wait for it to be healthy, check
  the lifecycle journal and the live console, then stop it.

## Writing a new plan

Start from `plan-console.json`. Prefer `assertVisible` and `assertText` over
reading pixels: a screenshot is for a person to confirm, an assertion is for the
report to be reproducible. Every assertion should be something a reviewer would
say out loud — "the journal table is on screen", not "the div has class x".
