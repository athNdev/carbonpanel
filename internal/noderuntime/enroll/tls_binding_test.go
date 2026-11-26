// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package enroll

import (
	"bytes"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/proto"
)

func TestBootstrapBoundToIssuingTLSConnection(t *testing.T) {
	ta := newTestAuthority(t, TopologyDirectTLS)
	boundary := &Boundary{Authority: ta.authority}
	bootstrap := Credential{Kind: CredentialBootstrap}
	ctx := fixtureContext(t)

	sessions := make(chan tls.ConnectionState, 2)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions <- *r.TLS
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)

	getSession := func() (*tls.ConnectionState, *tls.ConnectionState) {
		t.Helper()
		transport := server.Client().Transport.(*http.Transport).Clone()
		t.Cleanup(transport.CloseIdleConnections)
		client := &http.Client{Transport: transport}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		reply, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = reply.Body.Close()
		select {
		case state := <-sessions:
			return &state, reply.TLS
		case <-ctx.Done():
			t.Fatal(ctx.Err())
			return nil, nil
		}
	}
	issuedSession, clientSession := getSession()
	separateSession, _ := getSession()

	if _, err := boundary.BeginTLSEnrollment(ctx, bootstrap, ta.enrollmentID, nil); !errors.Is(err, ErrTopology) {
		t.Fatalf("missing TLS must refuse bootstrap, got %v", err)
	}
	operator := Credential{Kind: CredentialOperator}
	if _, err := boundary.BeginTLSEnrollment(ctx, operator, ta.enrollmentID, issuedSession); !errors.Is(err, ErrScope) {
		t.Fatalf("operator must not enter the bootstrap phase, got %v", err)
	}

	challenge, err := boundary.BeginTLSEnrollment(ctx, bootstrap, ta.enrollmentID, issuedSession)
	if err != nil {
		t.Fatal(err)
	}
	clientBinding, err := TLSChannelBinding(clientSession)
	if err != nil || len(clientBinding) != 32 || !bytes.Equal(clientBinding, challenge.GetChannelContext()) {
		t.Fatalf("client and controller did not derive the same enrollment binding, err=%v", err)
	}

	caps := []v1.NodeCapability{
		v1.NodeCapability_NODE_CAPABILITY_PRIVATE_SOCKET,
		v1.NodeCapability_NODE_CAPABILITY_DEDICATED_ROOTS,
	}
	claim, err := ta.bundle.BuildClaim(ta.enrollmentID, challenge, caps)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := boundary.ClaimTLSEnrollment(ctx, bootstrap, claim, separateSession); !errors.Is(err, ErrTopology) {
		t.Fatalf("fresh separate TLS connection must refuse the issued proof, got %v", err)
	}
	if _, err := boundary.ClaimEnrollment(ctx, bootstrap, claim); !errors.Is(err, ErrTopology) {
		t.Fatalf("fixture path must refuse the live challenge, got %v", err)
	}
	forged := proto.CloneOf(claim)
	forged.ChannelBindingEvidence = []byte("caller-supplied-context")
	if _, err := boundary.ClaimTLSEnrollment(ctx, bootstrap, forged, issuedSession); !errors.Is(err, ErrTopology) {
		t.Fatalf("caller-supplied context must refuse the proof, got %v", err)
	}

	reply, err := boundary.ClaimTLSEnrollment(ctx, bootstrap, claim, issuedSession)
	if err != nil || reply.GetIdentity().GetNodeId() != ta.nodeID || len(reply.GetEnrolledClientCertificateDer()) == 0 {
		t.Fatalf("issuer session must issue a credential for the approved node, err=%v", err)
	}
	if _, err := boundary.ClaimTLSEnrollment(ctx, bootstrap, claim, issuedSession); err == nil {
		t.Fatal("same connection cannot consume the challenge twice")
	}
}

func TestReportHashChangesWithPayload(t *testing.T) {
	report := &v1.NodePreflightReport{NodeId: "qualified-node"}
	first, err := ReportHash(report)
	if err != nil {
		t.Fatal(err)
	}
	repeat, err := ReportHash(proto.CloneOf(report))
	if err != nil || !bytes.Equal(first, repeat) {
		t.Fatalf("equivalent report changed the hash, err=%v", err)
	}
	report.NodeId = "different-node"
	changed, err := ReportHash(report)
	if err != nil || bytes.Equal(first, changed) {
		t.Fatalf("changed report must change the hash, err=%v", err)
	}
}
