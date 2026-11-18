// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/durationpb"
)

// Bundle metadata extensions live under a project-local numeric OID arc
// 2.25.42 (no enterprise-number registration, no external meaning).
// .1 is the salt, .2 is the digest. Go's asn1.ObjectIdentifier stores arcs as
// int, so the arc is kept well below int64 rather than using a full 128-bit
// UUID integer.
var (
	bundleOIDRoot = asn1.ObjectIdentifier{2, 25, 42}
	saltOID       = append(append(asn1.ObjectIdentifier{}, bundleOIDRoot...), 1)
	digestOID     = append(append(asn1.ObjectIdentifier{}, bundleOIDRoot...), 2)
)

// EnrollmentBundle is the OPERATOR-FACING registration material. It is, by
// construction, secret-free: no plaintext secret, no private key. S stays in
// enrollment.secret with 0600 permissions and is sent only once, in a claim.
type EnrollmentBundle struct {
	Schema                 string      `json:"schema"`
	NodeID                 string      `json:"node_id"`
	NodeLabel              string      `json:"node_label"`
	ApprovedPublicKeySPKI  []byte      `json:"approved_public_key_spki"`
	ApprovedKeyFingerprint []byte      `json:"approved_key_fingerprint"`
	EnrollmentSecretDigest []byte      `json:"enrollment_secret_digest"`
	EnrollmentSalt         []byte      `json:"enrollment_salt"`
	CSRPre                 []byte      `json:"csr_pre"`
	RequiredCapabilities   []string    `json:"required_capabilities"`
	ProtocolVersion        int32       `json:"protocol_version"`
	TTLSeconds             int64       `json:"ttl_seconds"`
	Scope                  BundleScope `json:"scope"`
}

// BundleScope makes the "scoped" part of the bundle explicit: the registration
// is valid only for this node, these capabilities and this protocol range.
type BundleScope struct {
	NodeID          string   `json:"node_id"`
	Capabilities    []string `json:"capabilities"`
	ProtocolVersion int32    `json:"protocol_version"`
}

type enrollmentMeta struct {
	Schema    string    `json:"schema"`
	Digest    []byte    `json:"digest"`
	Salt      []byte    `json:"salt"`
	CreatedAt time.Time `json:"created_at"`
}

func encodeSecret(raw []byte) string {
	return base64.RawURLEncoding.EncodeToString(raw)
}

// loadSecret reads the stored high-entropy secret. S is stored as a base64url
// STRING (not raw bytes) so it is valid UTF-8 when placed in the proto string
// field enrollment_token; the digest is computed over these same string bytes.
func loadSecret(s string) ([]byte, error) {
	b := []byte(strings.TrimSpace(s))
	if len(b) < 32 {
		return nil, fmt.Errorf("%w: stored enrollment material is too short", ErrIntegrity)
	}
	for _, c := range b {
		if c < 0x21 || c > 0x7e {
			return nil, fmt.Errorf("%w: stored secret is not printable ASCII", ErrIntegrity)
		}
	}
	return b, nil
}

var capabilityNames = map[string]v1.NodeCapability{
	"ROOTLESS":             v1.NodeCapability_NODE_CAPABILITY_ROOTLESS,
	"PRIVATE_SOCKET":       v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET,
	"DEDICATED_ROOTS":      v1.NodeCapability_NODE_CAPABILITY_DEDICATED_ROOTS,
	"CONTAINERD_NAMESPACE": v1.NodeCapability_NODE_CAPABILITY_CONTAINERD_NAMESPACE,
	"CGROUP_V2":            v1.NodeCapability_NODE_CAPABILITY_CGROUP_V2,
	"PRIVILEGED_PORT":      v1.NodeCapability_NODE_CAPABILITY_PRIVILEGED_PORT,
	"STORAGE_WORKER":       v1.NodeCapability_NODE_CAPABILITY_STORAGE_WORKER,
	"DRAIN":                v1.NodeCapability_NODE_CAPABILITY_DRAIN,
	"RESTART_REQUIRED":     v1.NodeCapability_NODE_CAPABILITY_RESTART_REQUIRED,
}

func parseCapabilities(names []string) ([]v1.NodeCapability, error) {
	out := make([]v1.NodeCapability, 0, len(names))
	for _, n := range names {
		c, ok := capabilityNames[strings.ToUpper(strings.TrimSpace(n))]
		if !ok {
			return nil, fmt.Errorf("%w: unknown capability %q", ErrInvalidInput, n)
		}
		out = append(out, c)
	}
	return out, nil
}

func extension(oid asn1.ObjectIdentifier, value []byte) (pkix.Extension, error) {
	der, err := asn1.Marshal(value)
	if err != nil {
		return pkix.Extension{}, err
	}
	return pkix.Extension{Id: oid, Critical: false, Value: der}, nil
}

