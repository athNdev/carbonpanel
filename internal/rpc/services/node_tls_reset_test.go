// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestTLSCredentialsClearOnlyThroughNamedAction proves the reset policy from
// ADR 0018: no ordinary edit can clear stored daemon TLS material, and the
// dedicated named action is the only path that can, with explicit operator,
// node-management permission, target name, confirmation phrase and audit.
func TestTLSCredentialsClearOnlyThroughNamedAction(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	var logs bytes.Buffer
	log := logger.New()
	log.SetOutput(&logs)
	svc := NewNodeService(store, nil, log)
	ctx := context.Background()

	created := createTLSNodeFixture(t, svc)
	before, err := store.GetNode(ctx, created.Id)
	if err != nil {
		t.Fatalf("stored node lookup failed: %v", err)
	}
	initialGeneration := before.EndpointGeneration
	const currentName = "edited-name"

	assertIntact := func(label string) {
		t.Helper()
		stored, err := store.GetNode(ctx, created.Id)
		if err != nil {
			t.Fatalf("%s: stored node lookup failed: %v", label, err)
		}
		if stored.TLSCACert != fakeNodeCASentinel || stored.TLSCert != fakeNodeCertSentinel || stored.TLSKey != fakeNodeKeySentinel {
			t.Fatalf("%s: stored TLS credentials were modified", label)
		}
		if stored.EndpointGeneration != initialGeneration {
			t.Fatalf("%s: endpoint generation changed without a real TLS change: got %d, want %d", label, stored.EndpointGeneration, initialGeneration)
		}
	}

	// Ordinary edits: empty echo, omitted fields and a non-secret change must
	// never clear or invalidate the endpoint.
	redacted := createTLSNodeFixtureResponse(t, svc, created.Id)
	for label, req := range map[string]*v1.UpdateNodeRequest{
		"empty TLS echo": {
			Id:        created.Id,
			TlsCaCert: strPtr(""),
			TlsCert:   strPtr(""),
			TlsKey:    strPtr(""),
			Name:      strPtr(currentName),
		},
		"omitted TLS": {
			Id:          created.Id,
			MaxMemoryMb: int64Ptr(1234),
		},
		"redacted response echo": {
			Id:        created.Id,
			TlsCaCert: strPtr(redacted.TlsCaCert),
			TlsCert:   strPtr(redacted.TlsCert),
			TlsKey:    strPtr(redacted.TlsKey),
		},
	} {
		if _, err := svc.UpdateNode(ctx, connect.NewRequest(req)); err != nil {
			t.Fatalf("%s: UpdateNode failed: %v", label, err)
		}
		assertIntact(label)
	}

	// Refusals: no operator, no node-management permission, wrong target name,
	// wrong confirmation phrase. Each refusal leaves the node untouched.
	for label, action := range map[string]NodeTLSClearAction{
		"missing operator": {
			OperatorCanUpdateNode: true,
			TargetNodeName:        currentName,
			ConfirmationPhrase:    NodeTLSClearConfirmationPhrase,
		},
		"nodes/read only": {
			OperatorID:         "reader-1",
			TargetNodeName:     currentName,
			ConfirmationPhrase: NodeTLSClearConfirmationPhrase,
		},
		"wrong target": {
			OperatorID:            "operator-1",
			OperatorCanUpdateNode: true,
			TargetNodeName:        "some-other-node",
			ConfirmationPhrase:    NodeTLSClearConfirmationPhrase,
		},
		"missing confirmation": {
			OperatorID:            "operator-1",
			OperatorCanUpdateNode: true,
			TargetNodeName:        currentName,
		},
	} {
		if err := svc.ClearNodeTLSCredentials(ctx, created.Id, action); !errors.Is(err, ErrNodeTLSClearRefused) {
			t.Fatalf("%s: expected ErrNodeTLSClearRefused, got %v", label, err)
		}
		assertIntact(label)
	}

	// The named action with all confirmations clears only this node's TLS
	// material, bumps the generation and records an audit entry.
	err = svc.ClearNodeTLSCredentials(ctx, created.Id, NodeTLSClearAction{
		OperatorID:            "operator-1",
		OperatorCanUpdateNode: true,
		TargetNodeName:        currentName,
		ConfirmationPhrase:    NodeTLSClearConfirmationPhrase,
	})
	if err != nil {
		t.Fatalf("named clear action failed: %v", err)
	}
	cleared, err := store.GetNode(ctx, created.Id)
	if err != nil {
		t.Fatalf("stored node lookup after clear failed: %v", err)
	}
	if cleared.TLSCACert != "" || cleared.TLSCert != "" || cleared.TLSKey != "" {
		t.Fatal("named clear action did not clear stored TLS credentials")
	}
	if cleared.EndpointGeneration <= initialGeneration {
		t.Fatalf("named clear action did not bump endpoint generation: got %d, want > %d", cleared.EndpointGeneration, initialGeneration)
	}
	if cleared.Name != currentName {
		t.Fatalf("clear action changed an unrelated field: name=%q", cleared.Name)
	}
	audit := logs.String()
	if !strings.Contains(audit, "node.tls_credentials_cleared") || !strings.Contains(audit, "operator-1") || !strings.Contains(audit, created.Id) {
		t.Fatalf("audit entry missing action/operator/node: %q", audit)
	}
	for _, sentinel := range []string{fakeNodeCASentinel, fakeNodeCertSentinel, fakeNodeKeySentinel} {
		if strings.Contains(audit, sentinel) {
			t.Fatal("audit entry leaked a TLS credential value")
		}
	}
}

func createTLSNodeFixtureResponse(t *testing.T, svc *NodeService, nodeID string) *v1.Node {
	t.Helper()
	res, err := svc.GetNode(context.Background(), connect.NewRequest(&v1.GetNodeRequest{Id: nodeID}))
	if err != nil {
		t.Fatalf("GetNode for redacted fixture failed: %v", err)
	}
	return res.Msg.Node
}
