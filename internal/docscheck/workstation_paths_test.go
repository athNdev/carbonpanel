// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docscheck

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// workstationPathPatterns match absolute workstation home directories that must
// never enter tracked source (knowledge/invariants/secrets-and-endpoints.md).
// Windows drive-letter homes and WSL mounts of Windows homes are the portable
// forms seen in practice; both are fail-closed below. Fixture-style absolute
// paths under /home/<service-user> are deliberately not matched: those describe
// disposable Linux qualification hosts, not a contributor's workstation.
var workstationPathPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)[a-z]:[\\/]Users[\\/][a-z0-9._-]+`),
	regexp.MustCompile(`(?i)/mnt/[a-z]/Users/[a-z0-9._-]+`),
}

// trackedFiles resolves the tracked file list through git. Any failure is
// fatal so the gate cannot silently pass when the file list is unavailable.
func trackedFiles(t *testing.T, root string) []string {
	t.Helper()
	cmd := exec.Command("git", "ls-files", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git ls-files failed (workstation-path gate cannot run): %v", err)
	}
	var files []string
	for _, raw := range bytes.Split(out, []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		files = append(files, filepath.ToSlash(string(raw)))
	}
	if len(files) == 0 {
		t.Fatal("git ls-files returned no tracked files; refusing to pass the workstation-path gate")
	}
	return files
}

// TestNoAbsoluteWorkstationPaths is the fail-closed backstop for OSS-397. A
// workstation path reintroduced anywhere in tracked source fails go test ./...,
// not just a review checklist.
func TestNoAbsoluteWorkstationPaths(t *testing.T) {
	root := repoRoot(t)
	var findings []string
	for _, rel := range trackedFiles(t, root) {
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			// A tracked file that vanished from the checkout is a broken
			// checkout, not a path violation; let other tests surface it.
			t.Logf("skip unreadable tracked file %s: %v", rel, err)
			continue
		}
		if bytes.IndexByte(content, 0) >= 0 {
			continue // binary asset
		}
		for i, line := range strings.Split(string(content), "\n") {
			for _, pattern := range workstationPathPatterns {
				if loc := pattern.FindStringIndex(line); loc != nil {
					findings = append(findings,
						fmt.Sprintf("%s:%d: absolute workstation path %q: %s",
							rel, i+1, line[loc[0]:loc[1]], strings.TrimSpace(line)))
					break
				}
			}
		}
	}
	for _, finding := range findings {
		t.Error(finding)
	}
}

// TestWorkstationPathPatternsDetectsBothForms proves the gate patterns catch
// both portable workstation path shapes without embedding a literal match in
// this source file (which would trip the gate itself).
func TestWorkstationPathPatternsDetectsBothForms(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"windows home", "C:" + "/Users/someone/project"},
		{"windows home backslash", "D:" + `\Users\someone\project`},
		{"wsl mount home", "/mnt/" + "c/Users/someone/project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			matched := false
			for _, pattern := range workstationPathPatterns {
				if pattern.MatchString(tc.path) {
					matched = true
					break
				}
			}
			if !matched {
				t.Fatalf("pattern set missed %q", tc.path)
			}
		})
	}

	negative := []string{
		"/home/ubuntu/carbon-qual/docker",
		"docs/runbook-mcp-smoke.md",
		"$CARBONPANEL_AUDIT_DIR/mcp-plans/evidence.json",
	}
	for _, path := range negative {
		for _, pattern := range workstationPathPatterns {
			if pattern.MatchString(path) {
				t.Errorf("pattern %s falsely matched non-workstation path %q", pattern, path)
			}
		}
	}
}
