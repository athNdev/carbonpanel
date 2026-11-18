// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func makeArtifact(t *testing.T, dir, version, content string) *ArtifactManifest {
	t.Helper()
	name := "bin/carbon-node"
	writeTestFile(t, filepath.Join(dir, filepath.FromSlash(name)), content)
	sum := sha256.Sum256([]byte(content))
	m := &ArtifactManifest{
		Schema: "carbon-panel-node-artifact/v1", Name: "carbon-node", Version: version,
		OS: "linux", Arch: "amd64", Files: map[string]string{name: hex.EncodeToString(sum[:])},
	}
	writeJSON(filepath.Join(dir, manifestName), m, 0o644)
	return m
}

func TestIdentityIdempotentAndUnique(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	id1, err := InitIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := InitIdentity(a)
	if err != nil {
		t.Fatal(err)
	}
	if id1.NodeID != id2.NodeID || string(id1.SPKI) != string(id2.SPKI) {
		t.Fatal("identity was regenerated on a repeat call")
	}
	id3, err := InitIdentity(b)
	if err != nil {
		t.Fatal(err)
	}
	if id3.NodeID == id1.NodeID || string(id3.SPKI) == string(id1.SPKI) {
		t.Fatal("two state directories share an identity")
	}
	// A state directory must never be the filesystem root.
	if _, err := InitIdentity(string(filepath.Separator)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("root state dir: got %v, want ErrInvalidInput", err)
	}
}

func TestIdentityMismatchIsIntegrityFailure(t *testing.T) {
	dir := t.TempDir()
	id, err := InitIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if _, err := InitIdentity(other); err != nil {
		t.Fatal(err)
	}
	otherPEM, err := os.ReadFile(filepath.Join(other, nodeKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, nodeKeyFile), otherPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadIdentity(dir); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("mismatched key: got %v, want ErrIntegrity", err)
	}
	_ = id
}

func TestBundleSecretFreeAndIdempotent(t *testing.T) {
	dir := t.TempDir()
	caps := []string{"ROOTLESS", "PRIVATE_SOCKET"}
	b1, err := BuildEnrollmentBundle(dir, "node-alpha", caps, 1, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := BuildEnrollmentBundle(dir, "node-alpha", caps, 1, 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if string(b1.EnrollmentSecretDigest) != string(b2.EnrollmentSecretDigest) || string(b1.CSRPre) != string(b2.CSRPre) {
		t.Fatal("bundle regenerated S or CSR on a repeat call")
	}
	if b1.NodeID != b2.NodeID || b1.NodeLabel != "node-alpha" || b1.ProtocolVersion != 1 {
		t.Fatal("bundle fields are inconsistent")
	}

	rawSecret, err := os.ReadFile(filepath.Join(dir, enrollmentSecret))
	if err != nil {
		t.Fatal(err)
	}
	s, err := loadSecret(string(rawSecret))
	if err != nil {
		t.Fatal(err)
	}
	if len(s) < 32 {
		t.Fatalf("secret is only %d bytes", len(s))
	}
	sum := sha256.Sum256(append(append([]byte{}, b1.EnrollmentSalt...), s...))
	if string(sum[:]) != string(b1.EnrollmentSecretDigest) {
		t.Fatal("digest is not SHA-256(salt || S)")
	}

	// The approved fingerprint must be SHA-256 of the approved SPKI.
	fp := sha256.Sum256(b1.ApprovedPublicKeySPKI)
	if string(fp[:]) != string(b1.ApprovedKeyFingerprint) {
		t.Fatal("fingerprint does not match approved SPKI")
	}

	// CSR_PRE is a self-signed registration artifact carrying the same key.
	csr, err := x509.ParseCertificateRequest(b1.CSRPre)
	if err != nil {
		t.Fatal(err)
	}
	if err := csr.CheckSignature(); err != nil {
		t.Fatalf("CSR_PRE signature invalid: %v", err)
	}
	csrSPKI, err := x509.MarshalPKIXPublicKey(csr.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(csrSPKI) != string(b1.ApprovedPublicKeySPKI) {
		t.Fatal("CSR_PRE key is not the approved key")
	}
	if csr.Subject.CommonName != "node-alpha" {
		t.Fatal("CSR_PRE does not carry the node label")
	}

	// The exported metadata files must contain no secret and no private key.
	for _, name := range []string{bundleFile, createRequestFile, identityFile} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), string(s)) || strings.Contains(string(b), encodeSecret(s)) {
			t.Fatalf("%s contains the enrollment secret", name)
		}
		if strings.Contains(string(b), "PRIVATE KEY") {
			t.Fatalf("%s contains a private key", name)
		}
	}

	// The generated CreateEnrollmentRequest is the SHARED proto contract.
	reqJSON, err := os.ReadFile(filepath.Join(dir, createRequestFile))
	if err != nil {
		t.Fatal(err)
	}
	var req v1.CreateEnrollmentRequest
	if err := protojson.Unmarshal(reqJSON, &req); err != nil {
		t.Fatalf("create request is not the shared proto message: %v", err)
	}
	if req.NodeId != b1.NodeID || len(req.ApprovedPublicKeySpki) == 0 ||
		len(req.ApprovedKeyFingerprint) == 0 || len(req.EnrollmentSecretDigest) == 0 ||
		len(req.EnrollmentSalt) == 0 || len(req.CsrPre) == 0 {
		t.Fatal("required ADR 0019 fields are missing from the create request")
	}
}

