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
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Identity is the PUBLIC, secret-free first-boot identity of one guest. The
// private half of the key never appears in this struct or in identity.json.
type Identity struct {
	NodeID      string    `json:"node_id"`
	SPKI        []byte    `json:"approved_public_key_spki"`
	Fingerprint []byte    `json:"approved_key_fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32], nil
}

func spkiAndFingerprint(pub ed25519.PublicKey) ([]byte, []byte, error) {
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(spki)
	return spki, sum[:], nil
}

// InitIdentity creates the unique first-boot identity once and is idempotent:
// a second call with the same state directory returns the same node ID and
// key. Two different state directories always get different identities.
func InitIdentity(stateDir string) (*Identity, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	identityPath := filepath.Join(dir, identityFile)
	keyPath := filepath.Join(dir, nodeKeyFile)
	_, idErr := os.Stat(identityPath)
	_, keyErr := os.Stat(keyPath)
	switch {
	case idErr == nil && keyErr == nil:
		id, _, err := LoadIdentity(dir)
		return id, err
	case (idErr == nil) != (keyErr == nil):
		return nil, fmt.Errorf("%w: partial identity in %s", ErrIntegrity, dir)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	spki, fp, err := spkiAndFingerprint(pub)
	if err != nil {
		return nil, err
	}
	nodeID, err := newUUIDv4()
	if err != nil {
		return nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := writeFile(keyPath, keyPEM, 0o600); err != nil {
		return nil, err
	}
	id := &Identity{NodeID: nodeID, SPKI: spki, Fingerprint: fp, CreatedAt: time.Now().UTC()}
	if err := writeJSON(identityPath, id, 0o644); err != nil {
		return nil, err
	}
	return id, nil
}

// LoadIdentity returns the persisted public identity and the private key it
// is bound to. A mismatch between identity.json and node.key is integrity
// failure, never a silent key regeneration.
func LoadIdentity(stateDir string) (*Identity, ed25519.PrivateKey, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, nil, err
	}
	var id Identity
	if err := readJSON(filepath.Join(dir, identityFile), &id); err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, nodeKeyFile))
	if err != nil {
		return nil, nil, fmt.Errorf("%w: node key: %v", ErrNotFound, err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		return nil, nil, fmt.Errorf("%w: node key is not PKCS8 PEM", ErrIntegrity)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: node key: %v", ErrIntegrity, err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("%w: node key is not Ed25519", ErrIntegrity)
	}
	spki, fp, err := spkiAndFingerprint(priv.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, nil, err
	}
	if string(spki) != string(id.SPKI) || string(fp) != string(id.Fingerprint) {
		return nil, nil, fmt.Errorf("%w: identity and node key do not match", ErrIntegrity)
	}
	return &id, priv, nil
}

// privateKeyPEM is used only by the offline fixture helper that verifies a
// claim signature with the approved public key; production code never marshals
// the private key except when first writing node.key.
func privateKeyPEM(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), nil
}
