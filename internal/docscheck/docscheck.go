// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package docscheck keeps the planning documents honest.
//
// docs/plans/oss-feature-completeness-matrix.md is a source-backed inventory:
// every row names the code that is supposed to implement the workflow. Those
// paths rot silently whenever a package moves, and a stale row then reads as
// evidence for a feature nobody can find. CheckMatrix resolves every reference
// and reports the ones that no longer exist, so the matrix cannot drift away
// from the repository without failing a test.
//
// CheckRecordedSeams additionally requires DESIGN.md to cite the five
// architecture seam packages. A rename of locator, transition, idle, PlanFor
// or worldstore fails this package's tests.
package docscheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// statusRoots are the claim words the legend defines. A row's status is the
// bold span that starts with one of them; anything else is ambiguous and is
// reported.
var statusRoots = []string{
	"Source-present",
	"Source integration gap",
	"Integration gap",
	"Unqualified",
	"Helper/catalog only",
	"Catalog/helper",
	"Mixed",
	"Not implemented",
	"Unsupported",
}

// pathAliases expand the document's UI shorthand.
var pathAliases = []struct {
	prefix string
	target string
}{
	{"Services/", "internal/rpc/services/"},
	{"Components/", "web/carbon-panel/src/lib/components/"},
	{"routes/", "web/carbon-panel/src/routes/"},
	{"lib/", "web/carbon-panel/src/lib/"},
}

// continuationRoots are tried when a reference is written relative to the
// document's own shorthand rather than from the repository root. The document
// is written for humans, so a reference such as "docker/client.go" inside a
// "Services/..." cell is common; each candidate is tried before reporting.
var continuationRoots = []string{
	"",
	"internal/",
	"internal/rpc/services/",
	"internal/docker/",
	"internal/minecraft/",
	"internal/scheduler/",
	"internal/module/",
	"web/carbon-panel/src/",
	"web/carbon-panel/src/lib/components/",
	"docs/",
}

var (
	// backtick matches a code span inside a table cell.
	backtick = regexp.MustCompile("`([^`]+)`")
	// bold matches a bold claim, which is where the status word lives.
	bold = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	// rowID matches a feature row such as "| O01 — Node registration ...".
	rowID = regexp.MustCompile(`^\|\s*(O\d{2})\s*—`)
)

// RecordedSeam is one architecture package DESIGN.md must name.
type RecordedSeam struct {
	Name string
	Path string
}

// RecordedSeams are the five landed runtime, lifecycle and world-data seams.
var RecordedSeams = []RecordedSeam{
	{Name: "Locator", Path: "internal/docker/locator.go"},
	{Name: "lifecycle handle", Path: "internal/docker/transition.go"},
	{Name: "idle-power module", Path: "internal/docker/idle.go"},
	{Name: "route PlanFor", Path: "internal/proxy/plan.go"},
	{Name: "worldstore", Path: "internal/worldstore"},
}

// Problem is one matrix defect with the row it came from.
type Problem struct {
	Row    string
	Detail string
}

// String renders the problem for a test failure message.
func (p Problem) String() string { return p.Row + ": " + p.Detail }

// CheckRecordedSeams verifies that DESIGN.md cites each architecture seam
// package and that those paths still exist under root.
func CheckRecordedSeams(root, designPath string) ([]Problem, error) {
	body, err := os.ReadFile(filepath.Join(root, designPath))
	if err != nil {
		return nil, err
	}
	text := string(body)
	var problems []Problem
	for _, seam := range RecordedSeams {
		cite := "`" + seam.Path + "`"
		if !strings.Contains(text, cite) {
			problems = append(problems, Problem{
				Row:    "DESIGN.md",
				Detail: fmt.Sprintf("missing citation for %s seam %q", seam.Name, seam.Path),
			})
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(seam.Path))); err != nil {
			problems = append(problems, Problem{
				Row:    "DESIGN.md",
				Detail: fmt.Sprintf("cited path %q for %s does not exist", seam.Path, seam.Name),
			})
		}
	}
	sort.SliceStable(problems, func(a, b int) bool { return problems[a].Detail < problems[b].Detail })
	return problems, nil
}

