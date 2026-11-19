// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package enroll

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// All fixtures here use synthetic keys, temp-dir private material and a fixed
// clock. No secret is printed, committed or stored in a real database.

const fixtureTimeValue = "2026-11-18T12:00:00Z"

func fixtureTime() time.Time {
	t, _ := time.Parse(time.RFC3339, fixtureTimeValue)
	return t
}

func fixtureContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type testAuthority struct {
	authority    *EnrollmentAuthority
	bundle       *NodeBundle
	enrollmentID string
	nodeID       string
}

func newTestAuthority(t *testing.T, topology TLSTopology) *testAuthority {
	t.Helper()
	a, err := NewEnrollmentAuthority(topology)
	if err != nil {
		t.Fatal(err)
	}
	a.WithClock(func() time.Time { return fixtureTime() })
	dir := t.TempDir()
	bundle, req, err := GenerateNodeBundle(dir, "node-fixture-shared", "fixture-node")
	if err != nil {
		t.Fatal(err)
	}
	operator := Credential{Kind: CredentialOperator}
	resp, err := a.CreateEnrollment(operator, req)
	if err != nil {
		t.Fatal(err)
	}
	return &testAuthority{
		authority:    a,
		bundle:       bundle,
		enrollmentID: resp.GetEnrollment().GetEnrollmentId(),
		nodeID:       bundle.NodeID,
	}
}

func (ta *testAuthority) validClaim(t *testing.T) *v1.ClaimEnrollmentRequest {
	t.Helper()
	challenge, err := ta.authority.BeginEnrollmentChallenge(&v1.BeginEnrollmentChallengeRequest{EnrollmentId: ta.enrollmentID})
	if err != nil {
		t.Fatal(err)
	}
	caps := []v1.NodeCapability{
		v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET,
		v1.NodeCapability_NODE_CAPABILITY_DEDICATED_ROOTS,
	}
	claim, err := ta.bundle.BuildClaim(ta.enrollmentID, challenge, caps)
	if err != nil {
		t.Fatal(err)
	}
	return claim
}

func otherFixtureKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func TestEnrollmentValidClaimIssuesPublicCertOnly(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	claim := ta.validClaim(t)
	resp, err := ta.authority.ClaimEnrollment(claim)
	if err != nil {
		t.Fatalf("valid claim refused: %v", err)
	}
	if resp.GetCredentialGeneration() != 1 || resp.GetCertificateSerial() == "" || len(resp.GetEnrolledClientCertificateDer()) == 0 {
		t.Fatalf("unexpected claim response: %+v", resp)
	}
	if resp.GetClaims().GetObjectId() != ta.nodeID || resp.GetIdentity().GetNodeId() != ta.nodeID {
		t.Fatal("node claims lost their per-node object binding")
	}
	cert, err := x509.ParseCertificate(resp.GetEnrolledClientCertificateDer())
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != ta.nodeID {
		t.Fatalf("certificate bound to wrong node: %s", cert.Subject.CommonName)
	}
	if view, err := ta.authority.EnrollmentView(ta.enrollmentID); err != nil || view.GetState() != v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_ACTIVE {
		t.Fatalf("enrollment not promoted atomically: %v %+v", err, view)
	}
	assertNoSecret(t, resp, ta.bundle.secret)
}

func TestEnrollmentMismatchedApprovedKey(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	claim := ta.validClaim(t)
	e := ta.authority.enrollments[ta.enrollmentID]
	other := otherFixtureKey(t)
	claim.FreshClaimSignature = ed25519.Sign(other, ClaimContext(
		e.NodeID, e.ID, claim.GetChallengeId(), e.Nonce, e.ExpiresAt,
		claim.GetCapabilities().GetCapabilities(), ta.authority.channelContextLocked()))
	if _, err := ta.authority.ClaimEnrollment(claim); !errors.Is(err, ErrMismatchedKey) {
		t.Fatalf("signature from non-approved key not refused: %v", err)
	}
	if e.State != "pending" {
		t.Fatal("refused claim mutated enrollment state")
	}
}

