// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package auth

import "context"

// Frozen Phase 0 cross-track contracts. Track A is the sole owner of these
// declarations (internal/auth/hooks.go, aligned with Track B B-00 in
// security-runtime-plans/security-hardening.md §5). Track B implements
// BootstrapAudit from internal/activity and consumes SessionValidator from
// internal/ws. internal/auth does not import ws, rpc or activity, so no cycle
// exists. No other track may redefine either hook.

// BootstrapAudit is the minimal audit sink for first-admin bootstrap and
// recovery-key use. Track B supplies the implementation; Track A calls it.
type BootstrapAudit interface {
	RecordFirstAdmin(ctx context.Context, userID string) error
	RecordRecoveryUse(ctx context.Context, reason, sourceIP string) error
}

// SessionSnapshot is the redacted result of validating a session credential.
// It intentionally contains no token, secret or raw claim values.
type SessionSnapshot struct {
	UserID string
	Roles  []string
	Valid  bool
}

// SessionValidator is the minimal read-only validation hook used by middleware
// outside the Connect interceptor. Track A owns issuance, rotation, revocation
// and the production implementation below.
type SessionValidator interface {
	Validate(ctx context.Context, epochOrTokenID string) (SessionSnapshot, error)
}

// managerSessionValidator is the production SessionValidator. It validates a
// session JWT or a dp_ API token against the same store-backed rules as the RPC
// interceptor, and never grants an identity from an empty or unknown token.
type managerSessionValidator struct {
	manager *Manager
}

// NewSessionValidator returns the production SessionValidator for a manager.
func NewSessionValidator(manager *Manager) SessionValidator {
	return &managerSessionValidator{manager: manager}
}

func (v *managerSessionValidator) Validate(ctx context.Context, epochOrTokenID string) (SessionSnapshot, error) {
	if v == nil || v.manager == nil || epochOrTokenID == "" {
		return SessionSnapshot{Valid: false}, ErrInvalidToken
	}
	var user *AuthenticatedUser
	var err error
	if len(epochOrTokenID) > 3 && epochOrTokenID[:3] == "dp_" {
		user, err = v.manager.ValidateAPIToken(ctx, epochOrTokenID)
	} else {
		user, err = v.manager.ValidateSession(ctx, epochOrTokenID)
	}
	if err != nil || user == nil {
		return SessionSnapshot{Valid: false}, ErrInvalidToken
	}
	roles := append([]string(nil), user.Roles...)
	return SessionSnapshot{UserID: user.ID, Roles: roles, Valid: true}, nil
}
