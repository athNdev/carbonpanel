// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docscheck

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot walks up from this file to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	dir := filepath.Dir(file)
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("go.mod not found above the test file")
	return ""
}

// TestFeatureMatrixCitesExistingSource is the regression this package exists
// for: a renamed or deleted package must fail here, not silently turn a matrix
// row into a claim with no implementation.
func TestFeatureMatrixCitesExistingSource(t *testing.T) {
	root := repoRoot(t)
	const matrix = "docs/plans/oss-feature-completeness-matrix.md"
	if _, err := os.Stat(filepath.Join(root, matrix)); err != nil {
		t.Skipf("%s is not present in this checkout: %v", matrix, err)
	}

	problems, err := CheckMatrix(root, matrix)
	if err != nil {
		t.Fatalf("CheckMatrix: %v", err)
	}
	for _, problem := range problems {
		t.Errorf("%s", problem)
	}
}

func TestAdminAuditInventoryIsSourceBacked(t *testing.T) {
	root := repoRoot(t)
	const inventory = "docs/plans/oss358-admin-action-audit-inventory.md"
	if _, err := os.Stat(filepath.Join(root, inventory)); err != nil {
		t.Fatalf("%s is not present in this checkout: %v", inventory, err)
	}

	problems, err := CheckAuditInventory(root, inventory)
	if err != nil {
		t.Fatalf("CheckAuditInventory: %v", err)
	}
	for _, problem := range problems {
		t.Errorf("%s", problem)
	}
}

func TestAdminAuditInventoryMissingFileFailsClosed(t *testing.T) {
	root := t.TempDir()
	if _, err := CheckAuditInventory(root, "docs/plans/missing-inventory.md"); err == nil {
		t.Fatal("CheckAuditInventory accepted a missing inventory file")
	}
}

