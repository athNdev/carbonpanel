// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package enroll

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SigningDomain is the domain-separation prefix of the canonical claim context.
// It is shared by every mode lane; a new curve or scheme requires a new domain.
const SigningDomain = "carbon-panel-enroll-v1"

// ProtocolVersion is the shared contract version advertised by node bundles.
// Mode lanes re-export it rather than defining their own enrollment version.
const ProtocolVersion int32 = 1

// Distinct refusal errors let callers and fixtures assert exact outcomes.
var (
	ErrRefused       = errors.New("enrollment refused")
	ErrMismatchedKey = errors.New("approved key mismatch")
	ErrStaleNonce    = errors.New("stale or replayed challenge nonce")
	ErrReplay        = errors.New("enrollment already consumed")
	ErrRevoked       = errors.New("credential revoked or generation mismatch")
	ErrScope         = errors.New("authorization domain or scope refused")
	ErrTopology      = errors.New("UNSUPPORTED_TLS_TOPOLOGY")
	ErrMissingProof  = errors.New("required enrollment proof material missing")
	ErrRateLimited   = errors.New("challenge rate limit exceeded")
)

// TLS topology, owned by the controller. A client assertion alone is never
// trusted; only topologies the controller can verify are accepted.
type TLSTopology int

const (
	TopologyUnset TLSTopology = iota
	TopologyDirectTLS
	TopologyL4PassThrough
	TopologyTrustedProxy
	TopologyUnsupported
)

// CredentialKind models the three non-crossing authorization domains.
type CredentialKind int

const (
	CredentialBootstrap CredentialKind = iota
	CredentialNode
	CredentialOperator
)

