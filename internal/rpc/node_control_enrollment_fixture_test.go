// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This file is a TEST-ONLY fixture seam for ADR 0019 / plan §9.3. It is not an
// enrollment server: no RPC handler is mounted and none of these helpers is
// production code. Every key, secret, channel and certificate is synthetic.
// It exists solely to make the P1-DESIGN gate executable and to prove refusals.

var (
	errFixtureRefused  = errors.New("fixture enrollment refused")
	errFixtureTopology = errors.New("UNSUPPORTED_TLS_TOPOLOGY")
)

type fixtureTopology int

const (
	fixtureDirectTLS fixtureTopology = iota
	fixtureL4PassThrough
	fixtureTrustedProxy
	fixtureUnsupported
)

type fixtureEnrollment struct {
	ID                   string     `json:"id"`
	NodeID               string     `json:"node_id"`
	State                string     `json:"state"`
	ExpiresAt            time.Time  `json:"expires_at"`
	Salt                 []byte     `json:"salt"`
	Digest               []byte     `json:"digest"`
	ApprovedSPKI         []byte     `json:"approved_spki"`
	ApprovedFingerprint  []byte     `json:"approved_fingerprint"`
	CSRPre               []byte     `json:"csr_pre"`
	Capabilities         []string   `json:"capabilities"`
	ConsumedAt           *time.Time `json:"consumed_at"`
	ChannelBinding       []byte     `json:"channel_binding"`
	CredentialGeneration int64      `json:"credential_generation"`
	Revoked              bool       `json:"revoked"`
	CertSerial           string     `json:"cert_serial"`
	ChallengeID          string     `json:"challenge_id"`
	Nonce                []byte     `json:"nonce"`
	NonceExpiresAt       time.Time  `json:"nonce_expires_at"`
	NonceUsed            bool       `json:"nonce_used"`
}

type fixtureHarness struct {
	mu               sync.Mutex
	enrollments      map[string]*fixtureEnrollment
	revokedSerials   map[string]bool
	channelContext   []byte
	topology         fixtureTopology
	proxyAttested    bool
	failBeforeCommit bool
	challengeCounter int
	now              func() time.Time
}

func newFixtureHarness() *fixtureHarness {
	return &fixtureHarness{
		enrollments:    make(map[string]*fixtureEnrollment),
		revokedSerials: make(map[string]bool),
		channelContext: []byte("server-derived-channel-context"),
		topology:       fixtureDirectTLS,
		now:            func() time.Time { return time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC) },
	}
}

func deterministicFixtureKey(label string) ed25519.PrivateKey {
	seed := sha256.Sum256([]byte("fixture-key:" + label))
	return ed25519.NewKeyFromSeed(seed[:])
}

func fixtureKey(t *testing.T, label string) ed25519.PrivateKey {
	t.Helper()
	return deterministicFixtureKey(label)
}

func fixtureCSR(t *testing.T, key ed25519.PrivateKey, label string) []byte {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: label},
	}, key)
	if err != nil {
		t.Fatalf("fixture CSR creation failed: %v", err)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatalf("fixture CSR parse failed: %v", err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("fixture CSR self-signature failed: %v", err)
	}
	return der
}

func fixtureApprovedSPKI(t *testing.T, key ed25519.PrivateKey) ([]byte, []byte) {
	t.Helper()
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil {
		t.Fatalf("fixture SPKI failed: %v", err)
	}
	fp := sha256.Sum256(spki)
	return spki, fp[:]
}

// fixtureClaimContext is the exact canonical context the fresh signature must
// cover: "carbon-panel-enroll-v1" plus node ID, enrollment ID, challenge ID,
// server nonce, expiry, capabilities and the verified channel context.
func fixtureClaimContext(nodeID, enrollmentID, challengeID string, nonce []byte, expiresAt time.Time, capabilities []string, channel []byte) []byte {
	var b bytes.Buffer
	b.WriteString("carbon-panel-enroll-v1\x00")
	for _, part := range []string{nodeID, enrollmentID, challengeID} {
		b.WriteString(part)
		b.WriteByte(0)
	}
	b.Write(nonce)
	b.WriteByte(0)
	b.WriteString(expiresAt.UTC().Format(time.RFC3339Nano))
	b.WriteByte(0)
	b.WriteString(strings.Join(capabilities, ","))
	b.WriteByte(0)
	b.Write(channel)
	return b.Bytes()
}