func TestAdminAuditInventoryReportsMissingCoveringTest(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "x.go"), []byte("func Foo() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "x_test.go"), []byte("func Present(t *testing.T) {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := "| A01 | Test | `internal/x.go:Foo` | `x:y` | `none` | `none` | `internal/x_test.go:Missing` | **Covered** |\n"
	problems, err := CheckAuditInventoryText(root, text)
	if err != nil {
		t.Fatalf("CheckAuditInventoryText: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Detail, `test "Missing" not found`) {
		t.Fatalf("problems = %v, want the missing covering-test defect", problems)
	}
}

// newAuditInventoryFixture builds the smallest repository docscheck can walk:
// an event source, an entry/write source and a test that reads activity rows.
func newAuditInventoryFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"internal/activity/activity.go": `package activity

const (
	EventAlpha = "alpha.one"
	EventBeta  = "beta.two"
)
`,
		"internal/rpc/services/admin.go": `package services

func DoBoth() {
	_ = "alpha.one"
	_ = "beta.two"
}

func DoOther() {
	_ = "unrelated.event"
}
`,
		"internal/rpc/services/admin_test.go": `package services

func TestAdminAudit(t *testing.T) {
	rows := adminAuditRows(t)
	_ = rows
	_ = "alpha.one"
	_ = "beta.two"
}

func TestNotAnAudit(t *testing.T) {
	_ = "alpha.one"
}
`,
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func auditInventoryRow(event, write, testRef, disposition string) string {
	return "| A01 | Test | `internal/rpc/services/admin.go:DoBoth` | `x:y` | " +
		event + " | " + write + " | " + testRef + " | **" + disposition + "** |\n"
}

func auditProblems(t *testing.T, root, text string) []Problem {
	t.Helper()
	problems, err := CheckAuditInventoryText(root, text)
	if err != nil {
		t.Fatalf("CheckAuditInventoryText: %v", err)
	}
	return problems
}

func requireAuditProblem(t *testing.T, problems []Problem, want string) {
	t.Helper()
	for _, problem := range problems {
		if strings.Contains(problem.Detail, want) {
			return
		}
	}
	t.Fatalf("problems = %v, want detail containing %q", problems, want)
}

func TestAdminAuditInventoryAcceptsCompleteAddedRow(t *testing.T) {
	root := newAuditInventoryFixture(t)
	row := auditInventoryRow(
		"`alpha.one` / `beta.two`",
		"`internal/rpc/services/admin.go:DoBoth`",
		"`internal/rpc/services/admin_test.go:TestAdminAudit`",
		"Added",
	)
	if problems := auditProblems(t, root, row); len(problems) != 0 {
		t.Fatalf("complete Added row problems = %v, want none", problems)
	}
}

func TestAdminAuditInventoryAddedRequiresEvent(t *testing.T) {
	root := newAuditInventoryFixture(t)
	row := auditInventoryRow(
		"none",
		"`internal/rpc/services/admin.go:DoBoth`",
		"`internal/rpc/services/admin_test.go:TestAdminAudit`",
		"Added",
	)
	requireAuditProblem(t, auditProblems(t, root, row), "requires a declared activity event literal")
}

func TestAdminAuditInventoryAddedRequiresWrite(t *testing.T) {
	root := newAuditInventoryFixture(t)
	row := auditInventoryRow(
		"`alpha.one`",
		"none",
		"`internal/rpc/services/admin_test.go:TestAdminAudit`",
		"Added",
	)
	requireAuditProblem(t, auditProblems(t, root, row), "requires an audit write location")
}

func TestAdminAuditInventoryAddedRequiresAuditTest(t *testing.T) {
	root := newAuditInventoryFixture(t)
	row := auditInventoryRow(
		"`alpha.one`",
		"`internal/rpc/services/admin.go:DoBoth`",
		"-",
		"Added",
	)
	requireAuditProblem(t, auditProblems(t, root, row), "requires a covering test that asserts the audit row")
}

func TestAdminAuditInventoryChecksEveryEventLiteral(t *testing.T) {
	root := newAuditInventoryFixture(t)
	// Remove the second declaration; the row still names both events.
	path := filepath.Join(root, "internal", "activity", "activity.go")
	if err := os.WriteFile(path, []byte("package activity\n\nconst EventAlpha = \"alpha.one\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	row := auditInventoryRow(
		"`alpha.one` / `beta.two`",
		"`internal/rpc/services/admin.go:DoBoth`",
		"`internal/rpc/services/admin_test.go:TestAdminAudit`",
		"Added",
	)
	requireAuditProblem(t, auditProblems(t, root, row), `event "beta.two" not declared`)
}

func TestAdminAuditInventoryWriteMustContainEvent(t *testing.T) {
	root := newAuditInventoryFixture(t)
	row := auditInventoryRow(
		"`alpha.one`",
		"`internal/rpc/services/admin.go:DoOther`",
		"`internal/rpc/services/admin_test.go:TestAdminAudit`",
		"Added",
	)
	requireAuditProblem(t, auditProblems(t, root, row), `does not reference event "alpha.one"`)
}

func TestAdminAuditInventoryRejectsNonAuditCoveringTest(t *testing.T) {
	root := newAuditInventoryFixture(t)
	row := auditInventoryRow(
		"`alpha.one`",
		"`internal/rpc/services/admin.go:DoBoth`",
		"`internal/rpc/services/admin_test.go:TestNotAnAudit`",
		"Covered",
	)
	requireAuditProblem(t, auditProblems(t, root, row), "does not assert an activity row")
}

func TestArchitectureSeamsAreCitedAndExist(t *testing.T) {
	root := repoRoot(t)
	const design = "DESIGN.md"
	if _, err := os.Stat(filepath.Join(root, design)); err != nil {
		t.Skipf("%s is not present in this checkout: %v", design, err)
	}

	problems, err := CheckRecordedSeams(root, design)
	if err != nil {
		t.Fatalf("CheckRecordedSeams: %v", err)
	}
	for _, problem := range problems {
		t.Errorf("%s", problem)
	}
}

func TestCheckRecordedSeamsReportsAMissingCitation(t *testing.T) {
	root := t.TempDir()
	for _, seam := range RecordedSeams {
		path := filepath.Join(root, filepath.FromSlash(seam.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if filepath.Ext(path) == "" {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(path, []byte("package docker\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "DESIGN.md"), []byte("# empty\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	problems, err := CheckRecordedSeams(root, "DESIGN.md")
	if err != nil {
		t.Fatalf("CheckRecordedSeams: %v", err)
	}
	if len(problems) != len(RecordedSeams) {
		t.Fatalf("problems = %v, want one missing citation per seam", problems)
	}
}

func TestCheckRecordedSeamsReportsAMissingPath(t *testing.T) {
	root := t.TempDir()
	var cites []string
	for _, seam := range RecordedSeams {
		cites = append(cites, "`"+seam.Path+"`")
	}
	if err := os.WriteFile(filepath.Join(root, "DESIGN.md"), []byte(strings.Join(cites, " ")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	problems, err := CheckRecordedSeams(root, "DESIGN.md")
	if err != nil {
		t.Fatalf("CheckRecordedSeams: %v", err)
	}
	if len(problems) != len(RecordedSeams) {
		t.Fatalf("problems = %v, want one missing path per seam", problems)
	}
}

func TestCheckMatrixReportsAMissingPath(t *testing.T) {
	problems, err := CheckMatrixText(t.TempDir(), "| O01 — Example | UI | `Services/gone.go` | **Source-present.** text |")
	if err != nil {
		t.Fatalf("CheckMatrixText: %v", err)
	}
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one missing path", problems)
	}
	if !strings.Contains(problems[0].Detail, "does not exist") {
		t.Errorf("detail = %q, want it to name the missing path", problems[0].Detail)
	}
}

func TestCheckMatrixReportsAnUnknownStatus(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal", "rpc", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "rpc", "services", "server.go"), []byte("package services"), 0o644); err != nil {
		t.Fatal(err)
	}

	problems, err := CheckMatrixText(root, "| O01 — Example | UI | `Services/server.go` | looks fine to me |")
	if err != nil {
		t.Fatalf("CheckMatrixText: %v", err)
	}
	if len(problems) != 1 || !strings.Contains(problems[0].Detail, "status") {
		t.Fatalf("problems = %v, want one unknown-status problem", problems)
	}
}

func TestCheckMatrixReportsADuplicateID(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "internal"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "internal", "x.go"), []byte("package internal"), 0o644); err != nil {
		t.Fatal(err)
	}
	text := strings.Join([]string{
		"| O01 — First | UI | `internal/x.go` | **Source-present.** |",
		"| O01 — Second | UI | `internal/x.go` | **Source-present.** |",
	}, "\n")

	problems, err := CheckMatrixText(root, text)
	if err != nil {
		t.Fatalf("CheckMatrixText: %v", err)
	}
	var found bool
	for _, problem := range problems {
		if strings.Contains(problem.Detail, "duplicate feature id") {
			found = true
		}
	}
	if !found {
		t.Fatalf("problems = %v, want a duplicate-id problem", problems)
	}
}

func TestReferencedPathStripsIdentifiers(t *testing.T) {
	cases := map[string]string{
		"internal/docker/client.go:applyContainerHardening:130": "internal/docker/client.go",
		"Services/server.go":    "Services/server.go",
		"EULA=TRUE":             "",
		"Source-present":        "",
		"https://example.com/x": "",
		"":                      "",
	}
	for input, want := range cases {
		if got := referencedPath(input); got != want {
			t.Errorf("referencedPath(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestResolveAlias(t *testing.T) {
	cases := map[string]string{
		"Services/server.go":      "internal/rpc/services/server.go",
		"Components/foo.svelte":   "web/carbon-panel/src/lib/components/foo.svelte",
		"routes/servers/+page.ts": "web/carbon-panel/src/routes/servers/+page.ts",
		"lib/utils/foo.ts":        "web/carbon-panel/src/lib/utils/foo.ts",
		"internal/docker":         "internal/docker",
	}
	for input, want := range cases {
		if got := resolveAlias(input); got != want {
			t.Errorf("resolveAlias(%q) = %q, want %q", input, got, want)
		}
	}
}