func TestClaimSignatureAndReplay(t *testing.T) {
	dir := t.TempDir()
	caps := []string{"ROOTLESS"}
	b, err := BuildEnrollmentBundle(dir, "node-claim", caps, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadIdentity(dir); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	ch := ClaimChallenge{
		ChallengeID: "chal-1", ServerNonce: []byte("0123456789abcdef0123456789abcdef"),
		ExpiresAt: now.Add(5 * time.Minute), NodeID: b.NodeID, EnrollmentID: "enr-1",
		ChannelContext: []byte("server-derived-channel"),
	}
	raw, err := BuildClaim(dir, ch, now)
	if err != nil {
		t.Fatal(err)
	}
	var claim v1.ClaimEnrollmentRequest
	if err := protojson.Unmarshal(raw, &claim); err != nil {
		t.Fatal(err)
	}
	if claim.ChallengeId != ch.ChallengeID || claim.EnrollmentId != ch.EnrollmentID {
		t.Fatal("claim challenge binding is wrong")
	}
	if string(claim.ChannelBindingEvidence) != string(ch.ChannelContext) {
		t.Fatal("claim channel binding is wrong")
	}
	ctx := ClaimContext(b.NodeID, ch.EnrollmentID, ch.ChallengeID, ch.ServerNonce, ch.ExpiresAt, caps, ch.ChannelContext)
	parsed, err := x509.ParsePKIXPublicKey(b.ApprovedPublicKeySPKI)
	if err != nil {
		t.Fatal(err)
	}
	approvedPub, ok := parsed.(ed25519.PublicKey)
	if !ok {
		t.Fatal("approved SPKI is not an Ed25519 public key")
	}
	if !ed25519.Verify(approvedPub, ctx, claim.FreshClaimSignature) {
		t.Fatal("fresh claim signature does not verify with the approved key")
	}
	// A signature from another key over the same context must fail.
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if ed25519.Verify(otherPriv.Public().(ed25519.PublicKey), ctx, claim.FreshClaimSignature) {
		t.Fatal("signature verified under a different key")
	}
	// Replay: a second claim from the same state is refused.
	if _, err := BuildClaim(dir, ch, now); !errors.Is(err, ErrConsumed) {
		t.Fatalf("replay: got %v, want ErrConsumed", err)
	}
}

func TestClaimRefusesBadChallenge(t *testing.T) {
	dir := t.TempDir()
	b, err := BuildEnrollmentBundle(dir, "node-bad", []string{"ROOTLESS"}, 1, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := ClaimChallenge{ChallengeID: "c", ServerNonce: make([]byte, 16), ExpiresAt: now.Add(time.Minute), NodeID: b.NodeID, EnrollmentID: "e", ChannelContext: []byte("channel")}
	cases := []struct {
		name string
		ch   ClaimChallenge
	}{
		{"short nonce", ClaimChallenge{ChallengeID: "c", ServerNonce: []byte("short"), ExpiresAt: base.ExpiresAt, NodeID: b.NodeID, EnrollmentID: "e", ChannelContext: base.ChannelContext}},
		{"expired", ClaimChallenge{ChallengeID: "c", ServerNonce: base.ServerNonce, ExpiresAt: now.Add(-time.Minute), NodeID: b.NodeID, EnrollmentID: "e", ChannelContext: base.ChannelContext}},
		{"other node", ClaimChallenge{ChallengeID: "c", ServerNonce: base.ServerNonce, ExpiresAt: base.ExpiresAt, NodeID: "other", EnrollmentID: "e", ChannelContext: base.ChannelContext}},
		{"no channel", ClaimChallenge{ChallengeID: "c", ServerNonce: base.ServerNonce, ExpiresAt: base.ExpiresAt, NodeID: b.NodeID, EnrollmentID: "e"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := BuildClaim(dir, tc.ch, now); err == nil {
				t.Fatal("bad challenge was accepted")
			}
		})
	}
}

func TestVerifyArtifactIntegrityPlatformAndSignature(t *testing.T) {
	dir := t.TempDir()
	m := makeArtifact(t, dir, "1.0.0", "binary-v1")
	if _, err := VerifyArtifact(dir, "linux", "amd64", nil); err != nil {
		t.Fatalf("valid artifact refused: %v", err)
	}
	if _, err := VerifyArtifact(dir, "linux", "arm64", nil); !errors.Is(err, ErrPlatform) {
		t.Fatalf("platform mismatch: got %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "bin/carbon-node"), "tampered")
	if _, err := VerifyArtifact(dir, "linux", "amd64", nil); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tamper: got %v", err)
	}
	writeTestFile(t, filepath.Join(dir, "bin/carbon-node"), "binary-v1")
	writeTestFile(t, filepath.Join(dir, "extra.sh"), "unsigned extra")
	if _, err := VerifyArtifact(dir, "linux", "amd64", nil); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("extra file: got %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "extra.sh")); err != nil {
		t.Fatal(err)
	}

	canonical, err := canonicalManifestBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	signPub, signPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der2, err := x509.MarshalPKIXPublicKey(signPub)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "pub.pem")
	writeTestFile(t, keyPath, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der2})))
	m.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(signPriv, canonical))
	writeJSON(filepath.Join(dir, manifestName), m, 0o644)
	if _, err := VerifyArtifact(dir, "linux", "amd64", &signPub); err != nil {
		t.Fatalf("signed artifact refused: %v", err)
	}
	if _, err := VerifyArtifact(dir, "linux", "amd64", nil); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("signed artifact without key accepted: %v", err)
	}
}

