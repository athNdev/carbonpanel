// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// This file holds the composed typed paths behind the OSS-374 audit. It adds
// no raw HTTP tool: every supported workflow resolves to an existing generated
// Connect client, and every byte/session surface stays a private transport
// mechanic. The integration owner decides how to merge these clients with the
// shared Clients struct when generating the combined catalog.
package mcp

import (
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

// FamilyStreamClients owns the typed clients named by the audit table. It is
// constructed from the same API token and base URL as the first-slice clients;
// no shared client.go field is modified and no anonymous fallback exists.
type FamilyStreamClients struct {
	Config    carbonpanelv1connect.ConfigServiceClient
	Upload    carbonpanelv1connect.UploadServiceClient
	File      carbonpanelv1connect.FileServiceClient
	Node      carbonpanelv1connect.NodeServiceClient
	ModBrowse carbonpanelv1connect.ModBrowseServiceClient
	Minecraft carbonpanelv1connect.MinecraftServiceClient
	Modpack   carbonpanelv1connect.ModpackServiceClient
	WorldView carbonpanelv1connect.WorldViewServiceClient
	Server    carbonpanelv1connect.ServerServiceClient
}

// NewFamilyStreamClients builds typed clients with the production bearer
// interceptor and bounded HTTP timeout. An empty token fails closed.
func NewFamilyStreamClients(httpClient connect.HTTPClient, baseURL, token string) (*FamilyStreamClients, error) {
	if httpClient == nil {
		httpClient = newHTTPClient()
	}
	if token == "" {
		return nil, fmt.Errorf("API token is required")
	}
	opts := []connect.ClientOption{connect.WithInterceptors(bearerInterceptor(token))}
	return &FamilyStreamClients{
		Config:    carbonpanelv1connect.NewConfigServiceClient(httpClient, baseURL, opts...),
		Upload:    carbonpanelv1connect.NewUploadServiceClient(httpClient, baseURL, opts...),
		File:      carbonpanelv1connect.NewFileServiceClient(httpClient, baseURL, opts...),
		Node:      carbonpanelv1connect.NewNodeServiceClient(httpClient, baseURL, opts...),
		ModBrowse: carbonpanelv1connect.NewModBrowseServiceClient(httpClient, baseURL, opts...),
		Minecraft: carbonpanelv1connect.NewMinecraftServiceClient(httpClient, baseURL, opts...),
		Modpack:   carbonpanelv1connect.NewModpackServiceClient(httpClient, baseURL, opts...),
		WorldView: carbonpanelv1connect.NewWorldViewServiceClient(httpClient, baseURL, opts...),
		Server:    carbonpanelv1connect.NewServerServiceClient(httpClient, baseURL, opts...),
	}, nil
}

// FamilyStreamToolSpec projects a generated catalog entry into a ToolSpec so
// Connect failures on a composed typed path map to the same structured
// ToolError shape as a registered tool. Excluded procedures return false.
func FamilyStreamToolSpec(procedure string) (ToolSpec, bool) {
	entry, ok := Catalog[procedure]
	if !ok {
		return ToolSpec{}, false
	}
	return ToolSpec{
		Name:        procedure,
		Procedure:   procedure,
		Class:       ClassSafe,
		Description: entry.Scope,
	}, true
}

// ValidateFamilyStreamSessionID rejects anything that could turn a bounded
// upload/download session handle into an arbitrary URL, path or query. Raw
// byte surfaces always receive server-generated opaque ids, never model input
// that reaches a transport directly.
func ValidateFamilyStreamSessionID(raw string) error {
	if raw == "" || len(raw) > maxIDLen {
		return &ToolError{
			Kind:      ErrKindInvalidArguments,
			Tool:      "family_stream_session",
			Message:   fmt.Sprintf("session id must be 1 to %d characters", maxIDLen),
			Procedure: "stream/session",
		}
	}
	lower := strings.ToLower(raw)
	rejected := []string{"://", "?", "#", "/", "\\", ".."}
	for _, marker := range rejected {
		if strings.Contains(raw, marker) || strings.Contains(lower, marker) {
			return &ToolError{
				Kind:      ErrKindInvalidArguments,
				Tool:      "family_stream_session",
				Message:   "session id must be an opaque handle, not a URL, path or query",
				Procedure: "stream/session",
			}
		}
	}
	return nil
}
