//go:build linux

// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/nodeinstall"
	"github.com/athNdev/carbon-panel/internal/noderuntime/agent"
	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	"github.com/athNdev/carbon-panel/internal/noderuntime/native"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	conn "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// This file is the OSS-377 controller/agent acceptance fixture. It is NOT a VM
// qualification: it runs the REAL managed node agent (internal/noderuntime/agent)
// against the REAL shared ADR 0019 enrollment authority (internal/noderuntime/
// enroll) over loopback TLS, with a fixture controller that maps the issued mTLS
// certificate back to the durable enrollment grant. All state, keys, sockets and
// Docker responses are test-owned; no host daemon, live controller, VM or user
// service is touched.
//
// Production deliberately does not mount NodeControlService outside the
// dedicated enrollment listener, so the post-enrollment control surface here is
// a controller fixture. It proves protocol/authorization compatibility only; it
// is not evidence that a production controller endpoint exists.

const (
	managedFixtureDaemonID = "fixture-private-engine"
	managedFixtureEndpoint = 1
)

type managedFixtureStateStore struct {
	mu       sync.Mutex
	revision int64
	snapshot []byte
}

func (s *managedFixtureStateStore) Load(context.Context) (int64, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.revision, bytes.Clone(s.snapshot), nil
}

func (s *managedFixtureStateStore) Commit(_ context.Context, expectedRevision int64, snapshot []byte) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expectedRevision != s.revision {
		return 0, errors.New("stale enrollment state revision")
	}
	s.revision++
	s.snapshot = bytes.Clone(snapshot)
	return s.revision, nil
}

type managedFixtureService struct {
	*nodeEnrollmentService
	authority   *enroll.EnrollmentAuthority
	boundary    *enroll.Boundary
	daemonID    string
	endpointGen int64
	challenges  atomic.Int32
	claims      atomic.Int32
	health      chan *v1.NodeHealthReport
	preflights  chan *v1.NodePreflightReport
	sessions    chan *v1.ControlSession
}

type managedFixtureRun struct {
	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	startedAt time.Time
	err       error
}

func startManagedFixtureRun(t *testing.T, config agent.Config) *managedFixtureRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	run := &managedFixtureRun{ctx: ctx, cancel: cancel, done: make(chan struct{}), startedAt: time.Now()}
	go func() {
		run.err = agent.Run(ctx, config)
		close(run.done)
	}()
	t.Cleanup(func() { run.stop(t) })
	return run
}

func (r *managedFixtureRun) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Error("managed agent did not stop after cancellation")
		return
	}
	if r.err != nil && !errors.Is(r.err, context.Canceled) && !errors.Is(r.err, context.DeadlineExceeded) {
		t.Errorf("managed agent stopped with %v", r.err)
	}
}

func newManagedFixtureCA(t *testing.T) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "OSS-377 fixture enrollment CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(48 * time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return ca, key
}

func (s *managedFixtureService) BeginEnrollmentChallenge(ctx context.Context, req *connect.Request[v1.BeginEnrollmentChallengeRequest]) (*connect.Response[v1.BeginEnrollmentChallengeResponse], error) {
	s.challenges.Add(1)
	return s.nodeEnrollmentService.BeginEnrollmentChallenge(ctx, req)
}

func (s *managedFixtureService) ClaimEnrollment(ctx context.Context, req *connect.Request[v1.ClaimEnrollmentRequest]) (*connect.Response[v1.ClaimEnrollmentResponse], error) {
	s.claims.Add(1)
	reply, err := s.nodeEnrollmentService.ClaimEnrollment(ctx, req)
	if err != nil {
		return nil, err
	}
	if reply == nil || reply.Msg.GetIdentity() == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("enrollment returned no node identity"))
	}
	// Endpoint generation belongs to the controller's node record, not to the
	// enrollment authority. The fixture controller supplies it exactly as a
	// durable controller would after creating the node.
	reply.Msg.Identity.EndpointGeneration = s.endpointGen
	return reply, nil
}

