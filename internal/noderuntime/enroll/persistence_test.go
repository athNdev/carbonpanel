// SPDX-License-Identifier: MPL-2.0

package enroll

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type faultingStateStore struct {
	StateStore
	failCommit bool
}

func (s *faultingStateStore) Commit(ctx context.Context, rev int64, snapshot []byte) (int64, error) {
	if s.failCommit {
		return 0, errors.New("injected state commit failure")
	}
	return s.StateStore.Commit(ctx, rev, snapshot)
}

func enrollmentTestCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "enrollment test CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(2 * time.Hour),
		BasicConstraintsValid: true,
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func enrollmentTestTLS(t *testing.T) *tls.ConnectionState {
	t.Helper()
	state := make(chan tls.ConnectionState, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state <- *r.TLS
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	ctx := fixtureContext(t)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	select {
	case connection := <-state:
		return &connection
	case <-ctx.Done():
		t.Fatal(ctx.Err())
		return nil
	}
}

func TestProductionAuthorityCommitAndRestart(t *testing.T) {
	ctx := t.Context()
	cfg := &config.Config{}
	cfg.Database.Path = filepath.Join(t.TempDir(), "carbon-panel.db")
	cfg.Database.AutoMigrate = true
	ca, key := enrollmentTestCA(t)
	store := &faultingStateStore{}
	var controller *db.Store
	t.Cleanup(func() {
		if controller != nil {
			if err := controller.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	restart := func() *Boundary {
		t.Helper()
		if controller != nil {
			if err := controller.Close(); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		controller, err = db.NewSQLiteStore(cfg)
		if err != nil {
			t.Fatal(err)
		}
		store.StateStore = controller.NodeEnrollmentStateStore()
		authority, err := NewProductionAuthority(ctx, AuthorityConfig{Topology: TopologyDirectTLS, SigningCertificate: ca, SigningKey: key, Store: store})
		if err != nil {
			t.Fatal(err)
		}
		return &Boundary{Authority: authority}
	}

	boundary := restart()
	operator := Credential{Kind: CredentialOperator}
	bootstrap := Credential{Kind: CredentialBootstrap}
	nodeID := "durable-node"
	bundle, req, err := GenerateNodeBundle(t.TempDir(), nodeID, "Durable test node")
	if err != nil {
		t.Fatal(err)
	}
	store.failCommit = true
	if res, err := boundary.CreateEnrollment(ctx, operator, req); res != nil || !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("failed create commit returned response or wrong error: %v", err)
	}
	revision, snapshot, err := store.Load(ctx)
	if err != nil || revision != 0 || len(snapshot) != 0 {
		t.Fatalf("failed create persisted state: revision=%d err=%v", revision, err)
	}

	store.failCommit = false
	created, err := boundary.CreateEnrollment(ctx, operator, req)
	if err != nil {
		t.Fatal(err)
	}
	id := created.GetEnrollment().GetEnrollmentId()
	if expires := created.GetEnrollment().GetExpiresAt().AsTime(); expires.Before(time.Now()) {
		t.Fatal("expired enrollment from the production clock")
	}

	boundary = restart()
	if _, err := boundary.BeginEnrollment(ctx, bootstrap, id); !errors.Is(err, ErrTopology) {
		t.Fatalf("production accepted fixture-only bootstrap path: %v", err)
	}
	connection := enrollmentTestTLS(t)
	store.failCommit = true
	if res, err := boundary.BeginTLSEnrollment(ctx, bootstrap, id, connection); res != nil || !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("failed challenge commit returned response or wrong error: %v", err)
	}
	store.failCommit = false
	var challenge *v1.BeginEnrollmentChallengeResponse
	for range 3 {
		challenge, err = boundary.BeginTLSEnrollment(ctx, bootstrap, id, connection)
		if err != nil {
			t.Fatal(err)
		}
	}
	boundary = restart()
	if _, err := boundary.BeginTLSEnrollment(ctx, bootstrap, id, connection); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("restart reset the challenge limit: %v", err)
	}
	claim, err := bundle.BuildClaim(id, challenge, req.GetRequiredCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	store.failCommit = true
	if res, err := boundary.ClaimTLSEnrollment(ctx, bootstrap, claim, connection); res != nil || !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("failed claim commit returned response or wrong error: %v", err)
	}
	if view, err := boundary.Authority.EnrollmentView(id); err != nil ||
		view.GetState() != v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_PENDING {
		t.Fatalf("failed claim consumed enrollment: %v", err)
	}

	store.failCommit = false
	claimed, err := boundary.ClaimTLSEnrollment(ctx, bootstrap, claim, connection)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(claimed.GetEnrolledClientCertificateDer())
	if err != nil {
		t.Fatal(err)
	}
	if cert.CheckSignatureFrom(ca) != nil || cert.SerialNumber.String() != claimed.GetCertificateSerial() ||
		cert.NotAfter.After(ca.NotAfter) || !cert.NotAfter.Equal(claimed.GetCertificateNotAfter().AsTime()) {
		t.Fatal("certificate does not match durable issuer, serial and expiry")
	}
	revision, snapshot, err = store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte(claim.GetEnrollmentToken()),
		[]byte(base64.StdEncoding.EncodeToString(key)), []byte(base64.StdEncoding.EncodeToString(cert.Raw))} {
		if bytes.Contains(snapshot, secret) {
			t.Fatal("durable snapshot retained an enrollment secret, signer or full credential")
		}
	}
	if _, err := store.Commit(ctx, revision-1, snapshot); err == nil {
		t.Fatal("SQLite accepted a stale revision")
	}
	boundary = restart()
	if res, err := boundary.ClaimTLSEnrollment(ctx, bootstrap, claim, connection); res != nil || err == nil {
		t.Fatal("restart allowed the same enrollment to be consumed again")
	}
	node, err := boundary.Authority.NodeCredentialForCertificate(cert)
	if err != nil || node.NodeID != nodeID || node.CredentialGeneration != 1 || node.ObjectID != nodeID ||
		boundary.AuthorizeNodeOperation(node, nodeID, "node:heartbeat") != nil {
		t.Fatalf("restart lost the enrolled node grant: %v", err)
	}
	boundary.Authority.WithClock(func() time.Time { return cert.NotAfter })
	if _, err := boundary.Authority.NodeCredentialForCertificate(cert); !errors.Is(err, ErrRevoked) {
		t.Fatalf("expired certificate accepted: %v", err)
	}
	boundary.Authority.WithClock(time.Now)

	copyCert := *cert
	copyCert.NotAfter = copyCert.NotAfter.Add(-time.Second)
	otherDER, err := x509.CreateCertificate(rand.Reader, &copyCert, ca, cert.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	otherCert, err := x509.ParseCertificate(otherDER)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.Authority.NodeCredentialForCertificate(otherCert); !errors.Is(err, ErrScope) {
		t.Fatalf("serial-matched cert with different DER accepted: %v", err)
	}

	other, err := boundary.CreateEnrollment(ctx, operator, req)
	if err != nil {
		t.Fatal(err)
	}
	revoke := &v1.RevokeCredentialRequest{NodeId: nodeID, ConfirmationPhrase: "REVOKE " + nodeID}
	store.failCommit = true
	if res, err := boundary.RevokeCredential(ctx, operator, revoke); res != nil || !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("failed revoke commit returned response or wrong error: %v", err)
	}
	if _, err := boundary.Authority.NodeCredentialForCertificate(cert); err != nil {
		t.Fatalf("failed revoke deactivated the prior grant: %v", err)
	}
	store.failCommit = false
	if _, err := boundary.RevokeCredential(ctx, operator, revoke); err != nil {
		t.Fatal(err)
	}
	boundary = restart()
	if _, err := boundary.Authority.NodeCredentialForCertificate(cert); !errors.Is(err, ErrRevoked) {
		t.Fatalf("restart restored a revoked certificate: %v", err)
	}
	if view, err := boundary.Authority.EnrollmentView(other.GetEnrollment().GetEnrollmentId()); err != nil ||
		view.GetState() != v1.NodeEnrollmentState_NODE_ENROLLMENT_STATE_REVOKED {
		t.Fatalf("revoke missed a second enrollment for the same node: %v", err)
	}
	recreated, err := boundary.CreateEnrollment(ctx, operator, req)
	if err != nil {
		t.Fatal(err)
	}
	newID := recreated.GetEnrollment().GetEnrollmentId()
	newChallenge, err := boundary.BeginTLSEnrollment(ctx, bootstrap, newID, connection)
	if err != nil {
		t.Fatal(err)
	}
	newClaim, err := bundle.BuildClaim(newID, newChallenge, req.GetRequiredCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	reissued, err := boundary.ClaimTLSEnrollment(ctx, bootstrap, newClaim, connection)
	if err != nil || reissued.GetCertificateSerial() == claimed.GetCertificateSerial() || reissued.GetCredentialGeneration() <= 1 {
		t.Fatalf("re-enrollment reused a serial or generation: %v", err)
	}

	replacementCA, replacementKey := enrollmentTestCA(t)
	if _, err := NewProductionAuthority(ctx, AuthorityConfig{
		Topology: TopologyDirectTLS, SigningCertificate: replacementCA, SigningKey: replacementKey, Store: store,
	}); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("restart silently substituted the enrollment CA: %v", err)
	}
}