// CheckMatrix reads the matrix and verifies that every path it cites exists
// under root, that feature IDs are unique, and that every row carries a status
// word the legend defines.
func CheckMatrix(root, matrixPath string) ([]Problem, error) {
	body, err := os.ReadFile(filepath.Join(root, matrixPath))
	if err != nil {
		return nil, err
	}
	return CheckMatrixText(root, string(body))
}

// CheckMatrixText is CheckMatrix for an in-memory document.
func CheckMatrixText(root, text string) ([]Problem, error) {
	var problems []Problem
	seenIDs := map[string]int{}
	rows := 0

	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		match := rowID.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		rows++
		row := match[1]
		where := fmt.Sprintf("line %d (%s)", i+1, row)

		if first, dup := seenIDs[row]; dup {
			problems = append(problems, Problem{Row: where, Detail: fmt.Sprintf("duplicate feature id, first seen on line %d", first)})
		} else {
			seenIDs[row] = i + 1
		}

		if !hasKnownStatus(line) {
			problems = append(problems, Problem{Row: where, Detail: "no recognised status word (see statusRoots)"})
		}

		lastDir := ""
		for _, cell := range strings.Split(line, "|") {
			for _, span := range backtick.FindAllStringSubmatch(cell, -1) {
				path := referencedPath(span[1])
				if path == "" {
					continue
				}
				resolved, ok := resolveExisting(root, path, lastDir)
				if !ok {
					problems = append(problems, Problem{Row: where, Detail: fmt.Sprintf("cited path %q does not exist", path)})
					continue
				}
				lastDir = filepath.Dir(resolved)
			}
		}
	}

	if rows == 0 {
		return nil, fmt.Errorf("the matrix contains no feature rows")
	}
	sort.SliceStable(problems, func(a, b int) bool { return problems[a].Row < problems[b].Row })
	return problems, nil
}

// hasKnownStatus reports whether the line carries a claim the legend defines.
func hasKnownStatus(line string) bool {
	for _, m := range bold.FindAllStringSubmatch(line, -1) {
		claim := strings.ToLower(strings.TrimSpace(m[1]))
		for _, root := range statusRoots {
			if strings.HasPrefix(claim, strings.ToLower(root)) {
				return true
			}
		}
	}
	return false
}

// referencedPath extracts the path portion of a code span. A span such as
// "internal/docker/client.go:applyContainerHardening:130" yields the file path;
// a span that is not a path (an environment variable, a glob, a phrase) yields
// "".
func referencedPath(span string) string {
	span = strings.TrimSpace(span)
	if span == "" {
		return ""
	}
	if strings.ContainsAny(span, " =,()<>*") {
		return ""
	}
	// Drop a trailing ":identifier" or ":line[:identifier]" suffix.
	if idx := strings.IndexByte(span, ':'); idx >= 0 {
		span = span[:idx]
	}
	span = strings.TrimSpace(span)
	if span == "" {
		return ""
	}
	if !strings.Contains(span, "/") && !strings.HasSuffix(span, ".go") && !strings.Contains(span, ".svelte") && !strings.HasSuffix(span, ".ts") {
		return ""
	}
	// Only relative paths inside the repository are checkable.
	if strings.HasPrefix(span, "/") || strings.HasPrefix(span, "~") || strings.Contains(span, "://") {
		return ""
	}
	return span
}

