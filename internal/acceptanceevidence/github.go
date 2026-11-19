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
// the workflow-provided token and never writes secrets to output.
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
	Conclusion string `json:"conclusion"`
	HeadSHA    string `json:"head_sha"`
}

type ghJobs struct {
	Jobs []struct {
		Name       string `json:"name"`
		Conclusion string `json:"conclusion"`
	} `json:"jobs"`
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
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s returned %d", path, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}

// VerifyGitHub checks the live GitHub facts referenced by the contract: the PR
// is merged at the exact SHA, carries the required label, the CI run is green on
// that SHA, and no deploy job is skipped. It reports drift as failures.
func (c *GitHubClient) VerifyGitHub(ctx context.Context, contract Contract) []string {
	var failures []string
	fail := func(format string, args ...interface{}) {
		failures = append(failures, fmt.Sprintf(format, args...))
	}
	for _, cand := range contract.Candidates {
		repo := cand.PR.Repository
		if repo == "" {
			repo = c.Repository
		}
		if repo == "" {
			fail("%s: no repository configured for PR drift check", cand.IssueKey)
			continue
		}
		base := "/repos/" + repo

		var pr ghPR
		if err := c.get(ctx, fmt.Sprintf("%s/pulls/%d", base, cand.PR.Number), &pr); err != nil {
			fail("%s: cannot read PR %d: %v", cand.IssueKey, cand.PR.Number, err)
			continue
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

		var ci ghRun
		if err := c.get(ctx, fmt.Sprintf("%s/actions/runs/%d", base, cand.CIRunID), &ci); err != nil {
			fail("%s: cannot read CI run %d: %v", cand.IssueKey, cand.CIRunID, err)
		} else {
			if ci.Conclusion != "success" {
				fail("%s: CI run %d conclusion is %q", cand.IssueKey, cand.CIRunID, ci.Conclusion)
			}
			if normalizeSHA(ci.HeadSHA) != normalizeSHA(cand.PR.MergeSHA) {
				fail("%s: CI run %d checked a different SHA %q", cand.IssueKey, cand.CIRunID, ci.HeadSHA)
			}
		}

		if cand.Deploy != nil {
			var jobs ghJobs
			if err := c.get(ctx, fmt.Sprintf("%s/actions/runs/%d/jobs", base, cand.Deploy.RunID), &jobs); err != nil {
				fail("%s: cannot read deploy run %d: %v", cand.IssueKey, cand.Deploy.RunID, err)
			} else {
				if len(jobs.Jobs) == 0 {
					fail("%s: deploy run %d has no jobs", cand.IssueKey, cand.Deploy.RunID)
				}
				for _, job := range jobs.Jobs {
					if job.Conclusion == "skipped" && cand.Tier == TierLiveVerified {
						fail("%s: deploy job %q was skipped", cand.IssueKey, job.Name)
					}
				}
			}
		}
	}
	return failures
}