func TestEnrollmentCSRPreIsNotClaimProof(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	claim := ta.validClaim(t)
	e := ta.authority.enrollments[ta.enrollmentID]
	csr, err := x509.ParseCertificateRequest(e.CSRPre)
	if err != nil {
		t.Fatal(err)
	}
	claim.FreshClaimSignature = append([]byte{}, csr.Signature...)
	if _, err := ta.authority.ClaimEnrollment(claim); err == nil {
		t.Fatal("CSR_PRE signature accepted as a fresh claim proof")
	}
	if e.State != "pending" {
		t.Fatal("CSR_PRE-as-proof attempt mutated state")
	}
}

func TestEnrollmentStaleAndReplayedNonce(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	// Start two challenges so the first nonce is superseded.
	if _, err := ta.authority.BeginEnrollmentChallenge(&v1.BeginEnrollmentChallengeRequest{EnrollmentId: ta.enrollmentID}); err != nil {
		t.Fatal(err)
	}
	current, err := ta.authority.BeginEnrollmentChallenge(&v1.BeginEnrollmentChallengeRequest{EnrollmentId: ta.enrollmentID})
	if err != nil {
		t.Fatal(err)
	}
	claim := ta.validClaim(t)
	// Present a stale challenge ID but re-sign over the current nonce so only
	// the single-use nonce check can cause the refusal.
	e := ta.authority.enrollments[ta.enrollmentID]
	claim.ChallengeId = "challenge-stale"
	claim.FreshClaimSignature = ed25519.Sign(ta.bundle.key, ClaimContext(
		e.NodeID, e.ID, claim.GetChallengeId(), e.Nonce, e.ExpiresAt,
		claim.GetCapabilities().GetCapabilities(), ta.authority.channelContextLocked()))
	_ = current
	if _, err := ta.authority.ClaimEnrollment(claim); !errors.Is(err, ErrStaleNonce) {
		t.Fatalf("stale nonce not refused: %v", err)
	}
	if e.State != "pending" {
		t.Fatal("stale nonce mutated state")
	}
}

func TestEnrollmentReplayAfterConsume(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	claim := ta.validClaim(t)
	if _, err := ta.authority.ClaimEnrollment(claim); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.authority.ClaimEnrollment(claim); !errors.Is(err, ErrRefused) {
		t.Fatalf("replay not refused: %v", err)
	}
}

func TestEnrollmentRevokedAndGenerationMismatch(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	e := ta.authority.enrollments[ta.enrollmentID]
	e.Revoked = true
	if _, err := ta.authority.ClaimEnrollment(ta.validClaim(t)); !errors.Is(err, ErrRefused) {
		t.Fatalf("revoked enrollment accepted: %v", err)
	}
	e.Revoked = false
	e.CredentialGeneration = 7
	if _, err := ta.authority.ClaimEnrollment(ta.validClaim(t)); !errors.Is(err, ErrRevoked) {
		t.Fatalf("generation mismatch not refused: %v", err)
	}
}

func TestEnrollmentTLSTopologySupportAndRefusal(t *testing.T) {
	t.Run("unsupported", func(t *testing.T) {
		ta := newTestAuthority(t, TopologyUnsupported)
		if _, err := ta.authority.ClaimEnrollment(ta.validClaim(t)); !errors.Is(err, ErrTopology) {
			t.Fatalf("unsupported topology not refused: %v", err)
		}
	})
	t.Run("unattested proxy", func(t *testing.T) {
		ta := newTestAuthority(t, TopologyTrustedProxy)
		if _, err := ta.authority.ClaimEnrollment(ta.validClaim(t)); !errors.Is(err, ErrTopology) {
			t.Fatalf("unattested trusted proxy not refused: %v", err)
		}
	})
	t.Run("attested proxy", func(t *testing.T) {
		ta := newTestAuthority(t, TopologyTrustedProxy)
		ta.authority.SetProxyAttested(true)
		if _, err := ta.authority.ClaimEnrollment(ta.validClaim(t)); err != nil {
			t.Fatalf("attested trusted proxy refused: %v", err)
		}
	})
	t.Run("l4 pass through", func(t *testing.T) {
		ta := newTestAuthority(t, TopologyL4PassThrough)
		if _, err := ta.authority.ClaimEnrollment(ta.validClaim(t)); err != nil {
			t.Fatalf("L4 pass-through refused: %v", err)
		}
	})
}