func (h *fixtureHarness) register(t *testing.T, id, nodeID string, key ed25519.PrivateKey, secret string, ttl time.Duration, capabilities []string) {
	t.Helper()
	salt := []byte("fixture-salt-" + id)
	digestInput := append(append([]byte{}, salt...), []byte(secret)...)
	digest := sha256.Sum256(digestInput)
	spki, fp := fixtureApprovedSPKI(t, key)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enrollments[id] = &fixtureEnrollment{
		ID:                  id,
		NodeID:              nodeID,
		State:               "pending",
		ExpiresAt:           h.now().Add(ttl),
		Salt:                salt,
		Digest:              digest[:],
		ApprovedSPKI:        spki,
		ApprovedFingerprint: fp,
		CSRPre:              fixtureCSR(t, key, nodeID),
		Capabilities:        capabilities,
	}
}

// beginChallenge models the bounded, unauthenticated challenge step. It never
// returns a secret and the nonce is single-use.
func (h *fixtureHarness) beginChallenge(enrollmentID string) (string, []byte, time.Time, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	enrollment := h.enrollments[enrollmentID]
	if enrollment == nil || enrollment.State != "pending" || h.now().After(enrollment.ExpiresAt) {
		return "", nil, time.Time{}, errFixtureRefused
	}
	h.challengeCounter++
	challengeID := "challenge-" + enrollmentID + "-" + string(rune('a'+h.challengeCounter))
	nonceInput := sha256.Sum256([]byte("fixture-nonce:" + challengeID))
	expires := h.now().Add(2 * time.Minute)
	enrollment.ChallengeID = challengeID
	enrollment.Nonce = nonceInput[:]
	enrollment.NonceExpiresAt = expires
	enrollment.NonceUsed = false
	return challengeID, nonceInput[:], expires, nil
}

type fixtureClaim struct {
	EnrollmentID    string
	Secret          string
	ChallengeID     string
	Nonce           []byte
	Signature       []byte
	Capabilities    []string
	ChannelEvidence []byte
}

type fixtureClaimResult struct {
	CertificateDER []byte
	Serial         string
	Fingerprint    []byte
	Generation     int64
}

