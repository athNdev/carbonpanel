// SPDX-License-Identifier: MPL-2.0

package enroll

import (
	"bytes"
	"context"
	"crypto"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

const productionStateSchema = "carbonpanel.node-enrollment-state/v1"

var ErrStateUnavailable = errors.New("enrollment state unavailable")

// StateStore persists the authority's digest-only snapshot. Load returns
// revision zero with no snapshot for an empty store. Commit atomically
// replaces exactly expectedRevision and returns the next revision.
type StateStore interface {
	Load(ctx context.Context) (revision int64, snapshot []byte, err error)
	Commit(ctx context.Context, expectedRevision int64, snapshot []byte) (revision int64, err error)
}

type AuthorityConfig struct {
	Topology           TLSTopology
	SigningCertificate *x509.Certificate
	SigningKey         crypto.Signer
	Store              StateStore
}

// NewProductionAuthority binds durable state to an operator-supplied CA and
// uses wall-clock time. The CA private key is never serialized into the store.
func NewProductionAuthority(ctx context.Context, config AuthorityConfig) (*EnrollmentAuthority, error) {
	switch config.Topology {
	case TopologyDirectTLS, TopologyL4PassThrough:
	default:
		return nil, ErrTopology
	}
	if config.Store == nil {
		return nil, ErrStateUnavailable
	}
	ca, err := validateSigningCA(config.SigningCertificate, config.SigningKey, time.Now())
	if err != nil {
		return nil, err
	}
	authority := &EnrollmentAuthority{
		topology:   config.Topology,
		clock:      time.Now,
		caCert:     ca,
		caKey:      config.SigningKey,
		stateStore: config.Store,
	}
	if err := authority.refreshStateLocked(ctx); err != nil {
		return nil, err
	}
	return authority, nil
}

func validateSigningCA(cert *x509.Certificate, key crypto.Signer, now time.Time) (*x509.Certificate, error) {
	if cert == nil || key == nil {
		return nil, errors.New("enrollment signing CA certificate and key are required")
	}
	ca, err := x509.ParseCertificate(bytes.Clone(cert.Raw))
	if err != nil || !ca.BasicConstraintsValid || !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, errors.New("enrollment signing certificate must be a valid CA")
	}
	if now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) {
		return nil, errors.New("enrollment signing CA is outside its validity period")
	}
	spki, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil || !bytes.Equal(spki, ca.RawSubjectPublicKeyInfo) {
		return nil, errors.New("enrollment signing key does not match its CA certificate")
	}
	return ca, nil
}

type productionSnapshot struct {
	Schema        string `json:"schema"`
	CADigest      []byte `json:"ca_digest"`
	SerialCounter int64  `json:"serial_counter"`
	ownedSnapshot
}

func (a *EnrollmentAuthority) loadStateLocked(ctx context.Context) (int64, productionSnapshot, error) {
	revision, raw, err := a.stateStore.Load(ctx)
	if err != nil {
		return 0, productionSnapshot{}, fmt.Errorf("%w: load", ErrStateUnavailable)
	}
	if revision == 0 && len(raw) == 0 {
		return 0, productionSnapshot{
			ownedSnapshot: ownedSnapshot{
				Enrollments:    make(map[string]*Enrollment),
				RevokedSerials: make(map[string]bool),
			},
		}, nil
	}
	var state productionSnapshot
	if revision <= 0 || revision == math.MaxInt64 ||
		json.Unmarshal(raw, &state) != nil || state.Schema != productionStateSchema ||
		!bytes.Equal(state.CADigest, caDigest(a.caCert.Raw)) ||
		state.SerialCounter < 0 || state.Enrollments == nil || state.RevokedSerials == nil {
		return 0, productionSnapshot{}, fmt.Errorf("%w: invalid snapshot or signing CA", ErrStateUnavailable)
	}
	for id, enrollment := range state.Enrollments {
		if !validStoredEnrollment(id, enrollment) {
			return 0, productionSnapshot{}, fmt.Errorf("%w: invalid enrollment record", ErrStateUnavailable)
		}
	}
	return revision, state, nil
}

func validStoredEnrollment(id string, e *Enrollment) bool {
	if e == nil || id == "" || id != e.ID || e.NodeID == "" || len(e.Digest) != sha256.Size ||
		e.ChallengeCount < 0 || e.ChallengeCount > 3 || e.CredentialGeneration < 0 {
		return false
	}
	switch e.State {
	case "pending":
		return !e.Revoked && e.CredentialGeneration == 0 && e.CertSerial == ""
	case "active":
		return !e.Revoked && e.CredentialGeneration > 0 && e.CertSerial != "" &&
			len(e.CertificateDigest) == sha256.Size && !e.CertificateExpiresAt.IsZero()
	case "revoked":
		return e.Revoked
	default:
		return false
	}
}

