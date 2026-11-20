// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type linkServiceFixture struct {
	store   *storage.Store
	handler *auth.OIDCHandler
	service *UserService
	adminID string
	plainID string
	oidcID  string
}

func setupLinkService(t *testing.T) *linkServiceFixture {
	t.Helper()
	dbName := strings.NewReplacer("/", "_", " ", "_").Replace(t.Name())
	store, err := storage.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{
			Path:           fmt.Sprintf("file:%s?mode=memory&cache=shared", dbName),
			AutoMigrate:    true,
			MaxConnections: 5,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	require.NoError(t, store.Migrate())

	m, err := auth.NewManager(store, nil, &config.AuthConfig{
		SessionTimeout: 3600,
		JWTSecret:      "explicit-link-test-secret-0123456789",
		Local:          config.LocalConfig{Enabled: true},
	})
	require.NoError(t, err)

	admin, err := m.CreateLocalUser(context.Background(), "link-admin", "admin@example.test", "ExplicitLink!2345")
	require.NoError(t, err)
	require.NoError(t, store.AssignRole(context.Background(), admin.ID, "admin", "local"))
	plain, err := m.CreateLocalUser(context.Background(), "link-plain", "", "PlainLink!2345")
	require.NoError(t, err)
	oidcUser := &storage.User{ID: "link-oidc-admin", Username: "oidc-admin", AuthProvider: "oidc", OIDCIssuer: "https://issuer.example", OIDCSubject: "oidc-sub", IsActive: true}
	require.NoError(t, store.CreateUser(context.Background(), oidcUser))
	require.NoError(t, store.AssignRole(context.Background(), oidcUser.ID, "admin", "oidc"))

	handler, err := auth.NewOIDCHandler(m, store, &config.OIDCConfig{}, logger.New())
	require.NoError(t, err)
	svc := NewUserService(store, m, logger.New())
	svc.SetOIDCLinkStore(handler.LinkStore())
	return &linkServiceFixture{store: store, handler: handler, service: svc, adminID: admin.ID, plainID: plain.ID, oidcID: oidcUser.ID}
}

func TestBeginOIDCLinkRequiresAuthenticatedLocalAdmin(t *testing.T) {
	fx := setupLinkService(t)
	req := connect.NewRequest(&v1.BeginOIDCLinkRequest{})

	resp, err := fx.service.BeginOIDCLink(auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: fx.adminID, Username: "link-admin", Provider: "local", Roles: []string{"admin"},
	}), req)
	require.NoError(t, err)
	require.NotEmpty(t, resp.Msg.LinkToken)

	_, err = fx.service.BeginOIDCLink(context.Background(), req)
	require.Equal(t, connect.CodeUnauthenticated, connectCode(t, err))

	_, err = fx.service.BeginOIDCLink(auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: fx.plainID, Username: "link-plain", Provider: "local",
	}), req)
	require.Equal(t, connect.CodePermissionDenied, connectCode(t, err))

	_, err = fx.service.BeginOIDCLink(auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: fx.oidcID, Username: "oidc-admin", Provider: "oidc", Roles: []string{"admin"},
	}), req)
	require.Equal(t, connect.CodePermissionDenied, connectCode(t, err))
}

func TestBeginOIDCLinkFailsClosedWithoutStore(t *testing.T) {
	fx := setupLinkService(t)
	svc := NewUserService(fx.store, fx.service.authManager, logger.New())
	_, err := svc.BeginOIDCLink(auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: fx.adminID, Username: "link-admin", Provider: "local", Roles: []string{"admin"},
	}), connect.NewRequest(&v1.BeginOIDCLinkRequest{}))
	require.Equal(t, connect.CodeFailedPrecondition, connectCode(t, err))
}