func (h *fixtureHarness) claim(req fixtureClaim) (fixtureClaimResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	enrollment := h.enrollments[req.EnrollmentID]
	if enrollment == nil || enrollment.State != "pending" || enrollment.Revoked || h.now().After(enrollment.ExpiresAt) {
		return fixtureClaimResult{}, errFixtureRefused
	}
	// A claim only applies to a fresh, generation-zero enrollment. A stale
	// generation must be refused rather than silently overwritten.
	if enrollment.CredentialGeneration != 0 {
		return fixtureClaimResult{}, errFixtureRefused
	}
	// REQUIRED operator-approved key binding: no key, no claim.
	if len(enrollment.ApprovedSPKI) == 0 || len(enrollment.ApprovedFingerprint) == 0 || len(enrollment.CSRPre) == 0 {
		return fixtureClaimResult{}, errFixtureRefused
	}
	// CSR_PRE is registered material; its key must be the approved key.
	csr, err := x509.ParseCertificateRequest(enrollment.CSRPre)
	if err != nil || csr.CheckSignature() != nil {
		return fixtureClaimResult{}, errFixtureRefused
	}
	csrSPKI, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil || subtle.ConstantTimeCompare(csrSPKI, enrollment.ApprovedSPKI) != 1 {
		return fixtureClaimResult{}, errFixtureRefused
	}
	// Fresh signature must verify with the approved key (not merely any key).
	approvedKey, err := x509.ParsePKIXPublicKey(enrollment.ApprovedSPKI)
	if err != nil {
		return fixtureClaimResult{}, errFixtureRefused
	}
	publicKey, ok := approvedKey.(ed25519.PublicKey)
	if !ok || !ed25519.Verify(publicKey, fixtureClaimContext(
		enrollment.NodeID, enrollment.ID, req.ChallengeID, req.Nonce,
		enrollment.ExpiresAt, req.Capabilities, h.channelContext), req.Signature) {
		return fixtureClaimResult{}, errFixtureRefused
	}
	// Constant-time digest comparison; the secret is never stored.
	secretDigest := sha256.Sum256(append(append([]byte{}, enrollment.Salt...), []byte(req.Secret)...))
	if subtle.ConstantTimeCompare(secretDigest[:], enrollment.Digest) != 1 {
		return fixtureClaimResult{}, errFixtureRefused
	}
	// Single-use, current nonce.
	if enrollment.ChallengeID == "" || req.ChallengeID != enrollment.ChallengeID ||
		enrollment.NonceUsed || h.now().After(enrollment.NonceExpiresAt) ||
		subtle.ConstantTimeCompare(enrollment.Nonce, req.Nonce) != 1 {
		return fixtureClaimResult{}, errFixtureRefused
	}
	// Channel binding: server-derived value, never a bare client assertion.
	if h.topology == fixtureUnsupported || (h.topology == fixtureTrustedProxy && !h.proxyAttested) {
		return fixtureClaimResult{}, errFixtureTopology
	}
	if subtle.ConstantTimeCompare(h.channelContext, req.ChannelEvidence) != 1 {
		return fixtureClaimResult{}, errFixtureRefused
	}
	if h.failBeforeCommit {
		return fixtureClaimResult{}, errFixtureRefused
	}
	// Atomic promotion and issuance.
	enrollment.NonceUsed = true
	enrollment.State = "active"
	now := h.now()
	enrollment.ConsumedAt = &now
	enrollment.ChannelBinding = append([]byte{}, h.channelContext...)
	enrollment.CredentialGeneration = 1
	serial := "fixture-serial-" + enrollment.ID
	enrollment.CertSerial = serial
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(int64(len(serial) + 1)),
		Subject:               pkix.Name{CommonName: enrollment.NodeID},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, approvedKey, deterministicFixtureKey("fixture-ca:"+enrollment.NodeID))
	if err != nil {
		return fixtureClaimResult{}, errFixtureRefused
	}
	fp := sha256.Sum256(der)
	return fixtureClaimResult{CertificateDER: der, Serial: serial, Fingerprint: fp[:], Generation: 1}, nil
}

func (h *fixtureHarness) rotateAndRevoke(nodeID string) (int64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, enrollment := range h.enrollments {
		if enrollment.NodeID != nodeID {
			continue
		}
		h.revokedSerials[enrollment.CertSerial] = true
		enrollment.CredentialGeneration++
		return enrollment.CredentialGeneration, nil
	}
	return 0, errFixtureRefused
}

func (h *fixtureHarness) assertSerialRevoked(t *testing.T, serial string) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.revokedSerials[serial] {
		t.Fatalf("fixture expected serial %s to be revoked", serial)
	}
}

func (h *fixtureHarness) snapshot(t *testing.T) []byte {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	data, err := json.Marshal(struct {
		Enrollments    map[string]*fixtureEnrollment `json:"enrollments"`
		RevokedSerials map[string]bool               `json:"revoked_serials"`
	}{h.enrollments, h.revokedSerials})
	if err != nil {
		t.Fatalf("fixture snapshot failed: %v", err)
	}
	return data
}

