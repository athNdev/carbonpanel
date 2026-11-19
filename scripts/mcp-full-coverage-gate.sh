#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# STRICT full-coverage / publication gate for the MCP catalog.
# This command passes only while every supported operation is LISTED, REACHABLE
# and VERIFIED with executable tools/call evidence and the PENDING inventory is
# empty. It is invoked by the MCP distribution verify job BEFORE any GHCR or
# MCP-registry write. It is NOT part of ordinary `go test ./...`. The gate is
# never waived: adding an unmounted operation back to PendingInventory re-reds it.
set -euo pipefail
cd "$(dirname "$0")/.."
exec go test -tags mcpfullcoverage ./internal/mcp/ -count=1 -run TestFamilyAuditFullCoverage -v
