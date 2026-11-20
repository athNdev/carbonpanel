---
type: Decision
title: Publication gates run from the trusted dispatch revision
description: Privileged release gates use trusted code with explicit candidate paths, and registry platform evidence has one strict validator.
status: stable
owners: [oss-runtime]
code_refs:
  - .github/workflows/release.yml
  - scripts/release.py
  - scripts/docs_publication_gate.py
  - scripts/browser_version_check.py
sources:
  - title: Channel promotion decision
    resource: ../../docs/adr/0052-channel-promotion-gates.md
  - title: Release operations and versioning
    resource: ../../docs/releases.md
---

# Decision

- The privileged Stable transaction runs its gate modules (`release.py`,
  `docs_publication_gate.py`, `browser_version_check.py`, `stable_recovery`)
  from the trusted dispatch SHA, not from the pinned candidate checkout. The
  trusted static gate probe reads the candidate gate script as DATA before any
  candidate Python is imported.
- Candidate artifacts, docs and the packaged site are supplied as explicit
  paths (the browser check receives `.website-build/site` positionally),
  because each module derives its root from its own `__file__`. Rendering tools
  may stay on the candidate checkout; trust decisions may not. A corrected gate
  can qualify an already-staged candidate without re-staging the product.
- Module invocation uses `python -P` with `PYTHONPATH=.trusted-release`: the
  candidate CWD otherwise wins module resolution even with PYTHONPATH set. The
  worked example test proves both resolutions. The maintained-guides policy
  declaration also comes from the trusted checkout while the candidate data
  root stays explicit.
- Registry platform evidence has one validator for in-process and CLI JSON
  observations: only well-formed two-string OS/architecture pairs qualify.
  Malformed entries are refused deterministically at every CLI boundary, never
  coerced from arbitrary iterables or mapping keys.
- Browser qualification uses the repository-pinned Playwright helper beside the
  e2e package, with one bounded browser session, DOMContentLoaded and cached
  route/viewport visits. Privileged Stable provisions that package from the
  trusted checkout and passes the candidate site as data; all route, pin,
  marker and retired-page assertions remain in the Python caller. An empty DOM
  is a browser error with the real message, never a missing-marker diagnostic.

# Read next

- [Channel promotion recovery](channel-promotion-recovery.md)
- [Release](../workflows/release.md)
