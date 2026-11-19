---
type: Decision
title: "Homepage UI fragments — Decision (part 2)"
description: "Prerendered declarative shadow roots contain the actual component HTML and"
status: stable
owners: [oss-runtime]
sources:
  - title: Parent concept
    resource: homepage-fragments.md
---
Prerendered declarative shadow roots contain the actual component HTML and
scoped application styles. Controls are disabled until hydration, while
installation and documentation links work independently. The website builder
publishes the compiled islands alongside the homepage and pinned docs.

The build scopes canonical token root selectors to `:host` in a content-hashed
stylesheet. This supplies the same font, control-size and motion roles inside
shadow roots without changing the shared source or Cloud's snapshot. Do not
assume a `:root` declaration inside a shadow stylesheet reaches its host.
