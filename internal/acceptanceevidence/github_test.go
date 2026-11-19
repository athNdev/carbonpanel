// SPDX-License-Identifier: MPL-2.0
package acceptanceevidence

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type deployFixture struct {
	runConclusion string
	runSHA        string
	jobs          string // JSON array of {name,conclusion,head_sha}
}

func githubFixture() Contract {
	return Contract{
		Version: "1",
		Gate:    Gate{RequiredPRList: "acceptance-evidence-verified", MaxEvidenceAgeDays: 7, Scope: "record-auditor"},
		ReferenceRuns: []ReferenceRun{{RunID: 555, Name: "Deploy Website to GitHub Pages",
			HeadSHA: deploySHA, Conclusion: "success"}},
		Candidates: []Candidate{{
			IssueKey:         "OSS-999",
			PR:               PRRef{Number: 42, MergeSHA: mergeSHA, Repository: "owner/repo"},
			CIRunID:          100,
			TrustedDeploySHA: deploySHA,
			Tier:             TierLiveVerified,
			Deploy:           &DeployRef{Workflow: deployWorkflow, RunID: 200, Job: "deploy", RevisionSHA: deploySHA},
		}},
	}
}

func githubServer(t *testing.T, f deployFixture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(req.URL.Path, "/pulls/42"):
			fmt.Fprintf(w, `{"merged":true,"merge_commit_sha":%q,"labels":[{"name":"acceptance-evidence-verified"}]}`, mergeSHA)
		case strings.HasSuffix(req.URL.Path, "/actions/runs/100"):
			fmt.Fprintf(w, `{"conclusion":"success","head_sha":%q}`, deploySHA)
		case strings.HasSuffix(req.URL.Path, "/actions/runs/200/jobs"):
			fmt.Fprint(w, f.jobs)
		case strings.HasSuffix(req.URL.Path, "/actions/runs/200"):
			fmt.Fprintf(w, `{"name":%q,"conclusion":%q,"head_sha":%q}`, deployWorkflow, f.runConclusion, f.runSHA)
		case strings.HasSuffix(req.URL.Path, "/actions/runs/555"):
			fmt.Fprintf(w, `{"name":%q,"conclusion":"success","head_sha":%q}`, deployWorkflow, deploySHA)
		default:
			http.NotFound(w, req)
		}
	}))
}

func jobs(conclusions ...string) string {
	names := []string{"build", "deploy", "indexable-changes", "notify-indexnow"}
	parts := make([]string, 0, len(conclusions))
	for i, c := range conclusions {
		parts = append(parts, fmt.Sprintf(`{"name":%q,"conclusion":%q}`, names[i], c))
	}
	return `{"jobs":[` + strings.Join(parts, ",") + `]}`
}

func testDrift(t *testing.T, f deployFixture) DriftResult {
	t.Helper()
	srv := githubServer(t, f)
	defer srv.Close()
	c := &GitHubClient{BaseURL: srv.URL}
	return c.VerifyGitHub(context.Background(), githubFixture())
}

func TestDriftPositiveTrustedDescendantAndOptionalSkip(t *testing.T) {
	r := testDrift(t, deployFixture{"success", deploySHA, jobs("success", "success", "success", "skipped")})
	if r.Audited != 1 || r.ReferenceRuns != 1 || len(r.Failures) != 0 {
		t.Fatalf("expected clean audit, got %+v", r)
	}
}

func TestDriftRejectsSkippedNamedJob(t *testing.T) {
	r := testDrift(t, deployFixture{"success", deploySHA, jobs("success", "skipped", "success", "skipped")})
	if !strings.Contains(strings.Join(r.Failures, " "), "required deploy job") {
		t.Fatalf("expected skipped named job failure, got %v", r.Failures)
	}
}

func TestDriftRejectsFailedNamedJob(t *testing.T) {
	r := testDrift(t, deployFixture{"success", deploySHA, jobs("success", "failure", "success", "skipped")})
	if !strings.Contains(strings.Join(r.Failures, " "), "required deploy job") {
		t.Fatalf("expected failed named job failure, got %v", r.Failures)
	}
}

func TestDriftRejectsBuildOnlyRun(t *testing.T) {
	r := testDrift(t, deployFixture{"success", deploySHA, jobs("success")})
	if !strings.Contains(strings.Join(r.Failures, " "), "required deploy job") {
		t.Fatalf("expected missing named job failure, got %v", r.Failures)
	}
}

func TestDriftRejectsWrongDeploySHA(t *testing.T) {
	r := testDrift(t, deployFixture{"success", wrongSHA, jobs("success", "success", "success", "skipped")})
	joined := strings.Join(r.Failures, " ")
	if !strings.Contains(joined, "deploy run SHA") {
		t.Fatalf("expected deploy SHA failure, got %v", r.Failures)
	}
}

func TestDriftReportsAuditedCounts(t *testing.T) {
	r := testDrift(t, deployFixture{"success", deploySHA, jobs("success", "success", "success", "skipped")})
	if r.Audited == 0 || r.ReferenceRuns == 0 {
		t.Fatalf("no-op audit must be visible, got %+v", r)
	}
}
