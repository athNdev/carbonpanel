# MCP gate separation evidence

Both gates are recorded from pinned Go 1.26.11. Section 2 is the HISTORICAL
baseline that motivated the byte-transport work; the current green result is
recorded in section 2a.

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

## 2. Strict full-coverage / publication gate — historical FAIL (superseded by 2a)

Command: `bash scripts/mcp-full-coverage-gate.sh`, equivalent to
`go test -tags mcpfullcoverage ./internal/mcp/ -count=1 -run TestFamilyAuditFullCoverage -v`.

```
family_audit_strict_test.go:62: OSS-375 STRICT FULL-COVERAGE GATE: 9 supported operations are not fully covered (9 PENDING); live: supported=201 listed=201 reachable=201 verified=192 excluded=58. Publication is BLOCKED until the PENDING inventory is empty.
--- FAIL: TestFamilyAuditFullCoverage
```

Exactly 9 gaps were listed: `DownloadArchive`, `ExportWorld`, `GetFile`,
`InitFileDownload`, `SaveUploadedFile`, `InitUpload`, `UploadChunk`,
`rest_upload_stream`, `rest_download_stream`. The strict gate is not part of
ordinary tests (`mcpfullcoverage` build tag) and was never waived.

## 2a. Strict full-coverage / publication gate — current PASS

After the upload/download byte-transport lanes were mounted in production, the
same command passes. The exact nine operations above have production
`tools/call` evidence through the generated upload/download tool sets; the gate
was not weakened. `PendingInventory` is empty (0 entries) and the ordinary
gate reports `207/207 supported operations verified`. Live counts are
`supported=207 listed=207 reachable=207 verified=207 excluded=76`.
Per-family evidence includes `FileService=17/17`, `UploadService=4/4`,
`PackwizService=2/2` and `surface=2/2`. The Packwiz fixture drives real Packwiz
storage through both typed tools and the production auth interceptor.
A global `modpacks/read` grant succeeds. A pack-specific grant and a token
without a grant both receive `permission_denied`.
The two supported byte transfers are exercised through their mounted MCP tools.
Prometheus scrapes are classified among the excluded HTTP surfaces. The hold mechanism remains live: adding an unmounted operation
back to `PendingInventory` re-reds both gates.

## 3. Publication blocking and ordinary CI

`.github/workflows/mcp-dist.yml` runs the strict command in the `verify` job with
`continue-on-error: true`, then fails the job unless the outcome is `success`.
GHCR and MCP-registry writes therefore cannot proceed while the strict gate is
RED. The generated-artifact reproducibility check runs in the same job.

The same strict command now also runs in **ordinary CI** as the `mcp-coverage`
job in `.github/workflows/ci.yml`, on the Go version pinned by `go.mod`
(1.26.11), with no `continue-on-error`. The job reuses
`scripts/mcp-full-coverage-gate.sh` and
`scripts/mcp-generated-artifacts-check.sh`, then builds `cmd/carbonpanel-mcp`
and drives `scripts/test_mcp_real_binary.py`: a real stdio `initialize`,
PUBLIC `tools/list` and a read-only `tools/call` (`list_servers`) against a
disposable owned Connect mock. No publication, registry or GHCR write was added.
`TestFamilyAuditFullCoverageFailClosed` is the OSS-406 negative fixture: it
injects a supported-but-unmounted NodeControlService RPC into a test-only copy
of the matrix, requires the shared evaluator to reject it, then removes the
overlay and requires GREEN again. A newly unmounted supported operation
therefore makes ordinary CI RED with no waiver or tolerance change.

## 4. Regeneration

Canonical generation: `go run ./internal/mcp/cataloggen` plus
`go test ./internal/mcp/ -run TestContentDescriptorArtifact -update-content-descriptors`.
`scripts/mcp-generated-artifacts-check.sh` reruns both and requires
`git diff --exit-code` on `catalog_data.go`, `integration_catalog.go`,
`family_content_descriptors.json` and `family_audit_matrix.json`. The corrected
LF-normalized `family_content_descriptors.json` is included explicitly in this
branch.

## Development server portability coverage

The expected hosted contract after OSS-422 adds export and preview is
`supported=207 listed=207 reachable=207 verified=207 excluded=76`. This
count describes the development branch coverage fixture. The earlier run above
records the exact checks and counts from the pinned publication candidate.
