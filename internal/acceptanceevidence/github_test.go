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

func githubFixture() Contract {
	return Contract{
		Version: "1",
		Gate:    Gate{RequiredPRList: "acceptance-evidence-verified", MaxEvidenceAgeDays: 7},
		Candidates: []Candidate{{
			IssueKey: "OSS-999",
			PR:       PRRef{Number: 42, MergeSHA: validSHA1, Repository: "owner/repo"},
			CIRunID:  100,
			Tier:     TierLiveVerified,
			Deploy:   &DeployRef{Workflow: "cd.yml", RunID: 200, Job: "publish"},
		}},
	}
}

func githubServer(t *testing.T, mergeSHA, conclusion, label string, jobConclusion string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(req.URL.Path, "/pulls/42"):
			fmt.Fprintf(w, `{"merged":true,"merge_commit_sha":%q,"labels":[{"name":%q}]}`, mergeSHA, label)
		case strings.HasSuffix(req.URL.Path, "/actions/runs/100"):
			fmt.Fprintf(w, `{"conclusion":%q,"head_sha":%q}`, conclusion, mergeSHA)
		case strings.HasSuffix(req.URL.Path, "/actions/runs/200/jobs"):
			fmt.Fprintf(w, `{"jobs":[{"name":"publish","conclusion":%q}]}`, jobConclusion)
		default:
			http.NotFound(w, req)
		}
	}))
}

func TestVerifyGitHubPositive(t *testing.T) {
	srv := githubServer(t, validSHA1, "success", "acceptance-evidence-verified", "success")
	defer srv.Close()
	c := &GitHubClient{BaseURL: srv.URL}
	failures := c.VerifyGitHub(context.Background(), githubFixture())
	if len(failures) != 0 {
		t.Fatalf("expected no failures, got %v", failures)
	}
}

func TestVerifyGitHubDetectsDriftAndSkippedJob(t *testing.T) {
	srv := githubServer(t, validSHA2, "failure", "other-label", "skipped")
	defer srv.Close()
	c := &GitHubClient{BaseURL: srv.URL}
	failures := c.VerifyGitHub(context.Background(), githubFixture())
	joined := strings.Join(failures, " ")
	for _, want := range []string{"merge SHA drifted", "missing required label", "conclusion is", "different SHA", "skipped"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q in failures, got %s", want, joined)
		}
	}
}
