// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func newActivityTestService(t *testing.T) (*ActivityService, *storage.Store) {
	t.Helper()
	store, err := storage.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{
			Path:        filepath.Join(t.TempDir(), "activity.db"),
			AutoMigrate: true,
		},
		Docker: config.DockerConfig{Host: "unix:///var/run/docker.sock"},
	})
	if err != nil {
		t.Fatalf("create test store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return NewActivityService(store, logger.New()), store
}

func TestListActivityLogsReturnsRequestIDForTheRequestedServerOnly(t *testing.T) {
	service, store := newActivityTestService(t)
	db := store.DB()
	if err := activity.Log(db, activity.Entry{
		Event: activity.EventServerStart, SubjectTyp: "server", SubjectID: "srv-1",
		RequestID: "req-join-1",
	}); err != nil {
		t.Fatalf("log srv-1: %v", err)
	}
	if err := activity.Log(db, activity.Entry{
		Event: activity.EventServerStart, SubjectTyp: "server", SubjectID: "srv-2",
		RequestID: "req-join-2",
	}); err != nil {
		t.Fatalf("log srv-2: %v", err)
	}

	res, err := service.ListActivityLogs(context.Background(), connect.NewRequest(&v1.ListActivityLogsRequest{
		ServerId: "srv-1",
		Limit:    10,
	}))
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	if len(res.Msg.Entries) != 1 {
		t.Fatalf("entries = %d, want only srv-1", len(res.Msg.Entries))
	}
	got := res.Msg.Entries[0]
	if got.SubjectId != "srv-1" {
		t.Fatalf("subject = %q, want srv-1", got.SubjectId)
	}
	if got.RequestId != "req-join-1" {
		t.Fatalf("request id = %q, want req-join-1 so the journal can join", got.RequestId)
	}
	if got.RequestId == "req-join-2" {
		t.Fatal("other server's request id leaked")
	}
}

func TestResolveOperationRecordsActivityWithRequestID(t *testing.T) {
	ops, store := newJournalTestService(t)
	op := beginInterrupted(t, store, "srv-1", "start")

	if _, err := ops.ResolveOperation(context.Background(), connect.NewRequest(&v1.ResolveOperationRequest{
		ServerId:    "srv-1",
		OperationId: op.ID,
		Note:        "checked container",
	})); err != nil {
		t.Fatalf("ResolveOperation: %v", err)
	}

	activitySvc := NewActivityService(store, logger.New())
	res, err := activitySvc.ListActivityLogs(context.Background(), connect.NewRequest(&v1.ListActivityLogsRequest{
		ServerId: "srv-1",
		Limit:    10,
	}))
	if err != nil {
		t.Fatalf("ListActivityLogs: %v", err)
	}
	if len(res.Msg.Entries) != 1 {
		t.Fatalf("entries = %d, want the resolve audit row", len(res.Msg.Entries))
	}
	if got := res.Msg.Entries[0].Event; got != activity.EventServerOperationResolve {
		t.Fatalf("event = %q, want %s", got, activity.EventServerOperationResolve)
	}
	if got := res.Msg.Entries[0].RequestId; got == "" {
		t.Fatal("resolve activity is missing the operation request id")
	}
}
