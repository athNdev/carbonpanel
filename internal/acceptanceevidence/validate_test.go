// SPDX-License-Identifier: MPL-2.0
package acceptanceevidence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	mergeSHA       = "1111111111111111111111111111111111111111"
	deploySHA      = "2222222222222222222222222222222222222222"
	wrongSHA       = "3333333333333333333333333333333333333333"
	nowRFC         = "2026-11-19T00:00:00Z"
	deployWorkflow = "Deploy Website to GitHub Pages"
)

func writeJSON(t *testing.T, path string, value interface{}) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (Contract, Evidence, string) {
	t.Helper()
	root := t.TempDir()
	artifactRel := "docs/acceptance/evidence/OSS-999.json"
	artifactPath := filepath.Join(root, filepath.FromSlash(artifactRel))
	if err := os.MkdirAll(filepath.Dir(artifactPath), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Contract{
		Version: "1",
		Project: Project{Key: "OSS", ID: "10001", Style: "team-managed"},
		Gate:    Gate{RequiredPRList: "acceptance-evidence-verified", MaxEvidenceAgeDays: 7, Scope: "record-auditor"},
		ReferenceRuns: []ReferenceRun{{RunID: 555, Name: "Deploy Website to GitHub Pages",
			HeadSHA: deploySHA, Conclusion: "success"}},
		Candidates: []Candidate{{
			IssueKey:          "OSS-999",
			Summary:           "fixture",
			AcceptanceSurface: SurfacePublic,
			Tier:              TierLiveVerified,
			PR:                PRRef{Number: 42, MergeSHA: mergeSHA, Repository: "owner/repo"},
			CIRunID:           100,
			TrustedDeploySHA:  deploySHA,
			Deploy:            &DeployRef{Workflow: deployWorkflow, RunID: 200, Job: "deploy", RevisionSHA: deploySHA},
			Live:              &LiveRef{URL: "https://example.invalid/docs", HTTPStatus: 200, CheckedAt: nowRFC},
			Verifier:          "independent-verifier",
			VerifiedAt:        nowRFC,
			Artifact:          artifactRel,
		}},
	}
	ev := Evidence{
		IssueKey: "OSS-999",
		Merge:    MergeEvidence{PR: 42, Merged: true, MergeSHA: mergeSHA, HeadSHA: mergeSHA, Author: "implementer"},
		CI:       CIEvidence{RunID: 100, HeadSHA: deploySHA, Conclusion: "success"},
		Build:    &BuildEvidence{RunID: 100, Conclusion: "success"},
		Deploy: &DeployEvidence{RunID: 200, Workflow: deployWorkflow, HeadSHA: deploySHA,
			Jobs: []JobEvidence{
				{Name: "build", Conclusion: "success"},
				{Name: "deploy", Conclusion: "success"},
				{Name: "indexable-changes", Conclusion: "success"},
				{Name: "notify-indexnow", Conclusion: "skipped"},
			}},
		Live: &LiveEvidence{URL: "https://example.invalid/docs", HTTPStatus: 200, CheckedAt: nowRFC,
			Evidence: "docs/acceptance/evidence/OSS-999-live.txt"},
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "acceptance", "evidence", "OSS-999-live.txt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, artifactPath, ev)
	return c, ev, root
}

func validateFixture(t *testing.T, c Contract, ev Evidence, root string) Report {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(c.Candidates[0].Artifact))
	writeJSON(t, path, ev)
	now, err := time.Parse(time.RFC3339, nowRFC)
	if err != nil {
		t.Fatal(err)
	}
	return ValidateContract(c, root, now)
}

func hasFailure(r Report, fragment string) bool {
	for _, f := range r.Failures {
		if strings.Contains(f, fragment) {
			return true
		}
	}
	return false
}

func TestPositiveTrustedDescendantAndOptionalSkip(t *testing.T) {
	c, ev, root := fixture(t)
	r := validateFixture(t, c, ev, root)
	if !r.Passed {
		t.Fatalf("expected pass, got %v", r.Failures)
	}
}

func TestMissingCIEvidence(t *testing.T) {
	c, ev, root := fixture(t)
	ev.CI = CIEvidence{}
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "CI run") {
		t.Fatalf("expected CI failure, got %v", r.Failures)
	}
}

func TestStaleMergeSHA(t *testing.T) {
	c, ev, root := fixture(t)
	ev.Merge.MergeSHA = wrongSHA
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "merge SHA is stale") {
		t.Fatalf("expected stale SHA failure, got %v", r.Failures)
	}
}

func TestRequiredDeployJobSkipped(t *testing.T) {
	c, ev, root := fixture(t)
	setJob(ev.Deploy, "deploy", "skipped")
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "required deploy job") {
		t.Fatalf("expected skipped named job failure, got %v", r.Failures)
	}
}

func TestRequiredDeployJobFailed(t *testing.T) {
	c, ev, root := fixture(t)
	setJob(ev.Deploy, "deploy", "failure")
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "required deploy job") {
		t.Fatalf("expected failed named job failure, got %v", r.Failures)
	}
}

