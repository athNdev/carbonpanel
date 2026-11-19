// SPDX-License-Identifier: MPL-2.0
// Package acceptanceevidence validates that an OSS Done candidate carries the
// full acceptance chain: merged code, exact CI, and deploy/live evidence where
// the original acceptance is public. It never calls Jira; the native workflow
// guard is the blocking boundary and this package is the repository-side
// verifier and drift detector.
package acceptanceevidence

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Tier is an evidence-strength tier. The rank orders strength; a candidate may
// carry a stronger tier than its acceptance surface requires.
type Tier string

const (
	TierCodeMerged    Tier = "CODE-MERGED"
	TierCIGreen       Tier = "CI-GREEN"
	TierBuildOnly     Tier = "BUILD-ONLY"
	TierCaptureOnly   Tier = "CAPTURE-ONLY"
	TierDeploySkipped Tier = "DEPLOY-SKIPPED"
	TierLiveVerified  Tier = "LIVE-VERIFIED"
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func (t Tier) Rank() int {
	switch t {
	case TierCodeMerged:
		return 0
	case TierCIGreen:
		return 1
	case TierBuildOnly:
		return 2
	case TierCaptureOnly:
		return 3
	case TierDeploySkipped:
		return 4
	case TierLiveVerified:
		return 5
	default:
		return -1
	}
}

func ValidTier(t Tier) bool { return t.Rank() >= 0 }

// Surface describes where acceptance can actually be observed.
type Surface string

const (
	SurfaceInternal Surface = "internal"
	SurfaceBuild    Surface = "build"
	SurfacePublic   Surface = "public"
	SurfaceLive     Surface = "live"
)

func requiredTier(s Surface) (Tier, bool) {
	switch s {
	case SurfaceInternal:
		return TierCIGreen, true
	case SurfaceBuild:
		return TierBuildOnly, true
	case SurfacePublic, SurfaceLive:
		return TierLiveVerified, true
	default:
		return "", false
	}
}

// Contract is the machine-readable acceptance-evidence contract.
type Contract struct {
	Version    string      `json:"version"`
	Project    Project     `json:"project"`
	Gate       Gate        `json:"gate"`
	Candidates []Candidate `json:"candidates"`
}

type Project struct {
	Key   string `json:"key"`
	ID    string `json:"id"`
	Style string `json:"style"`
}

type Gate struct {
	RequiredPRList     string `json:"required_pr_label"`
	MaxEvidenceAgeDays int    `json:"max_evidence_age_days"`
}

type Candidate struct {
	IssueKey          string     `json:"issue_key"`
	Summary           string     `json:"summary"`
	AcceptanceSurface Surface    `json:"acceptance_surface"`
	Tier              Tier       `json:"tier"`
	PR                PRRef      `json:"pr"`
	CIRunID           int64      `json:"ci_run_id"`
	Deploy            *DeployRef `json:"deploy,omitempty"`
	Live              *LiveRef   `json:"live,omitempty"`
	Verifier          string     `json:"verifier"`
	VerifiedAt        string     `json:"verified_at"`
	Exception         *Exception `json:"exception,omitempty"`
	Artifact          string     `json:"artifact"`
}

type PRRef struct {
	Number     int    `json:"number"`
	MergeSHA   string `json:"merge_sha"`
	Repository string `json:"repository"`
}

type DeployRef struct {
	Workflow string `json:"workflow"`
	RunID    int64  `json:"run_id"`
	Job      string `json:"job"`
}

type LiveRef struct {
	URL        string `json:"url"`
	HTTPStatus int    `json:"http_status"`
	CheckedAt  string `json:"checked_at"`
}

type Exception struct {
	Kind       string `json:"kind"`
	Reason     string `json:"reason"`
	RecordedAt string `json:"recorded_at"`
	Reviewer   string `json:"reviewer"`
}

// Evidence is the artifact captured for one candidate.
type Evidence struct {
	IssueKey string          `json:"issue_key"`
	Merge    MergeEvidence   `json:"merge"`
	CI       CIEvidence      `json:"ci"`
	Build    *BuildEvidence  `json:"build,omitempty"`
	Deploy   *DeployEvidence `json:"deploy,omitempty"`
	Live     *LiveEvidence   `json:"live,omitempty"`
}

type MergeEvidence struct {
	PR       int    `json:"pr"`
	Merged   bool   `json:"merged"`
	MergeSHA string `json:"merge_sha"`
	HeadSHA  string `json:"head_sha"`
	Author   string `json:"author"`
}

type CIEvidence struct {
	RunID      int64  `json:"run_id"`
	HeadSHA    string `json:"head_sha"`
	Conclusion string `json:"conclusion"`
}

type BuildEvidence struct {
	RunID      int64  `json:"run_id"`
	Conclusion string `json:"conclusion"`
}

type DeployEvidence struct {
	RunID    int64         `json:"run_id"`
	Workflow string        `json:"workflow"`
	Jobs     []JobEvidence `json:"jobs"`
	URL      string        `json:"url"`
}

type JobEvidence struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

type LiveEvidence struct {
	URL        string `json:"url"`
	HTTPStatus int    `json:"http_status"`
	CheckedAt  string `json:"checked_at"`
	Evidence   string `json:"evidence"`
}

func normalizeSHA(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func validSHA(s string) bool { return shaPattern.MatchString(normalizeSHA(s)) }

func parseTime(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("expected RFC3339, got %q: %w", value, err)
	}
	return t, nil
}