// resolveExisting tries the document's shorthand against the repository and
// returns the first candidate that exists.
func resolveExisting(root, path, lastDir string) (string, bool) {
	expanded := resolveAlias(path)
	candidates := []string{expanded}
	// A reference is often written relative to the cell's previous path, or to
	// one of its ancestors ("studio/[id]/+page.svelte" after
	// "routes/modpacks/studio/+page.svelte"). Try each ancestor before giving up.
	if lastDir != "" {
		for dir := lastDir; dir != "" && dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
			candidates = append(candidates, filepath.ToSlash(filepath.Join(dir, expanded)))
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	for _, prefix := range continuationRoots {
		candidates = append(candidates, prefix+expanded)
	}

	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		if _, err := os.Stat(filepath.Join(root, candidate)); err == nil {
			return candidate, true
		}
	}
	return "", false
}

// resolveAlias expands the document's UI aliases.
func resolveAlias(path string) string {
	for _, alias := range pathAliases {
		if strings.HasPrefix(path, alias.prefix) {
			return alias.target + strings.TrimPrefix(path, alias.prefix)
		}
	}
	return path
}

var (
	auditRowID = regexp.MustCompile(`^\|\s*(A\d{2,3})\s*\|`)
	auditSpan  = regexp.MustCompile("`([^`]+)`")
	// eventConstant matches a declared activity constant and its literal value.
	eventConstant = regexp.MustCompile(`(?m)^\s*(Event[A-Za-z0-9_]+)\s*=\s*"([^"]+)"`)
)

var auditDispositions = []string{"Added", "Covered", "Deferred", "Internal"}

// auditAssertionMarkers are call shapes that read stored activity rows. A
// covering test must contain at least one of them; merely naming an entry point
// or exercising an operation is not audit coverage.
var auditAssertionMarkers = []string{
	"adminAuditRows(",
	"requireOneAdminAuditRow(",
	"requireAuditRowForActor(",
	"commandAuditRows(",
	"activity.List(",
	"ListActivityLogs(",
	"countAudit(",
	"countWorldEvents(",
	"auditor.all()",
	".Where(",
	"ActivityLog",
}

// CheckAuditInventory verifies the OSS-358 admin-action inventory. The
// inventory is required: a missing file is a failure, not a skipped check.
// Every entry-point and audit-write citation must resolve to an existing file
// and symbol, every event literal in the event cell must be declared, and
// every named covering test must exist and assert a stored activity row. An
// Added row additionally requires a declared event, a write site whose function
// contains that event, and a covering test that asserts the row.
func CheckAuditInventory(root, inventoryPath string) ([]Problem, error) {
	body, err := os.ReadFile(filepath.Join(root, inventoryPath))
	if err != nil {
		return nil, err
	}
	return CheckAuditInventoryText(root, string(body))
}

