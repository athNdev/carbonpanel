// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package enroll

import (
	"context"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// Boundary is the shared, mode-agnostic authorization seam composed by every
// mode lane. It wires all three ADR 0019 domains into production paths:
//
//   - CreateEnrollment / RevokeCredential / Rotate require the OPERATOR domain;
//   - BeginEnrollment / ClaimEnrollment require the BOOTSTRAP domain marker;
//   - AuthorizeNodeOperation / AuthorizePreflight require the ENROLLED NODE
//     domain on the caller's own object_id.
//
// AuthorizeOperator was already enforced inside EnrollmentAuthority; wiring it
// here makes the shared boundary explicit. AuthorizeBootstrap and
// AuthorizeNodeOperation are no longer standalone helpers used only by tests:
// every lane enters through this boundary, so the domains cannot cross.
//
// The bootstrap marker models the bounded bootstrap PHASE, not a wire-
// authenticated principal. The wire RPC stays unauthenticated and rate-limited
// exactly as ADR 0019 specifies; no secret is a credential.
type Boundary struct {
	Authority *EnrollmentAuthority
}

// NewBoundary creates an owned shared boundary. No RPC is mounted and no live
// host, credential or deployment is touched.
func NewBoundary(topology TLSTopology) (*Boundary, error) {
	a, err := NewEnrollmentAuthority(topology)
	if err != nil {
		return nil, err
	}
	return &Boundary{Authority: a}, nil
}

// BeginEnrollment is the bounded challenge step, guarded by the bootstrap
// domain. The returned nonce is server-stored; there is no client nonce field.
func (b *Boundary) BeginEnrollment(ctx context.Context, cred Credential, enrollmentID string) (*v1.BeginEnrollmentChallengeResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.Authority.AuthorizeBootstrap(cred); err != nil {
		return nil, err
	}
	return b.Authority.BeginEnrollmentChallenge(&v1.BeginEnrollmentChallengeRequest{EnrollmentId: enrollmentID})
}

// ClaimEnrollment consumes the one-time enrollment, guarded by the bootstrap
// domain. Issuance happens before state commits inside the authority.
func (b *Boundary) ClaimEnrollment(ctx context.Context, cred Credential, req *v1.ClaimEnrollmentRequest) (*v1.ClaimEnrollmentResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := b.Authority.AuthorizeBootstrap(cred); err != nil {
		return nil, err
	}
	return b.Authority.ClaimEnrollment(req)
}

// CreateEnrollment is an operator-domain enrollment management action.
func (b *Boundary) CreateEnrollment(ctx context.Context, cred Credential, req *v1.CreateEnrollmentRequest) (*v1.CreateEnrollmentResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b.Authority.CreateEnrollment(cred, req)
}

// RevokeCredential is an operator-domain enrollment management action.
func (b *Boundary) RevokeCredential(ctx context.Context, cred Credential, req *v1.RevokeCredentialRequest) (*v1.RevokeCredentialResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b.Authority.RevokeCredential(cred, req)
}

// Rotate is an operator-domain credential action, never a node action.
func (b *Boundary) Rotate(ctx context.Context, cred Credential, nodeID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return b.Authority.Rotate(cred, nodeID)
}

// AuthorizeNodeOperation is the single production entry point for the enrolled
// node domain. A lane must call it before any heartbeat, report or ack on the
// caller's own object_id. A bootstrap or operator credential is refused here.
func (b *Boundary) AuthorizeNodeOperation(cred Credential, objectID, scope string) error {
	return b.Authority.AuthorizeNodeOperation(cred, objectID, scope)
}

// AuthorizePreflight refuses a report carrying scopes beyond the node's grant.
func (b *Boundary) AuthorizePreflight(cred Credential, objectID string, reportedScopes []string) error {
	return b.Authority.AuthorizePreflight(cred, objectID, reportedScopes)
}
