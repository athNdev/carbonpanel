// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// These are deliberately fake, non-functional sentinels in documentation/test
// address space (RFC 5737). No real credential or private endpoint is used.
const (
	fakeNodeCASentinel     = "FAKE-CA-SENTINEL-DO-NOT-USE"
	fakeNodeCertSentinel   = "FAKE-CERT-SENTINEL-DO-NOT-USE"
	fakeNodeKeySentinel    = "FAKE-KEY-SENTINEL-DO-NOT-USE"
	fakeNodeKeyReplacement = "FAKE-REPLACEMENT-KEY-SENTINEL-DO-NOT-USE"
)

func assertNodeTLSSecretsRedacted(t *testing.T, label string, node *v1.Node) {
	t.Helper()
	if node == nil {
		t.Fatalf("%s: expected a node in the response", label)
	}
	if node.TlsKey != "" {
		t.Errorf("%s: tls_key leaked into the response", label)
	}
	if node.TlsCaCert != "" {
		t.Errorf("%s: tls_ca_cert leaked into the response", label)
	}
	if node.TlsCert != "" {
		t.Errorf("%s: tls_cert leaked into the response", label)
	}
	// The fixture must not pass by returning an empty object: non-secret fields
	// stay authoritative for the console.
	if node.Id == "" || node.Name == "" || node.Host == "" || !node.TlsEnabled {
		t.Errorf("%s: non-secret node fields were lost: %+v", label, node)
	}
}

func mustMarshalNode(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	encoded, err := protojson.Marshal(msg)
	if err != nil {
		t.Fatalf("protojson.Marshal failed: %v", err)
	}
	return encoded
}

func assertNoTLSSentinels(t *testing.T, label, payload string) {
	t.Helper()
	for _, sentinel := range []string{fakeNodeCASentinel, fakeNodeCertSentinel, fakeNodeKeySentinel, fakeNodeKeyReplacement} {
		if strings.Contains(payload, sentinel) {
			t.Errorf("%s: TLS secret sentinel appeared in output", label)
		}
	}
}

func createTLSNodeFixture(t *testing.T, svc *NodeService) *v1.Node {
	t.Helper()
	res, err := svc.CreateNode(context.Background(), connect.NewRequest(&v1.CreateNodeRequest{
		Name:         "tls-secret-node",
		Host:         "tcp://192.0.2.10:2376",
		AdvertisedIp: "192.0.2.10",
		TlsEnabled:   true,
		TlsCaCert:    fakeNodeCASentinel,
		TlsCert:      fakeNodeCertSentinel,
		TlsKey:       fakeNodeKeySentinel,
		MaxMemoryMb:  4096,
		MaxServers:   3,
		// Disabled so no asynchronous daemon ping is started by the fixture.
		Enabled: false,
	}))
	if err != nil {
		t.Fatalf("CreateNode fixture failed: %v", err)
	}
	return res.Msg.Node
}

// TestNodeResponsesRedactTLSSecrets is the harmless nodes/read response-shape
// fixture. It proves that no TLS key or certificate material reaches a caller of
// CreateNode, GetNode or ListNodes, including in marshalled wire output, formatted
// values or logs. The stored values must still be present for the Docker pool.
func TestNodeResponsesRedactTLSSecrets(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	var logs bytes.Buffer
	log := logger.New()
	log.SetOutput(&logs)
	svc := NewNodeService(store, nil, log)
	ctx := context.Background()

	created := createTLSNodeFixture(t, svc)
	assertNodeTLSSecretsRedacted(t, "CreateNode", created)

	getRes, err := svc.GetNode(ctx, connect.NewRequest(&v1.GetNodeRequest{Id: created.Id}))
	if err != nil {
		t.Fatalf("GetNode failed: %v", err)
	}
	assertNodeTLSSecretsRedacted(t, "GetNode", getRes.Msg.Node)

	listRes, err := svc.ListNodes(ctx, connect.NewRequest(&v1.ListNodesRequest{}))
	if err != nil {
		t.Fatalf("ListNodes failed: %v", err)
	}
	if len(listRes.Msg.Nodes) != 1 {
		t.Fatalf("expected one node, got %d", len(listRes.Msg.Nodes))
	}
	assertNodeTLSSecretsRedacted(t, "ListNodes", listRes.Msg.Nodes[0])

	// Marshalled wire output is the actual consumer boundary.
	for label, encoded := range map[string][]byte{
		"create-json": mustMarshalNode(t, created),
		"get-json":    mustMarshalNode(t, getRes.Msg),
		"list-json":   mustMarshalNode(t, listRes.Msg),
	} {
		assertNoTLSSentinels(t, label, string(encoded))
	}
	assertNoTLSSentinels(t, "formatted responses", fmt.Sprintf("%v %v %v", created, getRes.Msg, listRes.Msg))
	assertNoTLSSentinels(t, "service logs", logs.String())

	// Redaction is response-only: the controller's stored TLS material is intact
	// for internal/docker.ClientPool's daemon connections.
	stored, err := store.GetNode(ctx, created.Id)
	if err != nil {
		t.Fatalf("stored node lookup failed: %v", err)
	}
	if stored.TLSCACert != fakeNodeCASentinel || stored.TLSCert != fakeNodeCertSentinel || stored.TLSKey != fakeNodeKeySentinel {
		t.Fatal("server-side TLS material was modified by redaction")
	}
}

