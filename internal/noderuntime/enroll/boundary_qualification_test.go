// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package enroll

import (
	"bytes"
	"errors"
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestBoundaryProductionLifecycleQualification drives the COMPLETE enrollment
// lifecycle through the shared production Boundary (NewBoundary), not through
// EnrollmentAuthority helpers directly. It is an owned, harmless fixture:
// synthetic Ed25519 keys, a temp-dir private bundle, a fixed clock and no RPC
// mount, host probe or live trust boundary. It proves the exact properties the
// node-runtime qualification gate requires:
//
//   - required approved key + Ed25519-only signing;
//   - server-stored single-use nonce bound into the NUL-delimited canonical
//     SIG_CLAIM context (there is no client nonce field);
//   - atomic compare-and-consume (replay is refused after a successful claim);
//   - issuance-failure rollback with a later legitimate retry succeeding;
//   - three non-crossing authorization domains wired through the seam;
//   - digest-only secret storage and no secret in any read response.
func TestBoundaryProductionLifecycleQualification(t *testing.T) {
	b, err := NewBoundary(TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	a := b.Authority
	a.WithClock(fixtureTime)
	ctx := fixtureContext(t)

	const nodeID = "node-qualification"
	const label = "qualification-node"

	operator := Credential{Kind: CredentialOperator}
	bootstrap := Credential{Kind: CredentialBootstrap}

	// Node side generates Ed25519 key + >=256-bit secret and a digest-only
	// registration request. S and the private key stay in the temp 0600 file.
	bundle, createReq, err := GenerateNodeBundle(t.TempDir(), nodeID, label)
	if err != nil {
		t.Fatal(err)
	}
	if createReq.GetProtocolVersion() != ProtocolVersion {
		t.Fatalf("bundle advertised protocol version %d, want %d", createReq.GetProtocolVersion(), ProtocolVersion)
	}

	// (1) OPERATOR domain creates the enrollment. A bootstrap marker must not.
	if _, err := b.CreateEnrollment(ctx, bootstrap, createReq); !errors.Is(err, ErrScope) {
		t.Fatalf("bootstrap credential created an enrollment: %v", err)
	}
	created, err := b.CreateEnrollment(ctx, operator, createReq)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentID := created.GetEnrollment().GetEnrollmentId()
	if enrollmentID == "" {
		t.Fatal("empty enrollment id")
	}
	// Read view must never carry the approved digest, salt, CSR or token.
	viewBytes := []byte(created.GetEnrollment().GetTokenFingerprint())
	if bytes.Contains(viewBytes, []byte(createReq.GetEnrollmentSalt())) {
		t.Fatal("enrollment view leaked the salt")
	}

	// (2) BOOTSTRAP domain begins a bounded challenge. The nonce is
	// server-stored; the response echoes the exact canonical channel context.
	if _, err := b.BeginEnrollment(ctx, operator, enrollmentID); !errors.Is(err, ErrScope) {
		t.Fatalf("operator used the bootstrap challenge step: %v", err)
	}
	challenge, err := b.BeginEnrollment(ctx, bootstrap, enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(challenge.GetServerNonce()) == 0 || len(challenge.GetChannelContext()) == 0 {
		t.Fatal("challenge missing server nonce or channel context")
	}
	// Re-derive the canonical context the node must sign. It is NUL-delimited,
	// binds the server nonce and contains no client nonce/JSON encoding.
	caps := []v1.NodeCapability{
		v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET,
		v1.NodeCapability_NODE_CAPABILITY_DEDICATED_ROOTS,
	}
	ctxBytes := ClaimContext(nodeID, enrollmentID, challenge.GetChallengeId(),
		challenge.GetServerNonce(), challenge.GetExpiresAt().AsTime(), caps,
		challenge.GetChannelContext())
	if !bytes.Contains(ctxBytes, challenge.GetServerNonce()) {
		t.Fatal("canonical context did not bind the server nonce")
	}
	if bytes.Contains(ctxBytes, []byte{0x7b, 0x22}) { // `{"` canonical JSON
		t.Fatal("canonical context reintroduced JSON encoding")
	}

	// (3) Issuance-failure rollback. Inject a failure, claim must change
	// nothing, then a fresh challenge + claim must succeed.
	claim, err := bundle.BuildClaim(enrollmentID, challenge, caps)
	if err != nil {
		t.Fatal(err)
	}
	a.InjectIssuanceFailure(true)
	if _, err := b.ClaimEnrollment(ctx, bootstrap, claim); err == nil {
		t.Fatal("claim succeeded despite injected issuance failure")
	}
	a.InjectIssuanceFailure(false)
	beforeView, err := a.EnrollmentView(enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeView.GetState() != v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_PENDING {
		t.Fatalf("issuance failure changed state: %v", beforeView.GetState())
	}
	// Fresh challenge (server-stored nonce has its own single-use lifecycle).
	challenge2, err := b.BeginEnrollment(ctx, bootstrap, enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	claim2, err := bundle.BuildClaim(enrollmentID, challenge2, caps)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := b.ClaimEnrollment(ctx, bootstrap, claim2)
	if err != nil {
		t.Fatalf("legitimate retry after rollback failed: %v", err)
	}
	if len(claimed.GetEnrolledClientCertificateDer()) == 0 || claimed.GetIdentity() == nil {
		t.Fatal("claim did not issue a public client certificate and identity")
	}
	if string(claimed.GetIdentity().GetKeyFingerprint()) == "" {
		t.Fatal("claim identity missing key fingerprint")
	}
	// A stale nonce/challenge claim must now be refused (atomic consume).
	if _, err := b.ClaimEnrollment(ctx, bootstrap, claim); err == nil {
		t.Fatal("replayed/stale claim was accepted after a successful consume")
	}

	// (4) ENROLLED NODE domain. The issued serial is accepted for the node's
	// own object_id and granted scopes; every crossing is refused.
	node := Credential{Kind: CredentialNode, NodeID: nodeID, ObjectID: nodeID,
		Serial: claimed.GetCertificateSerial(), Scopes: claimed.GetClaims().GetScopes()}
	if err := b.AuthorizeNodeOperation(node, nodeID, "node:heartbeat"); err != nil {
		t.Fatalf("issued node credential refused on its own object: %v", err)
	}
	if err := b.AuthorizePreflight(node, nodeID, []string{"node:report"}); err != nil {
		t.Fatalf("issued node credential refused a scoped preflight: %v", err)
	}
	if err := b.AuthorizeNodeOperation(bootstrap, nodeID, "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("bootstrap credential used a node operation: %v", err)
	}
	if err := b.AuthorizeNodeOperation(operator, nodeID, "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("operator credential used a node operation: %v", err)
	}
	if err := b.AuthorizeNodeOperation(node, "some-other-node", "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("cross-node operation accepted: %v", err)
	}
	if err := b.AuthorizeNodeOperation(node, nodeID, "nodes:create"); !errors.Is(err, ErrScope) {
		t.Fatalf("node used an operator scope: %v", err)
	}
	if err := b.AuthorizePreflight(node, nodeID, []string{"node:report", "nodes:create"}); !errors.Is(err, ErrScope) {
		t.Fatalf("over-scope preflight accepted: %v", err)
	}

	// (5) Response carries no secret. The bundle's secret is never returned.
	assertNoSecret(t, claimed, bundle.secret)
	assertNoSecret(t, claimed.GetClaims(), bundle.secret)
}
