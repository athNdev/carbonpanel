// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestScopedReadOperationalTools(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"srv-1", "srv-2"} {
		if err := store.CreateServer(ctx, &storage.Server{ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}

	started := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	const stagedCount = defaultLimit + 2
	changes := make([]storage.StagedConfigChange, 0, stagedCount+2)
	for i := range stagedCount {
		changes = append(changes, storage.StagedConfigChange{
			ID: fmt.Sprintf("change-%02d", i), ServerID: "srv-1", BatchID: "batch-1",
			ApplyMode: "on_restart", Status: storage.StagedStatusStaged, CreatedAt: started.Add(time.Duration(i) * time.Minute),
			Payload:         `{"rcon_password":"fixture-payload-value"}`,
			BasePayload:     `{"rcon_password":"fixture-base-value"}`,
			PreviousPayload: `{"rcon_password":"fixture-previous-value"}`,
		})
	}
	changes = append(changes,
		storage.StagedConfigChange{ID: "applied-change", ServerID: "srv-1", ApplyMode: "on_restart", Status: storage.StagedStatusApplied},
		storage.StagedConfigChange{ID: "other-tenant-change", ServerID: "srv-2", ApplyMode: "on_restart", Status: storage.StagedStatusStaged},
	)
	if err := store.DB().Create(&changes).Error; err != nil {
		t.Fatal(err)
	}

	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{Local: config.LocalConfig{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	interceptor := rpc.NewAuthInterceptor(manager, enforcer, store, logger.New())
	mux := http.NewServeMux()
	registerAuthHandlers(mux, interceptor, true)
	path, handler := carbonpanelv1connect.NewStagedConfigServiceHandler(
		services.NewStagedConfigService(store, handlers.NewStagedConfigOperations(store), enforcer),
		connect.WithInterceptors(interceptor),
	)
	mux.Handle(path, handler)

	// The deterministic fixture emits a typed collector snapshot, so this
	// asserts the actual fields the MCP tool returns over stdio.
	metricsProcedure := carbonpanelv1connect.ServerServiceGetServerProcedure
	mux.Handle(metricsProcedure, connect.NewUnaryHandler(metricsProcedure,
		func(_ context.Context, req *connect.Request[v1.GetServerRequest]) (*connect.Response[v1.GetServerResponse], error) {
			return connect.NewResponse(&v1.GetServerResponse{Server: &v1.Server{
				Id: req.Msg.Id, CpuPercent: 12.25, MemoryUsage: 512, DiskUsage: 1024, PlayersOnline: 7, Tps: 19.8,
			}}), nil
		}, connect.WithInterceptors(interceptor)))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	session := func(user, resource string) *sdkmcp.ClientSession {
		token := seedToken(t, store, enforcer, user, user,
			[]rbac.Permission{{Resource: resource, Action: rbac.ActionRead, ObjectID: "srv-1"}})
		clients, err := NewClients(server.Client(), server.URL, token)
		if err != nil {
			t.Fatal(err)
		}
		if err := clients.Verify(ctx); err != nil {
			t.Fatal(err)
		}
		return connectStdioSession(t, NewServer(clients), ctx)
	}
	configReader := session("scoped-config-reader", rbac.ResourceServerConfig)
	serverReader := session("scoped-server-reader", rbac.ResourceServers)

	t.Run("staged config scope and projection", func(t *testing.T) {
		tool := stagedConfigListSpec.Name
		out := callTool(t, configReader, tool, map[string]any{"server_id": "srv-1", "status": "staged", "limit": 1})
		assertSuccess(t, out, tool)
		raw, err := json.Marshal(out.Result)
		if err != nil {
			t.Fatal(err)
		}
		var page stagedConfigListResult
		if err := json.Unmarshal(raw, &page); err != nil {
			t.Fatal(err)
		}
		wantID := fmt.Sprintf("change-%02d", stagedCount-1)
		if page.ServerID != "srv-1" || len(page.Changes) != 1 || page.Changes[0].ID != wantID ||
			page.Changes[0].BatchID != "batch-1" || page.Changes[0].Status != "staged" ||
			page.Matched != stagedCount || !page.Truncated {
			t.Fatalf("staged result: %+v", page)
		}
		if strings.Contains(string(raw), "payload") || strings.Contains(string(raw), "fixture-") ||
			strings.Contains(string(raw), "other-tenant-change") {
			t.Fatal("staged result exposed a stored payload or a different server's change")
		}

		out = callTool(t, configReader, tool, map[string]any{"server_id": "srv-1", "status": "staged"})
		assertSuccess(t, out, tool)
		rows, ok := out.Result.(map[string]any)["changes"].([]any)
		if !ok || len(rows) != defaultLimit {
			t.Fatalf("expected default result limit, got %d", len(rows))
		}

		_ = assertStructuredError(t, callTool(t, configReader, tool, map[string]any{"server_id": "srv-2"}), tool, ErrKindPermissionDenied)
		_ = assertStructuredError(t, callTool(t, serverReader, tool, map[string]any{"server_id": "srv-1"}), tool, ErrKindPermissionDenied)
		_ = assertStructuredError(t, callTool(t, configReader, tool, map[string]any{"server_id": "srv-1", "status": "unknown"}), tool, ErrKindInvalidArguments)
		_ = assertStructuredError(t, callTool(t, configReader, tool, map[string]any{"server_id": "srv-1", "limit": maxLimit + 1}), tool, ErrKindInvalidArguments)

		if err := store.DB().Model(&storage.StagedConfigChange{}).Where("id = ?", "applied-change").
			Update("payload", strings.Repeat("x", stagedConfigResponseLimit+1)).Error; err != nil {
			t.Fatal(err)
		}
		errOut := callTool(t, configReader, tool, map[string]any{"server_id": "srv-1", "status": "applied"})
		structured := assertStructuredError(t, errOut, tool, ErrKindUpstream)
		if structured.Code != "resource_exhausted" {
			t.Fatalf("oversized response code: got %q, want resource_exhausted", structured.Code)
		}
	})

	t.Run("server metrics scope and response", func(t *testing.T) {
		out := callTool(t, serverReader, "get_server", map[string]any{"id": "srv-1"})
		assertSuccess(t, out, "get_server")
		raw, err := json.Marshal(out.Result)
		if err != nil {
			t.Fatal(err)
		}
		var details struct {
			Server *v1.Server `json:"server"`
		}
		if err := json.Unmarshal(raw, &details); err != nil {
			t.Fatal(err)
		}
		got := details.Server
		if got.GetId() != "srv-1" || got.GetCpuPercent() != 12.25 || got.GetMemoryUsage() != 512 ||
			got.GetDiskUsage() != 1024 || got.GetPlayersOnline() != 7 || got.GetTps() != 19.8 {
			t.Fatalf("server operational snapshot: %v", got)
		}
		_ = assertStructuredError(t, callTool(t, serverReader, "get_server", map[string]any{"id": "srv-2"}), "get_server", ErrKindPermissionDenied)
		_ = assertStructuredError(t, callTool(t, configReader, "get_server", map[string]any{"id": "srv-1"}), "get_server", ErrKindPermissionDenied)
	})
}
