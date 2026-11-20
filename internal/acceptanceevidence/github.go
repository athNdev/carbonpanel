// SPDX-License-Identifier: MPL-2.0
package acceptanceevidence

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// GitHubClient performs the scheduled drift audit against GitHub's API. It uses
// the workflow-provided token and never writes secrets to output. It is an
// opt-in record auditor; it does not read Jira and does not prevent a Done click.
type GitHubClient struct {
	BaseURL    string
	Token      string
	HTTP       *http.Client
	Repository string
}

type ghPR struct {
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Labels         []struct {
		Name string `json:"name"`
	} `json:"labels"`
}

type ghRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
}

type ghJobs struct {
	Jobs []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
	} `json:"jobs"`
}

// DriftResult reports what was actually audited so a no-op cannot be mistaken
// for a successful audit.
type DriftResult struct {
	Audited       int      `json:"audited"`
	ReferenceRuns int      `json:"reference_runs"`
	Failures      []string `json:"failures,omitempty"`
}

func (c *GitHubClient) get(ctx context.Context, path string, target interface{}) error {
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

func (c *GitHubClient) verifyCandidate(ctx context.Context, contract Contract, cand Candidate, fail func(string, ...interface{})) {
	repo := cand.PR.Repository
	if repo == "" {
		repo = c.Repository
	}
	if repo == "" {
		fail("%s: no repository configured for PR drift check", cand.IssueKey)
		return
	}
	base := "/repos/" + repo

	var pr ghPR
	if err := c.get(ctx, fmt.Sprintf("%s/pulls/%d", base, cand.PR.Number), &pr); err != nil {
		fail("%s: cannot read PR %d: %v", cand.IssueKey, cand.PR.Number, err)
		return
	}
	if !pr.Merged {
		fail("%s: PR %d is not merged", cand.IssueKey, cand.PR.Number)
	}
	if normalizeSHA(pr.MergeCommitSHA) != normalizeSHA(cand.PR.MergeSHA) {
		fail("%s: PR %d merge SHA drifted: live %q vs contract %q",
			cand.IssueKey, cand.PR.Number, pr.MergeCommitSHA, cand.PR.MergeSHA)
	}
	if contract.Gate.RequiredPRList != "" {
		found := false
		for _, l := range pr.Labels {
			if l.Name == contract.Gate.RequiredPRList {
				found = true
			}
		}
		if !found {
			fail("%s: PR %d is missing required label %q", cand.IssueKey, cand.PR.Number, contract.Gate.RequiredPRList)
		}
	}

	mergeSHA := normalizeSHA(cand.PR.MergeSHA)
	descendant := normalizeSHA(cand.TrustedDeploySHA)
	if cand.TrustedDeploySHA != "" && descendant != mergeSHA {
		if !validSHA(mergeSHA) || !validSHA(descendant) {
			fail("%s: cannot verify ancestry without full commit SHAs", cand.IssueKey)
			return
		}
		var comparison struct {
			Status    string `json:"status"`
			MergeBase struct {
				SHA string `json:"sha"`
			} `json:"merge_base_commit"`
		}
		path := fmt.Sprintf("%s/compare/%s...%s?per_page=1", base, mergeSHA, descendant)
		if err := c.get(ctx, path, &comparison); err != nil {
			fail("%s: cannot verify ancestry of trusted SHA %s: %v", cand.IssueKey, descendant, err)
		} else if comparison.Status != "ahead" || normalizeSHA(comparison.MergeBase.SHA) != mergeSHA {
			fail("%s: trusted SHA %s is not a descendant of PR merge SHA %s", cand.IssueKey, descendant, mergeSHA)
		}
	}

	var ci ghRun
	if err := c.get(ctx, fmt.Sprintf("%s/actions/runs/%d", base, cand.CIRunID), &ci); err != nil {
		fail("%s: cannot read CI run %d: %v", cand.IssueKey, cand.CIRunID, err)
	} else {
		if ci.Conclusion != "success" {
			fail("%s: CI run %d conclusion is %q", cand.IssueKey, cand.CIRunID, ci.Conclusion)
		}
		if !isAcceptedRevision(cand, ci.HeadSHA) {
			fail("%s: CI run %d checked SHA %q, neither merge nor trusted descendant", cand.IssueKey, cand.CIRunID, ci.HeadSHA)
		}
	}

	if cand.Deploy == nil {
		return
	}
	var run ghRun
	if err := c.get(ctx, fmt.Sprintf("%s/actions/runs/%d", base, cand.Deploy.RunID), &run); err != nil {
		fail("%s: cannot read deploy run %d: %v", cand.IssueKey, cand.Deploy.RunID, err)
		return
	}
	if strings.TrimSpace(cand.Deploy.Workflow) == "" || run.Name != cand.Deploy.Workflow {
		fail("%s: deploy run %d workflow is %q, want declared workflow %q", cand.IssueKey, cand.Deploy.RunID, run.Name, cand.Deploy.Workflow)
	}
	if run.Conclusion != "success" {
		fail("%s: deploy run %d conclusion is %q", cand.IssueKey, cand.Deploy.RunID, run.Conclusion)
	}
	if cand.Deploy.RevisionSHA == "" || normalizeSHA(run.HeadSHA) != normalizeSHA(cand.Deploy.RevisionSHA) {
		fail("%s: deploy run SHA %q does not match qualified revision %q", cand.IssueKey, run.HeadSHA, cand.Deploy.RevisionSHA)
	}
	if !isAcceptedRevision(cand, run.HeadSHA) {
		fail("%s: deploy run SHA %q is neither merge nor a recorded trusted descendant", cand.IssueKey, run.HeadSHA)
	}

	var jobs ghJobs
	if err := c.get(ctx, fmt.Sprintf("%s/actions/runs/%d/jobs", base, cand.Deploy.RunID), &jobs); err != nil {
		fail("%s: cannot read deploy run %d jobs: %v", cand.IssueKey, cand.Deploy.RunID, err)
		return
	}
	found := false
	for _, job := range jobs.Jobs {
		if job.Name != cand.Deploy.Job {
			if job.Conclusion == "failure" || job.Conclusion == "cancelled" || job.Conclusion == "timed_out" {
				fail("%s: optional deploy job %q concluded %q", cand.IssueKey, job.Name, job.Conclusion)
			}
			continue
		}
		found = true
		switch job.Conclusion {
		case "success":
		case "skipped":
			fail("%s: required deploy job %q was skipped", cand.IssueKey, job.Name)
		default:
			fail("%s: required deploy job %q concluded %q, want success", cand.IssueKey, job.Name, job.Conclusion)
		}
	}
	if !found {
		fail("%s: required deploy job %q is missing from run %d", cand.IssueKey, cand.Deploy.Job, cand.Deploy.RunID)
	}
}

// VerifyGitHub checks live GitHub facts referenced by the contract: PR merge
// SHA, required label, CI run, trusted descendant ancestry, and the named
// workflow and deploy job at the qualified revision. Reference runs prove the
// audit path is live even when candidates
// are few. It reports drift as failures.
func (c *GitHubClient) VerifyGitHub(ctx context.Context, contract Contract) DriftResult {
	result := DriftResult{}
	fail := func(format string, args ...interface{}) {
		result.Failures = append(result.Failures, fmt.Sprintf(format, args...))
	}
	for _, cand := range contract.Candidates {
		result.Audited++
		c.verifyCandidate(ctx, contract, cand, fail)
	}
	repo := c.Repository
	if len(contract.Candidates) > 0 && contract.Candidates[0].PR.Repository != "" {
		repo = contract.Candidates[0].PR.Repository
	}
	for _, ref := range contract.ReferenceRuns {
		result.ReferenceRuns++
		if repo == "" {
			fail("reference run %d: no repository configured", ref.RunID)
			continue
		}
		var run ghRun
		if err := c.get(ctx, fmt.Sprintf("/repos/%s/actions/runs/%d", repo, ref.RunID), &run); err != nil {
			fail("reference run %d: cannot read: %v", ref.RunID, err)
			continue
		}
		if run.Conclusion != ref.Conclusion {
			fail("reference run %d conclusion is %q, want %q", ref.RunID, run.Conclusion, ref.Conclusion)
		}
		if normalizeSHA(run.HeadSHA) != normalizeSHA(ref.HeadSHA) {
			fail("reference run %d head SHA drifted: live %q vs recorded %q", ref.RunID, run.HeadSHA, ref.HeadSHA)
		}
		if ref.Name != "" && run.Name != ref.Name {
			fail("reference run %d name is %q, want %q", ref.RunID, run.Name, ref.Name)
		}
	}
	return result
}
