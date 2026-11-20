// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"reflect"
	"testing"
)

func TestSetUserRolesBySourcePreservesOtherSources(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer func() { _ = store.Close() }()

	if err := store.DB().Create(&User{ID: "u1", Username: "alice", AuthProvider: "oidc", IsActive: true}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.AssignRole(ctx, "u1", "admin", "local"); err != nil {
		t.Fatalf("assign local admin: %v", err)
	}
	if err := store.AssignRole(ctx, "u1", "user", "oidc"); err != nil {
		t.Fatalf("assign oidc user: %v", err)
	}

	added, removed, err := store.SetUserRolesBySource(ctx, "u1", "oidc", []string{"viewer"})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !reflect.DeepEqual(added, []string{"viewer"}) || !reflect.DeepEqual(removed, []string{"user"}) {
		t.Fatalf("delta added=%v removed=%v", added, removed)
	}
	names, err := store.GetUserRoleNames(ctx, "u1")
	if err != nil || !reflect.DeepEqual(names, []string{"admin", "viewer"}) {
		t.Fatalf("roles=%v err=%v", names, err)
	}
	if roles, err := store.GetUserRoleNamesBySource(ctx, "u1", "local"); err != nil || !reflect.DeepEqual(roles, []string{"admin"}) {
		t.Fatalf("local roles=%v err=%v", roles, err)
	}
	if roles, err := store.GetUserRoleNamesBySource(ctx, "u1", "oidc"); err != nil || !reflect.DeepEqual(roles, []string{"viewer"}) {
		t.Fatalf("oidc roles=%v err=%v", roles, err)
	}
}

func TestSetUserRolesBySourceIdempotentAndEmpty(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer func() { _ = store.Close() }()
	if err := store.DB().Create(&User{ID: "u2", Username: "bob", AuthProvider: "oidc", IsActive: true}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := store.AssignRole(ctx, "u2", "admin", "admin"); err != nil {
		t.Fatalf("assign admin: %v", err)
	}

	added, removed, err := store.SetUserRolesBySource(ctx, "u2", "oidc", []string{"user", "user"})
	if err != nil || !reflect.DeepEqual(added, []string{"user"}) || len(removed) != 0 {
		t.Fatalf("first reconcile added=%v removed=%v err=%v", added, removed, err)
	}
	added, removed, err = store.SetUserRolesBySource(ctx, "u2", "oidc", []string{"user"})
	if err != nil || len(added) != 0 || len(removed) != 0 {
		t.Fatalf("idempotent reconcile added=%v removed=%v err=%v", added, removed, err)
	}
	// Absent OIDC claim (empty desired set) revokes only OIDC-sourced roles.
	added, removed, err = store.SetUserRolesBySource(ctx, "u2", "oidc", nil)
	if err != nil || len(added) != 0 || !reflect.DeepEqual(removed, []string{"user"}) {
		t.Fatalf("empty reconcile added=%v removed=%v err=%v", added, removed, err)
	}
	names, err := store.GetUserRoleNames(ctx, "u2")
	if err != nil || !reflect.DeepEqual(names, []string{"admin"}) {
		t.Fatalf("roles after empty=%v err=%v", names, err)
	}
}

func TestGetUserRoleNamesBySourceMissing(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	defer func() { _ = store.Close() }()
	if roles, err := store.GetUserRoleNamesBySource(ctx, "ghost", "local"); err != nil || len(roles) != 0 {
		t.Fatalf("roles=%v err=%v", roles, err)
	}
}
