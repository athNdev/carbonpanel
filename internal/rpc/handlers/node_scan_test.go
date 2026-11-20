// SPDX-License-Identifier: MPL-2.0
package handlers

import (
	"context"
	"encoding/json"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestNodeScanCompatibilityAuthorizationAndNumericJSON(t *testing.T) {
	for _, fixture := range []struct {
		name                                           string
		noAuth, anonymous, global, scoped, nilEnforcer bool
		status                                         int
	}{
		{name: "missing credentials", status: 401},
		{name: "missing permission", anonymous: true, status: 403},
		{name: "resource-scoped grant cannot scan globally", anonymous: true, scoped: true, status: 403},
		{name: "global read", anonymous: true, global: true, status: 200},
		{name: "explicit no-auth admin", noAuth: true, status: 200},
		{name: "missing enforcer", noAuth: true, nilEnforcer: true, status: 403},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "panel.db"), MaxConnections: 5}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			if err = store.Migrate(); err != nil {
				t.Fatal(err)
			}
			enforcer, err := rbac.NewEnforcer(store.DB())
			if err != nil {
				t.Fatal(err)
			}
			if err = enforcer.SeedDefaultPolicies(false); err != nil {
				t.Fatal(err)
			}
			if fixture.global || fixture.scoped {
				object := "*"
				if fixture.scoped {
					object = "node-one"
				}
				if err = enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{{Resource: rbac.ResourceNodes, Action: rbac.ActionRead, ObjectID: object}}); err != nil {
					t.Fatal(err)
				}
			}
			manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{Local: config.LocalConfig{Enabled: !fixture.noAuth}, AnonymousAccess: fixture.anonymous, AllowNoAuth: fixture.noAuth})
			if err != nil {
				t.Fatal(err)
			}
			if fixture.nilEnforcer {
				enforcer = nil
			}
			calls := 0
			handler := nodeScanHandler(func(context.Context) *v1.ScanNodesResponse {
				calls++
				return &v1.ScanNodesResponse{Candidates: []*v1.DockerCandidate{{Host: "fixture", LatencyMs: 42, Port: 2375, Reachable: true}}, KnownNodes: 2, ScannedAt: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))}
			}, manager, enforcer)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/nodes/scan", nil))
			if response.Code != fixture.status {
				t.Fatalf("status=%d want%d: %s", response.Code, fixture.status, response.Body.String())
			}
			if fixture.status != 200 {
				if calls != 0 {
					t.Fatal("denied request probed network")
				}
				return
			}
			var body map[string]any
			if err = json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			candidate := body["candidates"].([]any)[0].(map[string]any)
			if candidate["latencyMs"] != float64(42) || candidate["port"] != float64(2375) || body["knownNodes"] != float64(2) || body["scannedAt"] != "2026-01-02T03:04:05Z" {
				t.Fatalf("legacy JSON changed: %v", body)
			}
		})
	}
}

func TestNodeScanCookieSessionUsesGlobalNodesPolicy(t *testing.T) {
	store, err := db.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "panel.db"), MaxConnections: 5}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Migrate(); err != nil {
		t.Fatal(err)
	}
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err = enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatal(err)
	}
	if err = enforcer.SetPermissionsForRole("user", []rbac.Permission{{Resource: rbac.ResourceNodes, Action: rbac.ActionRead, ObjectID: "*"}}); err != nil {
		t.Fatal(err)
	}
	manager, err := auth.NewManager(store, enforcer, &config.AuthConfig{Local: config.LocalConfig{Enabled: true}, SessionTimeout: 3600})
	if err != nil {
		t.Fatal(err)
	}
	user, err := manager.CreateLocalUser(t.Context(), "alice", "alice@example.com", "ValidPass123!")
	if err != nil {
		t.Fatal(err)
	}
	if err = store.AssignRole(t.Context(), user.ID, "user", "local"); err != nil {
		t.Fatal(err)
	}
	_, _, token, _, err := manager.Login(t.Context(), "alice", "ValidPass123!")
	if err != nil {
		t.Fatal(err)
	}

	calls := 0
	handler := nodeScanHandler(func(context.Context) *v1.ScanNodesResponse {
		calls++
		return &v1.ScanNodesResponse{Candidates: []*v1.DockerCandidate{{Host: "fixture", LatencyMs: 42, Port: 2375, Reachable: true}}, KnownNodes: 2, ScannedAt: timestamppb.New(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))}
	}, manager, enforcer)

	t.Run("authorized cookie session", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/scan", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusOK {
			t.Fatalf("status=%d want 200: %s", response.Code, response.Body.String())
		}
		if calls != 1 {
			t.Fatalf("scan calls=%d want 1", calls)
		}
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		candidate := body["candidates"].([]any)[0].(map[string]any)
		if candidate["latencyMs"] != float64(42) || candidate["port"] != float64(2375) || body["knownNodes"] != float64(2) || body["scannedAt"] != "2026-01-02T03:04:05Z" {
			t.Fatalf("legacy JSON changed: %v", body)
		}
	})

	if err = enforcer.SetPermissionsForRole("user", nil); err != nil {
		t.Fatal(err)
	}
	t.Run("cookie session denied without global permission", func(t *testing.T) {
		before := calls
		req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/scan", nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: token})
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Fatalf("status=%d want 403: %s", response.Code, response.Body.String())
		}
		if calls != before {
			t.Fatal("denied request probed network")
		}
	})
}
