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
