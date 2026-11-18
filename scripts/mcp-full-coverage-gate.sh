#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# STRICT full-coverage / publication gate for the MCP catalog.
# This command is intentionally RED while the exact PENDING inventory is
# non-empty. It is invoked by the MCP distribution verify job BEFORE any GHCR
# or MCP-registry write. It is NOT part of ordinary `go test ./...`.
set -euo pipefail
cd "$(dirname "$0")/.."
exec go test -tags mcpfullcoverage ./internal/mcp/ -count=1 -run TestFamilyAuditFullCoverage -v
