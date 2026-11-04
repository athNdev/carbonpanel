// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"archive/tar"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// TestAddOperationsToBundleRecordsAnUncertainOutcome is the regression for the
// property that matters: a mutation whose external effect was never recorded
// appears in the bundle as "unknown", not as a success or a failure, and the
// bundle names the safe next step.
func TestAddOperationsToBundleRecordsAnUncertainOutcome(t *testing.T) {
	ctx := context.Background()
	svc := newSupportOperationsService(t)

	server := &storage.Server{
		ID: "bundle-op-server", Name: "Bundle Op", NodeID: "test-node",
		Port: 25590, Memory: 1024, Status: storage.StatusStopped,
		DataPath: t.TempDir(),
	}
	if err := svc.store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	log := svc.log
	op, _, err := svc.store.BeginServerOperation(ctx, server.ID, server.NodeID, "container-1", "key-1", "start", operation.Digest("start", server))
	if err != nil {
		t.Fatalf("BeginServerOperation: %v", err)
	}
	if err := svc.store.AdvanceServerOperation(ctx, op.ID, storage.ServerOperationPrepared, storage.ServerOperationExecuting, ""); err != nil {
		t.Fatalf("AdvanceServerOperation: %v", err)
	}
	_ = log
	// Leave the row executing: this is the "the effect may have happened" case.

	var buf strings.Builder
	bw := newStringTarWriter(&buf)
	tw := tar.NewWriter(bw)

	if err := svc.addOperationsToBundle(ctx, tw); err != nil {
		t.Fatalf("addOperationsToBundle: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	data := readTarEntry(t, strings.NewReader(buf.String()), "operations.json")
	var payload struct {
		Summary    map[string]int `json:"summary"`
		Operations []struct {
			ID         string `json:"id"`
			Kind       string `json:"kind"`
			Outcome    string `json:"outcome"`
			NextAction string `json:"nextAction"`
			Reason     string `json:"reason"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("operations.json is not valid JSON: %v\n%s", err, data)
	}

	var found bool
	for _, entry := range payload.Operations {
		if entry.ID != op.ID {
			continue
		}
		found = true
		if entry.Outcome != string(operation.OutcomeUnknown) {
			t.Errorf("outcome = %q, want unknown for an executing row", entry.Outcome)
		}
		if entry.NextAction != string(operation.ActionResolve) {
			t.Errorf("next action = %q, want resolve_operation", entry.NextAction)
		}
		if entry.Reason == "" {
			t.Error("reason is empty")
		}
	}
	if !found {
		t.Fatalf("operation %s missing from the bundle: %+v", op.ID, payload.Operations)
	}
	if payload.Summary[string(operation.OutcomeUnknown)] != 1 {
		t.Errorf("summary = %+v, want one unknown operation", payload.Summary)
	}
}

type stringTarWriter struct {
	buf *strings.Builder
}

func newStringTarWriter(buf *strings.Builder) io.Writer { return &stringTarWriter{buf: buf} }

func (w *stringTarWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func readTarEntry(t *testing.T, r io.Reader, name string) []byte {
	t.Helper()
	tr := tar.NewReader(r)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			t.Fatalf("%s not found in the bundle", name)
		}
		if err != nil {
			t.Fatalf("read tar: %v", err)
		}
		if header.Name != name {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return body
	}
}

// TestOperationBundleKeepsRedactedErrors proves the bundle path uses the same
// redaction as the projection.
func TestOperationBundleKeepsRedactedErrors(t *testing.T) {
	ctx := context.Background()
	svc := newSupportOperationsService(t)

	server := &storage.Server{
		ID: "bundle-redact-server", Name: "Bundle Redact", NodeID: "test-node",
		Port: 25591, Memory: 1024, Status: storage.StatusStopped,
		DataPath: t.TempDir(),
	}
	if err := svc.store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	op, _, err := svc.store.BeginServerOperation(ctx, server.ID, server.NodeID, "", "key-2", "backup", operation.Digest("backup", server))
	if err != nil {
		t.Fatalf("BeginServerOperation: %v", err)
	}
	if err := svc.store.AdvanceServerOperation(ctx, op.ID, storage.ServerOperationPrepared, storage.ServerOperationExecuting, ""); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := svc.store.AdvanceServerOperation(ctx, op.ID, storage.ServerOperationExecuting, storage.ServerOperationFailed,
		"dial tcp://admin:secret@node.example:2376: connection refused"); err != nil {
		t.Fatalf("advance to failed: %v", err)
	}

	var buf strings.Builder
	tw := tar.NewWriter(newStringTarWriter(&buf))
	if err := svc.addOperationsToBundle(ctx, tw); err != nil {
		t.Fatalf("addOperationsToBundle: %v", err)
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}

	data := readTarEntry(t, strings.NewReader(buf.String()), "operations.json")
	if strings.Contains(string(data), "secret") {
		t.Fatalf("bundle leaked a credential:\n%s", data)
	}
	if !strings.Contains(string(data), "node.example") {
		t.Errorf("bundle dropped the host, which is the evidence:\n%s", data)
	}
	var payload struct {
		Operations []struct {
			ID string `json:"id"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(payload.Operations) != 1 || payload.Operations[0].ID != op.ID {
		t.Fatalf("operations = %+v, want the failed row", payload.Operations)
	}
}

// newSupportOperationsService builds a SupportService over a fresh store.
func newSupportOperationsService(t *testing.T) *SupportService {
	t.Helper()
	store := setupTestStore(t)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	cfg := &config.Config{Storage: config.StorageConfig{DataDir: t.TempDir()}}
	return NewSupportService(store, nil, cfg, logger.New())
}
