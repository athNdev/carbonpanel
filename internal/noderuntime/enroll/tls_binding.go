// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package enroll

import (
	"context"
	"crypto/tls"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const tlsExporterLabel = "EXPORTER-CarbonPanel-Node-Enrollment-v1"

// TLSChannelBinding derives the bootstrap context from a completed TLS 1.3
// connection. Both peers derive it locally from the same connection.
func TLSChannelBinding(state *tls.ConnectionState) ([]byte, error) {
	if state == nil || !state.HandshakeComplete || state.Version < tls.VersionTLS13 {
		return nil, ErrTopology
	}
	binding, err := state.ExportKeyingMaterial(tlsExporterLabel, nil, 32)
	if err != nil {
		return nil, ErrTopology
	}
	return binding, nil
}

// BeginTLSEnrollment issues a challenge tied to the controller's TLS
// connection. The request supplies the enrollment identifier only.
func (b *Boundary) BeginTLSEnrollment(ctx context.Context, cred Credential, enrollmentID string, state *tls.ConnectionState) (*v1.BeginEnrollmentChallengeResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a := b.Authority
	if err := a.AuthorizeBootstrap(cred); err != nil {
		return nil, err
	}
	binding, err := a.bootstrapTLSBinding(state)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return commitMutationLocked(ctx, a, func() (*v1.BeginEnrollmentChallengeResponse, error) {
		return a.beginEnrollmentChallengeLocked(&v1.BeginEnrollmentChallengeRequest{EnrollmentId: enrollmentID}, binding, true)
	})
}

// ClaimTLSEnrollment verifies the signed request against the controller's
// connection and consumes the challenge on successful certificate issuance.
func (b *Boundary) ClaimTLSEnrollment(ctx context.Context, cred Credential, req *v1.ClaimEnrollmentRequest, state *tls.ConnectionState) (*v1.ClaimEnrollmentResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	a := b.Authority
	if err := a.AuthorizeBootstrap(cred); err != nil {
		return nil, err
	}
	binding, err := a.bootstrapTLSBinding(state)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return commitMutationLocked(ctx, a, func() (*v1.ClaimEnrollmentResponse, error) {
		return a.claimEnrollmentLocked(req, binding, true)
	})
}

func (a *EnrollmentAuthority) bootstrapTLSBinding(state *tls.ConnectionState) ([]byte, error) {
	if a.topology != TopologyDirectTLS && a.topology != TopologyL4PassThrough {
		return nil, ErrTopology
	}
	if state != nil && len(state.PeerCertificates) != 0 {
		return nil, ErrScope
	}
	return TLSChannelBinding(state)
}