// BuildEnrollmentBundle generates (once) the node secret, salt and key-bound
// pre-registration CSR, then exports ONLY public material. It is idempotent:
// repeating it with the same state dir reproduces the same bundle without
// regenerating S. The generated CreateEnrollmentRequest uses the shared,
// generated proto message and is written as protojson.
func BuildEnrollmentBundle(stateDir, nodeLabel string, capabilities []string, protocolVersion int32, ttl time.Duration) (*EnrollmentBundle, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(nodeLabel) == "" {
		return nil, fmt.Errorf("%w: node label is required", ErrInvalidInput)
	}
	if len(capabilities) == 0 {
		return nil, fmt.Errorf("%w: at least one required capability is needed for a scoped bundle", ErrInvalidInput)
	}
	if protocolVersion <= 0 || ttl <= 0 {
		return nil, fmt.Errorf("%w: protocol version and TTL must be positive", ErrInvalidInput)
	}
	if _, err := parseCapabilities(capabilities); err != nil {
		return nil, err
	}
	if _, err := InitIdentity(dir); err != nil { // idempotent first-boot identity
		return nil, err
	}
	id, priv, err := LoadIdentity(dir)
	if err != nil {
		return nil, err
	}

	secretPath := filepath.Join(dir, enrollmentSecret)
	metaPath := filepath.Join(dir, enrollmentMetaFile)
	var s, salt []byte
	var meta enrollmentMeta
	if b, err := os.ReadFile(secretPath); err == nil {
		s, err = loadSecret(string(b))
		if err != nil {
			return nil, err
		}
		if err := readJSON(metaPath, &meta); err != nil {
			return nil, err
		}
		salt = meta.Salt
		if len(salt) < 16 {
			return nil, fmt.Errorf("%w: stored enrollment material is too short", ErrIntegrity)
		}
	} else if os.IsNotExist(err) {
		rawS := make([]byte, 32)
		salt = make([]byte, 32)
		if _, err := rand.Read(rawS); err != nil {
			return nil, err
		}
		if _, err := rand.Read(salt); err != nil {
			return nil, err
		}
		s = []byte(encodeSecret(rawS)) // S as a UTF-8 string; >= 43 chars
		sum := sha256.Sum256(append(append([]byte{}, salt...), s...))
		meta = enrollmentMeta{
			Schema: "carbon-panel-enrollment-meta/v1",
			Digest: sum[:], Salt: salt, CreatedAt: time.Now().UTC(),
		}
		if err := writeFile(secretPath, []byte(string(s)), 0o600); err != nil {
			return nil, err
		}
		if err := writeJSON(metaPath, &meta, 0o600); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}

	sum := sha256.Sum256(append(append([]byte{}, salt...), s...))
	if !bytes.Equal(sum[:], meta.Digest) {
		return nil, fmt.Errorf("%w: enrollment secret does not match registered digest", ErrIntegrity)
	}

	saltExt, err := extension(saltOID, salt)
	if err != nil {
		return nil, err
	}
	digestExt, err := extension(digestOID, sum[:])
	if err != nil {
		return nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:            pkix.Name{CommonName: nodeLabel},
		ExtraExtensions:    []pkix.Extension{saltExt, digestExt},
		SignatureAlgorithm: x509.PureEd25519,
	}, priv)
	if err != nil {
		return nil, err
	}
	csr, err := x509.ParseCertificateRequest(csrDER)
	if err != nil {
		return nil, fmt.Errorf("%w: generated CSR is invalid: %v", ErrIntegrity, err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: generated CSR signature is invalid: %v", ErrIntegrity, err)
	}

	caps := append([]string{}, capabilities...)
	bundle := &EnrollmentBundle{
		Schema:                 "carbon-panel-enrollment-bundle/v1",
		NodeID:                 id.NodeID,
		NodeLabel:              nodeLabel,
		ApprovedPublicKeySPKI:  id.SPKI,
		ApprovedKeyFingerprint: id.Fingerprint,
		EnrollmentSecretDigest: sum[:],
		EnrollmentSalt:         salt,
		CSRPre:                 csrDER,
		RequiredCapabilities:   caps,
		ProtocolVersion:        protocolVersion,
		TTLSeconds:             int64(ttl.Seconds()),
		Scope: BundleScope{
			NodeID: id.NodeID, Capabilities: caps, ProtocolVersion: protocolVersion,
		},
	}

	bundleJSON, err := json.Marshal(bundle)
	if err != nil {
		return nil, err
	}
	if err := assertNoSecrets(bundleJSON, s, priv); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(dir, bundleFile), bundle, 0o644); err != nil {
		return nil, err
	}

	// Fill the SHARED generated message; do not fork the contract.
	protoCaps, err := parseCapabilities(capabilities)
	if err != nil {
		return nil, err
	}
	req := &v1.CreateEnrollmentRequest{
		NodeLabel:              nodeLabel,
		Ttl:                    durationpb.New(ttl),
		RequiredCapabilities:   protoCaps,
		ProtocolVersion:        protocolVersion,
		NodeId:                 id.NodeID,
		ApprovedPublicKeySpki:  id.SPKI,
		ApprovedKeyFingerprint: id.Fingerprint,
		EnrollmentSecretDigest: sum[:],
		EnrollmentSalt:         salt,
		CsrPre:                 csrDER,
	}
	reqJSON, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := assertNoSecrets(reqJSON, s, priv); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(dir, createRequestFile), append(reqJSON, '\n'), 0o644); err != nil {
		return nil, err
	}
	return bundle, nil
}

