// SPDX-License-Identifier: MPL-2.0

package db

import (
	"context"
	"errors"
	"testing"
)

func TestClaimArchiveImportIsAtomicWithIdempotency(t *testing.T) {
	store := setupTestStoreForArchiveImport(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	first, claimed, err := store.ClaimArchiveImport(ctx, "request-key", "kind-a", "srv-1", "data/servers/one_srv-1", "digest-a", "upload-a")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed || first.ClaimToken == "" || first.Phase != ArchiveImportClaimed {
		t.Fatalf("first claim = %+v claimed=%v", first, claimed)
	}

	key, err := store.GetIdempotencyKey(ctx, "request-key")
	if err != nil || key == nil || key.ServerID != "srv-1" || key.Kind != "kind-a" {
		t.Fatalf("idempotency row = %+v, err=%v", key, err)
	}

	second, claimed, err := store.ClaimArchiveImport(ctx, "request-key", "kind-a", "srv-2", "data/servers/two_srv-2", "digest-a", "upload-a")
	if err != nil {
		t.Fatalf("repeat claim: %v", err)
	}
	if claimed || second.ServerID != "srv-1" || second.DataPath != "data/servers/one_srv-1" {
		t.Fatalf("repeat claim = %+v claimed=%v", second, claimed)
	}

	if _, _, err := store.ClaimArchiveImport(ctx, "request-key", "kind-b", "srv-3", "data/servers/three_srv-3", "digest-b", "upload-b"); !errors.Is(err, ErrRequestIDReused) {
		t.Fatalf("different kind = %v, want ErrRequestIDReused", err)
	}
}

func TestArchiveImportNodeAndPhaseAreTargeted(t *testing.T) {
	store := setupTestStoreForArchiveImport(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	intent, claimed, err := store.ClaimArchiveImport(ctx, "request-key", "kind", "srv-1", "data/servers/one_srv-1", "digest", "upload")
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}
	if err := store.SetArchiveImportNode(ctx, intent.RequestKey, "node-1"); err != nil {
		t.Fatalf("set node: %v", err)
	}
	if err := store.SetArchiveImportPhase(ctx, intent.RequestKey, ArchiveImportStaged); err != nil {
		t.Fatalf("set phase: %v", err)
	}
	current, err := store.GetArchiveImport(ctx, intent.RequestKey)
	if err != nil {
		t.Fatalf("get intent: %v", err)
	}
	if current.NodeID != "node-1" || current.Phase != ArchiveImportStaged || current.ClaimToken != intent.ClaimToken || current.DataPath != intent.DataPath {
		t.Fatalf("intent = %+v", current)
	}
	if err := store.DeleteArchiveImport(ctx, intent.RequestKey); err != nil {
		t.Fatalf("delete intent: %v", err)
	}
	if _, err := store.GetArchiveImport(ctx, intent.RequestKey); !errors.Is(err, ErrArchiveImportUnproven) {
		t.Fatalf("missing intent = %v, want ErrArchiveImportUnproven", err)
	}
}

func TestReleaseArchiveImportClaimRequiresOwnership(t *testing.T) {
	store := setupTestStoreForArchiveImport(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	intent, claimed, err := store.ClaimArchiveImport(ctx, "request-key", "kind", "srv-1", "data/servers/one_srv-1", "digest", "upload")
	if err != nil || !claimed {
		t.Fatalf("claim: %v claimed=%v", err, claimed)
	}

	released, err := store.ReleaseArchiveImportClaim(ctx, intent.RequestKey, intent.Kind, "wrong-token")
	if err != nil || released {
		t.Fatalf("wrong owner release = %v, %v; want false, nil", released, err)
	}
	if _, err := store.GetIdempotencyKey(ctx, intent.RequestKey); err != nil {
		t.Fatalf("claim removed by wrong owner: %v", err)
	}
	if _, err := store.GetArchiveImport(ctx, intent.RequestKey); err != nil {
		t.Fatalf("intent removed by wrong owner: %v", err)
	}

	released, err = store.ReleaseArchiveImportClaim(ctx, intent.RequestKey, intent.Kind, intent.ClaimToken)
	if err != nil || !released {
		t.Fatalf("owner release = %v, %v; want true, nil", released, err)
	}
	if key, err := store.GetIdempotencyKey(ctx, intent.RequestKey); err != nil || key != nil {
		t.Fatalf("claim survived release: %+v, %v", key, err)
	}
	if _, err := store.GetArchiveImport(ctx, intent.RequestKey); !errors.Is(err, ErrArchiveImportUnproven) {
		t.Fatalf("intent survived release: %v", err)
	}
}

func TestServerExistsDistinguishesAbsence(t *testing.T) {
	store := setupTestStoreForArchiveImport(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()

	exists, err := store.ServerExists(ctx, "missing")
	if err != nil || exists {
		t.Fatalf("missing server = %v, %v", exists, err)
	}
	if err := store.CreateServer(ctx, &Server{ID: "present", Name: "present"}); err != nil {
		t.Fatalf("create server: %v", err)
	}
	exists, err = store.ServerExists(ctx, "present")
	if err != nil || !exists {
		t.Fatalf("present server = %v, %v", exists, err)
	}
}

func setupTestStoreForArchiveImport(t *testing.T) *Store {
	t.Helper()
	return newTestStore(t)
}
