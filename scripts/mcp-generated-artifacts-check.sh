#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Regenerate every MCP catalog/audit artifact with the canonical generators,
# then require byte-identical committed output. Any drift fails the check.
set -euo pipefail
cd "$(dirname "$0")/.."

go run ./internal/mcp/cataloggen
go test ./internal/mcp/ -count=1 -run TestContentDescriptorArtifact -update-content-descriptors

generated=(
  internal/mcp/catalog_data.go
  internal/mcp/integration_catalog.go
  internal/mcp/family_content_descriptors.json
  internal/mcp/family_audit_matrix.json
)
if ! git diff --exit-code -- "${generated[@]}"; then
  echo "::error::MCP generated artifacts are not reproducible from source" >&2
  exit 1
fi
echo "MCP generated artifacts are byte-identical to committed output"
