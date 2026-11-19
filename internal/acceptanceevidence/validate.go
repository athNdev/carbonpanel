// SPDX-License-Identifier: MPL-2.0
package acceptanceevidence

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var issueKeyPattern = regexp.MustCompile(`^OSS-\d+$`)

// Report collects every failure; validation is fail-closed, so a candidate with
// any failure blocks closure. All failures are reported, not just the first.
type Report struct {
	Passed   bool     `json:"passed"`
	Failures []string `json:"failures,omitempty"`
	Checked  int      `json:"checked"`
}

func (r *Report) failf(format string, args ...interface{}) {
	r.Failures = append(r.Failures, fmt.Sprintf(format, args...))
}

// ValidateContract checks the contract and each referenced evidence artifact.
// root is the repository root used to resolve artifact paths. All comparisons
// are made against the artifact, never against a self-declared contract claim.
func ValidateContract(c Contract, root string, now time.Time) Report {
	r := Report{Passed: true}
	if c.Version != "1" {
		r.failf("contract version %q is not 1", c.Version)
	}
	if c.Project.Key != "OSS" || c.Project.ID != "10001" {
		r.failf("contract project is %s/%s, want OSS/10001", c.Project.Key, c.Project.ID)
	}
	if c.Gate.MaxEvidenceAgeDays <= 0 {
		r.failf("gate.max_evidence_age_days must be positive")
	}
	// An empty candidate list is valid: it states that no work item is being
	// proposed for Done. The gate becomes meaningful as soon as a candidate is
	// added, at which point every required evidence field is enforced.
	seen := make(map[string]bool)
	for _, cand := range c.Candidates {
		r.Checked++
		validateCandidate(&r, c, cand, root, now, seen)
	}
	r.Passed = len(r.Failures) == 0
	return r
}

