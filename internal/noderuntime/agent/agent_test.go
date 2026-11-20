// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, you can obtain one at https://mozilla.org/MPL/2.0/.

package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/nodeinstall"
	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	"github.com/athNdev/carbon-panel/internal/noderuntime/native"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type tlsStateKey struct{}

type testController struct {
	carbonpanelv1connect.UnimplementedNodeControlServiceHandler
	bundle       *nodeinstall.EnrollmentBundle
	issuer       *x509.Certificate
	issuerKey    ed25519.PrivateKey
	health       chan *v1.NodeHealthReport
	claims       atomic.Int32
	challenges   atomic.Int32
	wrongChannel bool
}

func newTestController(t *testing.T, bundle *nodeinstall.EnrollmentBundle) *testController {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "enrollment fixture CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testController{bundle: bundle, issuer: issuer, issuerKey: key, health: make(chan *v1.NodeHealthReport, 12)}
}

func (c *testController) serve(t *testing.T, closeChallenge bool) *httptest.Server {
	t.Helper()
	_, handler := carbonpanelv1connect.NewNodeControlServiceHandler(c)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if closeChallenge && r.URL.Path == carbonpanelv1connect.NodeControlServiceBeginEnrollmentChallengeProcedure {
			w.Header().Set("Connection", "close")
		}
		handler.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tlsStateKey{}, r.TLS)))
	}))
	roots := x509.NewCertPool()
	roots.AddCert(c.issuer)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: roots}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

var challengeNonce = bytes.Repeat([]byte{0x7c}, 32)

func (c *testController) BeginEnrollmentChallenge(ctx context.Context, r *connect.Request[v1.BeginEnrollmentChallengeRequest]) (*connect.Response[v1.BeginEnrollmentChallengeResponse], error) {
	c.challenges.Add(1)
	state, _ := ctx.Value(tlsStateKey{}).(*tls.ConnectionState)
	channel, err := enroll.TLSChannelBinding(state)
	if err != nil || len(state.PeerCertificates) != 0 || r.Msg.GetEnrollmentId() != "approved-enrollment" {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("non-bootstrap call"))
	}
	if c.wrongChannel {
		channel[0] ^= 1
	}
	return connect.NewResponse(&v1.BeginEnrollmentChallengeResponse{
		ChallengeId: "bound-challenge", NodeId: c.bundle.NodeID, EnrollmentId: r.Msg.GetEnrollmentId(),
		ServerNonce: challengeNonce, ExpiresAt: timestamppb.New(c.bundleTime()), ChannelContext: channel,
	}), nil
}

func (c *testController) bundleTime() time.Time {
	return c.issuer.NotAfter.Add(-time.Minute)
}

func (c *testController) ClaimEnrollment(ctx context.Context, r *connect.Request[v1.ClaimEnrollmentRequest]) (*connect.Response[v1.ClaimEnrollmentResponse], error) {
	c.claims.Add(1)
	state, _ := ctx.Value(tlsStateKey{}).(*tls.ConnectionState)
	channel, err := enroll.TLSChannelBinding(state)
	if err != nil || len(state.PeerCertificates) != 0 {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("non-bootstrap call"))
	}
	pubKey, err := x509.ParsePKIXPublicKey(c.bundle.ApprovedPublicKeySPKI)
	if err != nil {
		return nil, err
	}
	claim := r.Msg
	digest := sha256.Sum256(append(bytes.Clone(c.bundle.EnrollmentSalt), claim.GetEnrollmentToken()...))
	signed := enroll.ClaimContext(c.bundle.NodeID, "approved-enrollment", "bound-challenge",
		challengeNonce, c.bundleTime(), claim.GetCapabilities().GetCapabilities(), channel)
	if claim.GetEnrollmentId() != "approved-enrollment" || claim.GetChallengeId() != "bound-challenge" ||
		!bytes.Equal(channel, claim.GetChannelBindingEvidence()) || !bytes.Equal(digest[:], c.bundle.EnrollmentSecretDigest) ||
		!ed25519.Verify(pubKey.(ed25519.PublicKey), signed, claim.GetFreshClaimSignature()) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid enrollment proof"))
	}
	now := time.Now().Truncate(time.Second)
	expires := now.Add(time.Minute)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: c.bundle.NodeID},
		NotBefore: now.Add(-time.Minute), NotAfter: expires, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, c.issuer, pubKey, c.issuerKey)
	if err != nil {
		return nil, err
	}
	fp := sha256.Sum256(der)
	caDigest := sha256.Sum256(c.issuer.Raw)
	return connect.NewResponse(&v1.ClaimEnrollmentResponse{
		Identity: &v1.NodeIdentity{
			NodeId: c.bundle.NodeID, Transport: v1.NodeTransport_NODE_TRANSPORT_MANAGED_AGENT,
			KeyFingerprint: hex.EncodeToString(c.bundle.ApprovedKeyFingerprint), CredentialGeneration: 1, EndpointGeneration: 3,
		},
		Claims: &v1.NodeAuthorizationClaims{
			NodeId: c.bundle.NodeID, ObjectId: c.bundle.NodeID, CredentialGeneration: 1,
			Scopes: []string{"node:heartbeat", "node:report", "node:ack"}, ExpiresAt: timestamppb.New(expires),
		},
		CredentialGeneration: 1, EnrolledClientCertificateDer: der, CertificateFingerprint: fp[:],
		CertificateSerial: "2", IssuingCaDigest: caDigest[:], CertificateNotAfter: timestamppb.New(expires),
	}), nil
}