func TestUpgradeRollbackPreserveData(t *testing.T) {
	stateDir := t.TempDir()
	dataDir := t.TempDir()
	world := filepath.Join(dataDir, "world", "level.dat")
	writeTestFile(t, world, "precious-world")

	a1 := t.TempDir()
	makeArtifact(t, a1, "1.0.0", "v1")
	st, err := Upgrade(stateDir, a1, "linux", "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveVersion != "1.0.0" || st.ActiveSlot == "" {
		t.Fatalf("bad active state: %+v", st)
	}
	st, err = Upgrade(stateDir, a1, "linux", "amd64", nil)
	if err != nil || st.ActiveVersion != "1.0.0" {
		t.Fatalf("idempotent upgrade failed: %v %+v", err, st)
	}

	a2 := t.TempDir()
	makeArtifact(t, a2, "2.0.0", "v2")
	st, err = Upgrade(stateDir, a2, "linux", "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveVersion != "2.0.0" {
		t.Fatalf("upgrade did not promote: %+v", st)
	}
	st, err = Rollback(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if st.ActiveVersion != "1.0.0" {
		t.Fatalf("rollback did not restore: %+v", st)
	}
	content, err := os.ReadFile(world)
	if err != nil || string(content) != "precious-world" {
		t.Fatalf("world data changed by upgrade/rollback: %q %v", content, err)
	}

	// Corrupt the undo slot: rollback must refuse and keep the active version.
	undoSlot := slotA
	if st.ActiveSlot == slotA {
		undoSlot = slotB
	}
	writeTestFile(t, filepath.Join(slotDir(stateDir, undoSlot), "bin", "carbon-node"), "corrupt")
	_, err = Rollback(stateDir)
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("corrupt undo: got %v, want ErrIntegrity", err)
	}
	st2, err := Status(stateDir)
	if err != nil || st2.ActiveVersion != "1.0.0" {
		t.Fatalf("active version changed after refused rollback: %v %+v", err, st2)
	}
}

