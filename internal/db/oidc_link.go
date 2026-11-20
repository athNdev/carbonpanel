// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

const oidcRoleSource = "oidc"

var (
	ErrOIDCLinkRequired   = errors.New("OIDC identity or local account already bound")
	ErrOIDCLinkIneligible = errors.New("OIDC link requires an active local administrator session")
	ErrOIDCRoleConflict   = errors.New("OIDC role conflicts with an existing privileged grant")
)

func eligibleOIDCLinkTarget(tx *gorm.DB, userID, sessionID string) *gorm.DB {
	return tx.Model(&User{}).
		Where("users.id = ? AND users.is_active = ? AND users.auth_provider = ?", userID, true, "local").
		Where(`EXISTS (SELECT 1 FROM user_roles
			WHERE user_id = users.id AND role_name = ? AND source = ?)`, "admin", "local").
		Where(`EXISTS (SELECT 1 FROM sessions
			WHERE id = ? AND user_id = users.id AND auth_provider = ?)`, sessionID, "local")
}

// CanLinkOIDC checks whether the identified session can authorize a link
// attempt. LinkOIDCIdentity repeats the same check when committing the link.
func (s *Store) CanLinkOIDC(ctx context.Context, userID, sessionID string) (bool, error) {
	db := s.db.WithContext(ctx)
	var count int64
	err := unboundOIDCLinkTarget(eligibleOIDCLinkTarget(db, userID, sessionID)).
		Count(&count).Error
	if err != nil || count == 0 {
		return false, err
	}
	return localOIDCLinkSessionUnexpired(db, userID, sessionID)
}

func unboundOIDCLinkTarget(tx *gorm.DB) *gorm.DB {
	return tx.Where("COALESCE(users.oidc_issuer, '') = '' AND COALESCE(users.oidc_subject, '') = ''")
}

func localOIDCLinkSessionUnexpired(tx *gorm.DB, userID, sessionID string) (bool, error) {
	var session Session
	err := tx.Select("expires_at").Take(&session, "id = ? AND user_id = ? AND auth_provider = ?", sessionID, userID, "local").Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	// SQLite stores timestamps with their original timezone offsets.
	return session.ExpiresAt.After(tx.NowFunc()), err
}

// LinkOIDCIdentity binds a verified issuer and subject, then reconciles its
// roles in the same transaction. The initiating local session and its local
// admin grant must still exist when this operation takes the writer lock.
func (s *Store) LinkOIDCIdentity(ctx context.Context, userID, issuer, subject, sessionID string, desired, privileged []string) (*User, []string, []string, error) {
	if userID == "" || issuer == "" || subject == "" || sessionID == "" {
		return nil, nil, nil, ErrOIDCLinkIneligible
	}

	var user User
	var added, removed []string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Start with a write so two callbacks cannot both observe an unbound
		// account or subject. A same-subject row with an unknown or different
		// issuer also blocks explicit linking.
		result := unboundOIDCLinkTarget(eligibleOIDCLinkTarget(tx, userID, sessionID)).
			Where("NOT EXISTS (SELECT 1 FROM users AS claimed WHERE claimed.oidc_subject = ?)", subject).
			Updates(map[string]any{"oidc_issuer": issuer, "oidc_subject": subject})
		if result.Error != nil {
			return result.Error
		}
		live, err := localOIDCLinkSessionUnexpired(tx, userID, sessionID)
		if err != nil {
			return err
		}
		if !live {
			return ErrOIDCLinkIneligible
		}
		if result.RowsAffected == 0 {
			var eligible int64
			if err := eligibleOIDCLinkTarget(tx, userID, sessionID).Count(&eligible).Error; err != nil {
				return err
			}
			if eligible != 0 {
				return ErrOIDCLinkRequired
			}
			return ErrOIDCLinkIneligible
		}
		if err := tx.First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		added, removed, err = reconcileOIDCGrants(tx, userID, desired, privileged, true)
		return err
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return &user, added, removed, nil
}

// ReconcileOIDCUserRoles applies grants for an already bound, active identity.
// A provider may refresh its own account's email. An explicitly linked local
// account keeps the profile administered in Carbon Panel.
func (s *Store) ReconcileOIDCUserRoles(ctx context.Context, userID, issuer, subject, email string, desired, privileged []string) (added, removed []string, err error) {
	if issuer == "" || subject == "" {
		return nil, nil, gorm.ErrRecordNotFound
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		fields := map[string]any{"last_login": tx.NowFunc()}
		if email != "" {
			fields["email"] = gorm.Expr("CASE WHEN auth_provider = ? THEN ? ELSE email END", "oidc", email)
		}
		result := tx.Model(&User{}).
			Where("id = ? AND oidc_issuer = ? AND oidc_subject = ? AND is_active = ?", userID, issuer, subject, true).
			Updates(fields)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		var user User
		if err := tx.Select("auth_provider").First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		added, removed, err = reconcileOIDCGrants(tx, userID, desired, privileged, user.AuthProvider == "local")
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return added, removed, nil
}

func reconcileOIDCGrants(tx *gorm.DB, userID string, desired, privileged []string, linkedLocal bool) ([]string, []string, error) {
	if len(privileged) != 0 {
		grants := tx.Model(&UserRole{}).
			Where("user_id = ? AND role_name IN ? AND source <> ?", userID, privileged, oidcRoleSource)
		if linkedLocal {
			grants = grants.Where("source <> ?", "local")
		}
		var conflicts int64
		if err := grants.Count(&conflicts).Error; err != nil {
			return nil, nil, err
		}
		if conflicts != 0 {
			return nil, nil, ErrOIDCRoleConflict
		}
	}
	return setUserRolesBySource(tx, userID, oidcRoleSource, desired)
}
