// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/mcp"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestMain re-execs this test binary as the real MCP server when requested by
// the subprocess fixture. CommandTransport supplies stdin/stdout; the server
// therefore uses the exact production stdio path without a go build in tests.
func TestMain(m *testing.M) {
	if os.Getenv("CP_MCP_HELPER") == "1" {
		if err := mcp.Run(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("CP_MCP_HELPER") == "1" {
		t.Fatal("helper process must exit in TestMain")
	}
}

func fakeController(t *testing.T) *httptest.Server {
	t.Helper()
	const procedure = "/carbonpanel.v1.ServerService/ListServers"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(_ context.Context, req *connect.Request[v1.ListServersRequest]) (*connect.Response[v1.ListServersResponse], error) {
			if !strings.HasPrefix(req.Header().Get("Authorization"), "Bearer dp_") {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing API token"))
			}
			return connect.NewResponse(&v1.ListServersResponse{}), nil
		}))
	const statusProc = "/carbonpanel.v1.AuthService/GetAuthStatus"
	mux.Handle(statusProc, connect.NewUnaryHandler(statusProc,
		func(context.Context, *connect.Request[v1.GetAuthStatusRequest]) (*connect.Response[v1.GetAuthStatusResponse], error) {
			return connect.NewResponse(&v1.GetAuthStatusResponse{LocalAuthEnabled: true}), nil
		}))
	const userProc = "/carbonpanel.v1.AuthService/GetCurrentUser"
	mux.Handle(userProc, connect.NewUnaryHandler(userProc,
		func(context.Context, *connect.Request[v1.GetCurrentUserRequest]) (*connect.Response[v1.GetCurrentUserResponse], error) {
			return connect.NewResponse(&v1.GetCurrentUserResponse{User: &v1.User{
				Id: "u-stdio", Username: "stdio", IsActive: true,
			}}), nil
		}))
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func helperCommand(t *testing.T, tsURL, token string, includeToken bool) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestMCPHelperProcess")
	env := append(os.Environ(), "CP_MCP_HELPER=1", "CARBONPANEL_URL="+tsURL)
	if includeToken {
		env = append(env, "CARBONPANEL_API_TOKEN="+token)
	}
	cmd.Env = env
	return cmd
}

func TestRealStdioBinaryToolCall(t *testing.T) {
	ts := fakeController(t)
	cmd := helperCommand(t, ts.URL, "dp_stdio_smoke_token", true)
	transport := &sdkmcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "stdio-smoke-client", Version: "v0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		t.Fatalf("connect to stdio binary: %v", err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	if len(tools.Tools) != len(mcp.Tools) {
		t.Fatalf("tools/list returned %d tools, want %d", len(tools.Tools), len(mcp.Tools))
	}

	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: "list_servers", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("tools/call list_servers: %v", err)
	}
	if res.IsError {
		t.Fatalf("list_servers failed over stdio: %+v", res)
	}
}

func TestRealStdioBinaryFailsClosedWithoutToken(t *testing.T) {
	ts := fakeController(t)
	cmd := helperCommand(t, ts.URL, "", false)
	transport := &sdkmcp.CommandTransport{Command: cmd, TerminateDuration: time.Second}
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "stdio-nofail-client", Version: "v0"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, transport, nil)
	if err == nil {
		_ = session.Close()
		t.Fatal("stdio binary started without an API token")
	}
}