// CheckAuditInventoryText is CheckAuditInventory for an in-memory document.
func CheckAuditInventoryText(root, text string) ([]Problem, error) {
	var problems []Problem
	seen := map[string]bool{}
	rows := 0

	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		match := auditRowID.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		rows++
		id := match[1]
		where := fmt.Sprintf("line %d (%s)", i+1, id)
		if seen[id] {
			problems = append(problems, Problem{Row: where, Detail: "duplicate admin-audit id"})
		}
		seen[id] = true

		cells := strings.Split(line, "|")
		if len(cells) < 10 {
			problems = append(problems, Problem{Row: where, Detail: fmt.Sprintf("row has %d cells, want 10", len(cells))})
			continue
		}

		disposition := strings.Trim(strings.TrimSpace(cells[8]), "*")
		recognised := false
		for _, want := range auditDispositions {
			if disposition == want {
				recognised = true
				break
			}
		}
		if !recognised {
			problems = append(problems, Problem{Row: where, Detail: fmt.Sprintf("unknown disposition %q", disposition)})
		}
		added := disposition == "Added"

		entries := auditSpans(cells[3])
		if len(entries) == 0 {
			problems = append(problems, Problem{Row: where, Detail: "entry point has no code span"})
		}
		for _, entry := range entries {
			if detail := checkAuditPathSymbol(root, entry); detail != "" {
				problems = append(problems, Problem{Row: where, Detail: "entry point: " + detail})
			}
		}

		eventCell := strings.TrimSpace(cells[5])
		eventNone := eventCell == "`none`" || eventCell == "none"
		events := auditSpans(cells[5])
		if eventNone {
			events = nil
		} else if len(events) == 0 {
			problems = append(problems, Problem{Row: where, Detail: "activity event has no code span"})
		}
		for _, literal := range events {
			if detail := checkAuditEvent(root, literal); detail != "" {
				problems = append(problems, Problem{Row: where, Detail: detail})
			}
		}

		writeCell := strings.TrimSpace(cells[6])
		writeNone := writeCell == "" || writeCell == "`none`" || writeCell == "none"
		writes := auditSpans(cells[6])
		if !writeNone {
			if len(writes) == 0 {
				problems = append(problems, Problem{Row: where, Detail: "audit write has no code span"})
			}
			for _, write := range writes {
				if detail := checkAuditPathSymbol(root, write); detail != "" {
					problems = append(problems, Problem{Row: where, Detail: "audit write: " + detail})
				}
			}
		}

		testCell := strings.TrimSpace(cells[7])
		testNone := testCell == "" || testCell == "-" || testCell == "—" || testCell == "`-`" || testCell == "`—`"
		tests := auditSpans(cells[7])
		if !testNone {
			if len(tests) == 0 {
				problems = append(problems, Problem{Row: where, Detail: "covering test has no code span"})
			}
			for _, ref := range tests {
				if detail := checkAuditTest(root, ref); detail != "" {
					problems = append(problems, Problem{Row: where, Detail: "covering test: " + detail})
				} else if detail := checkAuditTestAsserts(root, ref, events, added); detail != "" {
					problems = append(problems, Problem{Row: where, Detail: "covering test: " + detail})
				}
			}
		}

		if added {
			if len(events) == 0 {
				problems = append(problems, Problem{Row: where, Detail: "Added requires a declared activity event literal"})
			}
			if writeNone || len(writes) == 0 {
				problems = append(problems, Problem{Row: where, Detail: "Added requires an audit write location"})
			} else {
				for _, write := range writes {
					if detail := checkAuditWriteContainsEvents(root, write, events); detail != "" {
						problems = append(problems, Problem{Row: where, Detail: "audit write: " + detail})
					}
				}
			}
			if testNone || len(tests) == 0 {
				problems = append(problems, Problem{Row: where, Detail: "Added requires a covering test that asserts the audit row"})
			}
		}
	}

	if rows == 0 {
		return nil, fmt.Errorf("the admin audit inventory contains no rows")
	}
	sort.SliceStable(problems, func(a, b int) bool { return problems[a].Row < problems[b].Row })
	return problems, nil
}

// auditSpans returns every code span in a cell. Some cells name two event
// literals (for example player.op / player.deop); all of them are checked.
func auditSpans(cell string) []string {
	matches := auditSpan.FindAllStringSubmatch(cell, -1)
	spans := make([]string, 0, len(matches))
	for _, match := range matches {
		if span := strings.TrimSpace(match[1]); span != "" {
			spans = append(spans, span)
		}
	}
	return spans
}

func splitAuditReference(ref string) (string, string) {
	ref = strings.TrimSpace(ref)
	if idx := strings.LastIndexByte(ref, ':'); idx >= 0 {
		return ref[:idx], ref[idx+1:]
	}
	return ref, ""
}

func checkAuditPathSymbol(root, ref string) string {
	path, symbol := splitAuditReference(ref)
	if path == "" {
		return fmt.Sprintf("reference %q has no path", ref)
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return fmt.Sprintf("path %q does not exist", path)
	}
	if symbol == "" {
		return ""
	}
	if !strings.Contains(string(body), symbol+"(") {
		return fmt.Sprintf("symbol %q not found in %s", symbol, path)
	}
	return ""
}

