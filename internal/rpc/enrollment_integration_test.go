// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

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
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/nodeinstall"
	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	"github.com/athNdev/carbon-panel/internal/noderuntime/native"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

// This file is the OSS-385 INTEGRATION qualification for enrollment. It wires
// the REAL node-mode claim producer (internal/nodeinstall, used by every
// managed mode) to the REAL controller boundary (internal/noderuntime/enroll)
// with the shared generated proto messages. It is an owned, harmless fixture:
// no production RPC handler is mounted, no live credential, host, trust
// boundary or deployment is touched, and every key/secret/directory is
// synthetic and temp-dir bound. Its job is to prove that the shared Boundary is
// the single implementation the node agent/modes actually use (no duplicate
// encoder or authority), and that every ADR 0019 invariant survives the trip
// across the real seam.

type enrollmentIntegrationEnv struct {
	t            *testing.T
	boundary     *enroll.Boundary
	authority    *enroll.EnrollmentAuthority
	dir          string
	bundle       *nodeinstall.EnrollmentBundle
	create       *v1.CreateEnrollmentRequest
	enrollmentID string
	nodeID       string
	caps         []string
	secret       string
	operator     enroll.Credential
	bootstrap    enroll.Credential
	ctx          context.Context
	now          time.Time
}

func newEnrollmentIntegrationEnv(t *testing.T, caps []string) *enrollmentIntegrationEnv {
	t.Helper()
	b, err := enroll.NewBoundary(enroll.TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	b.Authority.WithClock(func() time.Time { return now })
	dir := t.TempDir()
	bundle, err := nodeinstall.BuildEnrollmentBundle(dir, "integration-node", caps, enroll.ProtocolVersion, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "create_enrollment_request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var create v1.CreateEnrollmentRequest
	if err := protojson.Unmarshal(raw, &create); err != nil {
		t.Fatalf("node-generated create request is not the shared proto message: %v", err)
	}
	created, err := b.CreateEnrollment(context.Background(), enroll.Credential{Kind: enroll.CredentialOperator}, &create)
	if err != nil {
		t.Fatalf("shared boundary refused the node-generated create request: %v", err)
	}
	secretBytes, err := os.ReadFile(filepath.Join(dir, "enrollment.secret"))
	if err != nil {
		t.Fatal(err)
	}
	return &enrollmentIntegrationEnv{
		t: t, boundary: b, authority: b.Authority, dir: dir, bundle: bundle,
		create: &create, enrollmentID: created.GetEnrollment().GetEnrollmentId(),
		nodeID: bundle.NodeID, caps: caps, secret: string(secretBytes),
		operator:  enroll.Credential{Kind: enroll.CredentialOperator},
		bootstrap: enroll.Credential{Kind: enroll.CredentialBootstrap},
		ctx:       context.Background(), now: now,
	}
}

func (e *enrollmentIntegrationEnv) challenge(t *testing.T) *v1.BeginEnrollmentChallengeResponse {
	t.Helper()
	ch, err := e.boundary.BeginEnrollment(e.ctx, e.bootstrap, e.enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

// claim drives the real node-installer claim builder against a controller
// challenge, then returns the exact generated proto message the RPC carries.
func (e *enrollmentIntegrationEnv) claim(t *testing.T, ch *v1.BeginEnrollmentChallengeResponse) *v1.ClaimEnrollmentRequest {
	t.Helper()
	raw, err := nodeinstall.BuildClaim(e.dir, nodeinstall.ClaimChallenge{
		ChallengeID:    ch.GetChallengeId(),
		ServerNonce:    ch.GetServerNonce(),
		ExpiresAt:      ch.GetExpiresAt().AsTime(),
		NodeID:         ch.GetNodeId(),
		EnrollmentID:   ch.GetEnrollmentId(),
		ChannelContext: ch.GetChannelContext(),
	}, e.now)
	if err != nil {
		t.Fatalf("node-mode claim builder refused a valid controller challenge: %v", err)
	}
	var req v1.ClaimEnrollmentRequest
	if err := protojson.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	return &req
}

// canonicalNames mirrors the controller's proto enum naming so the test can
// compare the node's claim context byte-for-byte with the single shared encoder.
func canonicalNames(caps []string) []string {
	known := map[string]v1.NodeCapability{
		"ROOTLESS":       v1.NodeCapability_NODE_CAPABILITY_ROOTLESS,
		"PRIVATE_SOCKET": v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET,
	}
	out := make([]string, 0, len(caps))
	for _, c := range caps {
		out = append(out, known[c].String())
	}
	return out
}

// TestEnrollmentIntegrationSharedBoundarySingleImplementation proves the node
// agent/modes and the controller share ONE enrollment encoder and authority.
func TestEnrollmentIntegrationSharedBoundarySingleImplementation(t *testing.T) {
	caps := []string{"ROOTLESS", "PRIVATE_SOCKET"}
	e := newEnrollmentIntegrationEnv(t, caps)
	ch := e.challenge(t)

	// (1) The node-mode claim context must be byte-identical to the single
	// shared encoder. A second delimiter/capability encoding would diverge here.
	localCtx := nodeinstall.ClaimContext(e.nodeID, e.enrollmentID, ch.GetChallengeId(),
		ch.GetServerNonce(), ch.GetExpiresAt().AsTime(), caps, ch.GetChannelContext())
	sharedCtx := enroll.ClaimContextRaw(e.nodeID, e.enrollmentID, ch.GetChallengeId(),
		ch.GetServerNonce(), ch.GetExpiresAt().AsTime(), canonicalNames(caps), ch.GetChannelContext())
	if !bytes.Equal(localCtx, sharedCtx) {
		t.Fatalf("node mode reimplemented the claim context encoder:\n node=%q\nshared=%q", localCtx, sharedCtx)
	}
	// Canonical NUL framing, the server-stored nonce, and no JSON encoding.
	if !bytes.HasPrefix(localCtx, []byte("carbon-panel-enroll-v1\x00")) ||
		!bytes.Contains(localCtx, ch.GetServerNonce()) ||
		bytes.Contains(localCtx, []byte{0x7b, 0x22}) {
		t.Fatalf("claim context is not the canonical NUL-delimited server-nonce form: %q", localCtx)
	}

	// (2) The real node-installer claim must verify at the shared controller
	// over the exact context the controller itself builds from the proto caps.
	claim := e.claim(t, ch)
	verifyCtx := enroll.ClaimContext(e.nodeID, e.enrollmentID, ch.GetChallengeId(),
		ch.GetServerNonce(), ch.GetExpiresAt().AsTime(), claim.GetCapabilities().GetCapabilities(), ch.GetChannelContext())
	pub, err := x509.ParsePKIXPublicKey(e.bundle.ApprovedPublicKeySPKI)
	if err != nil {
		t.Fatal(err)
	}
	approved, ok := pub.(ed25519.PublicKey)
	if !ok {
		t.Fatal("approved key is not Ed25519")
	}
	if !ed25519.Verify(approved, verifyCtx, claim.GetFreshClaimSignature()) {
		t.Fatal("node-mode claim signature does not verify over the shared controller context")
	}

	// (3) A node-mode consumer that tried to bypass the boundary with its own
	// divergent encoding is refused by the controller (the authority is single).
	_, priv, err := loadNodeKey(e.dir)
	if err != nil {
		t.Fatal(err)
	}
	bypass := proto.Clone(claim).(*v1.ClaimEnrollmentRequest)
	bypass.FreshClaimSignature = ed25519.Sign(priv, enroll.ClaimContextRaw(e.nodeID, e.enrollmentID,
		ch.GetChallengeId(), ch.GetServerNonce(), ch.GetExpiresAt().AsTime(), caps, ch.GetChannelContext()))
	if _, err := e.boundary.ClaimEnrollment(e.ctx, e.bootstrap, bypass); !errors.Is(err, enroll.ErrMismatchedKey) {
		t.Fatalf("divergent node-local encoding bypassed the shared boundary: %v", err)
	}

	// (4) The host-native mode lane also enters through this same boundary.
	runner, err := native.NewRunner(native.TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	if runner.Boundary == nil || runner.Authority == nil {
		t.Fatal("host-native runner is not bound to the shared boundary")
	}
	if err := runner.AuthorizeNodeOperation(e.operator, e.nodeID, "node:heartbeat"); !errors.Is(err, native.ErrScope) {
		t.Fatalf("host-native lane let an operator through the node domain: %v", err)
	}
	if err := runner.AuthorizeNodeOperation(e.bootstrap, e.nodeID, "node:heartbeat"); !errors.Is(err, native.ErrScope) {
		t.Fatalf("host-native lane let a bootstrap marker through the node domain: %v", err)
	}
}

// loadNodeKey is a test-only helper that reads the owned Ed25519 identity key
// from the installer state dir (production never marshals it after first boot).
func loadNodeKey(dir string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	id, priv, err := nodeinstall.LoadIdentity(dir)
	if err != nil {
		return nil, nil, err
	}
	pub, err := x509.ParsePKIXPublicKey(id.SPKI)
	if err != nil {
		return nil, nil, err
	}
	return pub.(ed25519.PublicKey), priv, nil
}

// TestEnrollmentIntegrationAtomicConsumeAndRollback drives the full lifecycle
// across the seam: domain separation, single-use consume, issuance-failure
// rollback with retry, and digest-only secret handling.
func TestEnrollmentIntegrationAtomicConsumeAndRollback(t *testing.T) {
	caps := []string{"ROOTLESS"}
	e := newEnrollmentIntegrationEnv(t, caps)

	// Domains cannot cross at the real boundary.
	if _, err := e.boundary.CreateEnrollment(e.ctx, e.bootstrap, e.create); !errors.Is(err, enroll.ErrScope) {
		t.Fatalf("bootstrap marker created an enrollment: %v", err)
	}
	if _, err := e.boundary.BeginEnrollment(e.ctx, e.operator, e.enrollmentID); !errors.Is(err, enroll.ErrScope) {
		t.Fatalf("operator used the bootstrap challenge step: %v", err)
	}

	ch := e.challenge(t)
	claim := e.claim(t, ch)

	// Issuance failure: zero ownership mutation and a later retry succeeds.
	e.authority.InjectIssuanceFailure(true)
	if _, err := e.boundary.ClaimEnrollment(e.ctx, e.bootstrap, claim); err == nil {
		t.Fatal("claim succeeded despite injected issuance failure")
	}
	view, err := e.authority.EnrollmentView(e.enrollmentID)
	if err != nil {
		t.Fatal(err)
	}
	if view.GetState() != v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_PENDING {
		t.Fatalf("issuance failure mutated enrollment state: %v", view.GetState())
	}
	e.authority.InjectIssuanceFailure(false)

	// A legitimate retry with a fresh challenge succeeds. The installer's
	// single-claim marker is cleared only in this owned fixture so the real
	// builder can produce the retry claim.
	if err := os.Remove(filepath.Join(e.dir, "claim.consumed")); err != nil {
		t.Fatal(err)
	}
	retryCh := e.challenge(t)
	retryClaim := e.claim(t, retryCh)
	result, err := e.boundary.ClaimEnrollment(e.ctx, e.bootstrap, retryClaim)
	if err != nil {
		t.Fatalf("legitimate retry after issuance rollback failed: %v", err)
	}
	if len(result.GetEnrolledClientCertificateDer()) == 0 || result.GetIdentity() == nil {
		t.Fatal("claim issued no public certificate or identity")
	}

	// Atomic consume: the original challenge and direct replay are refused.
	if _, err := e.boundary.ClaimEnrollment(e.ctx, e.bootstrap, claim); err == nil {
		t.Fatal("stale challenge was accepted after a successful consume")
	}
	if _, err := e.boundary.ClaimEnrollment(e.ctx, e.bootstrap, proto.Clone(retryClaim).(*v1.ClaimEnrollmentRequest)); err == nil {
		t.Fatal("replay of the consumed claim was accepted")
	}

	// Enrolled-node domain: own object and granted scopes pass; every crossing
	// is refused by the same authority that issued the credential.
	node := enroll.Credential{Kind: enroll.CredentialNode, NodeID: e.nodeID, ObjectID: e.nodeID,
		Serial: result.GetCertificateSerial(), Scopes: result.GetClaims().GetScopes()}
	if err := e.boundary.AuthorizeNodeOperation(node, e.nodeID, "node:heartbeat"); err != nil {
		t.Fatalf("issued node credential refused on its own object: %v", err)
	}
	if err := e.boundary.AuthorizePreflight(node, e.nodeID, []string{"node:report"}); err != nil {
		t.Fatalf("issued node credential refused a scoped preflight: %v", err)
	}
	for _, bad := range []struct {
		name string
		cred enroll.Credential
		obj  string
		sc   string
	}{
		{"bootstrap on node op", e.bootstrap, e.nodeID, "node:heartbeat"},
		{"operator on node op", e.operator, e.nodeID, "node:heartbeat"},
		{"cross node", node, "other-node", "node:heartbeat"},
		{"operator scope", node, e.nodeID, "nodes:create"},
	} {
		if err := e.boundary.AuthorizeNodeOperation(bad.cred, bad.obj, bad.sc); !errors.Is(err, enroll.ErrScope) {
			t.Errorf("%s: domain crossing was not refused: %v", bad.name, err)
		}
	}

	// Digest-only secrets: the plaintext S is in neither storage nor response.
	snapshot, err := e.authority.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	responseJSON, err := protojson.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(snapshot, []byte(e.secret)) || bytes.Contains(responseJSON, []byte(e.secret)) {
		t.Fatal("plaintext enrollment secret leaked into storage or response")
	}
	if result.GetIdentity().GetKeyFingerprint() == "" {
		t.Fatal("claim identity lost its key fingerprint")
	}
}

// TestEnrollmentIntegrationRefusals proves Ed25519-only, approved-key binding,
// server-nonce freshness and digest proof survive the real node→controller seam.
func TestEnrollmentIntegrationRefusals(t *testing.T) {
	t.Run("non-ed25519 key refused at registration", func(t *testing.T) {
		b, err := enroll.NewBoundary(enroll.TopologyDirectTLS)
		if err != nil {
			t.Fatal(err)
		}
		p256, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "p256-node"}}, p256)
		if err != nil {
			t.Fatal(err)
		}
		spki, err := x509.MarshalPKIXPublicKey(p256.Public())
		if err != nil {
			t.Fatal(err)
		}
		fp := sha256.Sum256(spki)
		salt := []byte("integration-p256-salt")
		digest := sha256.Sum256(append(append([]byte{}, salt...), []byte("fake-secret-do-not-use")...))
		req := &v1.CreateEnrollmentRequest{
			NodeLabel: "p256-node", NodeId: "p256-node", Ttl: durationpb.New(time.Hour),
			ProtocolVersion: enroll.ProtocolVersion, ApprovedPublicKeySpki: spki,
			ApprovedKeyFingerprint: fp[:], EnrollmentSecretDigest: digest[:],
			EnrollmentSalt: salt, CsrPre: csrDER,
		}
		if _, err := b.CreateEnrollment(context.Background(), enroll.Credential{Kind: enroll.CredentialOperator}, req); !errors.Is(err, enroll.ErrMismatchedKey) {
			t.Fatalf("non-Ed25519 key was not refused: %v", err)
		}
	})

	t.Run("signature from a non-approved key refused", func(t *testing.T) {
		e := newEnrollmentIntegrationEnv(t, []string{"ROOTLESS"})
		ch := e.challenge(t)
		claim := e.claim(t, ch)
		_, other, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		ctxBytes := enroll.ClaimContext(e.nodeID, e.enrollmentID, ch.GetChallengeId(),
			ch.GetServerNonce(), ch.GetExpiresAt().AsTime(), claim.GetCapabilities().GetCapabilities(), ch.GetChannelContext())
		claim.FreshClaimSignature = ed25519.Sign(other, ctxBytes)
		if _, err := e.boundary.ClaimEnrollment(e.ctx, e.bootstrap, claim); !errors.Is(err, enroll.ErrMismatchedKey) {
			t.Fatalf("non-approved key signature was not refused: %v", err)
		}
	})

	t.Run("superseded server nonce refused", func(t *testing.T) {
		e := newEnrollmentIntegrationEnv(t, []string{"ROOTLESS"})
		first := e.challenge(t)
		claim := e.claim(t, first)
		// A newer challenge replaces the server-stored nonce. Re-sign over the
		// CURRENT server nonce with the approved key so signature verification
		// passes and only the single-use challenge-id check can refuse it.
		second := e.challenge(t)
		_, priv, err := loadNodeKey(e.dir)
		if err != nil {
			t.Fatal(err)
		}
		ctxBytes := enroll.ClaimContext(e.nodeID, e.enrollmentID, first.GetChallengeId(),
			second.GetServerNonce(), second.GetExpiresAt().AsTime(), claim.GetCapabilities().GetCapabilities(), second.GetChannelContext())
		claim.FreshClaimSignature = ed25519.Sign(priv, ctxBytes)
		if _, err := e.boundary.ClaimEnrollment(e.ctx, e.bootstrap, claim); !errors.Is(err, enroll.ErrStaleNonce) {
			t.Fatalf("superseded server nonce was not refused: %v", err)
		}
	})

	t.Run("wrong digest proof refused", func(t *testing.T) {
		e := newEnrollmentIntegrationEnv(t, []string{"ROOTLESS"})
		ch := e.challenge(t)
		claim := e.claim(t, ch)
		claim.EnrollmentToken = "wrong-secret-does-not-match-stored-digest"
		if _, err := e.boundary.ClaimEnrollment(e.ctx, e.bootstrap, claim); !errors.Is(err, enroll.ErrRefused) {
			t.Fatalf("wrong digest proof was not refused: %v", err)
		}
	})
}
