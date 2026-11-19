---
type: Decision
title: MCP upload/commit family seam
description: Upload and commit operations are exposed as lane-owned typed tools and a composed bounded-stream path whose descriptors are handed to the integration owner, never wired directly into shared catalog files.
status: draft
owners: [oss-runtime]
code_refs: [internal/mcp/family_upload_tools.go, internal/mcp/family_upload_descriptors.json, internal/mcp/family_upload_tools_test.go]
sources:
  - title: MCP controller-side stream transport plan
    resource: ../../docs/plans/mcp-controller-side-stream-transport.md
  - title: MCP full-coverage and publication gate
    resource: https://carbonpanel.atlassian.net/browse/OSS-375
---

# Parts

- [MCP upload/commit family seam — Choice + Alternatives rejected + Consequence + Verification + Read next](mcp-upload-commit-family-choice.md) — The upload/commit operations (SaveUploadedFile, InitUpload, UploadChunk
