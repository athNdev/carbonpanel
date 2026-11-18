// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane: OSS-383 executable tools/call evidence for
// exact-identity lookup and the per-server manual allowlist add.
package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestIntegrationPlayerLookupAndAllowlistEvidence(t *testing.T) {
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	authManager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local: config.LocalConfig{Enabled: true},
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ic := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())

	statuses := []v1.PlayerLookupStatus{
		v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_FOUND,
		v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_NOT_FOUND,
		v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_AMBIGUOUS,
		v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_UNAVAILABLE,
	}
	statusIdx := 0
	mux := http.NewServeMux()
	mux.Handle("/carbonpanel.v1.AuthService/GetAuthStatus", connect.NewUnaryHandler(
		"/carbonpanel.v1.AuthService/GetAuthStatus",
		func(context.Context, *connect.Request[v1.GetAuthStatusRequest]) (*connect.Response[v1.GetAuthStatusResponse], error) {
			return connect.NewResponse(&v1.GetAuthStatusResponse{LocalAuthEnabled: true}), nil
		}, connect.WithInterceptors(ic)))
	mux.Handle("/carbonpanel.v1.PlayerService/LookupPlayer", connect.NewUnaryHandler(
		"/carbonpanel.v1.PlayerService/LookupPlayer",
		func(_ context.Context, req *connect.Request[v1.LookupPlayerRequest]) (*connect.Response[v1.LookupPlayerResponse], error) {
			invocationRecorder.record("/carbonpanel.v1.PlayerService/LookupPlayer", req.Msg)
			status := statuses[statusIdx%len(statuses)]
			statusIdx++
			res := &v1.LookupPlayerResponse{Status: status}
			if status == v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_FOUND {
				res.Name = "Notch"
				res.Uuid = "069a79f444e94726a5befca90e38aaf5"
			} else {
				// A non-FOUND result must never invent a partial identity.
				res.Note = "explicit non-FOUND status"
			}
			return connect.NewResponse(res), nil
		}, connect.WithInterceptors(ic)))
	mux.Handle("/carbonpanel.v1.PlayerService/SetWhitelistEntry", connect.NewUnaryHandler(
		"/carbonpanel.v1.PlayerService/SetWhitelistEntry",
		func(_ context.Context, req *connect.Request[v1.SetWhitelistEntryRequest]) (*connect.Response[v1.SetWhitelistEntryResponse], error) {
			invocationRecorder.record("/carbonpanel.v1.PlayerService/SetWhitelistEntry", req.Msg)
			if req.Msg.GetPlayerQuery() != "Notch" || req.Msg.GetRequestId() != "req-allow-1" {
				return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("bad manual allowlist mapping"))
			}
			return connect.NewResponse(&v1.SetWhitelistEntryResponse{}), nil
		}, connect.WithInterceptors(ic)))

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	token := seedToken(t, store, enforcer, "u-lookup", "admin", nil)
	clients, err := NewClients(ts.Client(), ts.URL, token)
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	session := connectStdioSession(t, NewServer(clients), context.Background())

	beforeUsers, err := store.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	invocationRecorder.enable()
	defer invocationRecorder.disable()

	wants := []struct {
		status v1.PlayerLookupStatus
		label  string
	}{
		{v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_FOUND, "FOUND"},
		{v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_NOT_FOUND, "NOT_FOUND"},
		{v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_AMBIGUOUS, "AMBIGUOUS"},
		{v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_UNAVAILABLE, "UNAVAILABLE"},
	}
	for i, want := range wants {
		out := callTool(t, session, "player_service_lookup_player", map[string]any{
			"query": "069a79f444e94726a5befca90e38aaf5",
		})
		assertSuccess(t, out, "player_service_lookup_player")
		result, _ := out.Result.(map[string]any)
		got, _ := result["status"].(float64)
		if int32(got) != int32(want.status) {
			t.Fatalf("lookup %d: status %v, want %s (%d)", i, got, want.label, want.status)
		}
		if want.status != v1.PlayerLookupStatus_PLAYER_LOOKUP_STATUS_FOUND {
			name, _ := result["name"].(string)
			uuid, _ := result["uuid"].(string)
			if name != "" || uuid != "" {
				t.Fatalf("non-FOUND status %s invented an identity: name=%q uuid=%q", want.label, name, uuid)
			}
		}
	}

	// Manual allowlist add carries the exact name/UUID the operator typed plus
	// a request id, through the players:access:write-scoped typed RPC.
	allow := callTool(t, session, "player_service_set_whitelist_entry", map[string]any{
		"server_id": "srv-1", "identity_key": "p-1", "player_query": "Notch", "request_id": "req-allow-1", "confirm": true,
	})
	assertSuccess(t, allow, "player_service_set_whitelist_entry")

	afterUsers, err := store.CountUsers(context.Background())
	if err != nil {
		t.Fatalf("CountUsers: %v", err)
	}
	if afterUsers != beforeUsers {
		t.Fatalf("read-only lookup persisted identities: before=%d after=%d", beforeUsers, afterUsers)
	}

	reached := map[string]bool{}
	for _, rec := range invocationRecorder.snapshot() {
		reached[rec.Procedure] = true
	}
	if !reached["/carbonpanel.v1.PlayerService/LookupPlayer"] || !reached["/carbonpanel.v1.PlayerService/SetWhitelistEntry"] {
		t.Fatalf("OSS-383 typed methods not reached: %+v", reached)
	}
	t.Log("OSS-383 evidence: FOUND/NOT_FOUND/AMBIGUOUS/UNAVAILABLE, no identity mutation, manual allowlist with player_query+request_id")
}
