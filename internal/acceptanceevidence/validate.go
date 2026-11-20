// SPDX-License-Identifier: MPL-2.0
package acceptanceevidence

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var issueKeyPattern = regexp.MustCompile(`^OSS-\d+$`)

var allowedExceptionKinds = map[string]bool{
	"external-blocker": true,
	"deploy-skipped":   true,
	"capture-only":     true,
	"build-only":       true,
	"ci-unavailable":   true,
}

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
	if c.Gate.Scope != "record-auditor" {
		r.failf("gate.scope %q must be record-auditor; this layer does not gate Jira", c.Gate.Scope)
	}
	// An empty candidate set means the scheduled verifier would audit nothing.
	// That is a configuration failure, not a successful run.
	if len(c.Candidates) == 0 {
		r.failf("contract has no Done candidates; scheduled audit would be a no-op")
	}
	if len(c.ReferenceRuns) == 0 {
		r.failf("contract has no reference runs; scheduled audit has no live proof fixture")
	}
	for _, ref := range c.ReferenceRuns {
		prefix := fmt.Sprintf("reference run %d", ref.RunID)
		if ref.RunID <= 0 {
			prefix = "reference run"
			r.failf("%s: run_id must be positive", prefix)
		}
		if !validSHA(ref.HeadSHA) {
			r.failf("%s: head_sha %q is not a full commit SHA", prefix, ref.HeadSHA)
		}
		if strings.TrimSpace(ref.Name) == "" {
			r.failf("%s: name is empty", prefix)
		}
		if strings.TrimSpace(ref.Conclusion) != "success" {
			r.failf("%s: conclusion %q must be success for a real audit fixture", prefix, ref.Conclusion)
		}
	}

	seen := make(map[string]bool)
	for _, cand := range c.Candidates {
		r.Checked++
		validateCandidate(&r, c, cand, root, now, seen)
	}
	r.Passed = len(r.Failures) == 0
	return r
}

func isAcceptedRevision(c Candidate, sha string) bool {
	for _, want := range acceptedRevisionSHAs(c) {
		if normalizeSHA(sha) == want {
			return true
		}
	}
	return false
}

func validDurableURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
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

	// Missing state defaults to the strict proposed path; a historical record
	// must opt in explicitly so a stale live claim cannot hide in the contract.
	if cand.ClosureState == "" {
		cand.ClosureState = ClosureProposed
	}
	if !cand.ClosureState.Valid() {
		fail("unknown closure_state %q (want proposed or recorded)", cand.ClosureState)
	}

	required, ok := requiredTier(cand.AcceptanceSurface)
	if !ok {
		fail("unknown acceptance_surface %q", cand.AcceptanceSurface)
		required = TierLiveVerified
	}
	if !ValidTier(cand.Tier) {
		fail("unknown evidence tier %q", cand.Tier)
	}
	rank := cand.Tier.Rank()

	if cand.TrustedDeploySHA != "" && !validSHA(cand.TrustedDeploySHA) {
		fail("trusted_deploy_sha %q is not a full commit SHA", cand.TrustedDeploySHA)
	}

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

	// CI evidence is required from CI-GREEN upward. It may run on the PR merge
	// SHA or on an explicitly recorded trusted descendant.
	if rank >= TierCIGreen.Rank() {
		if ev.CI.RunID != cand.CIRunID {
			fail("CI run id %d does not match contract run id %d", ev.CI.RunID, cand.CIRunID)
		}
		if ev.CI.Conclusion != "success" {
			fail("CI run %d conclusion is %q, want success", ev.CI.RunID, ev.CI.Conclusion)
		}
		if !isAcceptedRevision(cand, ev.CI.HeadSHA) {
			fail("CI head SHA %q is neither the merge SHA nor the recorded trusted descendant", ev.CI.HeadSHA)
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
			if ev.Deploy.URL != "" && !validDurableURL(ev.Deploy.URL) {
				fail("deploy evidence URL %q is not a durable http(s) URL", ev.Deploy.URL)
			}
			if cand.Deploy == nil {
				fail("contract has no deploy reference for tier %s", cand.Tier)
			} else {
				if ev.Deploy.RunID != cand.Deploy.RunID {
					fail("deploy run id %d does not match contract run id %d", ev.Deploy.RunID, cand.Deploy.RunID)
				}
				if ev.Deploy.Workflow != cand.Deploy.Workflow {
					fail("deploy workflow %q does not match contract workflow %q", ev.Deploy.Workflow, cand.Deploy.Workflow)
				}
				validateDeployJobs(fail, cand, ev, rank)
			}
		}
	}

	if cand.Tier == TierLiveVerified {
		if cand.Live == nil || !validDurableURL(cand.Live.URL) {
			fail("LIVE-VERIFIED requires a durable http(s) live URL in the contract")
		}
		if cand.Deploy == nil || cand.Deploy.Job == "" || cand.Deploy.RevisionSHA == "" {
			fail("LIVE-VERIFIED requires a named deploy job and an exact revision SHA")
		} else if ev.Deploy != nil {
			if !isAcceptedRevision(cand, cand.Deploy.RevisionSHA) {
				fail("deploy revision SHA %q is neither the merge SHA nor a recorded trusted descendant", cand.Deploy.RevisionSHA)
			}
			if !validSHA(ev.Deploy.HeadSHA) || normalizeSHA(ev.Deploy.HeadSHA) != normalizeSHA(cand.Deploy.RevisionSHA) {
				fail("deploy run SHA %q does not match the qualified revision %q", ev.Deploy.HeadSHA, cand.Deploy.RevisionSHA)
			}
		}
		if ev.Live == nil {
			fail("LIVE-VERIFIED requires live evidence")
		} else {
			if !validDurableURL(ev.Live.URL) {
				fail("live evidence URL %q is not a durable http(s) URL", ev.Live.URL)
			}
			if cand.Live == nil || ev.Live.URL != cand.Live.URL {
				fail("live URL does not match contract URL")
			}
			if ev.Live.HTTPStatus < 200 || ev.Live.HTTPStatus > 399 {
				fail("live HTTP status %d is not a success/redirect", ev.Live.HTTPStatus)
			}
			checkedAt, err := parseTime(ev.Live.CheckedAt)
			if err != nil {
				fail("live checked_at is invalid: %v", err)
			} else if checkedAt.After(now.Add(time.Hour)) {
				fail("live checked_at is in the future")
			} else if cand.ClosureState == ClosureProposed {
				// Freshness is a closure-time requirement, not an eternal one.
				if now.Sub(checkedAt) > time.Duration(c.Gate.MaxEvidenceAgeDays)*24*time.Hour {
					fail("proposed live evidence is stale (checked_at %s exceeds %d days)", ev.Live.CheckedAt, c.Gate.MaxEvidenceAgeDays)
				}
			} else if cand.Live == nil || ev.Live.CheckedAt != cand.Live.CheckedAt {
				// A recorded historical observable must remain exactly the one
				// verified at closure; a later refresh must update both fields.
				fail("recorded live observable changed; refresh the contract and evidence together")
			}
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
			if !allowedExceptionKinds[ex.Kind] {
				fail("exception kind %q is not a recognised exception", ex.Kind)
			}
			if _, err := parseTime(ex.RecordedAt); err != nil {
				fail("exception recorded_at is invalid: %v", err)
			}
			expected := map[Tier]string{
				TierDeploySkipped: "deploy-skipped",
				TierCaptureOnly:   "capture-only",
				TierBuildOnly:     "build-only",
				TierCodeMerged:    "ci-unavailable",
			}
			if want, ok := expected[cand.Tier]; ok && ex.Kind != want {
				fail("tier %s exception kind must be %q, got %q", cand.Tier, want, ex.Kind)
			}
		}
	}
}

// validateDeployJobs enforces the named publishing job. Optional jobs may be
// skipped, but no job may fail, be cancelled or time out. A build-only run has
// no named deploy job and therefore fails.
func validateDeployJobs(fail func(string, ...interface{}), cand Candidate, ev Evidence, rank int) {
	if ev.Deploy == nil {
		return
	}
	if cand.Deploy == nil || cand.Deploy.Job == "" {
		if rank >= TierDeploySkipped.Rank() {
			fail("no named deploy job configured; a build-only run cannot satisfy the tier")
		}
		return
	}
	found := false
	for _, job := range ev.Deploy.Jobs {
		if job.Name != cand.Deploy.Job {
			// Optional jobs may be skipped; other incomplete states are not
			// silently ignored because they can hide a partial publication.
			if job.Conclusion == "failure" || job.Conclusion == "cancelled" || job.Conclusion == "timed_out" {
				fail("optional deploy job %q concluded %q", job.Name, job.Conclusion)
			}
			continue
		}
		found = true
		switch job.Conclusion {
		case "success":
		case "skipped":
			// The only legitimate skip is a tier that explicitly declares it;
			// the exception itself is validated by validateCandidate.
			if cand.Tier != TierDeploySkipped {
				fail("required deploy job %q was skipped", job.Name)
			}
		default:
			fail("required deploy job %q concluded %q, want success", job.Name, job.Conclusion)
		}
	}
	if !found {
		fail("required deploy job %q is missing from run %d (build-only run?)", cand.Deploy.Job, ev.Deploy.RunID)
	}
}
