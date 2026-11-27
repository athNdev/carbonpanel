// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func newJournalTestService(t *testing.T) (*OperationService, *storage.Store) {
	t.Helper()

	store, err := storage.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{
			Path:        filepath.Join(t.TempDir(), "journal.db"),
			AutoMigrate: true,
		},
		Docker: config.DockerConfig{Host: "unix:///var/run/docker.sock"},
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	return NewOperationService(store, logger.New()), store
}

// createJournalNode records the node identity an operation is bound to. The
// daemon id is deliberately non-empty so the projection has to derive a
// correlation state instead of forwarding it.
func createJournalNode(t *testing.T, store *storage.Store, id, daemonID string) *storage.Node {
	t.Helper()
	node := &storage.Node{
		ID:       id,
		Name:     "Node " + id,
		Host:     "tcp://127.0.0.1:2375",
		Enabled:  true,
		Status:   storage.NodeStatusOnline,
		DaemonID: daemonID,
	}
	if err := store.CreateNode(context.Background(), node); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	return node
}

// beginInterrupted records an intent that was never finalized, which is what an
// interrupted mutation leaves behind.
func beginInterrupted(t *testing.T, store *storage.Store, serverID, kind string) *storage.ServerOperation {
	t.Helper()
	op, _, err := store.BeginServerOperation(context.Background(), serverID, "node-1", "cont-1", "req-"+serverID+"-"+kind, kind, "digest-"+kind)
	if err != nil {
		t.Fatalf("BeginServerOperation: %v", err)
	}
	if err := store.AdvanceServerOperation(context.Background(), op.ID, storage.ServerOperationPrepared, storage.ServerOperationExecuting, "attempt started"); err != nil {
		t.Fatalf("AdvanceServerOperation: %v", err)
	}
	return op
}

// backdate moves a row's update time into the past so the projection can be
// exercised at a chosen age without sleeping.
func backdate(t *testing.T, store *storage.Store, opID string, age time.Duration) {
	t.Helper()
	past := time.Now().Add(-age).UTC()
	if err := store.DB().Model(&storage.ServerOperation{}).Where("id = ?", opID).Update("updated_at", past).Error; err != nil {
		t.Fatalf("backdate %s: %v", opID, err)
	}
}

func TestListOperationsIsScopedToOneServer(t *testing.T) {
	service, store := newJournalTestService(t)
	beginInterrupted(t, store, "srv-1", "start")
	beginInterrupted(t, store, "srv-2", "stop")

	res, err := service.ListOperations(context.Background(), connect.NewRequest(&v1.ListOperationsRequest{ServerId: "srv-1"}))
	if err != nil {
		t.Fatalf("ListOperations: %v", err)
	}
	if len(res.Msg.Operations) != 1 {
		t.Fatalf("operations = %d, want only srv-1's row", len(res.Msg.Operations))
	}
	if got := res.Msg.Operations[0].ServerId; got != "srv-1" {
		t.Fatalf("server id = %q, want srv-1", got)
	}
	if got := res.Msg.Operations[0].RequestId; got == "" {
		t.Fatal("journal row is missing request_id; activity cannot join it")
	}
}

func TestListOperationsSeparatesInFlightFromUnknown(t *testing.T) {
	service, store := newJournalTestService(t)
	op := beginInterrupted(t, store, "srv-1", "start")

	// A row advanced moments ago is still being attempted: reporting it as
	// unknown would offer "resolve" while the panel is mid-call.
	running, err := service.ListOperations(context.Background(), connect.NewRequest(&v1.ListOperationsRequest{
		ServerId: "srv-1",
		Outcome:  "running",
	}))
	if err != nil {
		t.Fatalf("ListOperations(running): %v", err)
	}
	if len(running.Msg.Operations) != 1 {
		t.Fatalf("running operations = %d, want 1", len(running.Msg.Operations))
	}

	// The same row, once nothing has advanced it for longer than the execution
	// window, is an unknown outcome that needs an operator.
	backdate(t, store, op.ID, operation.ExecutionStaleAfter+time.Minute)

	unknown, err := service.ListOperations(context.Background(), connect.NewRequest(&v1.ListOperationsRequest{
		ServerId: "srv-1",
		Outcome:  "unknown",
	}))
	if err != nil {
		t.Fatalf("ListOperations(unknown): %v", err)
	}
	if len(unknown.Msg.Operations) != 1 {
		t.Fatalf("unknown operations = %d, want 1", len(unknown.Msg.Operations))
	}
	if got := unknown.Msg.CountsByOutcome["unknown"]; got != 1 {
		t.Fatalf("counts[unknown] = %d, want 1", got)
	}
	if got := unknown.Msg.Operations[0].NextAction; got != "resolve_operation" {
		t.Fatalf("next action = %q, want resolve_operation", got)
	}

	success, err := service.ListOperations(context.Background(), connect.NewRequest(&v1.ListOperationsRequest{
		ServerId: "srv-1",
		Outcome:  "success",
	}))
	if err != nil {
		t.Fatalf("ListOperations(success): %v", err)
	}
	if len(success.Msg.Operations) != 0 {
		t.Fatalf("success operations = %d, want 0", len(success.Msg.Operations))
	}
}

