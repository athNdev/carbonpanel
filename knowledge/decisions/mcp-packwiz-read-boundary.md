---
type: Decision
title: MCP Packwiz inventory reads
description: Typed MCP Packwiz reads page allowlisted Studio metadata under the existing global modpacks/read grant.
status: stable
owners: [oss-runtime]
code_refs: [internal/mcp/family_packwiz_read_tools.go, internal/mcp/family_packwiz_read_tools_test.go]
sources:
  - title: MCP server guide
    resource: ../../docs/carbon-panel/src/content/docs/guides/mcp-server.mdx
  - title: MCP catalogue coverage
    resource: https://carbonpanel.atlassian.net/browse/OSS-370
---

The Studio inventory uses the typed Packwiz service and the existing global
`modpacks/read` grant. The provider catalogue represents different data.
The first `main` build with the mounted service is
`eb7c2dbef6033fb988e3d1eefc3d3a2c155b99f3`.

The gateway caps the controller response at 4 MiB and returns at most 50
allowlisted records per page. It omits download URLs, file names, folders and
raw options. A page bounds gateway output while the controller RPC still reads
its complete inventory.

The production-auth fixture covers global grant success, no-grant and
pack-specific denial, pagination, redaction and response-size refusal.
Provider update checks require an egress budget because the controller visits
all pack entries before applying the requested filter. Studio writes require
separate confirmation, target validation and operation-specific bounds.
