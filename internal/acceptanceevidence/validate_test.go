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
	validSHA1 = "1111111111111111111111111111111111111111"
	validSHA2 = "2222222222222222222222222222222222222222"
	nowRFC    = "2026-11-19T00:00:00Z"
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
		Gate:    Gate{RequiredPRList: "acceptance-evidence-verified", MaxEvidenceAgeDays: 7},
		Candidates: []Candidate{{
			IssueKey:          "OSS-999",
			Summary:           "fixture",
			AcceptanceSurface: SurfacePublic,
			Tier:              TierLiveVerified,
			PR:                PRRef{Number: 42, MergeSHA: validSHA1, Repository: "athNdev/carbonpanel"},
			CIRunID:           100,
			Deploy:            &DeployRef{Workflow: "cd.yml", RunID: 200, Job: "publish"},
			Live:              &LiveRef{URL: "https://example.invalid/docs", HTTPStatus: 200, CheckedAt: nowRFC},
			Verifier:          "independent-verifier",
			VerifiedAt:        nowRFC,
			Artifact:          artifactRel,
		}},
	}
	ev := Evidence{
		IssueKey: "OSS-999",
		Merge:    MergeEvidence{PR: 42, Merged: true, MergeSHA: validSHA1, HeadSHA: validSHA1, Author: "implementer"},
		CI:       CIEvidence{RunID: 100, HeadSHA: validSHA1, Conclusion: "success"},
		Build:    &BuildEvidence{RunID: 100, Conclusion: "success"},
		Deploy: &DeployEvidence{RunID: 200, Workflow: "cd.yml", Jobs: []JobEvidence{
			{Name: "build", Conclusion: "success"}, {Name: "publish", Conclusion: "success"},
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

func TestPositiveLiveVerified(t *testing.T) {
	c, ev, root := fixture(t)
	r := validateFixture(t, c, ev, root)
	if !r.Passed {
		t.Fatalf("expected pass, got %v", r.Failures)
	}
	if r.Checked != 1 {
		t.Fatalf("checked = %d, want 1", r.Checked)
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
	ev.Merge.MergeSHA = validSHA2
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "merge SHA is stale") {
		t.Fatalf("expected stale SHA failure, got %v", r.Failures)
	}
}

func TestStaleCIHeadSHA(t *testing.T) {
	c, ev, root := fixture(t)
	ev.CI.HeadSHA = validSHA2
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "CI head SHA") {
		t.Fatalf("expected stale CI SHA failure, got %v", r.Failures)
	}
}

func TestSkippedDeployJob(t *testing.T) {
	c, ev, root := fixture(t)
	ev.Deploy.Jobs[1].Conclusion = "skipped"
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "skipped") {
		t.Fatalf("expected skipped job failure, got %v", r.Failures)
	}
}

func TestUnrecordedException(t *testing.T) {
	c, ev, root := fixture(t)
	c.Candidates[0].Tier = TierDeploySkipped
	ev.Deploy.Jobs[1].Conclusion = "skipped"
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "no exception is recorded") {
		t.Fatalf("expected unrecorded exception failure, got %v", r.Failures)
	}
}

func TestRecordedExceptionAllowsLowerTier(t *testing.T) {
	c, ev, root := fixture(t)
	c.Candidates[0].Tier = TierDeploySkipped
	ev.Deploy.Jobs[1].Conclusion = "skipped"
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

func TestUnknownTierAndSurface(t *testing.T) {
	c, ev, root := fixture(t)
	c.Candidates[0].Tier = Tier("NOT-A-TIER")
	c.Candidates[0].AcceptanceSurface = Surface("unknown")
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "unknown evidence tier") || !hasFailure(r, "unknown acceptance_surface") {
		t.Fatalf("expected tier/surface failures, got %v", r.Failures)
	}
}

func TestStaleLiveEvidence(t *testing.T) {
	c, ev, root := fixture(t)
	ev.Live.CheckedAt = "2026-11-01T00:00:00Z"
	r := validateFixture(t, c, ev, root)
	if r.Passed || !hasFailure(r, "live evidence is stale") {
		t.Fatalf("expected stale live failure, got %v", r.Failures)
	}
}