// Credential is a boundary-local principal used by the authorization checks.
// It carries no secret; a bootstrap "credential" is only a marker for the
// bounded bootstrap phase, not a wire-authenticated principal.
type Credential struct {
	Kind     CredentialKind
	NodeID   string
	Serial   string
	Scopes   []string
	ObjectID string
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

// Enrollment is the owned, digest-only stored state. The plaintext secret S is
// NEVER a field here: only salt, digest, approved key and metadata are kept.
type Enrollment struct {
	ID                   string
	NodeID               string
	Label                string
	State                string // pending | active | revoked
	ExpiresAt            time.Time
	Salt                 []byte
	Digest               []byte
	ApprovedSPKI         []byte
	ApprovedFingerprint  []byte
	CSRPre               []byte
	Capabilities         []v1.NodeCapability
	ProtocolVersion      int32
	ConsumedAt           *time.Time
	ChannelBinding       []byte
	CredentialGeneration int64
	Revoked              bool
	CertSerial           string
	ChallengeID          string
	ChallengeBinding     []byte
	Nonce                []byte
	NonceExpiresAt       time.Time
	NonceUsed            bool
	challengeCount       int
}

// EnrollmentAuthority is the controller-side live enrollment boundary. It uses
// an owned in-memory store; nothing is persisted to a live DB by this package.
// Rotation/revocation state survives a snapshot restore in fixtures.
type EnrollmentAuthority struct {
	mu               sync.Mutex
	enrollments      map[string]*Enrollment
	revokedSerials   map[string]bool
	topology         TLSTopology
	proxyAttested    bool
	failBeforeCommit bool
	failIssue        bool
	clock            func() time.Time
	caKey            ed25519.PrivateKey
	caCert           *x509.Certificate
	serialCounter    int64
}

// NewEnrollmentAuthority creates an owned authority with a synthetic in-memory
// CA. No private CA material is exported and no secret is stored.
func NewEnrollmentAuthority(topology TLSTopology) (*EnrollmentAuthority, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "carbon-panel-synthetic-node-ca"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, err
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &EnrollmentAuthority{
		enrollments:    make(map[string]*Enrollment),
		revokedSerials: make(map[string]bool),
		topology:       topology,
		clock:          func() time.Time { return now },
		caKey:          priv,
		caCert:         ca,
	}, nil
}

// WithClock injects a deterministic clock for fixtures.
func (a *EnrollmentAuthority) WithClock(fn func() time.Time) *EnrollmentAuthority {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.clock = fn
	return a
}

// SetProxyAttested records a verified, short-lived trusted-proxy attestation.
// A client header alone can never set this.
func (a *EnrollmentAuthority) SetProxyAttested(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.proxyAttested = v
}

// InjectPreCommitFailure is a fault seam for the after-verification,
// before-any-mutation rollback fixture (ADR 0019 verification item 9).
func (a *EnrollmentAuthority) InjectPreCommitFailure(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failBeforeCommit = v
}

// InjectIssuanceFailure is a fault seam for the MUST-FIX atomicity fixture:
// it fails certificate issuance itself, which previously happened AFTER the
// ownership state was mutated. With the fix, no state changes on this path.
func (a *EnrollmentAuthority) InjectIssuanceFailure(v bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failIssue = v
}

// CreateEnrollment is in the operator provisioning domain. All REQUIRED proof
// fields must be present; the plaintext secret is never accepted or returned.
func (a *EnrollmentAuthority) CreateEnrollment(cred Credential, req *v1.CreateEnrollmentRequest) (*v1.CreateEnrollmentResponse, error) {
	if err := a.AuthorizeOperator(cred); err != nil {
		return nil, err
	}
	if req == nil || req.GetNodeId() == "" || strings.ContainsRune(req.GetNodeId(), 0) || len(req.GetApprovedPublicKeySpki()) == 0 ||
		len(req.GetApprovedKeyFingerprint()) == 0 || len(req.GetEnrollmentSecretDigest()) == 0 ||
		len(req.GetEnrollmentSalt()) == 0 || len(req.GetCsrPre()) == 0 {
		return nil, ErrMissingProof
	}
	// The approved fingerprint must equal SHA-256(SPKI); a caller-declared
	// fingerprint is metadata only and is never an authentication factor.
	fp := sha256.Sum256(req.GetApprovedPublicKeySpki())
	if subtle.ConstantTimeCompare(fp[:], req.GetApprovedKeyFingerprint()) != 1 {
		return nil, ErrMissingProof
	}
	csr, err := x509.ParseCertificateRequest(req.GetCsrPre())
	if err != nil || csr.CheckSignature() != nil {
		return nil, ErrMissingProof
	}
	// Ed25519-only: a P-256 (or any other) key is refused at registration so
	// the single canonical signing context cannot be reused across curves.
	if _, ok := csr.PublicKey.(ed25519.PublicKey); !ok {
		return nil, ErrMismatchedKey
	}
	csrSPKI, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil || !bytes.Equal(csrSPKI, req.GetApprovedPublicKeySpki()) {
		return nil, ErrMismatchedKey
	}
	ttl := req.GetTtl().AsDuration()
	if ttl <= 0 || ttl > 24*time.Hour {
		return nil, fmt.Errorf("%w: TTL must be in (0, 24h]", ErrRefused)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	id := "enroll-" + rand.Text()
	e := &Enrollment{
		ID: id, NodeID: req.GetNodeId(), Label: req.GetNodeLabel(),
		State: "pending", ExpiresAt: a.clock().Add(ttl),
		Salt:                append([]byte{}, req.GetEnrollmentSalt()...),
		Digest:              append([]byte{}, req.GetEnrollmentSecretDigest()...),
		ApprovedSPKI:        append([]byte{}, req.GetApprovedPublicKeySpki()...),
		ApprovedFingerprint: append([]byte{}, req.GetApprovedKeyFingerprint()...),
		CSRPre:              append([]byte{}, req.GetCsrPre()...),
		Capabilities:        append([]v1.NodeCapability{}, req.GetRequiredCapabilities()...),
		ProtocolVersion:     req.GetProtocolVersion(),
	}
	a.enrollments[id] = e
	return &v1.CreateEnrollmentResponse{Enrollment: enrollmentView(e)}, nil
}

// BeginEnrollmentChallenge is unauthenticated on the wire by design but bounded
// and rate-limited. It never returns a secret and grants no node access. The
// nonce is created and stored server-side; the shared proto has no client nonce
// field. The returned expires_at is the ENROLLMENT expiry bound into SIG_CLAIM;
// the nonce has its own shorter internal TTL used only for staleness rejection.
func (a *EnrollmentAuthority) BeginEnrollmentChallenge(req *v1.BeginEnrollmentChallengeRequest) (*v1.BeginEnrollmentChallengeResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.beginEnrollmentChallengeLocked(req, a.channelContextLocked(), false)
}

func (a *EnrollmentAuthority) beginEnrollmentChallengeLocked(req *v1.BeginEnrollmentChallengeRequest, binding []byte, bound bool) (*v1.BeginEnrollmentChallengeResponse, error) {
	if req.GetEnrollmentId() == "" {
		return nil, ErrRefused
	}
	e := a.enrollments[req.GetEnrollmentId()]
	if e == nil || e.State != "pending" || a.clock().After(e.ExpiresAt) {
		return nil, ErrRefused
	}
	if e.challengeCount >= 3 {
		return nil, ErrRateLimited
	}
	nonce := make([]byte, 32)
	if _, err := rand.Read(nonce); err != nil {
		return nil, ErrRefused
	}
	e.challengeCount++
	e.ChallengeID = "challenge-" + rand.Text()
	e.ChallengeBinding = nil
	if bound {
		e.ChallengeBinding = append([]byte{}, binding...)
	}
	e.Nonce = nonce
	e.NonceExpiresAt = a.clock().Add(2 * time.Minute)
	e.NonceUsed = false
	return &v1.BeginEnrollmentChallengeResponse{
		ChallengeId:    e.ChallengeID,
		ServerNonce:    append([]byte{}, e.Nonce...),
		ExpiresAt:      timestamppb.New(e.ExpiresAt),
		NodeId:         e.NodeID,
		EnrollmentId:   e.ID,
		ChannelContext: append([]byte{}, binding...),
	}, nil
}

// ClaimEnrollment is the single-use, atomic compare-and-consume. Every check
// happens under one lock. Certificate issuance is performed FIRST into a local
// variable; ownership state is committed only after issuance succeeds. Any
// failure therefore leaves the enrollment untouched and a legitimate retry can
// succeed with a fresh challenge (MUST-FIX for OSS-385).
func (a *EnrollmentAuthority) ClaimEnrollment(req *v1.ClaimEnrollmentRequest) (*v1.ClaimEnrollmentResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.claimEnrollmentLocked(req, a.channelContextLocked(), false)
}

func (a *EnrollmentAuthority) claimEnrollmentLocked(req *v1.ClaimEnrollmentRequest, binding []byte, bound bool) (*v1.ClaimEnrollmentResponse, error) {
	if req == nil {
		return nil, ErrRefused
	}
	e := a.enrollments[req.GetEnrollmentId()]
	if e == nil || e.State != "pending" || e.Revoked || a.clock().After(e.ExpiresAt) {
		return nil, ErrRefused
	}
	if e.CredentialGeneration != 0 {
		return nil, ErrRevoked
	}
	if len(e.ApprovedSPKI) == 0 || len(e.ApprovedFingerprint) == 0 || len(e.CSRPre) == 0 {
		return nil, ErrMissingProof
	}
	// A bound challenge is redeemable only on the connection that issued it.
	// The offline fixture entry point cannot complete a bound challenge.
	if bound != (len(e.ChallengeBinding) != 0) ||
		(bound && subtle.ConstantTimeCompare(binding, e.ChallengeBinding) != 1) {
		return nil, ErrTopology
	}
	// CSR_PRE is registration material: its key must be the approved key.
	csr, err := x509.ParseCertificateRequest(e.CSRPre)
	if err != nil || csr.CheckSignature() != nil {
		return nil, ErrRefused
	}
	csrSPKI, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil || subtle.ConstantTimeCompare(csrSPKI, e.ApprovedSPKI) != 1 {
		return nil, ErrMismatchedKey
	}
	// Fresh SIG_CLAIM must verify with the approved key over the exact context.
	approved, err := x509.ParsePKIXPublicKey(e.ApprovedSPKI)
	if err != nil {
		return nil, ErrRefused
	}
	pub, ok := approved.(ed25519.PublicKey)
	// The nonce is the server-stored, single-use challenge nonce, not anything
	// supplied by the request. The shared proto has no client nonce field.
	claimedCaps := req.GetCapabilities().GetCapabilities()
	if !ok || !ed25519.Verify(pub, ClaimContext(
		e.NodeID, e.ID, req.GetChallengeId(), e.Nonce,
		e.ExpiresAt, claimedCaps, binding,
	), req.GetFreshClaimSignature()) {
		return nil, ErrMismatchedKey
	}
	// Digest-only compare; S is never stored.
	digest := sha256.Sum256(append(append([]byte{}, e.Salt...), []byte(req.GetEnrollmentToken())...))
	if subtle.ConstantTimeCompare(digest[:], e.Digest) != 1 {
		return nil, ErrRefused
	}
	// Single-use, current, unexpired nonce.
	if e.ChallengeID == "" || req.GetChallengeId() != e.ChallengeID || e.NonceUsed ||
		a.clock().After(e.NonceExpiresAt) {
		return nil, ErrStaleNonce
	}
	// TLS topology support/refusal, controller-derived only.
	if binding == nil || subtle.ConstantTimeCompare(binding, req.GetChannelBindingEvidence()) != 1 {
		return nil, ErrTopology
	}
	// Fault seam: after verification but before issuance or any mutation.
	if a.failBeforeCommit {
		return nil, ErrRefused
	}

	// ---- ISSUE FIRST, COMMIT SECOND -------------------------------------
	// Compute prospective values without touching owned state. The serial
	// counter advances only in the single commit step below.
	nextCounter := a.serialCounter + 1
	certSerial := fmt.Sprintf("cn-%s-%d", e.NodeID, nextCounter)
	der, err := a.issueClientCertificateLocked(e.NodeID, nextCounter, pub)
	if err != nil {
		return nil, fmt.Errorf("%w: certificate issuance failed", ErrRefused)
	}

	// ---- SINGLE COMMIT ---------------------------------------------------
	// Every ownership field flips together; there is no failure path after
	// this point, so no partial active/consumed state can be observed.
	now := a.clock()
	e.NonceUsed = true
	e.State = "active"
	e.ConsumedAt = &now
	e.ChannelBinding = append([]byte{}, binding...)
	e.ChallengeBinding = nil
	e.CredentialGeneration = 1
	e.CertSerial = certSerial
	a.serialCounter = nextCounter

	fp := sha256.Sum256(der)
	return &v1.ClaimEnrollmentResponse{
		Identity: &v1.NodeIdentity{
			NodeId: e.NodeID, Name: e.Label, KeyFingerprint: fmt.Sprintf("%x", e.ApprovedFingerprint),
			CredentialGeneration: 1, Transport: v1.NodeTransport_NODE_TRANSPORT_MANAGED_AGENT,
		},
		Claims: &v1.NodeAuthorizationClaims{
			NodeId: e.NodeID, ObjectId: e.NodeID,
			Scopes:               []string{"node:heartbeat", "node:report", "node:ack"},
			CredentialGeneration: 1, IssuedAt: timestamppb.New(now),
			ExpiresAt: timestamppb.New(now.Add(24 * time.Hour)),
		},
		EnrolledClientCertificateDer: der,
		CertificateSerial:            e.CertSerial,
		CertificateFingerprint:       fp[:],
		CertificateNotAfter:          timestamppb.New(now.Add(24 * time.Hour)),
		IssuingCaDigest:              caDigest(a.caCert.Raw),
		CredentialGeneration:         1,
	}, nil
}

func (a *EnrollmentAuthority) channelContextLocked() []byte {
	switch a.topology {
	case TopologyDirectTLS, TopologyL4PassThrough:
		return []byte("controller-derived:" + string(a.caCert.RawSubject))
	case TopologyTrustedProxy:
		if !a.proxyAttested {
			return nil
		}
		return []byte("verified-proxy-attestation:" + string(a.caCert.RawSubject))
	default:
		return nil
	}
}

// issueClientCertificateLocked is called with a.mu held. It is the single
// certificate issuance path; fixtures reach the same policy via
// IssueClientCertificate rather than reimplementing it.
func (a *EnrollmentAuthority) issueClientCertificateLocked(nodeID string, serialCounter int64, pub ed25519.PublicKey) ([]byte, error) {
	if a.failIssue {
		return nil, errors.New("injected certificate issuance failure")
	}
	now := a.clock()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(serialCounter + 1000),
		Subject:               pkix.Name{CommonName: nodeID},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	return x509.CreateCertificate(rand.Reader, tmpl, a.caCert, pub, a.caKey)
}

// IssueClientCertificate issues a public client certificate with the shared
// production policy and this authority's owned CA. It exists so owned fixtures
// exercise the exact same issuance path as ClaimEnrollment; it is safe for
// concurrent use. No private key or secret is returned.
func (a *EnrollmentAuthority) IssueClientCertificate(nodeID string, serialCounter int64, pub ed25519.PublicKey) ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.issueClientCertificateLocked(nodeID, serialCounter, pub)
}

func caDigest(der []byte) []byte {
	h := sha256.Sum256(der)
	return h[:]
}

// enrollmentView is metadata only. It never includes salt, digest, secret,
// certificate private key or channel context.
func enrollmentView(e *Enrollment) *v1.OneTimeEnrollment {
	state := v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_PENDING
	if e.State == "active" {
		state = v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_ACTIVE
	} else if e.Revoked {
		state = v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_REVOKED
	}
	return &v1.OneTimeEnrollment{
		EnrollmentId: e.ID, NodeLabel: e.Label,
		TokenFingerprint: fmt.Sprintf("sha256:%x", e.Digest[:4]),
		CreatedAt:        timestamppb.New(e.ExpiresAt.Add(-time.Hour)),
		ExpiresAt:        timestamppb.New(e.ExpiresAt),
		State:            state, RequiredCapabilities: e.Capabilities,
		ProtocolVersion: e.ProtocolVersion, NodeId: e.NodeID,
		ApprovedKeyFingerprint: append([]byte{}, e.ApprovedFingerprint...),
	}
}

// EnrollmentView returns the non-secret view of one owned enrollment.
func (a *EnrollmentAuthority) EnrollmentView(id string) (*v1.OneTimeEnrollment, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	e := a.enrollments[id]
	if e == nil {
		return nil, ErrRefused
	}
	return enrollmentView(e), nil
}

// RevokeCredential is durable in this owned store and invalidates the serial
// immediately. It requires the operator domain and an exact confirmation.
func (a *EnrollmentAuthority) RevokeCredential(cred Credential, req *v1.RevokeCredentialRequest) (*v1.RevokeCredentialResponse, error) {
	if err := a.AuthorizeOperator(cred); err != nil {
		return nil, err
	}
	if req == nil || req.GetConfirmationPhrase() != "REVOKE "+req.GetNodeId() {
		return nil, ErrRefused
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.enrollments {
		if e.NodeID != req.GetNodeId() {
			continue
		}
		if e.CertSerial != "" {
			a.revokedSerials[e.CertSerial] = true
		}
		e.Revoked = true
		e.State = "revoked"
		e.CredentialGeneration++
		return &v1.RevokeCredentialResponse{NodeId: e.NodeID, State: v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_REVOKED}, nil
	}
	return nil, ErrRefused
}

// Rotate issues a new generation with a bounded overlap and revokes the old
// serial. It is an operator action, never a node action.
func (a *EnrollmentAuthority) Rotate(cred Credential, nodeID string) (int64, error) {
	if err := a.AuthorizeOperator(cred); err != nil {
		return 0, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, e := range a.enrollments {
		if e.NodeID != nodeID {
			continue
		}
		if e.CertSerial != "" {
			a.revokedSerials[e.CertSerial] = true
		}
		e.CredentialGeneration++
		return e.CredentialGeneration, nil
	}
	return 0, ErrRefused
}

// IsSerialRevoked reports durable revocation state.
func (a *EnrollmentAuthority) IsSerialRevoked(serial string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.revokedSerials[serial]
}

// Snapshot and Restore model a controller restart for owned fixtures. Only
// digest/key metadata and revocation state are serialised; no secret exists.
func (a *EnrollmentAuthority) Snapshot() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return jsonSnapshot(a.enrollments, a.revokedSerials)
}

// RestoreAuthority rebuilds an authority from an owned snapshot plus a fresh
// synthetic CA. It never restores a plaintext secret because none was stored.
func RestoreAuthority(topology TLSTopology, data []byte) (*EnrollmentAuthority, error) {
	a, err := NewEnrollmentAuthority(topology)
	if err != nil {
		return nil, err
	}
	if err := jsonRestore(data, &a.enrollments, &a.revokedSerials); err != nil {
		return nil, err
	}
	return a, nil
}

// Authorization domain checks. No domain may cross into another. These are the
// single implementations consumed by the shared Boundary (see boundary.go) and
// by owner-side authorization; mode lanes must not duplicate them.
func (a *EnrollmentAuthority) AuthorizeOperator(c Credential) error {
	if c.Kind != CredentialOperator {
		return ErrScope
	}
	return nil
}

func (a *EnrollmentAuthority) AuthorizeBootstrap(c Credential) error {
	if c.Kind != CredentialBootstrap {
		return ErrScope
	}
	return nil
}

func (a *EnrollmentAuthority) AuthorizeNodeOperation(c Credential, objectID, scope string) error {
	if c.Kind != CredentialNode || c.ObjectID != objectID || !hasScope(c.Scopes, scope) {
		return ErrScope
	}
	return a.serialRevoked(c.Serial)
}

// AuthorizePreflight additionally refuses a report carrying scopes beyond its
// own narrow node grant.
func (a *EnrollmentAuthority) AuthorizePreflight(c Credential, objectID string, reportedScopes []string) error {
	if err := a.AuthorizeNodeOperation(c, objectID, "node:report"); err != nil {
		return err
	}
	for _, s := range reportedScopes {
		if !hasScope(c.Scopes, s) {
			return ErrScope
		}
	}
	return nil
}

func (a *EnrollmentAuthority) serialRevoked(serial string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.revokedSerials[serial] {
		return ErrRevoked
	}
	return nil
}

// ClaimContext is the canonical SIG_CLAIM signing context of ADR 0019 §9.3.
// Both node and controller construct it identically; it is never a caller-
// supplied value. Capabilities are encoded as their proto enum names in
// declaration order. See ClaimContextRaw for the exact byte encoding.
func ClaimContext(nodeID, enrollmentID, challengeID string, nonce []byte, expiresAt time.Time, capabilities []v1.NodeCapability, channel []byte) []byte {
	names := make([]string, 0, len(capabilities))
	for _, c := range capabilities {
		names = append(names, c.String())
	}
	return ClaimContextRaw(nodeID, enrollmentID, challengeID, nonce, expiresAt, names, channel)
}

// ClaimContextRaw is the ONE byte-level encoder for the canonical context.
// Callers pass already-canonical capability names (proto enum names, in
// declaration order). There is deliberately NO client nonce parameter: the
// nonce is always the server-stored, single-use challenge nonce.
//
// Exact encoding (all strings UTF-8, all separators a single 0x00 byte):
//
//	"carbon-panel-enroll-v1" 0x00
//	node_id 0x00 enrollment_id 0x00 challenge_id 0x00
//	server_nonce_raw_bytes 0x00
//	expires_at.UTC().Format(time.RFC3339Nano) 0x00
//	comma_join(capability_names) 0x00
//	server_observed_channel_context_raw_bytes
//
// No trailing separator follows the channel context. Numbers are encoded only
// through the RFC3339Nano expiry string; capability names contain no commas.
func ClaimContextRaw(nodeID, enrollmentID, challengeID string, nonce []byte, expiresAt time.Time, capabilityNames []string, channel []byte) []byte {
	var b bytes.Buffer
	b.WriteString(SigningDomain)
	b.WriteByte(0)
	for _, part := range []string{nodeID, enrollmentID, challengeID} {
		b.WriteString(part)
		b.WriteByte(0)
	}
	b.Write(nonce)
	b.WriteByte(0)
	b.WriteString(expiresAt.UTC().Format(time.RFC3339Nano))
	b.WriteByte(0)
	b.WriteString(strings.Join(capabilityNames, ","))
	b.WriteByte(0)
	b.Write(channel)
	return b.Bytes()
}

// NodeBundle is the node side of enrollment. It keeps the secret S and the
// private key local to the node's private runtime directory (0600); only the
// exported PublicBundle crosses to the operator domain.
type NodeBundle struct {
	EnrollmentID string
	NodeID       string
	secret       []byte
	key          ed25519.PrivateKey
	salt         []byte
}

// String redacts the secret so accidental logging cannot leak it.
func (b *NodeBundle) String() string { return "NodeBundle[" + b.EnrollmentID + ":<redacted>]" }

// GenerateNodeBundle creates S (>=256 bits), salt and an Ed25519 key pair,
// computes digest = SHA-256(salt || S) and a CSR_PRE signed by the key. Only S
// and the private key are written to a 0600 file under runtimeDir; the caller
// receives a PublicBundle and a handle for the later claim.
func GenerateNodeBundle(runtimeDir, nodeID, label string) (*NodeBundle, *v1.CreateEnrollmentRequest, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, nil, err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: label},
	}, priv)
	if err != nil {
		return nil, nil, err
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	fp := sha256.Sum256(spki)
	digestInput := append(append([]byte{}, salt...), secret...)
	digest := sha256.Sum256(digestInput)
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		return nil, nil, err
	}
	privatePath := filepath.Join(runtimeDir, "enrollment.private")
	// Store S and the private key only; never the public bundle or a token.
	privateBytes := append(append([]byte{}, secret...), priv...)
	if err := os.WriteFile(privatePath, privateBytes, 0o600); err != nil {
		return nil, nil, err
	}
	bundle := &NodeBundle{NodeID: nodeID, secret: secret, key: priv, salt: salt}
	req := &v1.CreateEnrollmentRequest{
		NodeLabel: label, NodeId: nodeID,
		Ttl: durationpb.New(time.Hour),
		RequiredCapabilities: []v1.NodeCapability{
			v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET,
			v1.NodeCapability_NODE_CAPABILITY_DEDICATED_ROOTS,
		},
		ProtocolVersion:        ProtocolVersion,
		ApprovedPublicKeySpki:  spki,
		ApprovedKeyFingerprint: fp[:],
		EnrollmentSecretDigest: digest[:],
		EnrollmentSalt:         salt,
		CsrPre:                 csrDER,
	}
	return bundle, req, nil
}

// BuildClaim produces the challenge-bound fresh signature. The controller
// returned channel context is echoed as evidence but is always re-derived
// server-side; a client assertion alone is never trusted.
func (b *NodeBundle) BuildClaim(enrollmentID string, challenge *v1.BeginEnrollmentChallengeResponse, capabilities []v1.NodeCapability) (*v1.ClaimEnrollmentRequest, error) {
	if b == nil || challenge == nil || enrollmentID == "" {
		return nil, ErrRefused
	}
	expires := challenge.GetExpiresAt().AsTime()
	ctx := ClaimContext(challenge.GetNodeId(), enrollmentID, challenge.GetChallengeId(),
		challenge.GetServerNonce(), expires, capabilities, challenge.GetChannelContext())
	sig := ed25519.Sign(b.key, ctx)
	return &v1.ClaimEnrollmentRequest{
		EnrollmentId:           enrollmentID,
		EnrollmentToken:        string(b.secret),
		ChallengeId:            challenge.GetChallengeId(),
		FreshClaimSignature:    sig,
		Capabilities:           &v1.NodeCapabilities{Capabilities: capabilities},
		ChannelBindingEvidence: append([]byte{}, challenge.GetChannelContext()...),
	}, nil
}