func TestUpgradeRecoversInterruptedPromotion(t *testing.T) {
	stateDir := t.TempDir()
	a1 := t.TempDir()
	makeArtifact(t, a1, "1.0.0", "v1")
	st, err := Upgrade(stateDir, a1, "linux", "amd64", nil)
	if err != nil {
		t.Fatal(err)
	}
	target := slotA
	if st.ActiveSlot == slotA {
		target = slotB
	}
	// Simulate a crash with a fully staged slot but no state commit: the
	// journal says "staged" and the staged directory contains a verified v2.
	a2 := t.TempDir()
	makeArtifact(t, a2, "2.0.0", "v2")
	if err := copyDirTree(a2, stageDir(stateDir, target)); err != nil {
		t.Fatal(err)
	}
	writeJSON(filepath.Join(stateDir, journalFile), &upgradeJournal{TargetSlot: target, Version: "2.0.0", Phase: "staged"}, 0o644)
	recovered, err := Status(stateDir)
	if err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
	if recovered.ActiveVersion != "1.0.0" {
		t.Fatalf("uncommitted promotion was applied: %+v", recovered)
	}
	if _, err := os.Stat(slotDir(stateDir, target)); err != nil {
		t.Fatalf("staged slot was not completed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, journalFile)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("journal was not cleared after recovery")
	}
	// A valid retry can now promote the recovered slot.
	st2, err := Upgrade(stateDir, a2, "linux", "amd64", nil)
	if err != nil || st2.ActiveVersion != "2.0.0" {
		t.Fatalf("retry after recovery failed: %v %+v", err, st2)
	}
}

func TestVolumeBackupRecoverAndImport(t *testing.T) {
	stateDir := t.TempDir()
	dataDir := t.TempDir()
	writeTestFile(t, filepath.Join(dataDir, "world", "level.dat"), "world-bytes")
	writeTestFile(t, filepath.Join(dataDir, "carbon-panel.db"), "db-bytes")

	binding, err := ImportVolume(stateDir, dataDir)
	if err != nil || binding.FileCount != 2 || binding.VolumeID == "" {
		t.Fatalf("import failed: %v %+v", err, binding)
	}
	binding2, err := ImportVolume(stateDir, dataDir)
	if err != nil || binding2.VolumeID != binding.VolumeID {
		t.Fatalf("import is not idempotent: %v %+v", err, binding2)
	}

	backup := filepath.Join(t.TempDir(), "snapshot")
	m, err := BackupVolume(dataDir, backup)
	if err != nil || len(m.Files) != 2 {
		t.Fatalf("backup failed: %v %+v", err, m)
	}
	if _, err := BackupVolume(dataDir, backup); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("backup overwrite: got %v", err)
	}

	// Damage the live files, add an unrelated file, then recover.
	writeTestFile(t, filepath.Join(dataDir, "world", "level.dat"), "damaged")
	writeTestFile(t, filepath.Join(dataDir, "keep-me.txt"), "unrelated")
	if err := RecoverVolume(backup, dataDir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dataDir, "world", "level.dat"))
	if err != nil || string(got) != "world-bytes" {
		t.Fatalf("recovery did not restore world: %q %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "keep-me.txt")); err != nil {
		t.Fatal("recovery deleted an unrelated file")
	}

	// A tampered backup is refused before any file is touched.
	writeTestFile(t, filepath.Join(backup, "world", "level.dat"), "tampered")
	if err := RecoverVolume(backup, dataDir); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("tampered backup: got %v", err)
	}
	got, _ = os.ReadFile(filepath.Join(dataDir, "world", "level.dat"))
	if string(got) != "world-bytes" {
		t.Fatal("refused recovery still changed data")
	}
}

func TestPrivateKeyPEMHelper(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	b, err := privateKeyPEM(priv)
	if err != nil || !strings.Contains(string(b), "PRIVATE KEY") {
		t.Fatalf("privateKeyPEM helper failed: %v", err)
	}
}
