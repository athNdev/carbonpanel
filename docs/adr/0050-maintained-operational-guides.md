# ADR 0050: Release-independent maintained operational guides

Status: Accepted

## Context

ADR 0045 pins authored docs, examples and the generated API schema to the exact
application commit of each channel. That is correct for versioned product
behaviour, but wrong for operational procedures whose correctness depends on a
currently maintained first-party installer. Both active channel snapshots still
published a `curl ... community-scripts/ProxmoxVE/main/ct/carbon-panel.sh`
command after that third-party helper was retired (HTTP 404). Fixing the prose
in a snapshot or repinning the product would either be a one-off edit or would
freeze unrelated versioned content. Adding a SHA to a second manifest would
simply create the next stale pin.

## Decision

`docs/maintained-guides.json` is a narrow classification/path/compatibility
declaration, separate from `docs/website-source.json` and the pinned content
paths. It declares only: which docs slugs are release-independent operational
guides, each source path under the allowlisted docs content tree, the page mode
(whole-page replacement, or replacement of one `<details>` answer identified by
summary labels), provenance text, and the guarantees that the slug exists in
every pinned channel and that product pins, channel markers and generated schema
remain untouched. It contains no revision and no content hashes.

The exact source revision is the checked-out `git HEAD` of the trusted website
build, and nothing else; there is no secondary revision input that could
disclose an older or foreign guide. The builder reads the guide text from those
exact Git blobs, overlays the declared files onto each channel's exported snapshot
before link rebasing, and writes a generated `maintained-guides.json` beside the
channel's `source.json` attesting the revision, its origin and each blob's
SHA-256. A whole maintained page also renders a separately labelled **Guide
source** link to that exact blob, so the application `Pinned to` link is not
mistaken for the guide's source. The FAQ uses fragment mode so the rest of that
versioned page stays pinned; the Proxmox page uses whole-page mode. Product
identity files are never rewritten by the overlay.

Missing declarations, out-of-allowlist or duplicate paths/slugs, absent blobs,
absent pinned slugs, floating refs, duplicated or broken fragment anchors all
fail closed before packaging.

## Consequences

A maintained guide edit becomes effective in both channels on the next build
from the committed source, with its attested digest changing automatically and
no manifest edit. Product documentation outside the declared files remains
reproducible from the exact pinned commit, including aliases and schema. The
source revision is the same trusted exact build that produced the site, not an
independently drifting release pin; publishing a different source means
building from that trusted checkout. The fragment overlay is idempotent across
old and already-current channel snapshots. This is not a cross-repository or
cross-process guarantee: the attestation is
about exact Git objects in the verified checkout, not a live installer download.
