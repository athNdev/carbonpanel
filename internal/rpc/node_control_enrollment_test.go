// SPDX-License-Identifier: MPL-2.0
package rpc

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/noderuntime/enroll"
	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	conn "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"google.golang.org/protobuf/proto"
)

func TestNodeEnrollmentHandlerAuthorization(t *testing.T) {
	ctx := t.Context()
	const nodeID = "node-fixture"
	const password = "NodeFixturePass123!"
	store := newTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SetPermissionsForRole("user", []rbac.Permission{
		{Resource: rbac.ResourceNodes, Action: rbac.ActionCreate, ObjectID: nodeID},
		{Resource: rbac.ResourceNodes, Action: rbac.ActionUpdate, ObjectID: nodeID},
	}); err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local: config.LocalConfig{Enabled: true}, AnonymousAccess: true, SessionTimeout: 300,
	})
	if err != nil {
		t.Fatal(err)
	}
	operator, err := manager.CreateLocalUser(ctx, "node-fixture-operator", "", password)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AssignRole(ctx, operator.ID, "user", "local"); err != nil {
		t.Fatal(err)
	}
	_, _, token, _, err := manager.Login(ctx, operator.Username, password)
	if err != nil {
		t.Fatal(err)
	}

	boundary, err := enroll.NewBoundary(enroll.TopologyDirectTLS)
	if err != nil {
		t.Fatal(err)
	}
	panel := &Server{authManager: manager, enforcer: enforcer}
	server := httptest.NewUnstartedServer(panel.nodeEnrollmentHandler(boundary))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	client := conn.NewNodeControlServiceClient(server.Client(), server.URL)
	bundle, msg, err := enroll.GenerateNodeBundle(t.TempDir(), nodeID, "Fixture node")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateEnrollment(ctx, connect.NewRequest(msg)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("anonymous create returned %v", err)
	}
	other := proto.CloneOf(msg)
	other.NodeId = "ungranted-node"
	denied := connect.NewRequest(other)
	denied.Header().Set("Authorization", "Bearer "+token)
	if _, err := client.CreateEnrollment(ctx, denied); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("create on another node returned %v", err)
	}
	request := connect.NewRequest(msg)
	request.Header().Set("Authorization", "Bearer "+token)
	created, err := client.CreateEnrollment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.GetEnrollment().GetEnrollmentId()
	begin := &v1.BeginEnrollmentChallengeRequest{EnrollmentId: id}
	withOperator := connect.NewRequest(begin)
	withOperator.Header().Set("Authorization", "Bearer "+token)
	if _, err := client.BeginEnrollmentChallenge(ctx, withOperator); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("operator on bootstrap returned %v", err)
	}
	challenge, err := client.BeginEnrollmentChallenge(ctx, connect.NewRequest(begin))
	if err != nil {
		t.Fatal(err)
	}
	claim, err := bundle.BuildClaim(id, challenge.Msg, msg.GetRequiredCapabilities())
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.ClaimEnrollment(ctx, connect.NewRequest(claim))
	if err != nil || result.Msg.GetIdentity().GetNodeId() != nodeID || len(result.Msg.GetEnrolledClientCertificateDer()) == 0 {
		t.Fatalf("scoped certificate was not issued, err=%v", err)
	}
	if _, err := client.ClaimEnrollment(ctx, connect.NewRequest(claim)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("replayed claim returned %v", err)
	}
	revoke := &v1.RevokeCredentialRequest{NodeId: nodeID, ConfirmationPhrase: "REVOKE " + nodeID}
	if _, err := client.RevokeCredential(ctx, connect.NewRequest(revoke)); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("bootstrap revocation returned %v", err)
	}
	revokeRequest := connect.NewRequest(revoke)
	revokeRequest.Header().Set("Authorization", "Bearer "+token)
	if _, err := client.RevokeCredential(ctx, revokeRequest); err != nil {
		t.Fatalf("operator revocation failed: %v", err)
	}

	plain := httptest.NewServer(panel.nodeEnrollmentHandler(boundary))
	t.Cleanup(plain.Close)
	plainClient := conn.NewNodeControlServiceClient(plain.Client(), plain.URL)
	if _, err := plainClient.BeginEnrollmentChallenge(ctx, connect.NewRequest(begin)); err == nil {
		t.Fatal("plain HTTP reached bootstrap")
	}
}
