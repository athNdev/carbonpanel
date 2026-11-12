# Manual review harness

A small browser driver for reviewing the console the way a person would: it
logs in, clicks, asserts what is on screen, and writes one screenshot per named
step plus a Markdown report. The report and images are the evidence a review
quote can point at.

This is deliberately not an end-to-end test suite. It exists for the things a
unit test cannot see — a table showing `Invalid Date`, a tab indicator sitting
on the wrong tab, a scrollbar appearing under the pointer.

## Requirements

- Node 20+.
- `playwright-core`, plus a Chromium build. Playwright's own browser cache is
  reused automatically.
- A running panel with a local account. Credentials are read from the
  environment and never written to the report.

```sh
npm i playwright-core            # once, anywhere
npx playwright install chromium  # once, if you have no browser build yet
```

## Run

```sh
export PANEL_REVIEW_USER=you
export PANEL_REVIEW_PASSWORD=...
export REVIEW_MODULES=/path/to/node_modules   # where playwright-core lives

node scripts/review/journey.mjs \
  --plan scripts/review/plan-console.json \
  --base http://127.0.0.1:8080 \
  --out /tmp/review
```

Exit code is 0 when every step passed, 1 when a step failed, 2 on a setup
error. Screenshots of failed steps are written as `FAILED-<name>.png` with the
full page, so a clipped element is still visible.

Environment overrides:

| Variable                                      | Purpose                                |
| --------------------------------------------- | -------------------------------------- |
| `PANEL_REVIEW_USER` / `PANEL_REVIEW_PASSWORD` | login (names configurable per plan)    |
| `REVIEW_MODULES`                              | directory containing `playwright-core` |
| `REVIEW_CHROME`                               | explicit Chromium executable path      |

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