func TestEnrollmentThreeAuthorizationDomains(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	resp, err := ta.authority.ClaimEnrollment(ta.validClaim(t))
	if err != nil {
		t.Fatal(err)
	}
	node := Credential{Kind: CredentialNode, NodeID: ta.nodeID, ObjectID: ta.nodeID,
		Serial: resp.GetCertificateSerial(), Scopes: []string{"node:heartbeat", "node:report"}}
	bootstrap := Credential{Kind: CredentialBootstrap, NodeID: ta.nodeID}
	operator := Credential{Kind: CredentialOperator}

	// Positive controls: each domain can perform its own operation.
	if err := ta.authority.AuthorizeNodeOperation(node, ta.nodeID, "node:heartbeat"); err != nil {
		t.Fatalf("legitimate node operation refused: %v", err)
	}
	if err := ta.authority.AuthorizePreflight(node, ta.nodeID, []string{"node:report"}); err != nil {
		t.Fatalf("legitimate preflight refused: %v", err)
	}
	if err := ta.authority.AuthorizeOperator(operator); err != nil {
		t.Fatalf("legitimate operator action refused: %v", err)
	}
	if err := ta.authority.AuthorizeBootstrap(bootstrap); err != nil {
		t.Fatalf("legitimate bootstrap use refused: %v", err)
	}

	// Negative: no domain may cross into another.
	if err := ta.authority.AuthorizeNodeOperation(bootstrap, ta.nodeID, "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("bootstrap credential used on node operation: %v", err)
	}
	if err := ta.authority.AuthorizeOperator(node); !errors.Is(err, ErrScope) {
		t.Fatalf("node credential used on operator action: %v", err)
	}
	if err := ta.authority.AuthorizeNodeOperation(node, "node-other", "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("cross-node object ID accepted: %v", err)
	}
	if err := ta.authority.AuthorizePreflight(node, ta.nodeID, []string{"node:report", "nodes:create"}); !errors.Is(err, ErrScope) {
		t.Fatalf("over-scope preflight accepted: %v", err)
	}
}

