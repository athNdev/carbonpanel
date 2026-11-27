// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/command"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type commandAuditExecutor struct {
	output string
	err    error
}

func (e commandAuditExecutor) ExecCommand(context.Context, string, string) (string, error) {
	return e.output, e.err
}

// newCommandAuditTestService runs SendCommand against a loopback Docker API
// that reports its container as running, while the command sender uses the
// supplied in-process executor.
func newCommandAuditTestService(t *testing.T, executor command.DockerExecutor) (*ServerService, *storage.Store) {
	t.Helper()

	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/cont-audit/json") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":    "cont-audit",
				"State": map[string]any{"Status": "running", "Running": true},
			})
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(dockerAPI.Close)

	dockerCli, err := docker.NewClient("tcp://"+strings.TrimPrefix(dockerAPI.URL, "http://"), logger.New())
	if err != nil {
		t.Fatalf("create docker test client: %v", err)
	}
	t.Cleanup(func() { _ = dockerCli.Close() })

	if err := store.CreateServer(context.Background(), &storage.Server{
		ID:          "srv-command",
		Name:        "Command audit",
		NodeID:      "local",
		ContainerID: "cont-audit",
		Status:      storage.StatusRunning,
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	cfg := &config.Config{}
	return &ServerService{
		store:  store,
		docker: dockerCli,
		sender: command.NewSender(store, cfg, executor, nil),
		config: cfg,
		log:    logger.New(),
	}, store
}

func commandAuditRows(t *testing.T, store *storage.Store) []storage.ActivityLog {
	t.Helper()
	var rows []storage.ActivityLog
	if err := store.DB().Where("event = ?", activity.EventServerCommand).Find(&rows).Error; err != nil {
		t.Fatalf("query command audit rows: %v", err)
	}
	return rows
}

func TestSendCommandWritesOneBoundedAuditRow(t *testing.T) {
	svc, store := newCommandAuditTestService(t, commandAuditExecutor{output: "ok"})
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "actor-1", Username: "alice"})
	commandText := "say " + strings.Repeat("x", 600)

	res, err := svc.SendCommand(ctx, connect.NewRequest(&v1.SendCommandRequest{
		Id:      "srv-command",
		Command: commandText,
		Silent:  boolPtr(true),
	}))
	if err != nil {
		t.Fatalf("SendCommand: %v", err)
	}
	if !res.Msg.Success {
		t.Fatalf("SendCommand success = false, error = %q", res.Msg.Error)
	}

	rows := commandAuditRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("command audit rows = %d, want exactly one", len(rows))
	}
	row := rows[0]
	if row.ActorID != "actor-1" || row.ActorName != "alice" {
		t.Fatalf("audit actor = (%q, %q), want actor-1/alice", row.ActorID, row.ActorName)
	}
	if row.Event != activity.EventServerCommand {
		t.Fatalf("audit event = %q, want %q", row.Event, activity.EventServerCommand)
	}
	if row.SubjectTyp != "server" || row.SubjectID != "srv-command" || row.ServerID != "srv-command" {
		t.Fatalf("audit subject = (%q, %q, %q), want server/srv-command/srv-command",
			row.SubjectTyp, row.SubjectID, row.ServerID)
	}
	if row.RequestID != "" {
		t.Fatalf("audit request id = %q, want empty because SendCommand has no request id", row.RequestID)
	}

	var props map[string]any
	if err := json.Unmarshal([]byte(row.Properties), &props); err != nil {
		t.Fatalf("decode audit properties %q: %v", row.Properties, err)
	}
	gotCommand, _ := props["command"].(string)
	if !strings.HasPrefix(gotCommand, "say ") {
		t.Fatalf("audit command = %q, want the attempted command prefix", gotCommand)
	}
	if len(gotCommand) > auditPropertyStringMaxBytes {
		t.Fatalf("audit command is %d bytes, want at most %d", len(gotCommand), auditPropertyStringMaxBytes)
	}
	if props["silent"] != true {
		t.Fatalf("audit silent = %v, want true", props["silent"])
	}
	if props["success"] != true {
		t.Fatalf("audit success = %v, want true", props["success"])
	}
	if _, ok := props["error"]; ok {
		t.Fatalf("successful command audit carried an error: %v", props["error"])
	}
}

func TestSendCommandFailureStillWritesOneAuditRow(t *testing.T) {
	svc, store := newCommandAuditTestService(t, commandAuditExecutor{err: errors.New("sender unavailable")})
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "actor-2", Username: "bob"})

	res, err := svc.SendCommand(ctx, connect.NewRequest(&v1.SendCommandRequest{
		Id:      "srv-command",
		Command: "list",
	}))
	if err != nil {
		t.Fatalf("SendCommand returned transport error %v, want a soft failure response", err)
	}
	if res.Msg.Success {
		t.Fatal("SendCommand success = true, want false")
	}
	if !strings.Contains(res.Msg.Error, "sender unavailable") {
		t.Fatalf("SendCommand error = %q, want the sender failure", res.Msg.Error)
	}

	rows := commandAuditRows(t, store)
	if len(rows) != 1 {
		t.Fatalf("command audit rows = %d, want exactly one after a failed send", len(rows))
	}
	row := rows[0]
	if row.ActorID != "actor-2" || row.SubjectID != "srv-command" {
		t.Fatalf("failed-send audit lacks actor/server: %+v", row)
	}
	var props map[string]any
	if err := json.Unmarshal([]byte(row.Properties), &props); err != nil {
		t.Fatalf("decode audit properties %q: %v", row.Properties, err)
	}
	if props["success"] != false {
		t.Fatalf("audit success = %v, want false", props["success"])
	}
	errText, _ := props["error"].(string)
	if !strings.Contains(errText, "sender unavailable") {
		t.Fatalf("audit error = %q, want the sender failure", errText)
	}
}

func TestAuditServerLifecycleRowShapeUnchanged(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	svc := &ServerService{store: store, log: logger.New()}
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "actor-3", Username: "carol"})

	svc.auditServerLifecycle(ctx, "203.0.113.7", activity.EventServerStart, "srv-life", " req-life ")

	var rows []storage.ActivityLog
	if err := store.DB().Where("event = ?", activity.EventServerStart).Find(&rows).Error; err != nil {
		t.Fatalf("query lifecycle audit rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("lifecycle audit rows = %d, want exactly one", len(rows))
	}
	row := rows[0]
	if row.ActorID != "actor-3" || row.ActorName != "carol" {
		t.Fatalf("lifecycle actor = (%q, %q), want actor-3/carol", row.ActorID, row.ActorName)
	}
	if row.IP != "203.0.113.7" || row.Event != activity.EventServerStart {
		t.Fatalf("lifecycle row = ip %q event %q", row.IP, row.Event)
	}
	if row.SubjectTyp != "server" || row.SubjectID != "srv-life" {
		t.Fatalf("lifecycle subject = (%q, %q), want server/srv-life", row.SubjectTyp, row.SubjectID)
	}
	if row.ServerID != "" {
		t.Fatalf("lifecycle server id = %q, want empty as before", row.ServerID)
	}
	if row.Properties != "" {
		t.Fatalf("lifecycle properties = %q, want empty as before", row.Properties)
	}
	if row.RequestID != "req-life" {
		t.Fatalf("lifecycle request id = %q, want trimmed req-life", row.RequestID)
	}
}
