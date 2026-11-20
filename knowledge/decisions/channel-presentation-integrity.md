---
type: Decision
title: Channel presentation integrity is proven from built bytes
description: The docs-only gate reads real presentation and changelog bytes at canonical and archive paths, omits legacy fields, and leaves channel authority with the verified promotion path.
status: stable
owners: [oss-runtime]
code_refs:
  - scripts/docs_publication_gate.py
  - scripts/changelog_page.py
  - scripts/changelog_browser_check.py
  - scripts/test_docs_publication_gate.py
sources:
  - title: Channel changelog presentation
    resource: channel-changelog-presentation.md
  - title: Website and documentation publication
    resource: ../../docs/website-publishing.md
---

# Decision

- An echoed `presentation_fingerprint` is not proof. The gate reads the ACTUAL
  `presentation.json` at canonical, stable-archive and nightly paths, requires a
  valid SHA-256 equal to the candidate, and byte-hashes those files plus the
  generated changelog HTML (archive included). Missing or mismatched files fail
  closed before and after deploy.
- Legacy candidates have no presentation input. `guide_fingerprint` and
  `stamp_publication_receipt` omit the field entirely so historical candidate
  and receipt bytes stay unchanged; the field is written only when non-empty.
- The fingerprint covers ONLY static presentation inputs: source declaration,
  attested notes, generator and banner. It excludes the raw version contract so
  an arbitrary or unverifiable channel-state edit cannot arm the docs-only
  publisher. Channel identity changes stay under the verified separate
  channel-promotion path; after that path publishes, the docs-only gate compares
  the already-verified live index and reconciles Pages. This is a fallback for
  presentation-only drift, not a second channel authority.

# Rejected alternatives

- Pinned prose or hardcoded retired headings: duplicated H1 and unearned tags.
- Maintaining guides receipt reuse: would arm unrelated channel edits (ADR 0050).
- Trusting the echoed receipt field: a missing or wrong file could pass.
- Empty presentation field on legacy candidates: changed historical bytes.
- Raw checkout hashes or CSS truncation: platform-specific hashes and a hidden tag.
- Display-encoded note copies: produced `â€”` for an em dash.

# Read next

- [Channel changelog presentation](channel-changelog-presentation.md)
- [Website and documentation publication](website-docs.md)
