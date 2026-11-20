---
type: Workflow
title: MCP artifact distribution
description: Build and verify the MCP image independently of Carbon Panel releases.
status: stable
owners: [oss-runtime]
code_refs: [.github/workflows/mcp-dist.yml, .github/workflows/mcp-packaged-smoke.yml, docker/Dockerfile.mcp, docker/mcp.server.json, scripts/validate_mcp_server_json.py]
sources:
  - title: Artifact distribution ADR
    resource: ../../docs/adr/0034-mcp-server-distribution.md
  - title: MCP smoke runbook
    resource: ../../docs/runbook-mcp-smoke.md
---
# Rule

Build from the pinned revision for linux/amd64 and linux/arm64. The read-only
packaging workflow verifies both entrypoint ELF headers and runs the native
container over stdio against an isolated owned fixture. Publication requires
trusted-main dispatch, exact-main CI, the strict coverage gate and the
packaged smoke. GHCR must resolve the immutable digest to both platforms and
permit anonymous pull before official MCP Registry publication.

The OCI package identifier carries the registry, version tag and digest.
Verify the public Registry response for the exact version and an active
record with the submitted OCI identifier. Keep the MCP version and release
workflow independent of Carbon Panel release aliases.