func TestEnrollmentDigestOnlyStorageAndNoSecretsInResponses(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	resp, err := ta.authority.ClaimEnrollment(ta.validClaim(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := ta.authority.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(snapshot)
	if strings.Contains(encoded, base64.StdEncoding.EncodeToString(ta.bundle.secret)) {
		t.Fatal("plaintext secret found in owned storage snapshot")
	}
	assertNoSecret(t, resp, ta.bundle.secret)
	view, err := ta.authority.EnrollmentView(ta.enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecret(t, view, ta.bundle.secret)
	// Only digest and approved-key metadata are stored, never S or the key.
	e := ta.authority.enrollments[ta.enrollmentID]
	if bytes.Equal(e.Digest, ta.bundle.secret) || bytes.Contains(e.Digest, ta.bundle.secret) {
		t.Fatal("stored digest equals the plaintext secret")
	}
}

func TestEnrollmentAtomicRollback(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	ta.authority.InjectPreCommitFailure(true)
	if _, err := ta.authority.ClaimEnrollment(ta.validClaim(t)); err == nil {
		t.Fatal("injected pre-commit failure did not refuse the claim")
	}
	e := ta.authority.enrollments[ta.enrollmentID]
	if e.State != "pending" || e.NonceUsed || e.CredentialGeneration != 0 {
		t.Fatal("partial state survived an aborted claim")
	}
}

// MUST-FIX (OSS-385): a failure in certificate issuance must not leave a
// consumed+active enrollment with no credential. The certificate is issued
// before any ownership state is written, so a legitimate retry succeeds.
func TestEnrollmentIssuanceFailureLeavesStateUnchangedAndRetrySucceeds(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	firstClaim := ta.validClaim(t) // establish the server-stored challenge first
	before := *ta.authority.enrollments[ta.enrollmentID]
	beforeNonce := append([]byte{}, before.Nonce...)
	serialBefore := ta.authority.serialCounter

	ta.authority.InjectIssuanceFailure(true)
	if _, err := ta.authority.ClaimEnrollment(firstClaim); !errors.Is(err, ErrRefused) {
		t.Fatalf("issuance failure not refused as ErrRefused: %v", err)
	}

	e := ta.authority.enrollments[ta.enrollmentID]
	if e.State != before.State || e.NonceUsed != before.NonceUsed ||
		e.CredentialGeneration != before.CredentialGeneration || e.CertSerial != before.CertSerial ||
		e.ConsumedAt != nil || len(e.ChannelBinding) != 0 ||
		ta.authority.serialCounter != serialBefore {
		t.Fatalf("issuance failure mutated enrollment state: %+v (serialCounter=%d)", e, ta.authority.serialCounter)
	}
	if !bytes.Equal(e.Nonce, beforeNonce) || e.ChallengeID != before.ChallengeID {
		t.Fatal("issuance failure consumed or replaced the challenge nonce")
	}

	// Legitimate retry with a fresh challenge and a healthy issuer.
	ta.authority.InjectIssuanceFailure(false)
	retryClaim := ta.validClaim(t) // begins a new challenge and re-signs
	resp, err := ta.authority.ClaimEnrollment(retryClaim)
	if err != nil {
		t.Fatalf("legitimate retry after issuance failure refused: %v", err)
	}
	if resp.GetCertificateSerial() == "" || len(resp.GetEnrolledClientCertificateDer()) == 0 {
		t.Fatal("retry returned no credential")
	}
	if e.State != "active" || !e.NonceUsed || e.CredentialGeneration != 1 || e.CertSerial == "" {
		t.Fatalf("retry did not commit atomically: %+v", e)
	}
}

func TestEnrollmentDurableRotationAcrossRestart(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	resp, err := ta.authority.ClaimEnrollment(ta.validClaim(t))
	if err != nil {
		t.Fatal(err)
	}
	gen, err := ta.authority.Rotate(Credential{Kind: CredentialOperator}, ta.nodeID)
	if err != nil || gen != 2 {
		t.Fatalf("rotation failed: gen=%d err=%v", gen, err)
	}
	snapshot, err := ta.authority.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreAuthority(TopologyDirectTLS, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if restored.enrollments[ta.enrollmentID].CredentialGeneration != 2 {
		t.Fatal("credential generation did not survive restart")
	}
	if !restored.IsSerialRevoked(resp.GetCertificateSerial()) {
		t.Fatal("old certificate serial revocation did not survive restart")
	}
	if err := restored.AuthorizeNodeOperation(Credential{
		Kind: CredentialNode, ObjectID: ta.nodeID, Serial: resp.GetCertificateSerial(),
		Scopes: []string{"node:heartbeat"},
	}, ta.nodeID, "node:heartbeat"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("revoked serial still authorized after restart: %v", err)
	}
}

func TestCreateEnrollmentRequiresEveryProofField(t *testing.T) {
	a, err := NewEnrollmentAuthority(TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CreateEnrollment(Credential{Kind: CredentialOperator}, &v1.CreateEnrollmentRequest{NodeId: "n"}); !errors.Is(err, ErrMissingProof) {
		t.Fatalf("incomplete proof bundle accepted: %v", err)
	}
	if _, err := a.CreateEnrollment(Credential{Kind: CredentialBootstrap}, &v1.CreateEnrollmentRequest{NodeId: "n"}); !errors.Is(err, ErrScope) {
		t.Fatalf("non-operator created an enrollment: %v", err)
	}
}

// Ed25519-only is an explicit implementation decision narrowing ADR 0019's
// "Ed25519 or P-256" allowance. A P-256 CSR must be refused at registration so
// there is exactly one signing path and one canonical context encoding.
func TestCreateEnrollmentRefusesP256Key(t *testing.T) {
	a, err := NewEnrollmentAuthority(TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "p256-node"},
	}, key)
	if err != nil {
		t.Fatal(err)
	}
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	fp := sha256.Sum256(spki)
	req := &v1.CreateEnrollmentRequest{
		NodeId: "p256-node", Ttl: durationpb.New(time.Hour),
		ApprovedPublicKeySpki: spki, ApprovedKeyFingerprint: fp[:],
		EnrollmentSecretDigest: make([]byte, 32), EnrollmentSalt: make([]byte, 16),
		CsrPre: csrDER,
	}
	if _, err := a.CreateEnrollment(Credential{Kind: CredentialOperator}, req); !errors.Is(err, ErrMismatchedKey) {
		t.Fatalf("P-256 approved key was not refused: %v", err)
	}
}

// TestSharedBoundaryWiresAllThreeDomains proves AuthorizeBootstrap and
// AuthorizeNodeOperation are enforced by the production seam, not just called
// by tests. AuthorizeOperator is enforced by every management path.
func TestSharedBoundaryWiresAllThreeDomains(t *testing.T) {
	b, err := NewBoundary(TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	ctx := fixtureContext(t)
	operator := Credential{Kind: CredentialOperator}
	bootstrap := Credential{Kind: CredentialBootstrap}
	node := Credential{Kind: CredentialNode, NodeID: "node", ObjectID: "node",
		Scopes: []string{"node:heartbeat", "node:report"}}

	// Bootstrap domain is required for both bounded enrollment steps.
	if _, err := b.BeginEnrollment(ctx, operator, "missing"); !errors.Is(err, ErrScope) {
		t.Fatalf("operator used the bootstrap challenge step: %v", err)
	}
	if _, err := b.ClaimEnrollment(ctx, operator, nil); !errors.Is(err, ErrScope) {
		t.Fatalf("operator used the bootstrap claim step: %v", err)
	}
	if _, err := b.BeginEnrollment(ctx, bootstrap, "missing"); err == nil || errors.Is(err, ErrScope) {
		t.Fatalf("bootstrap challenge step returned an unexpected result: %v", err)
	}

	// Operator domain is enforced by CreateEnrollment.
	if _, err := b.CreateEnrollment(ctx, bootstrap, nil); !errors.Is(err, ErrScope) {
		t.Fatalf("bootstrap credential created an enrollment: %v", err)
	}
	if _, err := b.CreateEnrollment(ctx, operator, nil); !errors.Is(err, ErrMissingProof) {
		t.Fatalf("operator enrollment did not reach proof validation: %v", err)
	}

	// Node domain is enforced on every node operation and preflight report.
	if err := b.AuthorizeNodeOperation(bootstrap, "node", "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("bootstrap credential used on a node operation: %v", err)
	}
	if err := b.AuthorizeNodeOperation(operator, "node", "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("operator credential used on a node operation: %v", err)
	}
	if err := b.AuthorizeNodeOperation(node, "other-node", "node:heartbeat"); !errors.Is(err, ErrScope) {
		t.Fatalf("cross-node operation accepted: %v", err)
	}
	if err := b.AuthorizeNodeOperation(node, "node", "nodes:create"); !errors.Is(err, ErrScope) {
		t.Fatalf("node used an operator scope: %v", err)
	}
	if err := b.AuthorizePreflight(node, "node", []string{"node:report", "nodes:create"}); !errors.Is(err, ErrScope) {
		t.Fatalf("over-scope preflight accepted: %v", err)
	}
}

// TestClaimContextCanonicalEncoding pins the exact bytes documented in ADR
// 0019 so no lane can reintroduce a client nonce or change the delimiters.
func TestClaimContextCanonicalEncoding(t *testing.T) {
	expires := fixtureTime()
	nonce := []byte{0x01, 0x00, 0x02}
	channel := []byte{0xaa, 0x00, 0xbb}
	caps := []v1.NodeCapability{v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET}

	got := ClaimContext("node", "enroll", "challenge", nonce, expires, caps, channel)
	var want bytes.Buffer
	want.WriteString("carbon-panel-enroll-v1")
	want.WriteByte(0)
	want.WriteString("node")
	want.WriteByte(0)
	want.WriteString("enroll")
	want.WriteByte(0)
	want.WriteString("challenge")
	want.WriteByte(0)
	want.Write(nonce)
	want.WriteByte(0)
	want.WriteString(expires.UTC().Format(time.RFC3339Nano))
	want.WriteByte(0)
	want.WriteString(v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET.String())
	want.WriteByte(0)
	want.Write(channel)
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("canonical context changed:\n got %x\nwant %x", got, want.Bytes())
	}
	// The nonce is the server-stored challenge value, never a request field.
	if !bytes.Contains(got, nonce) {
		t.Fatal("server nonce missing from canonical context")
	}
	// A different nonce must produce a different context.
	if bytes.Equal(got, ClaimContext("node", "enroll", "challenge", []byte{0x09}, expires, caps, channel)) {
		t.Fatal("canonical context does not bind the server nonce")
	}
}

func assertNoSecret(t *testing.T, msg proto.Message, secret []byte) {
	t.Helper()
	data, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, secret) || strings.Contains(string(data), base64.StdEncoding.EncodeToString(secret)) {
		t.Fatalf("plaintext secret appeared in a response: %s", string(data))
	}
}