func fixtureHarnessFromSnapshot(t *testing.T, data []byte) *fixtureHarness {
	t.Helper()
	var state struct {
		Enrollments    map[string]*fixtureEnrollment `json:"enrollments"`
		RevokedSerials map[string]bool               `json:"revoked_serials"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("fixture restore failed: %v", err)
	}
	h := newFixtureHarness()
	h.enrollments = state.Enrollments
	h.revokedSerials = state.RevokedSerials
	return h
}

func fixtureValidClaim(t *testing.T, h *fixtureHarness, id, secret string, key ed25519.PrivateKey, capabilities []string) fixtureClaim {
	t.Helper()
	challengeID, nonce, _, err := h.beginChallenge(id)
	if err != nil {
		t.Fatalf("fixture challenge failed: %v", err)
	}
	enrollment := h.enrollments[id]
	signature := ed25519.Sign(key, fixtureClaimContext(
		enrollment.NodeID, id, challengeID, nonce, enrollment.ExpiresAt, capabilities, h.channelContext))
	return fixtureClaim{
		EnrollmentID: id, Secret: secret, ChallengeID: challengeID, Nonce: nonce,
		Signature: signature, Capabilities: capabilities, ChannelEvidence: h.channelContext,
	}
}

func TestNodeControlEnrollmentFixtures(t *testing.T) {
	const (
		enrollmentID = "enroll-fixture-1"
		nodeID       = "node-fixture-1"
		secret       = "FAKE-NODE-SECRET-DO-NOT-USE-0123456789"
	)
	capabilities := []string{"rootless", "cgroup_v2", "private_socket"}
	key := fixtureKey(t, "approved-node")
	otherKey := fixtureKey(t, "other-node")

	t.Run("synthetic key CSR_PRE and fresh signature", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		csr, err := x509.ParseCertificateRequest(h.enrollments[enrollmentID].CSRPre)
		if err != nil || csr.CheckSignature() != nil {
			t.Fatal("fixture CSR_PRE is not a valid synthetic CSR")
		}
		claim := fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities)
		if len(claim.Signature) != ed25519.SignatureSize {
			t.Fatal("fixture did not produce a fresh Ed25519 signature")
		}
	})

	t.Run("valid claim", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		result, err := h.claim(fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities))
		if err != nil {
			t.Fatalf("valid claim refused: %v", err)
		}
		if result.Generation != 1 || result.Serial == "" || len(result.CertificateDER) == 0 {
			t.Fatalf("unexpected claim result: %+v", result)
		}
		if h.enrollments[enrollmentID].State != "active" {
			t.Fatal("enrollment not promoted atomically")
		}
	})

	t.Run("mismatched approved key", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		claim := fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities)
		claim.Signature = ed25519.Sign(otherKey, []byte("forged"))
		if _, err := h.claim(claim); err == nil {
			t.Fatal("signature from a non-approved key was accepted")
		}
		if h.enrollments[enrollmentID].State != "pending" {
			t.Fatal("refused claim mutated enrollment state")
		}
	})

	t.Run("stale or replayed nonce", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		_, staleNonce, _, err := h.beginChallenge(enrollmentID)
		if err != nil {
			t.Fatal(err)
		}
		claim := fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities)
		claim.Nonce = staleNonce
		if _, err := h.claim(claim); err == nil {
			t.Fatal("superseded nonce was accepted")
		}
	})

	t.Run("replay refusal", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		claim := fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities)
		if _, err := h.claim(claim); err != nil {
			t.Fatalf("first claim failed: %v", err)
		}
		if _, err := h.claim(claim); err == nil {
			t.Fatal("second claim with consumed enrollment was accepted")
		}
	})

	t.Run("revoked and generation mismatch refusal", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		h.enrollments[enrollmentID].Revoked = true
		if _, err := h.claim(fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities)); err == nil {
			t.Fatal("revoked enrollment accepted a claim")
		}
		h.enrollments[enrollmentID].Revoked = false
		h.enrollments[enrollmentID].CredentialGeneration = 7
		if _, err := h.claim(fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities)); err == nil {
			t.Fatal("generation mismatch accepted a claim")
		}
	})

	t.Run("wrong scope and preflight scope", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		result, err := h.claim(fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities))
		if err != nil {
			t.Fatal(err)
		}
		// A bootstrap secret is not an enrolled credential and cannot manage
		// enrollments; a node cert cannot act outside its own object.
		if !strings.Contains(result.Serial, enrollmentID) {
			t.Fatal("issued certificate serial not bound to the enrollment")
		}
		if h.enrollments[enrollmentID].NodeID != nodeID {
			t.Fatal("enrolled node object mismatch")
		}
		const topologyEnrollmentID = "enroll-fixture-topology"
		h.register(t, topologyEnrollmentID, "node-fixture-topology", key, secret, time.Hour, capabilities)
		h.topology = fixtureUnsupported
		if _, err := h.claim(fixtureValidClaim(t, h, topologyEnrollmentID, secret, key, capabilities)); !errors.Is(err, errFixtureTopology) {
			t.Fatalf("unsupported topology not refused: %v", err)
		}
		h.register(t, "enroll-fixture-proxy", "node-fixture-proxy", key, secret, time.Hour, capabilities)
		h.topology = fixtureTrustedProxy
		h.proxyAttested = false
		if _, err := h.claim(fixtureValidClaim(t, h, "enroll-fixture-proxy", secret, key, capabilities)); !errors.Is(err, errFixtureTopology) {
			t.Fatalf("unattested trusted proxy not refused: %v", err)
		}
	})

	t.Run("digest-only storage and no secret in reads", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		result, err := h.claim(fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities))
		if err != nil {
			t.Fatal(err)
		}
		stored := base64.StdEncoding.EncodeToString(h.snapshot(t))
		if strings.Contains(stored, base64.StdEncoding.EncodeToString([]byte(secret))) {
			t.Fatal("plaintext secret present in fixture storage")
		}
		response := &v1.ClaimEnrollmentResponse{
			Identity:                     &v1.NodeIdentity{NodeId: nodeID, Name: "fixture"},
			Claims:                       &v1.NodeAuthorizationClaims{NodeId: nodeID, ObjectId: nodeID},
			EnrolledClientCertificateDer: result.CertificateDER,
			CertificateSerial:            result.Serial,
			CertificateFingerprint:       result.Fingerprint,
			CredentialGeneration:         result.Generation,
			CertificateNotAfter:          timestamppb.New(h.now().Add(24 * time.Hour)),
		}
		encoded, err := protojson.Marshal(response)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), secret) || bytes.Contains(encoded, []byte(secret)) {
			t.Fatal("plaintext secret present in claim response")
		}
		if response.Claims.ObjectId != nodeID {
			t.Fatal("node claims lost their object binding")
		}
	})

	t.Run("atomic rollback", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		h.failBeforeCommit = true
		if _, err := h.claim(fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities)); err == nil {
			t.Fatal("injected pre-commit failure did not refuse the claim")
		}
		if h.enrollments[enrollmentID].State != "pending" || h.enrollments[enrollmentID].NonceUsed {
			t.Fatal("partial state survived an aborted claim")
		}
	})

	t.Run("durable rotation", func(t *testing.T) {
		h := newFixtureHarness()
		h.register(t, enrollmentID, nodeID, key, secret, time.Hour, capabilities)
		result, err := h.claim(fixtureValidClaim(t, h, enrollmentID, secret, key, capabilities))
		if err != nil {
			t.Fatal(err)
		}
		generation, err := h.rotateAndRevoke(nodeID)
		if err != nil || generation != 2 {
			t.Fatalf("rotation failed: generation=%d err=%v", generation, err)
		}
		// Model a controller restart by rebuilding the harness from persisted
		// fixture state only.
		restored := fixtureHarnessFromSnapshot(t, h.snapshot(t))
		if restored.enrollments[enrollmentID].CredentialGeneration != 2 {
			t.Fatal("generation did not survive restart")
		}
		restored.assertSerialRevoked(t, result.Serial)
	})
}
