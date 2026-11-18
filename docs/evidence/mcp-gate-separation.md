# MCP gate separation evidence

Both gates are recorded from pinned Go 1.26.11 on the exact head of this branch.

## 1. Ordinary gate — PASS

Command: `go test ./internal/mcp/ -count=1 -run TestFamilyAudit -v` (also covered
by ordinary `go test ./... -count=1`).

```
family_audit_test.go:428: ORDINARY GATE PASS: 192/192 non-pending operations verified; exact 9-entry PENDING inventory intact
--- PASS: TestFamilyAudit
```

The ordinary gate validates the exact 9-entry `PendingInventory` (each entry
with a reason and follow-up, no duplicates, no missing or extra entries) and
requires every supported non-pending operation to be LISTED, REACHABLE and
VERIFIED with executable `tools/call` evidence. No skips, no waivers.

## 2. Strict full-coverage / publication gate — FAIL (intended)

Command: `bash scripts/mcp-full-coverage-gate.sh`, equivalent to
`go test -tags mcpfullcoverage ./internal/mcp/ -count=1 -run TestFamilyAuditFullCoverage -v`.

```
family_audit_strict_test.go:62: OSS-375 STRICT FULL-COVERAGE GATE: 9 supported operations are not fully covered (9 PENDING); live: supported=201 listed=201 reachable=201 verified=192 excluded=58. Publication is BLOCKED until the PENDING inventory is empty.
--- FAIL: TestFamilyAuditFullCoverage
```

Exactly 9 gaps are listed: `DownloadArchive`, `ExportWorld`, `GetFile`,
`InitFileDownload`, `SaveUploadedFile`, `InitUpload`, `UploadChunk`,
`rest_upload_stream`, `rest_download_stream`. The strict gate is not part of
ordinary tests (`mcpfullcoverage` build tag) and is not waived.

## 3. Publication blocking

`.github/workflows/mcp-dist.yml` runs the strict command in the `verify` job with
`continue-on-error: true`, then fails the job unless the outcome is `success`.
GHCR and MCP-registry writes therefore cannot proceed while the strict gate is
RED. The generated-artifact reproducibility check runs in the same job.

## 4. Regeneration

Canonical generation: `go run ./internal/mcp/cataloggen` plus
`go test ./internal/mcp/ -run TestContentDescriptorArtifact -update-content-descriptors`.
`scripts/mcp-generated-artifacts-check.sh` reruns both and requires
`git diff --exit-code` on `catalog_data.go`, `integration_catalog.go`,
`family_content_descriptors.json` and `family_audit_matrix.json`. The corrected
LF-normalized `family_content_descriptors.json` is included explicitly in this
branch.
