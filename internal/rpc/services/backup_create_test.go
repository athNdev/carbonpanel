// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	appconfig "github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func createBackupTestServer(t *testing.T, store *storage.Store, ctx context.Context, id string) {
	t.Helper()
	if err := store.CreateServer(ctx, &storage.Server{
		ID: id, Name: id, DataPath: t.TempDir(), NodeID: "local",
	}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
}

func TestCreateBackupFailsClosedWithoutScheduler(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	svc := NewBackupService(store, nil, nil, appconfig.S3Config{}, logger.New())

	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	svc.SetEnforcer(enforcer)

	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: "u-admin", Username: "admin", Roles: []string{"admin"},
	})
	createBackupTestServer(t, store, ctx, "srv-1")

	_, err = svc.CreateBackup(ctx, connect.NewRequest(&v1.CreateBackupRequest{ServerId: "srv-1"}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("expected unavailable without a scheduler, got %v (%v)", connect.CodeOf(err), err)
	}
}

func TestCreateBackupChecksServerScope(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	svc := NewBackupService(store, nil, nil, appconfig.S3Config{}, logger.New())

	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	svc.SetEnforcer(enforcer)

	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: "u-admin", Username: "admin", Roles: []string{"admin"},
	})
	createBackupTestServer(t, store, ctx, "srv-1")

	readerCtx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: "u-reader", Username: "reader", Roles: []string{"reader"},
	})
	if _, err := svc.CreateBackup(readerCtx, connect.NewRequest(&v1.CreateBackupRequest{ServerId: "srv-1"})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("expected permission_denied without a create grant, got %v (%v)", connect.CodeOf(err), err)
	}

	if _, err := svc.CreateBackup(ctx, connect.NewRequest(&v1.CreateBackupRequest{ServerId: "nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected not_found for a missing server, got %v (%v)", connect.CodeOf(err), err)
	}
}