// TestUpdateNodeRedactedFieldsDoNotClearTLSCredentials is the no-round-trip
// fixture. An editor that echoes the redacted read response back must not wipe
// stored credentials or invalidate the cached pool client by bumping the
// endpoint generation. Non-empty values still replace credentials.
func TestUpdateNodeRedactedFieldsDoNotClearTLSCredentials(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	log := logger.New()
	svc := NewNodeService(store, nil, log)
	ctx := context.Background()

	created := createTLSNodeFixture(t, svc)
	before, err := store.GetNode(ctx, created.Id)
	if err != nil {
		t.Fatalf("stored node lookup failed: %v", err)
	}
	initialGeneration := before.EndpointGeneration

	getRes, err := svc.GetNode(ctx, connect.NewRequest(&v1.GetNodeRequest{Id: created.Id}))
	if err != nil {
		t.Fatalf("GetNode failed: %v", err)
	}
	redacted := getRes.Msg.Node

	// The form-shaped payload: empty TLS strings from the redacted response are
	// present, alongside a genuine non-secret edit.
	updateRes, err := svc.UpdateNode(ctx, connect.NewRequest(&v1.UpdateNodeRequest{
		Id:        created.Id,
		Name:      strPtr("renamed-after-redaction"),
		TlsCaCert: strPtr(redacted.TlsCaCert),
		TlsCert:   strPtr(redacted.TlsCert),
		TlsKey:    strPtr(redacted.TlsKey),
	}))
	if err != nil {
		t.Fatalf("UpdateNode with redacted fields failed: %v", err)
	}
	assertNodeTLSSecretsRedacted(t, "UpdateNode", updateRes.Msg.Node)

	after, err := store.GetNode(ctx, created.Id)
	if err != nil {
		t.Fatalf("stored node lookup after redacted update failed: %v", err)
	}
	if after.TLSCACert != fakeNodeCASentinel || after.TLSCert != fakeNodeCertSentinel || after.TLSKey != fakeNodeKeySentinel {
		t.Fatal("redacted/empty TLS fields cleared stored credentials")
	}
	if after.EndpointGeneration != initialGeneration {
		t.Fatalf("redacted/empty TLS fields bumped endpoint generation: got %d, want %d", after.EndpointGeneration, initialGeneration)
	}
	if after.Name != "renamed-after-redaction" {
		t.Fatalf("non-secret edit was not applied: %q", after.Name)
	}

	// Omitted TLS fields are equally harmless.
	if _, err := svc.UpdateNode(ctx, connect.NewRequest(&v1.UpdateNodeRequest{
		Id:          created.Id,
		MaxMemoryMb: int64Ptr(8192),
	})); err != nil {
		t.Fatalf("UpdateNode with omitted TLS fields failed: %v", err)
	}
	after, err = store.GetNode(ctx, created.Id)
	if err != nil {
		t.Fatalf("stored node lookup after omitted update failed: %v", err)
	}
	if after.TLSKey != fakeNodeKeySentinel || after.EndpointGeneration != initialGeneration {
		t.Fatal("omitted TLS fields changed stored credentials or generation")
	}

	// A genuine non-empty replacement still works and still invalidates the
	// cached client via the endpoint generation.
	replaceRes, err := svc.UpdateNode(ctx, connect.NewRequest(&v1.UpdateNodeRequest{
		Id:     created.Id,
		TlsKey: strPtr(fakeNodeKeyReplacement),
	}))
	if err != nil {
		t.Fatalf("UpdateNode with a real replacement failed: %v", err)
	}
	assertNodeTLSSecretsRedacted(t, "UpdateNode replacement", replaceRes.Msg.Node)
	replaced, err := store.GetNode(ctx, created.Id)
	if err != nil {
		t.Fatalf("stored node lookup after replacement failed: %v", err)
	}
	if replaced.TLSKey != fakeNodeKeyReplacement {
		t.Fatal("non-empty TLS key replacement was not stored")
	}
	if replaced.EndpointGeneration <= initialGeneration {
		t.Fatalf("real TLS replacement did not bump endpoint generation: got %d, want > %d", replaced.EndpointGeneration, initialGeneration)
	}
	assertNoTLSSentinels(t, "replacement response", fmt.Sprintf("%v", replaceRes.Msg))
}
