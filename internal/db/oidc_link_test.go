// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const testOIDCIssuer = "https://issuer.test"

func createLocalOIDCLinkFixture(t *testing.T, store *Store, id string) (*User, *Session) {
	t.Helper()
	ctx := t.Context()
	email := id + "@local.test"
	user := &User{ID: id, Username: id, Email: &email, AuthProvider: "local", IsActive: true}
	require.NoError(t, store.CreateUser(ctx, user))
	require.NoError(t, store.AssignRole(ctx, id, "admin", "local"))
	session := &Session{UserID: id, Token: "fixture-" + id, AuthProvider: "local", ExpiresAt: time.Now().UTC().Add(time.Hour)}
	require.NoError(t, store.CreateSession(ctx, session))
	return user, session
}

func TestLinkOIDCIdentityReconcilesIndependentGrants(t *testing.T) {
	ctx := t.Context()
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	user, session := createLocalOIDCLinkFixture(t, store, "local-admin")
	require.NoError(t, store.AssignRole(ctx, user.ID, "user", "local"))

	eligible, err := store.CanLinkOIDC(ctx, user.ID, session.ID)
	require.NoError(t, err)
	require.True(t, eligible)
	linked, added, removed, err := store.LinkOIDCIdentity(ctx, user.ID, testOIDCIssuer, "sub", session.ID,
		[]string{"user", "admin"}, []string{"admin"})
	require.NoError(t, err)
	require.Equal(t, user.ID, linked.ID)
	require.Equal(t, testOIDCIssuer, linked.OIDCIssuer)
	require.Equal(t, "sub", linked.OIDCSubject)
	require.Equal(t, "local", linked.AuthProvider)
	require.Equal(t, []string{"admin", "user"}, added)
	require.Empty(t, removed)

	eligible, err = store.CanLinkOIDC(ctx, user.ID, session.ID)
	require.NoError(t, err)
	require.False(t, eligible)
	all, err := store.GetUserRoleNames(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"admin", "user"}, all)
	local, err := store.GetUserLocalRoleNames(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, all, local)
	oidc, err := store.GetUserRoleNamesBySource(ctx, user.ID, "oidc")
	require.NoError(t, err)
	require.Equal(t, all, oidc)

	// A normal returning OIDC sign-in keeps the same identity and role origins.
	added, removed, err = store.ReconcileOIDCUserRoles(ctx, user.ID, testOIDCIssuer, "sub",
		"upstream@issuer.test", []string{"admin"}, []string{"admin"})
	require.NoError(t, err)
	require.Empty(t, added)
	require.Equal(t, []string{"user"}, removed)
	_, removed, err = store.ReconcileOIDCUserRoles(ctx, user.ID, testOIDCIssuer, "sub", "", nil, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"admin"}, removed)
	oidc, err = store.GetUserRoleNamesBySource(ctx, user.ID, "oidc")
	require.NoError(t, err)
	require.Empty(t, oidc)
	local, err = store.GetUserLocalRoleNames(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, all, local)
	saved, err := store.GetUser(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, user.Email, saved.Email)

	// A profile edit prepared before binding leaves the identity alone.
	user.Username = "renamed-admin"
	require.NoError(t, store.UpdateUserProfile(ctx, user))
	saved, err = store.GetUserByOIDCIssuerAndSubject(ctx, testOIDCIssuer, "sub")
	require.NoError(t, err)
	require.Equal(t, user.Username, saved.Username)
}

func TestLinkOIDCIdentityRollsBackOnRoleFailure(t *testing.T) {
	ctx := t.Context()
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	user, session := createLocalOIDCLinkFixture(t, store, "local-admin")
	require.NoError(t, store.AssignRole(ctx, user.ID, "previous", "oidc"))
	require.NoError(t, store.db.Exec(`
		CREATE TRIGGER reject_fixture_grant BEFORE INSERT ON user_roles
		WHEN NEW.source = 'oidc' AND NEW.role_name = 'broken'
		BEGIN SELECT RAISE(ABORT, 'fixture rejected grant'); END`).Error)

	linked, added, removed, err := store.LinkOIDCIdentity(ctx, user.ID, testOIDCIssuer, "sub", session.ID,
		[]string{"admin", "broken"}, []string{"admin"})
	require.ErrorContains(t, err, "fixture rejected grant")
	require.Nil(t, linked)
	require.Empty(t, added)
	require.Empty(t, removed)
	after, err := store.GetUser(ctx, user.ID)
	require.NoError(t, err)
	require.Empty(t, after.OIDCIssuer)
	require.Empty(t, after.OIDCSubject)
	require.Nil(t, after.LastLogin)
	oidc, err := store.GetUserRoleNamesBySource(ctx, user.ID, "oidc")
	require.NoError(t, err)
	require.Equal(t, []string{"previous"}, oidc)
	require.NoError(t, store.db.Exec("DROP TRIGGER reject_fixture_grant").Error)
	_, _, _, err = store.LinkOIDCIdentity(ctx, user.ID, testOIDCIssuer, "sub", session.ID, nil, nil)
	require.NoError(t, err)
}