// assertNoSecrets is the enforcement behind "secret-free provisioning
// metadata". It fails if the generated bytes contain the secret string, its
// raw bytes, a PEM private-key header or DER PKCS8 prefix.
func assertNoSecrets(exported []byte, secret []byte, priv ed25519.PrivateKey) error {
	markers := [][]byte{
		[]byte(encodeSecret(secret)),
		secret,
		[]byte("PRIVATE KEY"),
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err == nil {
		markers = append(markers, der[:min(len(der), 16)])
	}
	for _, m := range markers {
		if len(m) == 0 {
			continue
		}
		if bytes.Contains(exported, m) {
			return fmt.Errorf("%w: exported metadata contains protected material", ErrSecretLeak)
		}
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err == nil && bytes.Contains(exported, pemBytes[:min(len(pemBytes), 20)]) {
		return fmt.Errorf("%w: exported metadata contains a private key", ErrSecretLeak)
	}
	return nil
}

// ClaimChallenge is the public response from the bounded, unauthenticated
// BeginEnrollmentChallenge RPC. Nothing in it is secret.
type ClaimChallenge struct {
	ChallengeID    string    `json:"challenge_id"`
	ServerNonce    []byte    `json:"server_nonce"`
	ExpiresAt      time.Time `json:"expires_at"`
	NodeID         string    `json:"node_id"`
	EnrollmentID   string    `json:"enrollment_id"`
	ChannelContext []byte    `json:"channel_context"`
}

// ClaimContext builds the exact canonical context that ADR 0019 / the P1
// fixture require the fresh signature to cover. It is exported so other mode
// lanes and fixtures verify against one implementation, not a copy.
func ClaimContext(nodeID, enrollmentID, challengeID string, nonce []byte, expiresAt time.Time, capabilities []string, channel []byte) []byte {
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

// BuildClaim produces the one-time ClaimEnrollmentRequest as protojson. It
// validates the challenge fail-closed, signs the exact ADR context with the
// approved key, and marks the secret consumed so a second claim is refused.
func BuildClaim(stateDir string, ch ClaimChallenge, now time.Time) ([]byte, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, err
	}
	if ch.ChallengeID == "" || ch.EnrollmentID == "" || len(ch.ServerNonce) < 16 || len(ch.ChannelContext) == 0 {
		return nil, fmt.Errorf("%w: challenge is incomplete", ErrInvalidInput)
	}
	id, priv, err := LoadIdentity(dir)
	if err != nil {
		return nil, err
	}
	if ch.NodeID != "" && ch.NodeID != id.NodeID {
		return nil, fmt.Errorf("%w: challenge targets a different node", ErrInvalidInput)
	}
	if !ch.ExpiresAt.After(now) {
		return nil, fmt.Errorf("%w: challenge has expired", ErrInvalidInput)
	}
	if _, err := os.Stat(filepath.Join(dir, claimConsumed)); err == nil {
		return nil, fmt.Errorf("%w: claim already produced for this enrollment", ErrConsumed)
	}
	var bundle EnrollmentBundle
	if err := readJSON(filepath.Join(dir, bundleFile), &bundle); err != nil {
		return nil, err
	}
	rawSecret, err := os.ReadFile(filepath.Join(dir, enrollmentSecret))
	if err != nil {
		return nil, fmt.Errorf("%w: enrollment secret: %v", ErrNotFound, err)
	}
	s, err := loadSecret(string(rawSecret))
	if err != nil {
		return nil, err
	}

	ctx := ClaimContext(id.NodeID, ch.EnrollmentID, ch.ChallengeID, ch.ServerNonce, ch.ExpiresAt, bundle.RequiredCapabilities, ch.ChannelContext)
	sig := ed25519.Sign(priv, ctx)
	protoCaps, err := parseCapabilities(bundle.RequiredCapabilities)
	if err != nil {
		return nil, err
	}
	req := &v1.ClaimEnrollmentRequest{
		EnrollmentId:           ch.EnrollmentID,
		EnrollmentToken:        string(s),
		KeyFingerprint:         encodeSecret(id.Fingerprint),
		Capabilities:           &v1.NodeCapabilities{ProtocolVersion: bundle.ProtocolVersion, Capabilities: protoCaps},
		ChallengeId:            ch.ChallengeID,
		FreshClaimSignature:    sig,
		ChannelBindingEvidence: ch.ChannelContext,
	}
	out, err := protojson.MarshalOptions{Multiline: true, Indent: "  "}.Marshal(req)
	if err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(dir, claimRequestFile), append(out, '\n'), 0o600); err != nil {
		return nil, err
	}
	if err := writeFile(filepath.Join(dir, claimConsumed), []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		return nil, err
	}
	return out, nil
}
