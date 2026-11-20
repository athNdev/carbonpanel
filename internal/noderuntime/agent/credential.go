// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, you can obtain one at https://mozilla.org/MPL/2.0/.

package agent

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"slices"
	"time"

	"github.com/athNdev/carbon-panel/internal/nodeinstall"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func validateCredential(credential *nodeinstall.ManagedCredential, controller controller, identity *nodeinstall.Identity, key ed25519.PrivateKey, now time.Time) (tls.Certificate, error) {
	var zero tls.Certificate
	if credential.ControllerURL != controller.endpoint || credential.DaemonID == "" {
		return zero, errors.New("managed credential does not match the configured controller or daemon identity")
	}
	claim := credential.Claim
	node := claim.GetIdentity()
	generation := claim.GetCredentialGeneration()
	if node.GetNodeId() != identity.NodeID || node.GetKeyFingerprint() != hex.EncodeToString(identity.Fingerprint) ||
		node.GetTransport() != v1.NodeTransport_NODE_TRANSPORT_MANAGED_AGENT || node.GetEndpointGeneration() <= 0 ||
		generation <= 0 || node.GetCredentialGeneration() != generation ||
		(node.GetDaemonId() != "" && node.GetDaemonId() != credential.DaemonID) {
		return zero, errors.New("managed credential does not match the enrolled node identity")
	}
	claims := claim.GetClaims()
	if claims.GetNodeId() != identity.NodeID || claims.GetObjectId() != identity.NodeID || claims.GetCredentialGeneration() != generation ||
		claims.GetExpiresAt().CheckValid() != nil || !claims.GetExpiresAt().AsTime().After(now) ||
		!slices.Contains(claims.GetScopes(), "node:heartbeat") || !slices.Contains(claims.GetScopes(), "node:report") {
		return zero, errors.New("managed credential has expired or lacks node reporting scopes")
	}
	for _, scope := range claims.GetScopes() {
		if scope != "node:heartbeat" && scope != "node:report" && scope != "node:ack" {
			return zero, errors.New("managed credential has an unexpected scope")
		}
	}
	caDigest := sha256.Sum256(controller.issuingCA.Raw)
	certDER := claim.GetEnrolledClientCertificateDer()
	certDigest := sha256.Sum256(certDER)
	if !bytes.Equal(caDigest[:], claim.GetIssuingCaDigest()) || !bytes.Equal(certDigest[:], claim.GetCertificateFingerprint()) {
		return zero, errors.New("enrollment issuer or certificate fingerprint does not match")
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil || cert.IsCA || cert.Subject.CommonName != identity.NodeID ||
		!bytes.Equal(cert.RawSubjectPublicKeyInfo, identity.SPKI) ||
		claim.GetCertificateNotAfter().CheckValid() != nil || !cert.NotAfter.Equal(claim.GetCertificateNotAfter().AsTime().Truncate(time.Second)) {
		return zero, errors.New("enrolled certificate does not match the local node key or identity")
	}
	roots := x509.NewCertPool()
	roots.AddCert(controller.issuingCA)
	_, err = cert.Verify(x509.VerifyOptions{Roots: roots, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
	if err != nil {
		return zero, errors.New("enrolled certificate failed issuer or lifetime verification")
	}
	return tls.Certificate{Certificate: [][]byte{certDER}, PrivateKey: key, Leaf: cert}, nil
}