func checkAuditEvent(root, literal string) string {
	path := "internal/activity/activity.go"
	if strings.HasPrefix(literal, "world_view.") {
		path = "internal/worldview/service.go"
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return fmt.Sprintf("event source %q does not exist", path)
	}
	if !strings.Contains(string(body), `"`+literal+`"`) {
		return fmt.Sprintf("event %q not declared in %s", literal, path)
	}
	return ""
}

func checkAuditTest(root, ref string) string {
	path, testName := splitAuditReference(ref)
	if path == "" || testName == "" {
		return fmt.Sprintf("reference %q must be a _test.go path plus test name", ref)
	}
	if !strings.HasSuffix(path, "_test.go") {
		return fmt.Sprintf("covering test path %q is not a _test.go file", path)
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return fmt.Sprintf("test file %q does not exist", path)
	}
	if !strings.Contains(string(body), "func "+testName+"(") {
		return fmt.Sprintf("test %q not found in %s", testName, path)
	}
	return ""
}

// checkAuditTestAsserts requires the cited test function to read an activity
// row. Added rows must also name the row's event, so a test that only checks
// request/response behavior cannot pass as audit coverage.
func checkAuditTestAsserts(root, ref string, events []string, requireEvent bool) string {
	path, testName := splitAuditReference(ref)
	body, ok := goFunctionBody(root, path, testName)
	if !ok {
		return fmt.Sprintf("test %q not found in %s", testName, path)
	}
	if requireEvent {
		for _, literal := range events {
			if !containsAny(body, eventTokens(root, literal)) {
				return fmt.Sprintf("test %q does not assert event %q", testName, literal)
			}
		}
	}
	if !containsAny(body, auditAssertionMarkers) {
		return fmt.Sprintf("test %q does not assert an activity row", testName)
	}
	return ""
}

// checkAuditWriteContainsEvents requires the cited write function to reference
// every event it claims to record. A symbol that exists but records a
// different event is not a valid audit location for this row.
func checkAuditWriteContainsEvents(root, ref string, events []string) string {
	path, symbol := splitAuditReference(ref)
	if path == "" || symbol == "" {
		return fmt.Sprintf("reference %q must be a path plus write symbol", ref)
	}
	body, ok := goFunctionBody(root, path, symbol)
	if !ok {
		return fmt.Sprintf("symbol %q not found in %s", symbol, path)
	}
	for _, literal := range events {
		if !containsAny(body, eventTokens(root, literal)) {
			return fmt.Sprintf("write site %s does not reference event %q", ref, literal)
		}
	}
	return ""
}

// goFunctionBody returns the exact source of the named function or method. It
// uses the Go parser so braces in strings and comments cannot confuse the
// boundary.
func goFunctionBody(root, path, name string) (string, bool) {
	filename := filepath.Join(root, filepath.FromSlash(path))
	src, err := os.ReadFile(filename)
	if err != nil {
		return "", false
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		return "", false
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != name {
			continue
		}
		start := fset.Position(fn.Pos()).Offset
		end := fset.Position(fn.End()).Offset
		if start < 0 || end > len(src) || start >= end {
			return "", false
		}
		return string(src[start:end]), true
	}
	return "", false
}

// eventTokens returns the quoted literal plus any declared constant name for an
// event. Source normally uses the constant (activity.EventNodeCreate), while
// fixture tests may use the literal string.
func eventTokens(root, literal string) []string {
	tokens := []string{`"` + literal + `"`}
	path := "internal/activity/activity.go"
	if strings.HasPrefix(literal, "world_view.") {
		path = "internal/worldview/service.go"
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		return tokens
	}
	for _, match := range eventConstant.FindAllStringSubmatch(string(body), -1) {
		if match[2] == literal {
			tokens = append(tokens, match[1])
		}
	}
	return tokens
}

func containsAny(body string, tokens []string) bool {
	for _, token := range tokens {
		if token != "" && strings.Contains(body, token) {
			return true
		}
	}
	return false
}
