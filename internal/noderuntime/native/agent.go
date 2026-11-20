// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"context"
	"time"

	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// Plan is the complete, disposable output of one host-native preflight. It is
// safe to print: it contains only codes, stable paths the operator chose and
// capability metadata, never a secret, token or private key.
type Plan struct {
	NodeID       string
	Selection    Selection
	Report       *v1.NodePreflightReport
	Capabilities *v1.NodeCapabilities
	Daemon       DaemonSpec
	Refusal      string
}

// BuildPlan runs the compatibility gates and derives the private daemon spec.
// No host configuration is changed and no daemon is started.
func BuildPlan(nodeID string, facts HostFacts, observedAt time.Time) Plan {
	sel := ApplyGates(facts)
	var daemon DaemonSpec
	var refusal string
	if sel.Mode == ModeNone {
		refusal = Describe(sel)
	} else {
		candidate := SpecFromFacts(nodeID, facts, sel)
		if failures := failedChecks(candidate.Validate()); len(failures) != 0 {
			sel.Mode = ModeNone
			sel.Checks = append(sel.Checks, failures...)
			sel.Reasons = failures
			refusal = failures[0].Code
		} else {
			daemon = candidate
		}
	}
	return Plan{
		NodeID:       nodeID,
		Selection:    sel,
		Report:       PreflightReport(nodeID, sel, observedAt),
		Capabilities: Capabilities(facts, sel),
		Daemon:       daemon,
		Refusal:      refusal,
	}
}

// Runner is the host-native runtime seam used by cmd/carbon-node. It composes
// the mode-specific plan with the SHARED enrollment boundary. It never
// provisions a live host or mounts a production enrollment RPC: the boundary
// is owned in memory and all keys are synthetic in fixtures. All enrollment
// and authorization work is delegated to internal/noderuntime/enroll; there is
// no mode-local crypto or auth path.
type Runner struct {
	Boundary  *enroll.Boundary
	Authority *enroll.EnrollmentAuthority // compatibility view of the owned authority
}

// NewRunner creates a runner bound to the shared enrollment boundary.
func NewRunner(topology TLSTopology) (*Runner, error) {
	b, err := enroll.NewBoundary(topology)
	if err != nil {
		return nil, err
	}
	return &Runner{Boundary: b, Authority: b.Authority}, nil
}

// Negotiate delegates version skew to the shared contract semantics.
func (r *Runner) Negotiate(ctx context.Context, endpoint *v1.VersionNegotiation) (*v1.NegotiateVersionResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return NegotiateVersion(endpoint), nil
}

// BeginEnrollment is the bounded, unauthenticated challenge step. The shared
// boundary enforces the bootstrap-domain marker; the wire RPC remains bounded
// and rate-limited, and no secret is treated as a credential.
func (r *Runner) BeginEnrollment(ctx context.Context, cred Credential, enrollmentID string) (*v1.BeginEnrollmentChallengeResponse, error) {
	return r.Boundary.BeginEnrollment(ctx, cred, enrollmentID)
}

// ClaimEnrollment consumes the single-use enrollment through the shared
// boundary. The bootstrap domain is enforced before the atomic issue-then-
// commit path in the authority.
func (r *Runner) ClaimEnrollment(ctx context.Context, cred Credential, req *v1.ClaimEnrollmentRequest) (*v1.ClaimEnrollmentResponse, error) {
	return r.Boundary.ClaimEnrollment(ctx, cred, req)
}

// AuthorizeNodeOperation enforces the enrolled-node domain on the caller's own
// object. This is the wired production seam for node heartbeats/reports/acks.
func (r *Runner) AuthorizeNodeOperation(cred Credential, objectID, scope string) error {
	return r.Boundary.AuthorizeNodeOperation(cred, objectID, scope)
}

// AuthorizePreflight additionally refuses a report carrying scopes beyond the
// node's narrow grant.
func (r *Runner) AuthorizePreflight(cred Credential, objectID string, reportedScopes []string) error {
	return r.Boundary.AuthorizePreflight(cred, objectID, reportedScopes)
}