func (c *testController) authenticate(ctx context.Context, nodeID string) error {
	state, _ := ctx.Value(tlsStateKey{}).(*tls.ConnectionState)
	if state == nil || len(state.VerifiedChains) != 1 || state.PeerCertificates[0].Subject.CommonName != c.bundle.NodeID || nodeID != c.bundle.NodeID {
		return connect.NewError(connect.CodePermissionDenied, errors.New("missing authenticated node"))
	}
	return nil
}

func (c *testController) NegotiateVersion(ctx context.Context, r *connect.Request[v1.NegotiateVersionRequest]) (*connect.Response[v1.NegotiateVersionResponse], error) {
	if err := c.authenticate(ctx, r.Msg.GetNodeId()); err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.NegotiateVersionResponse{Supported: true, SelectedVersion: native.ProtocolVersion}), nil
}

func (c *testController) OpenControlSession(ctx context.Context, r *connect.Request[v1.OpenControlSessionRequest]) (*connect.Response[v1.OpenControlSessionResponse], error) {
	if err := c.authenticate(ctx, r.Msg.GetIdentity().GetNodeId()); err != nil {
		return nil, err
	}
	if r.Msg.GetIdentity().GetDaemonId() != "private-engine" {
		return nil, errors.New("session requests an unexpected daemon")
	}
	return connect.NewResponse(&v1.OpenControlSessionResponse{Session: &v1.ControlSession{
		SessionId: "test-session", NodeId: c.bundle.NodeID, Deadline: timestamppb.New(time.Now().Add(time.Minute)),
		MaxInFlight: 1, MaxMessageBytes: maxMessageSize, HeartbeatInterval: durationpb.New(100 * time.Millisecond),
	}}), nil
}

func (c *testController) acknowledge(ctx context.Context, nodeID string, envelope *v1.OperationEnvelope, report proto.Message) (*v1.OperationResult, error) {
	if err := c.authenticate(ctx, nodeID); err != nil {
		return nil, err
	}
	digest, err := enroll.ReportHash(report)
	if err != nil || !bytes.Equal(digest, envelope.GetRequestHash()) || envelope.GetOperationId() == "" ||
		envelope.GetNodeId() != nodeID || envelope.GetEndpointGeneration() != 3 || envelope.GetCredentialGeneration() != 1 {
		return nil, errors.New("invalid report envelope")
	}
	return &v1.OperationResult{OperationId: envelope.GetOperationId(), Phase: v1.NodeOperationPhase_NODE_OPERATION_PHASE_SUCCEEDED}, nil
}

func (c *testController) ReportPreflight(ctx context.Context, r *connect.Request[v1.ReportPreflightRequest]) (*connect.Response[v1.ReportPreflightResponse], error) {
	result, err := c.acknowledge(ctx, r.Msg.GetReport().GetNodeId(), r.Msg.GetEnvelope(), r.Msg.GetReport())
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.ReportPreflightResponse{Result: result}), nil
}