func validateCandidate(r *Report, c Contract, cand Candidate, root string, now time.Time, seen map[string]bool) {
	prefix := cand.IssueKey
	if prefix == "" {
		prefix = "<unnamed>"
	}
	fail := func(format string, args ...interface{}) {
		r.failf("%s: %s", prefix, fmt.Sprintf(format, args...))
	}

	if !issueKeyPattern.MatchString(cand.IssueKey) {
		fail("issue key %q is not an OSS work item", cand.IssueKey)
	}
	if seen[cand.IssueKey] {
		fail("duplicate candidate")
	}
	seen[cand.IssueKey] = true

	required, ok := requiredTier(cand.AcceptanceSurface)
	if !ok {
		fail("unknown acceptance_surface %q", cand.AcceptanceSurface)
		required = TierLiveVerified
	}
	if !ValidTier(cand.Tier) {
		fail("unknown evidence tier %q", cand.Tier)
	}
	rank := cand.Tier.Rank()

	verifiedAt, err := parseTime(cand.VerifiedAt)
	if err != nil {
		fail("verified_at is invalid (%v)", err)
	} else if verifiedAt.After(now.Add(time.Hour)) {
		fail("verified_at %s is in the future", cand.VerifiedAt)
	}
	if cand.Verifier == "" {
		fail("verifier is empty")
	}

	artifactPath := cand.Artifact
	if artifactPath == "" {
		artifactPath = filepath.Join("docs", "acceptance", "evidence", cand.IssueKey+".json")
	}
	full := artifactPath
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, filepath.FromSlash(artifactPath))
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		fail("evidence artifact %s is missing or unreadable: %v", artifactPath, err)
		return
	}
	var ev Evidence
	if err := json.Unmarshal(raw, &ev); err != nil {
		fail("evidence artifact %s is not valid JSON: %v", artifactPath, err)
		return
	}
	if ev.IssueKey != cand.IssueKey {
		fail("evidence artifact is for %q, not %s", ev.IssueKey, cand.IssueKey)
	}

	// Merge evidence is mandatory for every tier.
	if ev.Merge.PR != cand.PR.Number {
		fail("merge PR %d does not match contract PR %d", ev.Merge.PR, cand.PR.Number)
	}
	if !ev.Merge.Merged {
		fail("merge record says PR %d was not merged", cand.PR.Number)
	}
	if !validSHA(ev.Merge.MergeSHA) || normalizeSHA(ev.Merge.MergeSHA) != normalizeSHA(cand.PR.MergeSHA) {
		fail("merge SHA is stale or invalid: artifact %q vs contract %q", ev.Merge.MergeSHA, cand.PR.MergeSHA)
	}
	if ev.Merge.Author == "" {
		fail("merge author is empty; independence cannot be established")
	} else if strings.EqualFold(ev.Merge.Author, cand.Verifier) {
		fail("verifier %q is also the merge author %q (not independent)", cand.Verifier, ev.Merge.Author)
	}

	// CI evidence is required from CI-GREEN upward.
	if rank >= TierCIGreen.Rank() {
		if ev.CI.RunID != cand.CIRunID {
			fail("CI run id %d does not match contract run id %d", ev.CI.RunID, cand.CIRunID)
		}
		if ev.CI.Conclusion != "success" {
			fail("CI run %d conclusion is %q, want success", ev.CI.RunID, ev.CI.Conclusion)
		}
		if normalizeSHA(ev.CI.HeadSHA) != normalizeSHA(cand.PR.MergeSHA) {
			fail("CI head SHA %q is stale; expected merged SHA %q", ev.CI.HeadSHA, cand.PR.MergeSHA)
		}
	}

	if rank >= TierBuildOnly.Rank() {
		if ev.Build == nil {
			fail("tier %s requires build evidence", cand.Tier)
		} else if ev.Build.Conclusion != "success" {
			fail("build conclusion is %q, want success", ev.Build.Conclusion)
		}
	}

	if rank >= TierCaptureOnly.Rank() {
		if ev.Live == nil || ev.Live.Evidence == "" {
			fail("tier %s requires a captured evidence artifact", cand.Tier)
		} else {
			evidenceFile := ev.Live.Evidence
			if !filepath.IsAbs(evidenceFile) {
				evidenceFile = filepath.Join(root, filepath.FromSlash(evidenceFile))
			}
			if _, err := os.Stat(evidenceFile); err != nil {
				fail("captured evidence file %s is missing: %v", ev.Live.Evidence, err)
			}
		}
	}

	if rank >= TierDeploySkipped.Rank() {
		if ev.Deploy == nil {
			fail("tier %s requires deploy evidence", cand.Tier)
		} else {
			if cand.Deploy == nil {
				fail("contract has no deploy reference for tier %s", cand.Tier)
			} else {
				if ev.Deploy.RunID != cand.Deploy.RunID {
					fail("deploy run id %d does not match contract run id %d", ev.Deploy.RunID, cand.Deploy.RunID)
				}
				if ev.Deploy.Workflow != cand.Deploy.Workflow {
					fail("deploy workflow %q does not match contract workflow %q", ev.Deploy.Workflow, cand.Deploy.Workflow)
				}
			}
			if len(ev.Deploy.Jobs) == 0 {
				fail("deploy run has no job conclusions")
			}
			for _, job := range ev.Deploy.Jobs {
				switch job.Conclusion {
				case "success":
				case "skipped":
					if cand.Tier == TierLiveVerified {
						fail("deploy job %q was skipped but tier claims LIVE-VERIFIED", job.Name)
					}
				default:
					if cand.Tier == TierLiveVerified {
						fail("deploy job %q concluded %q, want success", job.Name, job.Conclusion)
					}
				}
			}
		}
	}

	if cand.Tier == TierLiveVerified {
		if ev.Live == nil {
			fail("LIVE-VERIFIED requires live evidence")
		} else {
			if cand.Live == nil || ev.Live.URL != cand.Live.URL {
				fail("live URL does not match contract URL")
			}
			if ev.Live.HTTPStatus < 200 || ev.Live.HTTPStatus > 399 {
				fail("live HTTP status %d is not a success/redirect", ev.Live.HTTPStatus)
			}
			checkedAt, err := parseTime(ev.Live.CheckedAt)
			if err != nil {
				fail("live checked_at is invalid: %v", err)
			} else if now.Sub(checkedAt) > time.Duration(c.Gate.MaxEvidenceAgeDays)*24*time.Hour {
				fail("live evidence is stale (checked_at %s exceeds %d days)", ev.Live.CheckedAt, c.Gate.MaxEvidenceAgeDays)
			} else if checkedAt.After(now.Add(time.Hour)) {
				fail("live checked_at is in the future")
			}
		}
	}

	if cand.Tier == TierDeploySkipped {
		hasSkipped := false
		if ev.Deploy != nil {
			for _, job := range ev.Deploy.Jobs {
				if job.Conclusion == "skipped" {
					hasSkipped = true
				}
			}
		}
		if !hasSkipped {
			fail("DEPLOY-SKIPPED tier has no skipped deploy job in the evidence")
		}
	}

	// A weaker tier than the surface requires is only legal with an explicit,
	// well-formed exception. Missing or malformed exceptions fail closed.
	if rank < required.Rank() {
		if cand.Exception == nil {
			fail("tier %s is below required %s and no exception is recorded", cand.Tier, required)
		} else {
			ex := cand.Exception
			if ex.Kind == "" || ex.Reason == "" || ex.RecordedAt == "" || ex.Reviewer == "" {
				fail("exception is incomplete (kind/reason/recorded_at/reviewer all required)")
			}
			if _, err := parseTime(ex.RecordedAt); err != nil {
				fail("exception recorded_at is invalid: %v", err)
			}
			if cand.Tier == TierDeploySkipped && ex.Kind != "deploy-skipped" {
				fail("DEPLOY-SKIPPED exception kind must be deploy-skipped, got %q", ex.Kind)
			}
			if cand.Tier == TierCaptureOnly && ex.Kind != "capture-only" {
				fail("CAPTURE-ONLY exception kind must be capture-only, got %q", ex.Kind)
			}
			if cand.Tier == TierBuildOnly && ex.Kind != "build-only" {
				fail("BUILD-ONLY exception kind must be build-only, got %q", ex.Kind)
			}
		}
	}
}