func TestLinkOIDCIdentityRejectsIneligibleProof(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *Store, *User, *Session)
	}{
		{name: "other user session", setup: func(t *testing.T, store *Store, _ *User, session *Session) {
			_, other := createLocalOIDCLinkFixture(t, store, "other-admin")
			session.ID = other.ID
		}},
		{name: "provider session", setup: func(t *testing.T, store *Store, _ *User, session *Session) {
			require.NoError(t, store.db.Model(session).Update("auth_provider", "oidc").Error)
		}},
		{name: "legacy session", setup: func(t *testing.T, store *Store, _ *User, session *Session) {
			require.NoError(t, store.db.Model(session).Update("auth_provider", "").Error)
		}},
		{name: "expired session", setup: func(t *testing.T, store *Store, _ *User, session *Session) {
			require.NoError(t, store.db.Model(session).Update("expires_at", time.Now().UTC().Add(-time.Hour)).Error)
		}},
		{name: "expired session stored with offset", setup: func(t *testing.T, store *Store, _ *User, session *Session) {
			expired := time.Now().Add(-time.Hour).In(time.FixedZone("AEDT", 11*60*60))
			require.NoError(t, store.db.Model(session).Update("expires_at", expired).Error)
		}},
		{name: "revoked session", setup: func(t *testing.T, store *Store, _ *User, session *Session) {
			require.NoError(t, store.DeleteSession(t.Context(), session.Token))
		}},
		{name: "inactive account", setup: func(t *testing.T, store *Store, user *User, _ *Session) {
			user.IsActive = false
			require.NoError(t, store.UpdateUserProfile(t.Context(), user))
		}},
		{name: "OIDC-only admin role", setup: func(t *testing.T, store *Store, user *User, _ *Session) {
			require.NoError(t, store.UnassignRole(t.Context(), user.ID, "admin"))
			require.NoError(t, store.AssignRole(t.Context(), user.ID, "admin", "oidc"))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			store := newTestStore(t)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			user, session := createLocalOIDCLinkFixture(t, store, "local-admin")
			test.setup(t, store, user, session)
			eligible, err := store.CanLinkOIDC(ctx, user.ID, session.ID)
			require.NoError(t, err)
			require.False(t, eligible)
			_, _, _, err = store.LinkOIDCIdentity(ctx, user.ID, testOIDCIssuer, "sub", session.ID, nil, nil)
			require.ErrorIs(t, err, ErrOIDCLinkIneligible)
			saved, err := store.GetUser(ctx, user.ID)
			require.NoError(t, err)
			require.Empty(t, saved.OIDCSubject)
		})
	}
}

func TestLinkOIDCIdentitySerializesConflictingLinks(t *testing.T) {
	for _, collision := range []string{"same subject", "same account"} {
		t.Run(collision, func(t *testing.T) {
			ctx := t.Context()
			store := newTestStore(t)
			t.Cleanup(func() { require.NoError(t, store.Close()) })
			first, firstSession := createLocalOIDCLinkFixture(t, store, "first")
			second, secondSession, secondSubject := first, firstSession, "other-sub"
			if collision == "same subject" {
				second, secondSession = createLocalOIDCLinkFixture(t, store, "second")
				secondSubject = "sub"
			}
			type attempt struct {
				userID, sessionID, subject, role string
			}
			attempts := []attempt{
				{first.ID, firstSession.ID, "sub", "first-grant"},
				{second.ID, secondSession.ID, secondSubject, "second-grant"},
			}
			start := make(chan struct{})
			type result struct {
				attempt
				err error
			}
			results := make(chan result, len(attempts))
			for _, a := range attempts {
				go func() {
					<-start
					_, _, _, err := store.LinkOIDCIdentity(ctx, a.userID, testOIDCIssuer, a.subject, a.sessionID, []string{a.role}, nil)
					results <- result{a, err}
				}()
			}
			close(start)
			var winner attempt
			var conflicts int
			for range attempts {
				outcome := <-results
				switch {
				case outcome.err == nil:
					require.Empty(t, winner.userID, "more than one link succeeded")
					winner = outcome.attempt
				case errors.Is(outcome.err, ErrOIDCLinkRequired):
					conflicts++
				default:
					t.Fatalf("unexpected link result: %v", outcome.err)
				}
			}
			require.NotEmpty(t, winner.userID)
			require.Equal(t, 1, conflicts)
			linked, err := store.GetUserByOIDCIssuerAndSubject(ctx, testOIDCIssuer, winner.subject)
			require.NoError(t, err)
			require.Equal(t, winner.userID, linked.ID)
			roles, err := store.GetUserRoleNamesBySource(ctx, winner.userID, "oidc")
			require.NoError(t, err)
			require.Equal(t, []string{winner.role}, roles)
		})
	}
}

