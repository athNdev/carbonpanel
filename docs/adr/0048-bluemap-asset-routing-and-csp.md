# ADR 0048: CSP-compatible, exactly routed BlueMap web assets

Status: Accepted (2026-11-20)

## Context

Visual QA of showcase preview 44530053075 produced a black map screenshot even
though the "renderer canvas with real map tiles is visible" assertion passed.
The console evidence (`showcase-preview-44530053075/report.md`) showed two
independent root causes:

1. `GET .../maps/world/textures.json` returned 404. The disposable stack served
   BlueMap's generated web root with CPython `http.server`, which has no
   BlueMap compressed-resource routing. BlueMap v5.29 stores map data gzipped:
   the renderer web root contains `textures.json.gz` and `*.prbm.gz`, and only
   BlueMap's own webserver implements the exact plain-path / gzip-negotiated
   and literal-`.gz` routes (see `MapStorageRequestHandler.java` in the pinned
   source). A visible canvas therefore proved nothing about loaded map data.
2. The webapp's `vue-i18n` runtime template compiler called `new Function()`,
   blocked by the panel CSP (`script-src 'self' 'unsafe-inline'`, no
   `unsafe-eval`). The upstream `common/webapp/vite.config.js` at v5.29 sets
   `__VUE_I18N_FULL_INSTALL__`, `__VUE_I18N_LEGACY_API__` and
   `__INTLIFY_PROD_DEVTOOLS__`, but omits the Intlify JIT define.

The same panel CSP applies to the same-origin map iframe by design
(ADR 0015). Widening it, adding `unsafe-eval`, or adding a screenshot-only
header bypass would remove a real security control from the whole console.

## Decision

1. **The panel CSP is unchanged.** No `unsafe-eval`, no new `unsafe-inline`,
   no renderer-specific or capture-only CSP relaxation, and no bypass flag in
   capture code. `internal/rpc/security_headers_test.go` pins the exact policy
   string.

2. **BlueMap compatibility comes from a pinned, CSP-safe webapp build.**
   Upstream BlueMap source tag `v5.29` commit
   `a61e143fe1e069c536159e0bc79410d5b6da3885` is rebuilt with exactly one
   addition to `common/webapp/vite.config.js`:
   `__INTLIFY_JIT_COMPILATION__: true` in `define`. Intlify v9.3+ then interprets
   messages with the JIT AST path instead of runtime `new Function()`, while
   `src/i18n.js` keeps dynamically fetching `lang/*.conf` locales. Verified in
   an owned rebuild (Node v26.13.1, npm 12.2.2, upstream lockfile): the pristine
   bundle `index-B6LhiKap.js` has SHA-256
   `6d45f229b24db91e5d8d30cb559ebc9cf14eebf8a110e785d57e176b2ebe15c8` and
   contains 1 `new Function` / 2 `compileToFunction` occurrences; the JIT bundle
   `index-BA9KJ4pi.js` has SHA-256
   `ba940f930de0642acfd2ca8b0eda6aa0914ce02fcf673cee18262b3dd5217da3` and
   contains 0 of each. This is a build provenance record, not a claim that
   minified output is byte-identical across toolchains; the build recipe
   verifies the digest before use and fails closed on drift.

3. **Prefer BlueMap's own webserver** in disposable stacks. Where a plain
   static web root must be used, the renderer proxy reproduces BlueMap's exact
   gzip routing inside the existing session/auth/scope gates:
   - `Accept-Encoding` is forwarded so a native BlueMap webserver performs its
     own negotiation, and `Content-Encoding` is relayed end to end.
   - The special routing applies **only to the BlueMap renderer adapter** and
     **only to its known map-data paths**: a map's `textures.json` and its
     PRBM/PNG tile payloads. Browser assets (HTML, JS, CSS, fonts, locales) and
     every non-BlueMap renderer keep their previous pass-through behavior.
   - A plain map-data path that upstream answers 404 gets exactly one retry of
     the exact `+".gz"` sibling. On success the response carries
     `Content-Encoding: gzip` and the uncompressed media type from an explicit,
     OS-independent table (`.json` → `application/json`, `.prbm` →
     `application/octet-stream`, etc.). A failed retry keeps the original 404.
   - A literal `.gz` request (`clientDecompression` mode) is served without
     `Content-Encoding` and with the uncompressed media type, as BlueMap's own
     webserver does.
   - `Range` requests never switch representation: they are passed through as
     before, so `Accept-Ranges`/`Content-Range` semantics are preserved. The
     session, authorization, scope and private no-store cache gates run before
     any of this and are unchanged.
   - No path is relaxed beyond the already-validated browser asset plus
     BlueMap's own suffix; no other encoding format is invented (BlueMap v5.29
     produces gzip only). Missing tile/texture responses stay 404 instead of
     becoming an empty 200.

4. **READY requires real map data over the network.** For BlueMap, the adapter
   probes `/maps/{id}/textures.json` for **every** map in the authoritative
   list (capped at 32 probes per health check). It accepts the same two routes
   as the webapp: an encoded plain response or the exact `.gz` sibling from a
   gzip-only static root. A bounded, decompressed body must be a JSON object
   whose `textures` field is an array; a 404, empty body, SPA/HTML fallback or
   unrelated JSON is reported as `ABSENT` with an actionable diagnostic and no
   map session key, so a black canvas cannot be captured as a qualified map
   view. Probing all maps prevents a healthy first map from hiding a broken
   second one. On-disk tile checks in discovery still apply; this is an
   additional network-side assertion at the readiness seam.

## Alternatives rejected

- Adding `unsafe-eval` to the panel CSP, a per-route CSP for the map iframe,
  or a screenshot-only browser flag: all remove or bypass a real control for
  every console user or hide the defect in capture code.
- Replacing the proxy's bounded streaming with in-proxy decompression: it would
  buffer attacker-controlled asset bytes on the controller and duplicate work
  the browser already does.
- Treating a visible iframe/canvas, an HTTP 200 on `/`, or a non-empty
  `settings.json` map list as proof of a rendered view: each was satisfied in
  the defective preview while the map was black.

## Consequences and evidence limits

- The gateway fix is exercised with local `httptest` renderers serving real
  gzip bytes; no live container, browser or rendered WebGL map is claimed by
  these backend tests. The browser capture and the disposable-stack recipe are
  owned by the sole screenshot owner and are recorded in the audit handoff.
- The rebuilt bundle still contains one unused `eval` in `hocon-parser` for
  HOCON `${...}` substitutions. None of the shipped `lang/*.conf` files uses
  substitutions (verified: zero `${` occurrences), so that branch is not
  reached by this webapp. Do not point it at untrusted locale files.
- Minified bundle names and digests are toolchain-bound; re-verification with a
  different Node/npm version must update this record deliberately, not
  silently.
