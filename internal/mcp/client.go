// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

const (
	// TokenEnv is the primary API-token environment variable. It mirrors the
	// existing cmd/status CLI convention.
	TokenEnv = "CARBONPANEL_API_TOKEN"
	// TokenEnvAlt is an accepted alias for client configurations that use the
	// generic Carbon Panel token name.
	TokenEnvAlt = "CARBONPANEL_TOKEN"
	// URLEnv is the controller base URL for generated Connect clients.
	URLEnv = "CARBONPANEL_URL"
)

// TokenFromEnv reads the API token and fails closed when it is absent or does
// not look like an API token. Session cookies, anonymous access and the
// no-auth admin bypass are never accepted.
func TokenFromEnv() (string, error) {
	token := strings.TrimSpace(os.Getenv(TokenEnv))
	if token == "" {
		token = strings.TrimSpace(os.Getenv(TokenEnvAlt))
	}
	if token == "" {
		return "", fmt.Errorf("%s is required (an API token beginning with dp_)", TokenEnv)
	}
	if !strings.HasPrefix(token, "dp_") || len(token) <= len("dp_") {
		return "", fmt.Errorf("%s must be an API token beginning with dp_", TokenEnv)
	}
	return token, nil
}

// ControllerURLFromEnv reads the controller base URL and fails closed when it
// is missing, so the MCP server never guesses an endpoint.
func ControllerURLFromEnv() (string, error) {
	url := strings.TrimSpace(os.Getenv(URLEnv))
	if url == "" {
		return "", fmt.Errorf("%s is required", URLEnv)
	}
	return strings.TrimRight(url, "/"), nil
}

// DefaultRequestTimeout bounds every controller call so a hung or disconnected
// backend fails instead of holding an MCP request open indefinitely.
const DefaultRequestTimeout = 30 * time.Second

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: DefaultRequestTimeout}
}

// Clients owns the typed generated Connect clients used by the tools. There
// is exactly one client instance per service; no hand-rolled HTTP is used.
type Clients struct {
	Auth      carbonpanelv1connect.AuthServiceClient
	Server    carbonpanelv1connect.ServerServiceClient
	Player    carbonpanelv1connect.PlayerServiceClient
	World     carbonpanelv1connect.WorldServiceClient
	WorldView carbonpanelv1connect.WorldViewServiceClient
	Backup    carbonpanelv1connect.BackupServiceClient
	File      carbonpanelv1connect.FileServiceClient
}

// NewClients constructs typed clients with the bearer Authorization
// interceptor. A missing token is an error: tools must never run anonymous.
func NewClients(httpClient connect.HTTPClient, baseURL, token string) (*Clients, error) {
	if httpClient == nil {
		httpClient = newHTTPClient()
	}
	if token == "" {
		return nil, fmt.Errorf("API token is required")
	}
	opts := []connect.ClientOption{connect.WithInterceptors(bearerInterceptor(token))}
	return &Clients{
		Auth:      carbonpanelv1connect.NewAuthServiceClient(httpClient, baseURL, opts...),
		Server:    carbonpanelv1connect.NewServerServiceClient(httpClient, baseURL, opts...),
		Player:    carbonpanelv1connect.NewPlayerServiceClient(httpClient, baseURL, opts...),
		World:     carbonpanelv1connect.NewWorldServiceClient(httpClient, baseURL, opts...),
		WorldView: carbonpanelv1connect.NewWorldViewServiceClient(httpClient, baseURL, opts...),
		Backup:    carbonpanelv1connect.NewBackupServiceClient(httpClient, baseURL, opts...),
		File:      carbonpanelv1connect.NewFileServiceClient(httpClient, baseURL, opts...),
	}, nil
}

// Verify proves that the controller has a real authentication provider and
// that the bearer token resolves to a concrete active user before any tool is
// exposed. A well-formed token against a no-auth installation is refused.
func (c *Clients) Verify(ctx context.Context) error {
	status, err := c.Auth.GetAuthStatus(ctx, connect.NewRequest(&v1.GetAuthStatusRequest{}))
	if err != nil {
		return fmt.Errorf("auth status check failed: %w", err)
	}
	s := status.Msg
	if !s.GetLocalAuthEnabled() && !s.GetOidcEnabled() {
		return fmt.Errorf("controller has no authentication provider enabled; refusing a no-auth installation")
	}

	current, err := c.Auth.GetCurrentUser(ctx, connect.NewRequest(&v1.GetCurrentUserRequest{}))
	if err != nil {
		return fmt.Errorf("current user check failed: %w", err)
	}
	user := current.Msg.GetUser()
	if user == nil || user.GetId() == "" || user.GetUsername() == "" {
		return fmt.Errorf("controller did not return a concrete authenticated user identity")
	}
	if !user.GetIsActive() || strings.EqualFold(user.GetUsername(), "anonymous") {
		return fmt.Errorf("API token does not identify an active non-anonymous user")
	}
	return nil
}

func bearerInterceptor(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}
}