func TestStaleExecutingOperationProjectsAsUnknownDoNotRetry(t *testing.T) {
	service, store := newJournalTestService(t)
	node := createJournalNode(t, store, "node-1", "raw-daemon-fingerprint-stale")
	op := beginInterrupted(t, store, "srv-1", "start")
	backdate(t, store, op.ID, operation.ExecutionStaleAfter+time.Minute)

	res, err := service.GetOperation(context.Background(), connect.NewRequest(&v1.GetOperationRequest{
		ServerId:    "srv-1",
		OperationId: op.ID,
	}))
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	got := res.Msg.Operation
	if got.GetOutcome() != "unknown" {
		t.Fatalf("outcome = %q, want unknown for an attempt that stopped reporting", got.GetOutcome())
	}
	if got.GetNextAction() != "resolve_operation" {
		t.Fatalf("next action = %q, want resolve_operation, not a blind retry", got.GetNextAction())
	}
	if got.GetReasonCode() != string(operation.ReasonExecutingStale) {
		t.Fatalf("reason code = %q, want %q", got.GetReasonCode(), operation.ReasonExecutingStale)
	}
	if got.GetPhase() != storage.ServerOperationExecuting {
		t.Fatalf("phase = %q, want executing", got.GetPhase())
	}
	if got.GetNodeId() != node.ID {
		t.Fatalf("node id = %q, want %q", got.GetNodeId(), node.ID)
	}
	if got.GetEndpointGeneration() != node.EndpointGeneration {
		t.Fatalf("endpoint generation = %d, want %d", got.GetEndpointGeneration(), node.EndpointGeneration)
	}
	if got.GetRequestKey() != "req-srv-1-start" {
		t.Fatalf("request key = %q, want the stored journal key", got.GetRequestKey())
	}
	if got.GetRequestId() != "req-srv-1-start" {
		t.Fatalf("request id = %q, want the client request id", got.GetRequestId())
	}
	if got.GetCorrelation() != string(operation.CorrelationMatched) {
		t.Fatalf("correlation = %q, want matched for the recorded endpoint", got.GetCorrelation())
	}
	if strings.Contains(got.String(), "raw-daemon-fingerprint-stale") {
		t.Fatal("raw daemon id leaked into the operation wire projection")
	}
}

func TestUncertainOperationKeepsCorrelationWithoutDaemonID(t *testing.T) {
	service, store := newJournalTestService(t)
	node := createJournalNode(t, store, "node-1", "raw-daemon-fingerprint-uncertain")
	op := beginInterrupted(t, store, "srv-1", "start")
	note := operation.UncertainOutcomeMarker + "; confirm the live world on the node before starting the server"
	if err := store.NoteActiveServerOperation(context.Background(), op.ID, note); err != nil {
		t.Fatalf("NoteActiveServerOperation: %v", err)
	}

	res, err := service.GetOperation(context.Background(), connect.NewRequest(&v1.GetOperationRequest{
		ServerId:    "srv-1",
		OperationId: op.ID,
	}))
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	got := res.Msg.Operation
	if got.GetOutcome() != "unknown" {
		t.Fatalf("outcome = %q, want unknown for an uncertain external effect", got.GetOutcome())
	}
	if got.GetNextAction() != "resolve_operation" {
		t.Fatalf("next action = %q, want resolve_operation", got.GetNextAction())
	}
	if got.GetReasonCode() != string(operation.ReasonUncertainEffect) {
		t.Fatalf("reason code = %q, want %q", got.GetReasonCode(), operation.ReasonUncertainEffect)
	}
	if got.GetNodeId() != node.ID || got.GetEndpointGeneration() != node.EndpointGeneration {
		t.Fatalf("node correlation fields = (%q, %d), want (%q, %d)",
			got.GetNodeId(), got.GetEndpointGeneration(), node.ID, node.EndpointGeneration)
	}
	if got.GetCorrelation() != string(operation.CorrelationMatched) {
		t.Fatalf("correlation = %q, want matched", got.GetCorrelation())
	}
	if got.GetRequestKey() == "" || got.GetRequestId() == "" {
		t.Fatalf("request key/id = (%q, %q), want the journal join keys", got.GetRequestKey(), got.GetRequestId())
	}
	if got.GetPhase() != storage.ServerOperationExecuting {
		t.Fatalf("phase = %q, want executing", got.GetPhase())
	}
	if strings.Contains(got.String(), "raw-daemon-fingerprint-uncertain") {
		t.Fatal("raw daemon id leaked into the operation wire projection")
	}
}

