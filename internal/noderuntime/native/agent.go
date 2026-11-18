// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package native

import (
	"context"
	"time"

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
	p := Plan{
		NodeID:       nodeID,
		Selection:    sel,
		Report:       PreflightReport(nodeID, sel, observedAt),
		Capabilities: Capabilities(facts, sel),
		Daemon:       SpecFromFacts(nodeID, facts, sel),
	}
	if sel.Mode == ModeNone {
		p.Refusal = Describe(sel)
		return p
	}
	if code := p.Daemon.ValidateOrRefuse(); code != "" {
		p.Refusal = code
	}
	return p
}

// Runner is the explicit runtime seam used by cmd/carbon-node. It composes the
// shared-contract enrollment boundary with the mode-specific plan. It never
// provisions a live host and never mounts a production enrollment RPC: the
// authority is owned in memory and all keys are synthetic in fixtures.
type Runner struct {
	Authority *EnrollmentAuthority
}

// NewRunner creates a runner bound to an owned enrollment authority.
func NewRunner(topology TLSTopology) (*Runner, error) {
	a, err := NewEnrollmentAuthority(topology)
	if err != nil {
		return nil, err
	}
	return &Runner{Authority: a}, nil
}

// Negotiate delegates version skew to the shared contract semantics.
func (r *Runner) Negotiate(ctx context.Context, endpoint *v1.VersionNegotiation) (*v1.NegotiateVersionResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return NegotiateVersion(endpoint), nil
}

// BeginEnrollment is the bounded, unauthenticated challenge step.
func (r *Runner) BeginEnrollment(ctx context.Context, enrollmentID string) (*v1.BeginEnrollmentChallengeResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.Authority.BeginEnrollmentChallenge(&v1.BeginEnrollmentChallengeRequest{EnrollmentId: enrollmentID})
}

// ClaimEnrollment consumes the single-use enrollment through the shared
// boundary. The caller must have passed the three-domain authorization checks
// for the operator/bootstrap phase before reaching this method.
func (r *Runner) ClaimEnrollment(ctx context.Context, req *v1.ClaimEnrollmentRequest) (*v1.ClaimEnrollmentResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return r.Authority.ClaimEnrollment(req)
}