func (s *managedFixtureService) nodeCredential(ctx context.Context) (enroll.Credential, error) {
	state, _ := ctx.Value(nodeEnrollmentTLSKey{}).(*tls.ConnectionState)
	if state == nil || len(state.PeerCertificates) == 0 || len(state.VerifiedChains) == 0 {
		return enroll.Credential{}, connect.NewError(connect.CodePermissionDenied, errors.New("a verified node client certificate is required"))
	}
	credential, err := s.authority.NodeCredentialForCertificateContext(ctx, state.PeerCertificates[0])
	if err != nil {
		return enroll.Credential{}, connect.NewError(connect.CodePermissionDenied, errors.New("certificate has no active enrollment grant"))
	}
	return credential, nil
}

func (s *managedFixtureService) authorizeNode(ctx context.Context, objectID, scope string) (enroll.Credential, error) {
	credential, err := s.nodeCredential(ctx)
	if err != nil {
		return credential, err
	}
	if objectID == "" || objectID != credential.NodeID {
		return credential, connect.NewError(connect.CodePermissionDenied, errors.New("cross-node object refused"))
	}
	if err := s.boundary.AuthorizeNodeOperation(credential, objectID, scope); err != nil {
		return credential, connect.NewError(connect.CodePermissionDenied, errors.New("node scope refused"))
	}
	return credential, nil
}

func (s *managedFixtureService) acknowledge(credential enroll.Credential, envelope *v1.OperationEnvelope, report proto.Message) (*v1.OperationResult, error) {
	if envelope == nil || envelope.GetOperationId() == "" ||
		envelope.GetNodeId() != credential.NodeID ||
		envelope.GetEndpointGeneration() != s.endpointGen ||
		envelope.GetCredentialGeneration() != credential.CredentialGeneration ||
		envelope.GetDeadline().CheckValid() != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("report envelope does not match the enrolled node"))
	}
	hash, err := enroll.ReportHash(report)
	if err != nil || !bytes.Equal(hash, envelope.GetRequestHash()) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("report hash mismatch"))
	}
	return &v1.OperationResult{
		OperationId: envelope.GetOperationId(),
		Phase:       v1.NodeOperationPhase_NODE_OPERATION_PHASE_SUCCEEDED,
	}, nil
}

func (s *managedFixtureService) NegotiateVersion(ctx context.Context, req *connect.Request[v1.NegotiateVersionRequest]) (*connect.Response[v1.NegotiateVersionResponse], error) {
	if _, err := s.authorizeNode(ctx, req.Msg.GetNodeId(), "node:report"); err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.NegotiateVersionResponse{
		Supported: true, SelectedVersion: native.ProtocolVersion,
	}), nil
}

func (s *managedFixtureService) OpenControlSession(ctx context.Context, req *connect.Request[v1.OpenControlSessionRequest]) (*connect.Response[v1.OpenControlSessionResponse], error) {
	credential, err := s.authorizeNode(ctx, req.Msg.GetIdentity().GetNodeId(), "node:report")
	if err != nil {
		return nil, err
	}
	if req.Msg.GetIdentity().GetDaemonId() != s.daemonID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("session requests a different daemon"))
	}
	session := &v1.ControlSession{
		SessionId: "oss377-fixture-session", NodeId: credential.NodeID,
		Deadline:    timestamppb.New(time.Now().Add(8 * time.Second)),
		MaxInFlight: 1, MaxMessageBytes: 128 << 10,
		HeartbeatInterval: durationpb.New(200 * time.Millisecond),
	}
	select {
	case s.sessions <- session:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return connect.NewResponse(&v1.OpenControlSessionResponse{Session: session}), nil
}

