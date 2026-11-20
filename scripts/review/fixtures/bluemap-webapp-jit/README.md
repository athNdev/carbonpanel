# Pinned CSP-safe BlueMap webapp (disposable showcase fixture only)

This directory is a **real** build of the upstream BlueMap webapp used only by
the disposable showcase review fixture. It is not a mock, placeholder or
repainted UI. It exists because Carbon Panel's panel-wide CSP
(`script-src 'self' 'unsafe-inline'`, no `unsafe-eval`) is deliberately
unchanged (ADR 0048): the pristine BlueMap v5.29 webapp calls `new Function()`
during vue-i18n locale compilation and the map fails to load inside the
authenticated iframe.

## Provenance

- Upstream: `https://github.com/BlueMap-Minecraft/BlueMap` tag `v5.29`, source
  commit `a61e143fe1e069c536159e0bc79410d5b6da3885`.
- Exactly one source change: `__INTLIFY_JIT_COMPILATION__: true` added to the
  `define` block in `common/webapp/vite.config.js`. Dynamic `lang/*.conf`
  locales are preserved.
- Built with Node v26.13.1 / npm 12.2.2 and the upstream `package-lock.json`
  (`npm ci && npm run build`). Bundle
  `assets/index-BA9KJ4pi.js` SHA-256
  `ba940f930de0642acfd2ca8b0eda6aa0914ce02fcf673cee18262b3dd5217da3`;
  0 `new Function` / 0 `compileToFunction` occurrences (pristine bundle
  `index-B6LhiKap.js` has 1 / 2).
- `manifest.sha256` covers every delivered file (paths use `/`). Verify from
  this directory with `sha256sum -c manifest.sha256` on Linux. The source map
  from the handoff artifact is intentionally omitted: it is not served at
  runtime and would add 4.9 MB without changing behavior.

## Use

`scripts/review/devstack.sh` overlays `index.html`, `assets/` and `lang/` over
the rendered BlueMap web root **after** BlueMap has generated its own web
directory and before the journey opens the map. `settings.json` and the real
`maps/` tile tree are left untouched. The overlay is verified against the
digest above before use; a drift fails the fixture closed.

Do not feed this webapp untrusted locale files: one unused `eval` remains in
`hocon-parser` for HOCON `${...}` substitutions and zero shipped locales use
them (ADR 0048).