func (c *testController) ReportHealth(ctx context.Context, r *connect.Request[v1.ReportHealthRequest]) (*connect.Response[v1.ReportHealthResponse], error) {
	result, err := c.acknowledge(ctx, r.Msg.GetReport().GetNodeId(), r.Msg.GetEnvelope(), r.Msg.GetReport())
	if err != nil {
		return nil, err
	}
	select {
	case c.health <- r.Msg.GetReport():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return connect.NewResponse(&v1.ReportHealthResponse{Result: result}), nil
}

func TestAgentLiveEnrollmentAndPinnedHeartbeat(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("requires a Linux Unix socket")
	}
	state := t.TempDir()
	bundle, err := nodeinstall.BuildEnrollmentBundle(state, "test node", []string{"PRIVATE_SOCKET", "CGROUP_V2"}, native.ProtocolVersion, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	controller := newTestController(t, bundle)
	server := controller.serve(t, false)
	runDir := t.TempDir()
	socket := filepath.Join(runDir, "d.sock")
	hostSocket := filepath.Join(runDir, "host.sock")
	data := filepath.Join(runDir, "docker-data")
	if err := os.Mkdir(data, 0o700); err != nil {
		t.Fatal(err)
	}
	var wrongDaemon atomic.Bool
	serveSocket(t, socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/_ping") {
			w.Header().Set("API-Version", "1.53")
			_, _ = w.Write([]byte("OK"))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/info") {
			id := "private-engine"
			if wrongDaemon.Load() {
				id = "replacement-engine"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ID": id, "ServerVersion": "29.3.0", "DockerRootDir": data,
				"CgroupVersion": "2", "MemoryLimit": true, "PidsLimit": true,
			})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	var hostRequests atomic.Int32
	serveSocket(t, hostSocket, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hostRequests.Add(1)
	}))
	t.Setenv("DOCKER_HOST", "unix://"+hostSocket)
	config := Config{
		StateDir: state, ControllerURL: server.URL, ControllerCAs: server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs,
		EnrollmentCA: controller.issuer, EnrollmentID: "approved-enrollment",
		Facts: native.HostFacts{
			OS: "linux", Systemd: true, RootfulServiceQualified: true, XDG_RUNTIME_DIR: runDir,
			ChosenSocket: socket, HostSocketPath: hostSocket, ChosenDataRoot: data, ChosenExecRoot: filepath.Join(runDir, "exec"),
			StorageDriver: "vfs", ContainerdNamespace: "carbon-test", BridgeCIDR: "10.231.0.1/24", ChosenPortRange: "30550-30600",
			CgroupVersion: "v2", DelegatedControllers: []string{"cpu", "cpuset", "io", "memory", "pids"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- Run(ctx, config) }()
	awaitHealth(t, ctx, controller.health, result, true)
	wrongDaemon.Store(true)
	awaitHealth(t, ctx, controller.health, result, false)
	cancel()
	if err := <-result; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if hostRequests.Load() != 0 || controller.challenges.Load() != 1 || controller.claims.Load() != 1 {
		t.Fatal("agent retried enrollment or contacted the shared socket")
	}
	if _, err := os.Stat(filepath.Join(state, "claim_request.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("live claim unexpectedly wrote an offline request: %v", err)
	}
	credential, err := nodeinstall.LoadManagedCredential(state)
	if err != nil || credential.DaemonID != "private-engine" {
		t.Fatalf("persisted daemon pin missing: %v", err)
	}
	config.EnrollmentID = ""
	wrongDaemon.Store(false)
	resume, stop := context.WithTimeout(context.Background(), 15*time.Second)
	defer stop()
	go func() { result <- Run(resume, config) }()
	awaitHealth(t, resume, controller.health, result, true)
	stop()
	<-result
	if controller.challenges.Load() != 1 || controller.claims.Load() != 1 {
		t.Fatal("agent did not reuse the enrolled certificate")
	}
}

func awaitHealth(t *testing.T, ctx context.Context, health <-chan *v1.NodeHealthReport, result <-chan error, reachable bool) {
	t.Helper()
	for {
		select {
		case h := <-health:
			if h.GetDaemonId() != "private-engine" {
				t.Fatal("agent changed the enrolled daemon identity")
			}
			if h.GetReachable() == reachable {
				return
			}
		case err := <-result:
			t.Fatalf("agent stopped before reporting reachability %v: %v", reachable, err)
		case <-ctx.Done():
			t.Fatalf("no health report with reachability %v: %v", reachable, ctx.Err())
		}
	}
}

func serveSocket(t *testing.T, path string, handler http.Handler) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func TestBootstrapRefusesWrongChannelAndReplacementConnection(t *testing.T) {
	for _, wrongBinding := range []bool{true, false} {
		name := "closed connection"
		if wrongBinding {
			name = "wrong binding"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			bundle, err := nodeinstall.BuildEnrollmentBundle(dir, "test", []string{"PRIVATE_SOCKET"}, native.ProtocolVersion, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			testServer := newTestController(t, bundle)
			testServer.wrongChannel = wrongBinding
			server := testServer.serve(t, !wrongBinding)
			remote, err := newController(server.URL, server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs, testServer.issuer)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err = remote.enroll(ctx, dir, bundle.NodeID, "approved-enrollment", &v1.NodeCapabilities{
				ProtocolVersion: native.ProtocolVersion, Capabilities: []v1.NodeCapability{v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET},
			})
			if err == nil || testServer.challenges.Load() != 1 || testServer.claims.Load() != 0 {
				t.Fatalf("expected one challenge and no claim, got %v", err)
			}
		})
	}
}
