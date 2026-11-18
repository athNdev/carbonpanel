// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package ws

import (
	"testing"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

func TestRevokeSocketIdentityClearsPrivilegedState(t *testing.T) {
	c := &Client{
		hub:           &Hub{log: logger.New()},
		send:          make(chan []byte, 4),
		subscriptions: map[string]*subscription{},
		authenticated: true,
		user:          &auth.AuthenticatedUser{ID: "u-admin", Username: "admin", Roles: []string{"admin"}},
	}

	c.revokeSocketIdentity("invalid token")

	if c.authenticated {
		t.Fatal("failed re-authentication must clear authenticated state")
	}
	if c.user != nil {
		t.Fatal("failed re-authentication must clear the previous privileged identity")
	}

	msgs := drainSend(t, c)
	if len(msgs) == 0 {
		t.Fatal("AUTH_FAIL control message was not sent")
	}
	if got := unmarshalServerMessage(t, msgs[0]); got.GetAuthFail() == nil {
		t.Fatalf("expected AUTH_FAIL, got %T", got.Type)
	}
}
