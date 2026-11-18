// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ServerVersion is independent of the Carbon Panel product release.
const ServerVersion = "v0.1.0"

// ToolOutput is the structured result for every tool. On failure Error is set
// and the MCP result is marked IsError; on success Result carries the typed
// Connect response message.
type ToolOutput struct {
	Status string     `json:"status" jsonschema:"ok or error"`
	Error  *ToolError `json:"error,omitempty" jsonschema:"structured failure detail"`
	Result any        `json:"result,omitempty" jsonschema:"typed Connect response"`
}

// NewServer builds the MCP server with the combined OSS-370 tool catalog:
// the pinned first slice, every family hand-off and the integration-owned
// additions. Tools are registered before the first connection so capability
// inference works.
func NewServer(clients *Clients) *sdkmcp.Server {
	if clients == nil {
		panic("mcp.NewServer: nil clients")
	}
	s := sdkmcp.NewServer(
		&sdkmcp.Implementation{
			Name:        "carbonpanel-mcp",
			Version:     ServerVersion,
			Description: "Read-first MCP access to Carbon Panel operator workflows over generated Connect RPCs.",
		},
		&sdkmcp.ServerOptions{
			// Diagnostics go to stderr only: stdout is reserved for JSON-RPC
			// frames in stdio mode.
			Logger: slog.New(slog.NewTextHandler(os.Stderr, nil)),
			Instructions: fmt.Sprintf("COMBINED OSS-370 CATALOG: %d tools, including the pinned %d-tool first slice. "+
				"Inventory coverage is not implemented API coverage. "+
				"Use read-only tools first. Mutations require confirm=true. "+
				"An older controller may return a structured capability_unavailable error; "+
				"the tool catalog itself never changes between panel builds.", len(exposedTools), FirstSliceToolCount),
		},
	)
	if err := registerCombinedTools(s, clients); err != nil {
		panic(fmt.Sprintf("mcp.NewServer: %v", err))
	}
	return s
}

// Run starts the stdio MCP server using credentials and endpoint from the
// environment. Both are required; no anonymous or discovered endpoint is used.
func Run(ctx context.Context) error {
	token, err := TokenFromEnv()
	if err != nil {
		return err
	}
	url, err := ControllerURLFromEnv()
	if err != nil {
		return err
	}
	clients, err := NewClients(newHTTPClient(), url, token)
	if err != nil {
		return err
	}
	if err := clients.Verify(ctx); err != nil {
		return fmt.Errorf("controller authentication verification failed: %w", err)
	}
	if err := NewServer(clients).Run(ctx, &sdkmcp.StdioTransport{}); err != nil {
		return fmt.Errorf("mcp stdio server: %w", err)
	}
	return nil
}
