# Validation policy and reports

Run the ordinary backend tests and static checks from the contributor guide.
`bun run lint` in `web/carbon-panel` uses Prettier's `--end-of-line auto`, then
ESLint, so a native Windows CRLF checkout does not need a formatting rewrite.

## Vulnerability scan

```sh
go install golang.org/x/vuln/cmd/govulncheck@v1.9.0
go run ./cmd/govulncheck-policy -output govulncheck.json
```

The wrapper runs `govulncheck -json -scan=symbol ./...`. Its stdout is a JSON
policy result with `outcome` of `clean`, `unfixed`, `fixable` or `error`.
`fixable` and `error` exit nonzero. A reachable finding needs a function in the
first trace frame; module and package findings alone do not establish reachability.
An empty fixed version means no fix is published and remains non-blocking under
the existing project policy. The complete scanner stream is retained at the
selected output path, including when the process fails.

The scanner version and JSON protocol are deliberately checked together. When
upgrading, verify the upstream JSON handler, configuration, finding trace and
exit behavior, then update the pin, runner and fixtures together. JSON scans in
v1.9.0 exit zero even when vulnerabilities exist; text-mode exit-code handling
cannot be substituted. The wrapper requires process success, valid JSON, the
expected source/symbol configuration and a populated SBOM. The protocol has no
final completion message, so successful process exit is the completion evidence.
Install, startup, typecheck, database/network and parse errors fail the job.

CI uploads raw scanner JSON and the policy result as `govulncheck-<revision>`.
Inspect the failing step log if installation failed before any report existed.
These outputs describe a scan of the recorded source and toolchain; they do not
establish that production is free of vulnerabilities.

## Lint debt

The changed-line golangci-lint gate continues to block new findings. A second
full run uses the same installed v2.13.2 tool and uploads JSON/text findings,
revision, Go/linter versions and exit code as `full-lint-<revision>`. Exit 1
is accepted as historical findings only with valid structured output, an empty
`Report.Error` and no `typecheck` finding. A linter can return 1 even when an
analyzer also failed, so exit status alone does not establish completion. Missing,
malformed or contradictory reports and all other nonzero exits fail the step.
Use this artifact for the current debt count instead of a hard-coded estimate
in a workflow comment. Parser and subprocess fixtures run with
`python3 -m unittest scripts.test_check_lint_report`.

The runner's regression cases are part of `go test ./cmd/govulncheck-policy`.
They include real child-process startup/failure handling and malformed or
partial output as well as clean, unfixed and fixable policy results.