func TestReconcileOIDCUserRolesRefusesExistingPrivilegedConflicts(t *testing.T) {
	ctx := t.Context()
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	user := &User{Username: "remote-admin", OIDCIssuer: testOIDCIssuer, OIDCSubject: "sub", AuthProvider: "oidc", IsActive: true}
	require.NoError(t, store.CreateUser(ctx, user))
	require.NoError(t, store.AssignRole(ctx, user.ID, "admin", "local"))
	require.NoError(t, store.AssignRole(ctx, user.ID, "user", "oidc"))

	added, removed, err := store.ReconcileOIDCUserRoles(ctx, user.ID, testOIDCIssuer, "sub", "changed@issuer.test", []string{"admin"}, []string{"admin"})
	require.ErrorIs(t, err, ErrOIDCRoleConflict)
	require.Empty(t, added)
	require.Empty(t, removed)
	roles, err := store.GetUserRoleNamesBySource(ctx, user.ID, "oidc")
	require.NoError(t, err)
	require.Equal(t, []string{"user"}, roles)
	saved, err := store.GetUser(ctx, user.ID)
	require.NoError(t, err)
	require.Nil(t, saved.LastLogin)
	require.Nil(t, saved.Email)
	_, _, err = store.ReconcileOIDCUserRoles(ctx, user.ID, testOIDCIssuer, "unknown-sub", "", nil, nil)
	require.ErrorIs(t, err, gorm.ErrRecordNotFound)
}

func TestOIDCLinkMigrationPreservesLegacyGrantsAndSession(t *testing.T) {
	ctx := t.Context()
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	user, session := createLocalOIDCLinkFixture(t, store, "legacy-admin")
	require.NoError(t, store.db.Exec("ALTER TABLE sessions DROP COLUMN auth_provider").Error)
	require.NoError(t, store.db.Exec("DROP INDEX idx_user_role_source").Error)
	require.NoError(t, store.db.Create(&UserRole{ID: "legacy-duplicate", UserID: user.ID, RoleName: "admin", Source: "local"}).Error)
	require.NoError(t, store.Migrate())

	existing, err := store.GetSession(ctx, session.Token)
	require.NoError(t, err)
	require.Equal(t, session.ID, existing.ID)
	require.Empty(t, existing.AuthProvider)
	eligible, err := store.CanLinkOIDC(ctx, user.ID, session.ID)
	require.NoError(t, err)
	require.False(t, eligible)
	require.NoError(t, store.AssignRole(ctx, user.ID, "admin", "local"))
	require.NoError(t, store.AssignRole(ctx, user.ID, "admin", "oidc"))
	var localCount int64
	require.NoError(t, store.db.Model(&UserRole{}).
		Where("user_id = ? AND role_name = ? AND source = ?", user.ID, "admin", "local").
		Count(&localCount).Error)
	require.EqualValues(t, 2, localCount, "old rows are preserved and new exact duplicates are suppressed")
	roles, err := store.GetUserRoleNames(ctx, user.ID)
	require.NoError(t, err)
	require.Equal(t, []string{"admin"}, roles)
}

func TestCleanExpiredSessionsUsesAbsoluteExpiry(t *testing.T) {
	ctx := t.Context()
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	user, _ := createLocalOIDCLinkFixture(t, store, "offset-admin")
	now := time.Now()
	for _, session := range []Session{
		{ID: "live", UserID: user.ID, Token: "live", ExpiresAt: now.Add(time.Hour).In(time.FixedZone("west", -11*3600))},
		{ID: "expired", UserID: user.ID, Token: "expired", ExpiresAt: now.Add(-time.Hour).In(time.FixedZone("east", 11*3600))},
	} {
		require.NoError(t, store.CreateSession(ctx, &session))
	}
	require.NoError(t, store.CleanExpiredSessions(ctx))
	for id, expected := range map[string]int64{"live": 1, "expired": 0} {
		var count int64
		require.NoError(t, store.db.Model(&Session{}).Where("id = ?", id).Count(&count).Error)
		require.Equal(t, expected, count, id)
	}
}

func TestUserRoleGrantAssignmentSerializesExactSources(t *testing.T) {
	ctx := t.Context()
	store := newTestStore(t)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	user, _ := createLocalOIDCLinkFixture(t, store, "local-admin")

	const calls = 12
	start := make(chan struct{})
	errs := make(chan error, calls)
	for range calls {
		go func() {
			<-start
			errs <- store.AssignRole(ctx, user.ID, "admin", "oidc")
		}()
	}
	close(start)
	for range calls {
		require.NoError(t, <-errs)
	}
	var grants []UserRole
	require.NoError(t, store.db.Where("user_id = ? AND role_name = ?", user.ID, "admin").
		Order("source").Find(&grants).Error)
	require.Len(t, grants, 2)
	require.Equal(t, "local", grants[0].Source)
	require.Equal(t, "oidc", grants[1].Source)
}