func TestMissingNamedDeployJobBuildOnly(t *testing.T) {
	c, ev, root := fixture(t)
	ev.Deploy.Jobs = []JobEvidence{{Name: "build", Conclusion: "success"}}
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "missing from run") {
		t.Fatalf("expected missing named job failure, got %v", r.Failures)
	}
}

func TestOptionalSkippedJobAllowed(t *testing.T) {
	c, ev, root := fixture(t)
	setJob(ev.Deploy, "notify-indexnow", "skipped")
	r := validateFixture(t, c, ev, root)
	if !r.Passed {
		t.Fatalf("optional skip must be allowed, got %v", r.Failures)
	}
}

func TestWrongDeployRunSHA(t *testing.T) {
	c, ev, root := fixture(t)
	ev.Deploy.HeadSHA = wrongSHA
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "deploy run SHA") {
		t.Fatalf("expected wrong deploy SHA failure, got %v", r.Failures)
	}
}

func TestTrustedDescendantDeploySHAAccepted(t *testing.T) {
	c, ev, root := fixture(t)
	c.Candidates[0].TrustedDeploySHA = deploySHA
	c.Candidates[0].Deploy.RevisionSHA = deploySHA
	ev.CI.HeadSHA = deploySHA
	ev.Deploy.HeadSHA = deploySHA
	r := validateFixture(t, c, ev, root)
	if !r.Passed {
		t.Fatalf("expected trusted descendant pass, got %v", r.Failures)
	}
}

func TestUnrecordedException(t *testing.T) {
	c, ev, root := fixture(t)
	c.Candidates[0].Tier = TierDeploySkipped
	setJob(ev.Deploy, "deploy", "skipped")
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "no exception is recorded") {
		t.Fatalf("expected unrecorded exception failure, got %v", r.Failures)
	}
}

func TestRecordedExceptionAllowsLowerTier(t *testing.T) {
	c, ev, root := fixture(t)
	c.Candidates[0].Tier = TierDeploySkipped
	setJob(ev.Deploy, "deploy", "skipped")
	c.Candidates[0].Exception = &Exception{
		Kind: "deploy-skipped", Reason: "registry outage", RecordedAt: nowRFC, Reviewer: "release-auditor",
	}
	r := validateFixture(t, c, ev, root)
	if !r.Passed {
		t.Fatalf("expected pass with recorded exception, got %v", r.Failures)
	}
}

func TestVerifierIsAlsoAuthor(t *testing.T) {
	c, ev, root := fixture(t)
	ev.Merge.Author = "independent-verifier"
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "not independent") {
		t.Fatalf("expected independence failure, got %v", r.Failures)
	}
}

func TestMissingArtifact(t *testing.T) {
	c, _, root := fixture(t)
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(c.Candidates[0].Artifact))); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse(time.RFC3339, nowRFC)
	r := ValidateContract(c, root, now)
	if r.Passed || !hasFailure(r, "missing or unreadable") {
		t.Fatalf("expected missing artifact failure, got %v", r.Failures)
	}
}

func TestEmptyCandidatesFailClosed(t *testing.T) {
	c, _, root := fixture(t)
	c.Candidates = nil
	now, _ := time.Parse(time.RFC3339, nowRFC)
	r := ValidateContract(c, root, now)
	if r.Passed || !hasFailure(r, "no Done candidates") {
		t.Fatalf("expected empty-candidate failure, got %v", r.Failures)
	}
}

func TestStaleLiveEvidenceForProposedClosure(t *testing.T) {
	c, ev, root := fixture(t)
	ev.Live.CheckedAt = "2026-11-01T00:00:00Z"
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "proposed live evidence is stale") {
		t.Fatalf("expected stale live failure, got %v", r.Failures)
	}
}

func TestHistoricalRecordedObservableStaysValid(t *testing.T) {
	c, ev, root := fixture(t)
	c.Candidates[0].ClosureState = ClosureRecorded
	c.Candidates[0].Live.CheckedAt = "2026-11-01T00:00:00Z"
	c.Candidates[0].VerifiedAt = "2026-11-01T00:00:00Z"
	ev.Live.CheckedAt = "2026-11-01T00:00:00Z"
	r := validateFixture(t, c, ev, root)
	if !r.Passed {
		t.Fatalf("recorded historical observable must not expire, got %v", r.Failures)
	}
}

func TestRecordedObservableCannotDrift(t *testing.T) {
	c, ev, root := fixture(t)
	c.Candidates[0].ClosureState = ClosureRecorded
	c.Candidates[0].Live.CheckedAt = "2026-11-01T00:00:00Z"
	ev.Live.CheckedAt = "2026-11-19T00:00:00Z"
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "recorded live observable changed") {
		t.Fatalf("expected recorded observable drift failure, got %v", r.Failures)
	}
}

func setJob(deploy *DeployEvidence, name, conclusion string) {
	for i := range deploy.Jobs {
		if deploy.Jobs[i].Name == name {
			deploy.Jobs[i].Conclusion = conclusion
			return
		}
	}
	deploy.Jobs = append(deploy.Jobs, JobEvidence{Name: name, Conclusion: conclusion})
}
