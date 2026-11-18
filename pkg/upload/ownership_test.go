// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package upload

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/pkg/logger"
)

func ownershipManager(t *testing.T) *Manager {
	t.Helper()
	m := NewManager(t.TempDir(), time.Hour, 0, logger.New())
	t.Cleanup(m.Stop)
	return m
}

// A session created with a real user context must refuse every operation
// attempted by a different user, even when that user knows the session ID.
func TestOwnerBoundSessionRefusesOtherUser(t *testing.T) {
	m := ownershipManager(t)
	sess, err := m.InitSessionFor("payload.bin", 8, 4, "user-a")
	if err != nil {
		t.Fatalf("InitSessionFor: %v", err)
	}
	if sess.OwnerUserID != "user-a" {
		t.Fatalf("OwnerUserID = %q, want user-a", sess.OwnerUserID)
	}

	if _, err := m.GetSessionFor(sess.ID, "user-b"); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("GetSessionFor cross-user err = %v, want ErrOwnerMismatch", err)
	}
	if _, err := m.WriteChunkFor(sess.ID, "user-b", 0, []byte("abcd")); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("WriteChunkFor cross-user err = %v, want ErrOwnerMismatch", err)
	}
	if _, _, err := m.WriteStreamFor(sess.ID, "user-b", strings.NewReader("abcd"), 0); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("WriteStreamFor cross-user err = %v, want ErrOwnerMismatch", err)
	}
	if _, err := m.GetMissingChunksFor(sess.ID, "user-b"); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("GetMissingChunksFor cross-user err = %v, want ErrOwnerMismatch", err)
	}
	if _, _, err := m.GetTempPathFor(sess.ID, "user-b"); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("GetTempPathFor cross-user err = %v, want ErrOwnerMismatch", err)
	}
	if _, _, _, _, _, _, err := m.GetSessionStatusFor(sess.ID, "user-b"); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("GetSessionStatusFor cross-user err = %v, want ErrOwnerMismatch", err)
	}
	if err := m.CancelFor(sess.ID, "user-b"); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("CancelFor cross-user err = %v, want ErrOwnerMismatch", err)
	}
	if err := m.CleanupSessionFor(sess.ID, "user-b"); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("CleanupSessionFor cross-user err = %v, want ErrOwnerMismatch", err)
	}

	// The rejected write must not have touched the owner's session.
	if got, err := m.GetSessionFor(sess.ID, "user-a"); err != nil {
		t.Fatalf("owner lookup after cross-user attempts: %v", err)
	} else if got.BytesReceived != 0 {
		t.Fatalf("cross-user write changed BytesReceived to %d", got.BytesReceived)
	}
}

// A missing user context must not read or mutate a session bound to a real
// user: anonymous access fails closed.
func TestOwnerBoundSessionRefusesMissingUser(t *testing.T) {
	m := ownershipManager(t)
	sess, err := m.InitSessionFor("payload.bin", 4, 4, "user-a")
	if err != nil {
		t.Fatalf("InitSessionFor: %v", err)
	}
	if _, err := m.GetSessionFor(sess.ID, ""); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("anonymous lookup err = %v, want ErrOwnerMismatch", err)
	}
	if _, err := m.WriteChunkFor(sess.ID, "", 0, []byte("abcd")); !errors.Is(err, ErrOwnerMismatch) {
		t.Errorf("anonymous write err = %v, want ErrOwnerMismatch", err)
	}
}

// The owner can complete an ordinary resumable upload through the owner-aware
// seam; the session ID alone still works for legacy unowned sessions.
func TestOwnerBoundSessionAllowsOwnerAndLegacySessions(t *testing.T) {
	m := ownershipManager(t)

	owned, err := m.InitSessionFor("owned.bin", 8, 4, "user-a")
	if err != nil {
		t.Fatalf("InitSessionFor: %v", err)
	}
	if _, err := m.WriteChunkFor(owned.ID, "user-a", 0, []byte("abcd")); err != nil {
		t.Fatalf("owner chunk write: %v", err)
	}
	if _, _, err := m.WriteStreamFor(owned.ID, "user-a", strings.NewReader("efgh"), 4); err != nil {
		t.Fatalf("owner stream write: %v", err)
	}
	if _, _, err := m.GetTempPathFor(owned.ID, "user-a"); err != nil {
		t.Fatalf("owner temp path: %v", err)
	}
	if err := m.CleanupSessionFor(owned.ID, "user-a"); err != nil {
		t.Fatalf("owner cleanup: %v", err)
	}
	if err := m.CancelFor(owned.ID, "user-a"); err == nil {
		t.Error("cleaned-up session must no longer be cancellable")
	}

	cancellable, err := m.InitSessionFor("cancel.bin", 4, 4, "user-a")
	if err != nil {
		t.Fatalf("InitSessionFor: %v", err)
	}
	if err := m.CancelFor(cancellable.ID, "user-a"); err != nil {
		t.Fatalf("owner cancel: %v", err)
	}

	legacy, err := m.InitSession("legacy.bin", 4, 4)
	if err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	if legacy.OwnerUserID != "" {
		t.Fatalf("legacy OwnerUserID = %q, want empty", legacy.OwnerUserID)
	}
	if _, err := m.GetSessionFor(legacy.ID, "user-b"); err != nil {
		t.Fatalf("legacy session lookup by other user: %v", err)
	}
}