func (s *managedFixtureService) ReportPreflight(ctx context.Context, req *connect.Request[v1.ReportPreflightRequest]) (*connect.Response[v1.ReportPreflightResponse], error) {
	credential, err := s.authorizeNode(ctx, req.Msg.GetReport().GetNodeId(), "node:report")
	if err != nil {
		return nil, err
	}
	result, err := s.acknowledge(credential, req.Msg.GetEnvelope(), req.Msg.GetReport())
	if err != nil {
		return nil, err
	}
	select {
	case s.preflights <- req.Msg.GetReport():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return connect.NewResponse(&v1.ReportPreflightResponse{Result: result}), nil
}

func (s *managedFixtureService) ReportHealth(ctx context.Context, req *connect.Request[v1.ReportHealthRequest]) (*connect.Response[v1.ReportHealthResponse], error) {
	credential, err := s.authorizeNode(ctx, req.Msg.GetReport().GetNodeId(), "node:heartbeat")
	if err != nil {
		return nil, err
	}
	result, err := s.acknowledge(credential, req.Msg.GetEnvelope(), req.Msg.GetReport())
	if err != nil {
		return nil, err
	}
	select {
	case s.health <- req.Msg.GetReport():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return connect.NewResponse(&v1.ReportHealthResponse{Result: result}), nil
}

func serveManagedFixtureSocket(t *testing.T, path string, handler http.Handler) {
	t.Helper()
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
}

func managedFixtureHTTPClient(t *testing.T, server *httptest.Server, certificate *tls.Certificate) *http.Client {
	t.Helper()
	roots := server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	tlsConfig := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	if certificate != nil {
		tlsConfig.Certificates = []tls.Certificate{*certificate}
	}
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig, ForceAttemptHTTP2: true,
			MaxConnsPerHost: 1, MaxIdleConnsPerHost: 1,
		},
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func awaitManagedFixtureHealth(t *testing.T, run *managedFixtureRun, health <-chan *v1.NodeHealthReport) *v1.NodeHealthReport {
	t.Helper()
	for {
		select {
		case report := <-health:
			if report.GetObservedAt().AsTime().Before(run.startedAt) {
				continue
			}
			if report.GetDaemonId() != managedFixtureDaemonID {
				t.Fatalf("agent reported an unexpected daemon %q", report.GetDaemonId())
			}
			if report.GetReachable() {
				return report
			}
		case <-run.done:
			t.Fatalf("agent stopped before reporting healthy: %v", run.err)
		case <-run.ctx.Done():
			t.Fatalf("no healthy report before the fixture deadline: %v", run.ctx.Err())
		}
	}
}

func awaitManagedFixturePreflight(t *testing.T, run *managedFixtureRun, reports <-chan *v1.NodePreflightReport) *v1.NodePreflightReport {
	t.Helper()
	for {
		select {
		case report := <-reports:
			if !report.GetObservedAt().AsTime().Before(run.startedAt) {
				return report
			}
		case <-run.done:
			t.Fatalf("agent stopped before reporting preflight: %v", run.err)
		case <-run.ctx.Done():
			t.Fatalf("no preflight report before the fixture deadline: %v", run.ctx.Err())
		}
	}
}

func assertManagedFixtureDenial(t *testing.T, err error, reason string) {
	t.Helper()
	var rejection *connect.Error
	if !errors.As(err, &rejection) || rejection.Code() != connect.CodePermissionDenied || rejection.Message() != reason {
		t.Fatalf("control call returned %v, want permission denied for %q", err, reason)
	}
}

func hasManagedCapability(capabilities *v1.NodeCapabilities, want v1.NodeCapability) bool {
	for _, capability := range capabilities.GetCapabilities() {
		if capability == want {
			return true
		}
	}
	return false
}

// TestOSS377ManagedControllerAgentFixture is the reproducible acceptance run
// for challenge, TLS-bound claim, scoped mTLS, heartbeat and daemon health. It
// is Linux-only because the real agent requires a Linux private Unix socket.
func TestOSS377ManagedControllerAgentFixture(t *testing.T) {
	ctx := context.Background()
	ca, caKey := newManagedFixtureCA(t)
	authority, err := enroll.NewProductionAuthority(ctx, enroll.AuthorityConfig{
		Topology: enroll.TopologyDirectTLS, SigningCertificate: ca, SigningKey: caKey,
		Store: &managedFixtureStateStore{},
	})
	if err != nil {
		t.Fatal(err)
	}
	boundary := &enroll.Boundary{Authority: authority}

	stateDir := t.TempDir()
	bundle, err := nodeinstall.BuildEnrollmentBundle(stateDir, "oss377-managed-fixture", []string{"PRIVATE_SOCKET", "CGROUP_V2"}, native.ProtocolVersion, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	rawCreate, err := os.ReadFile(filepath.Join(stateDir, "create_enrollment_request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var create v1.CreateEnrollmentRequest
	if err := protojson.Unmarshal(rawCreate, &create); err != nil {
		t.Fatalf("create request is not the shared proto JSON: %v", err)
	}
	created, err := boundary.CreateEnrollment(ctx, enroll.Credential{Kind: enroll.CredentialOperator}, &create)
	if err != nil {
		t.Fatalf("shared authority refused the operator registration: %v", err)
	}
	enrollmentID := created.GetEnrollment().GetEnrollmentId()
	if enrollmentID == "" {
		t.Fatal("registration returned no enrollment ID")
	}

	runDir := t.TempDir()
	socket := filepath.Join(runDir, "engine.sock")
	hostSocket := filepath.Join(runDir, "host-engine.sock")
	dataRoot := filepath.Join(runDir, "engine-data")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	var hostRequests atomic.Int32
	serveManagedFixtureSocket(t, socket, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.53")
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(r.URL.Path, "/info"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ID": managedFixtureDaemonID, "ServerVersion": "29.3.0", "DockerRootDir": dataRoot,
				"CgroupVersion": "2", "MemoryLimit": true, "PidsLimit": true,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	serveManagedFixtureSocket(t, hostSocket, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		hostRequests.Add(1)
	}))

	service := &managedFixtureService{
		nodeEnrollmentService: &nodeEnrollmentService{boundary: boundary},
		authority:             authority, boundary: boundary,
		daemonID: managedFixtureDaemonID, endpointGen: managedFixtureEndpoint,
		health:     make(chan *v1.NodeHealthReport, 32),
		preflights: make(chan *v1.NodePreflightReport, 4),
		sessions:   make(chan *v1.ControlSession, 4),
	}
	_, controlHandler := conn.NewNodeControlServiceHandler(service,
		connect.WithReadMaxBytes(256<<10), connect.WithSendMaxBytes(256<<10))
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || !r.TLS.HandshakeComplete || r.TLS.Version < tls.VersionTLS13 {
			http.Error(w, "direct TLS 1.3 required", http.StatusForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), nodeEnrollmentTLSKey{}, r.TLS)
		controlHandler.ServeHTTP(w, r.WithContext(ctx))
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, ClientAuth: tls.VerifyClientCertIfGiven, ClientCAs: roots}
	server.StartTLS()
	t.Cleanup(server.Close)

	controllerRoots := server.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
	facts := native.HostFacts{
		OS: "linux", Systemd: true, RootfulServiceQualified: true, XDG_RUNTIME_DIR: runDir,
		ChosenSocket: socket, HostSocketPath: hostSocket, ChosenDataRoot: dataRoot,
		ChosenExecRoot: filepath.Join(runDir, "exec"), StorageDriver: "vfs",
		ContainerdNamespace: "oss377-fixture", BridgeCIDR: "10.231.0.1/24",
		ChosenPortRange: "30550-30600", CgroupVersion: "v2",
		DelegatedControllers: []string{"cpu", "cpuset", "io", "memory", "pids"},
	}
	config := agent.Config{
		StateDir: stateDir, ControllerURL: server.URL, ControllerCAs: controllerRoots,
		EnrollmentCA: ca, EnrollmentID: enrollmentID, Facts: facts,
	}

	// First run: challenge, TLS-bound claim, scoped mTLS session, preflight and
	// a real private-daemon probe.
	first := startManagedFixtureRun(t, config)
	preflight := awaitManagedFixturePreflight(t, first, service.preflights)
	if preflight.GetNodeId() != bundle.NodeID {
		t.Fatalf("preflight reported node %q, want %q", preflight.GetNodeId(), bundle.NodeID)
	}
	health := awaitManagedFixtureHealth(t, first, service.health)
	if health.GetNodeId() != bundle.NodeID {
		t.Fatalf("health reported node %q, want %q", health.GetNodeId(), bundle.NodeID)
	}
	if !hasManagedCapability(health.GetCapabilities(), v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET) ||
		!hasManagedCapability(health.GetCapabilities(), v1.NodeCapability_NODE_CAPABILITY_CGROUP_V2) {
		t.Fatalf("health did not report the probed private daemon capabilities: %v", health.GetCapabilities())
	}
	if health.GetPreflight() == nil {
		t.Fatal("health report carried no preflight snapshot")
	}
	heartbeat := awaitManagedFixtureHealth(t, first, service.health)
	if heartbeat.GetNodeId() != bundle.NodeID || !heartbeat.GetObservedAt().AsTime().After(health.GetObservedAt().AsTime()) {
		t.Fatal("the first run did not send a distinct periodic heartbeat for the enrolled node")
	}
	select {
	case session := <-service.sessions:
		if session.GetNodeId() != bundle.NodeID || session.GetHeartbeatInterval().AsDuration() <= 0 {
			t.Fatalf("unexpected control session: %+v", session)
		}
	default:
		t.Fatal("agent never opened a control session")
	}
	first.stop(t)
	if hostRequests.Load() != 0 {
		t.Fatal("agent contacted the shared host engine socket")
	}
	if service.challenges.Load() != 1 || service.claims.Load() != 1 {
		t.Fatalf("enrollment ran %d challenges and %d claims, want one each", service.challenges.Load(), service.claims.Load())
	}
	credential, err := nodeinstall.LoadManagedCredential(stateDir)
	if err != nil {
		t.Fatalf("agent did not persist its managed credential: %v", err)
	}
	if credential.DaemonID != managedFixtureDaemonID {
		t.Fatalf("persisted daemon pin = %q", credential.DaemonID)
	}

	// Restart reuses the persisted credential and never re-enrolls.
	config.EnrollmentID = ""
	second := startManagedFixtureRun(t, config)
	restartedPreflight := awaitManagedFixturePreflight(t, second, service.preflights)
	restartedHealth := awaitManagedFixtureHealth(t, second, service.health)
	if restartedPreflight.GetNodeId() != bundle.NodeID || restartedHealth.GetNodeId() != bundle.NodeID {
		t.Fatal("the restarted agent did not report as the original node")
	}
	second.stop(t)
	if service.challenges.Load() != 1 || service.claims.Load() != 1 {
		t.Fatalf("restart re-enrolled: %d challenges, %d claims", service.challenges.Load(), service.claims.Load())
	}

	identity, key, err := nodeinstall.LoadIdentity(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	enrolledCertificate := tls.Certificate{
		Certificate: [][]byte{credential.Claim.GetEnrolledClientCertificateDer()},
		PrivateKey:  key,
	}
	enrolledClient := conn.NewNodeControlServiceClient(managedFixtureHTTPClient(t, server, &enrolledCertificate), server.URL)
	if _, err := enrolledClient.NegotiateVersion(ctx, connect.NewRequest(&v1.NegotiateVersionRequest{NodeId: identity.NodeID})); err != nil {
		t.Fatalf("issued client certificate was refused: %v", err)
	}

	// Anonymous and unenrolled certificates must both be refused before any
	// node scope is considered.
	anonymous := conn.NewNodeControlServiceClient(managedFixtureHTTPClient(t, server, nil), server.URL)
	_, err = anonymous.NegotiateVersion(ctx, connect.NewRequest(&v1.NegotiateVersionRequest{NodeId: identity.NodeID}))
	assertManagedFixtureDenial(t, err, "a verified node client certificate is required")
	unenrolled := managedFixtureUnenrolledCertificate(t, ca, caKey)
	unenrolledClient := conn.NewNodeControlServiceClient(managedFixtureHTTPClient(t, server, &unenrolled), server.URL)
	_, err = unenrolledClient.NegotiateVersion(ctx, connect.NewRequest(&v1.NegotiateVersionRequest{NodeId: "node-unenrolled"}))
	assertManagedFixtureDenial(t, err, "certificate has no active enrollment grant")
	_, err = enrolledClient.NegotiateVersion(ctx, connect.NewRequest(&v1.NegotiateVersionRequest{NodeId: "node-other"}))
	assertManagedFixtureDenial(t, err, "cross-node object refused")

	// Revocation in the shared authority must invalidate the same mTLS
	// certificate without restarting the controller.
	if _, err := boundary.RevokeCredential(ctx, enroll.Credential{Kind: enroll.CredentialOperator}, &v1.RevokeCredentialRequest{
		NodeId: identity.NodeID, ConfirmationPhrase: "REVOKE " + identity.NodeID,
	}); err != nil {
		t.Fatalf("operator revocation failed: %v", err)
	}
	_, err = enrolledClient.NegotiateVersion(ctx, connect.NewRequest(&v1.NegotiateVersionRequest{NodeId: identity.NodeID}))
	assertManagedFixtureDenial(t, err, "certificate has no active enrollment grant")
}

func managedFixtureUnenrolledCertificate(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey) tls.Certificate {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "node-unenrolled"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