func (a *EnrollmentAuthority) refreshStateLocked(ctx context.Context) error {
	if a.stateStore == nil {
		return nil
	}
	_, state, err := a.loadStateLocked(ctx)
	if err != nil {
		return err
	}
	a.enrollments = state.Enrollments
	a.revokedSerials = state.RevokedSerials
	a.serialCounter = state.SerialCounter
	return nil
}

// commitMutationLocked runs validation and mutation on a detached copy of the
// latest state. The caller holds the authority mutex and can only receive a
// successful response after the versioned store commit succeeds.
func commitMutationLocked[T any](ctx context.Context, a *EnrollmentAuthority, mutate func() (T, error)) (T, error) {
	if a.stateStore == nil {
		return mutate()
	}
	var empty T
	revision, state, err := a.loadStateLocked(ctx)
	if err != nil {
		return empty, err
	}
	previousEnrollments, previousRevoked, previousCounter := a.enrollments, a.revokedSerials, a.serialCounter
	a.enrollments, a.revokedSerials, a.serialCounter = state.Enrollments, state.RevokedSerials, state.SerialCounter
	committed := false
	defer func() {
		if !committed {
			a.enrollments, a.revokedSerials, a.serialCounter = previousEnrollments, previousRevoked, previousCounter
		}
	}()

	result, err := mutate()
	if err != nil {
		return empty, err
	}
	raw, err := json.Marshal(productionSnapshot{
		Schema:        productionStateSchema,
		CADigest:      caDigest(a.caCert.Raw),
		SerialCounter: a.serialCounter,
		ownedSnapshot: ownedSnapshot{Enrollments: a.enrollments, RevokedSerials: a.revokedSerials},
	})
	if err != nil {
		return empty, fmt.Errorf("%w: encode", ErrStateUnavailable)
	}
	newRevision, err := a.stateStore.Commit(ctx, revision, raw)
	if err != nil || newRevision != revision+1 {
		return empty, fmt.Errorf("%w: commit", ErrStateUnavailable)
	}
	committed = true
	return result, nil
}

func enrolledNodeScopes() []string {
	return []string{"node:heartbeat", "node:report", "node:ack"}
}

func (a *EnrollmentAuthority) activeCertificateLocked(e *Enrollment) bool {
	return e.State == "active" && !e.Revoked && e.CertSerial != "" &&
		!a.revokedSerials[e.CertSerial] &&
		(e.CertificateExpiresAt.IsZero() || a.clock().Before(e.CertificateExpiresAt))
}

func (a *EnrollmentAuthority) nextNodeCredentialGenerationLocked(nodeID string) (int64, error) {
	generation := int64(1)
	for _, enrollment := range a.enrollments {
		if enrollment.NodeID != nodeID {
			continue
		}
		if a.activeCertificateLocked(enrollment) || enrollment.CredentialGeneration == math.MaxInt64 {
			return 0, ErrRefused
		}
		if enrollment.CredentialGeneration >= generation {
			generation = enrollment.CredentialGeneration + 1
		}
	}
	return generation, nil
}

// NodeCredentialForCertificate resolves a certificate from a separately
// verified mTLS connection to its current durable enrollment grant.
func (a *EnrollmentAuthority) NodeCredentialForCertificate(cert *x509.Certificate) (Credential, error) {
	return a.NodeCredentialForCertificateContext(context.Background(), cert)
}

func (a *EnrollmentAuthority) NodeCredentialForCertificateContext(ctx context.Context, cert *x509.Certificate) (Credential, error) {
	if cert == nil || a.stateStore == nil {
		return Credential{}, ErrScope
	}
	parsed, err := x509.ParseCertificate(cert.Raw)
	if err != nil {
		return Credential{}, ErrScope
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.clock()
	if now.Before(parsed.NotBefore) || !now.Before(parsed.NotAfter) {
		return Credential{}, ErrRevoked
	}
	if err := a.refreshStateLocked(ctx); err != nil {
		return Credential{}, err
	}
	serial := parsed.SerialNumber.String()
	digest := sha256.Sum256(parsed.Raw)
	for _, e := range a.enrollments {
		if e.NodeID != parsed.Subject.CommonName || e.CertSerial != serial ||
			subtle.ConstantTimeCompare(e.CertificateDigest, digest[:]) != 1 {
			continue
		}
		if !a.activeCertificateLocked(e) {
			return Credential{}, ErrRevoked
		}
		return Credential{
			Kind: CredentialNode, NodeID: e.NodeID, ObjectID: e.NodeID, Serial: serial,
			CredentialGeneration: e.CredentialGeneration, Scopes: enrolledNodeScopes(),
		}, nil
	}
	return Credential{}, ErrScope
}

func (a *EnrollmentAuthority) authorizeActiveCredential(ctx context.Context, c Credential) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.refreshStateLocked(ctx); err != nil {
		return err
	}
	for _, e := range a.enrollments {
		if e.NodeID == c.NodeID && e.NodeID == c.ObjectID && e.CertSerial == c.Serial &&
			e.CredentialGeneration == c.CredentialGeneration && a.activeCertificateLocked(e) {
			return nil
		}
	}
	return ErrRevoked
}