func TestListOperationsRejectsAnUnknownOutcomeFilter(t *testing.T) {
	service, _ := newJournalTestService(t)

	_, err := service.ListOperations(context.Background(), connect.NewRequest(&v1.ListOperationsRequest{
		ServerId: "srv-1",
		Outcome:  "done",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want invalid_argument: a typo must not silently match nothing", connect.CodeOf(err))
	}
}

func TestGetOperationRefusesAServerThatDoesNotOwnIt(t *testing.T) {
	service, store := newJournalTestService(t)
	op := beginInterrupted(t, store, "srv-1", "start")

	// The operation exists, but not for this server: answering "forbidden"
	// would confirm the id exists elsewhere.
	_, err := service.GetOperation(context.Background(), connect.NewRequest(&v1.GetOperationRequest{
		ServerId:    "srv-2",
		OperationId: op.ID,
	}))
	if connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("code = %v, want not_found", connect.CodeOf(err))
	}
}

func TestResolveOperationReleasesTheLockAndNudgesTheReconciler(t *testing.T) {
	service, store := newJournalTestService(t)
	op := beginInterrupted(t, store, "srv-1", "start")

	var nudged []string
	service.SetReconcilerNudge(func(serverID string) { nudged = append(nudged, serverID) })

	res, err := service.ResolveOperation(context.Background(), connect.NewRequest(&v1.ResolveOperationRequest{
		ServerId:    "srv-1",
		OperationId: op.ID,
		Note:        "container was pruned by hand",
	}))
	if err != nil {
		t.Fatalf("ResolveOperation: %v", err)
	}
	if res.Msg.Operation.HoldsLock {
		t.Fatal("operation still holds the lock after resolve")
	}
	if got := res.Msg.Operation.Outcome; got != "unknown" {
		t.Fatalf("outcome = %q, want unknown: releasing a lock does not confirm the effect", got)
	}
	if err := store.AssertNoServerOperation(context.Background(), "srv-1"); err != nil {
		t.Fatalf("server still blocked: %v", err)
	}
	if len(nudged) != 1 || nudged[0] != "srv-1" {
		t.Fatalf("nudged = %v, want the server to converge immediately", nudged)
	}
}

func TestResolveOperationIsIdempotent(t *testing.T) {
	service, store := newJournalTestService(t)
	op := beginInterrupted(t, store, "srv-1", "start")

	for i := 0; i < 2; i++ {
		if _, err := service.ResolveOperation(context.Background(), connect.NewRequest(&v1.ResolveOperationRequest{
			ServerId:    "srv-1",
			OperationId: op.ID,
		})); err != nil {
			t.Fatalf("ResolveOperation attempt %d: %v", i+1, err)
		}
	}
}

func TestResolveOperationBoundsTheNote(t *testing.T) {
	service, store := newJournalTestService(t)
	op := beginInterrupted(t, store, "srv-1", "start")

	note := strings.Repeat("x", maxResolveNote*3)
	if _, err := service.ResolveOperation(context.Background(), connect.NewRequest(&v1.ResolveOperationRequest{
		ServerId:    "srv-1",
		OperationId: op.ID,
		Note:        note,
	})); err != nil {
		t.Fatalf("ResolveOperation: %v", err)
	}

	stored, err := store.GetServerOperation(context.Background(), op.ID)
	if err != nil {
		t.Fatalf("GetServerOperation: %v", err)
	}
	if len(stored.LastError) > maxResolveNote {
		t.Fatalf("stored note = %d bytes, want at most %d", len(stored.LastError), maxResolveNote)
	}
}

func TestOperationResponsesRedactCredentials(t *testing.T) {
	service, store := newJournalTestService(t)
	op := beginInterrupted(t, store, "srv-1", "start")
	if err := store.AdvanceServerOperation(context.Background(), op.ID, storage.ServerOperationExecuting, storage.ServerOperationFailed, "dial tcp://admin:hunter2@10.0.0.5:2376: refused"); err != nil {
		t.Fatalf("AdvanceServerOperation: %v", err)
	}

	res, err := service.GetOperation(context.Background(), connect.NewRequest(&v1.GetOperationRequest{
		ServerId:    "srv-1",
		OperationId: op.ID,
	}))
	if err != nil {
		t.Fatalf("GetOperation: %v", err)
	}
	got := res.Msg.Operation.LastError
	if got == "" {
		t.Fatal("last error was dropped; the evidence has to survive redaction")
	}
	for _, secret := range []string{"hunter2", "admin:"} {
		if strings.Contains(got, secret) {
			t.Fatalf("last error = %q, want %q removed", got, secret)
		}
	}
	if !strings.Contains(got, "10.0.0.5") {
		t.Fatalf("last error = %q, want the host kept as evidence", got)
	}
	if res.Msg.Operation.Outcome != "failed" {
		t.Fatalf("outcome = %q, want failed", res.Msg.Operation.Outcome)
	}
}
