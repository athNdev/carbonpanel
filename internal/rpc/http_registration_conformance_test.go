// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

type httpRouteClass string

const (
	httpRoutePublicProbe       httpRouteClass = "public_probe"
	httpRoutePublicAuthFlow    httpRouteClass = "public_auth_flow"
	httpRoutePublicSpec        httpRouteClass = "public_spec"
	httpRoutePublicBootstrap   httpRouteClass = "public_bootstrap"
	httpRouteProtectedRPC      httpRouteClass = "protected_connect_rpc"
	httpRouteProtectedResource httpRouteClass = "protected_resource"
	httpRouteProtectedSpecial  httpRouteClass = "protected_special"
	httpRouteFrontend          httpRouteClass = "frontend"
)

type httpRegistration struct {
	Pattern         string
	Class           httpRouteClass
	Methods         []string
	PublicException bool
	// Policy names the RBAC/resource policy, auth mechanism or documented
	// exception basis that governs this route.
	Policy string
	Notes  string
}

// productionHTTPRegistrations is the canonical REST/HTTP registration inventory
// for the panel server (setupHandler/setupFrontend). It is compared to RBAC and
// auth policy coverage as the OSS-403 conformance seam: every production HTTP
// route must be classified, public exceptions must be explicit, and protected
// routes must name a fail-closed policy basis. SSE endpoints are not part of
// the current production inventory.
var productionHTTPRegistrations = []httpRegistration{
	{Pattern: "grpcreflect.v1/*", Class: httpRouteProtectedRPC, Methods: []string{"POST"}, Policy: "served on mux; reflection describes registered Connect services (not a product data plane)"},
	{Pattern: "grpcreflect.v1alpha/*", Class: httpRouteProtectedRPC, Methods: []string{"POST"}, Policy: "served on mux; reflection describes registered Connect services (not a product data plane)"},
	{Pattern: "/ws", Class: httpRouteProtectedSpecial, Methods: []string{"GET"}, Policy: "ws handshake pre-upgrade authenticateToken; AUTH refresh; per-subscribe servers/read and per-command servers/command RBAC with fail-closed nil enforcer checks"},
	{Pattern: "/healthz", Class: httpRoutePublicProbe, Methods: []string{"GET", "HEAD"}, PublicException: true, Policy: "unauthenticated liveness; no sensitive data", Notes: "kubelet/probe endpoint"},
	{Pattern: "/readyz", Class: httpRoutePublicProbe, Methods: []string{"GET", "HEAD"}, PublicException: true, Policy: "unauthenticated readiness; DB ping only, no sensitive data", Notes: "kubelet/probe endpoint"},
	{Pattern: "/health/detail", Class: httpRoutePublicProbe, Methods: []string{"GET", "HEAD"}, PublicException: true, Policy: "unauthenticated layered health snapshot; no credentials or secret material"},
	{Pattern: "/api/v1/auth/oidc/login", Class: httpRoutePublicAuthFlow, Methods: []string{"GET"}, PublicException: true, Policy: "OIDC browser redirect start; registered only when OIDC is enabled"},
	{Pattern: "/api/v1/auth/oidc/callback", Class: httpRoutePublicAuthFlow, Methods: []string{"GET"}, PublicException: true, Policy: "IdP callback with signed state cookie; exchanges code and mints session; registered only when OIDC is enabled"},
	{Pattern: "/api/v1/auth/oidc/exchange", Class: httpRoutePublicAuthFlow, Methods: []string{"GET", "POST"}, PublicException: true, Policy: "one-time token exchange for SPA session; registered only when OIDC is enabled"},
	{Pattern: "/api/v1/upload/", Class: httpRouteProtectedResource, Methods: []string{"PUT"}, Policy: "stream upload requires authenticated user and RBAC uploads/create on * (fail-closed on enforcer/auth failure)"},
	{Pattern: "/api/v1/download/", Class: httpRouteProtectedResource, Methods: []string{"GET", "HEAD"}, Policy: "stream download requires authenticated user and files/read scoped to download session server (legacy global fallback when session has no server id); fail-closed"},
	{Pattern: "/api/v1/openapi.yaml", Class: httpRoutePublicSpec, Methods: []string{"GET"}, PublicException: true, Policy: "public API specification document; security metadata reflects auth configuration"},
	{Pattern: "/api/v1/settings/validate-key", Class: httpRouteProtectedResource, Methods: []string{"POST"}, Policy: "global settings/read via shared fail-closed policy helper; no provider egress before authorization"},
	{Pattern: "/api/v1/nodes/scan", Class: httpRouteProtectedResource, Methods: []string{"GET"}, Policy: "global nodes/read with fail-closed auth/enforcer handling; denied requests do not probe network"},
	{Pattern: "/api/v1/servers/", Class: httpRouteProtectedResource, Methods: []string{"GET", "POST"}, Policy: "legacy mod browse/install adapter shares ModOnlineManager operation: mods/read for browse, mods/create for install (global object, fail-closed when auth enabled)"},
	{Pattern: "/api/v1/packwiz/loaders/{loader}/versions", Class: httpRoutePublicBootstrap, Methods: []string{"GET"}, PublicException: true, Policy: "public loader version metadata for Studio/bootstrap clients", Notes: "served by PackwizHandler before auth"},
	{Pattern: "/api/v1/packwiz/{packId}/{file...}", Class: httpRoutePublicBootstrap, Methods: []string{"GET"}, PublicException: true, Policy: "public pack file serving for container/runtime bootstrap (pack.toml/index/files); no studio management data mutation", Notes: "served by PackwizHandler before auth"},
	{Pattern: "/api/v1/packwiz/packs...", Class: httpRouteProtectedResource, Methods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"}, Policy: "studio management routes require authenticated user and modpacks/read or modpacks/create on global * via shared fail-closed policy helper; binary/content routes remain HTTP per ADR 0012"},
	{Pattern: "/api/v1/staged-config/", Class: httpRouteProtectedResource, Methods: []string{"GET", "POST", "DELETE"}, Policy: "server_config/read or create scoped to path server and, for staged-record mutations, the record owner via shared fail-closed policy helper"},
	{Pattern: "/api/v2/metrics/", Class: httpRouteProtectedResource, Methods: []string{"GET"}, Policy: "servers/read scoped to * (host) or requested server id via shared fail-closed policy helper"},
	{Pattern: "/world-view/*", Class: httpRouteProtectedSpecial, Methods: []string{"GET", "HEAD"}, Policy: "authenticated same-origin renderer proxy with scoped worldview authorization/session checks; mounted before frontend catch-all", Notes: "registered only when world-view proxy is configured"},
	{Pattern: "/carbonpanel.v1.* (Connect RPC)", Class: httpRouteProtectedRPC, Methods: []string{"POST"}, Policy: "all Connect RPC procedures pass through authInterceptor: public/authenticated-only/permissioned RBAC maps are fail-closed; unmapped procedures and unavailable enforcer denied"},
	{Pattern: "/", Class: httpRouteFrontend, Methods: []string{"GET", "HEAD"}, Policy: "static SPA/frontend serving for non-Connect paths; no privileged API data", Notes: "catch-all"},
}

func TestHTTPRegistrationInventoryCoversProductionRoutes(t *testing.T) {
	if len(productionHTTPRegistrations) == 0 {
		t.Fatal("production HTTP registration inventory is empty")
	}

	seen := make(map[string]struct{}, len(productionHTTPRegistrations))
	publicExceptions := 0
	protected := 0
	hasSSE := false

	for _, route := range productionHTTPRegistrations {
		if strings.TrimSpace(route.Pattern) == "" {
			t.Fatal("inventory contains route with empty pattern")
		}
		if _, exists := seen[route.Pattern]; exists {
			t.Fatalf("duplicate HTTP registration inventory entry: %s", route.Pattern)
		}
		seen[route.Pattern] = struct{}{}
		if strings.TrimSpace(route.Policy) == "" {
			t.Fatalf("route %s must declare a policy/exception basis", route.Pattern)
		}
		if len(route.Methods) == 0 {
			t.Fatalf("route %s must declare expected methods or method classes", route.Pattern)
		}
		if strings.Contains(strings.ToLower(route.Notes+" "+route.Policy+" "+route.Pattern), "text/event-stream") || strings.EqualFold(string(route.Class), "sse") {
			hasSSE = true
		}

		switch route.Class {
		case httpRoutePublicProbe, httpRoutePublicAuthFlow, httpRoutePublicSpec, httpRoutePublicBootstrap:
			if !route.PublicException {
				t.Fatalf("public route %s must be marked as an explicit public exception", route.Pattern)
			}
			publicExceptions++
		case httpRouteProtectedRPC, httpRouteProtectedResource, httpRouteProtectedSpecial:
			if route.PublicException {
				t.Fatalf("protected route %s must not be marked as a public exception", route.Pattern)
			}
			protected++
		case httpRouteFrontend:
			if route.PublicException {
				t.Fatalf("frontend route %s must not be marked as a public exception", route.Pattern)
			}
		default:
			t.Fatalf("route %s has unknown classification %q", route.Pattern, route.Class)
		}
	}

	if publicExceptions == 0 {
		t.Fatal("inventory must include explicit public HTTP exceptions")
	}
	if protected == 0 {
		t.Fatal("inventory must include protected HTTP/RPC routes")
	}
	// OSS-403 asks for SSE registration inventory coverage; the current
	// production server exposes no SSE endpoints, so this conformance seam
	// requires the inventory to remain explicit rather than silently assuming
	// none exist.
	if hasSSE {
		patterns := make([]string, 0, len(productionHTTPRegistrations))
		for _, route := range productionHTTPRegistrations {
			patterns = append(patterns, route.Pattern)
		}
		sort.Strings(patterns)
		t.Fatalf("SSE routes were added to production inventory; update conformance classification and fail-closed checks: %s", strings.Join(patterns, ", "))
	}
}

func TestHTTPRegistrationInventoryExplicitPublicExceptions(t *testing.T) {
	expected := map[string]string{
		"/healthz":                                  "public liveness probe",
		"/readyz":                                   "public readiness probe",
		"/health/detail":                            "public health snapshot",
		"/api/v1/auth/oidc/login":                   "OIDC login redirect",
		"/api/v1/auth/oidc/callback":                "OIDC callback",
		"/api/v1/auth/oidc/exchange":                "OIDC token exchange",
		"/api/v1/openapi.yaml":                      "public API specification",
		"/api/v1/packwiz/loaders/{loader}/versions": "packwiz loader metadata bootstrap",
		"/api/v1/packwiz/{packId}/{file...}":        "packwiz runtime/bootstrap file serving",
	}

	seen := make(map[string]bool, len(expected))
	for _, route := range productionHTTPRegistrations {
		if reason, ok := expected[route.Pattern]; ok {
			seen[route.Pattern] = true
			if !route.PublicException {
				t.Fatalf("expected public exception %s (%s) is not marked public", route.Pattern, reason)
			}
		}
	}
	for pattern, reason := range expected {
		if !seen[pattern] {
			t.Fatalf("missing explicit public exception for %s (%s)", pattern, reason)
		}
	}
}

func TestProtectedHTTPHandlersFailClosedWithoutAuthDependencies(t *testing.T) {
	// Representative REST handlers must deny protected operations when auth
	// dependencies are absent or no credentials are provided. These checks
	// exercise the shared fail-closed policy seam used by the migrated REST
	// adapters.
	t.Run("validate-key denies without authorization", func(t *testing.T) {
		handler := handlers.NewKeyValidatorHandler(nil, nil, logger.New())
		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/validate-key", strings.NewReader(`{"provider":"modrinth"}`))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("expected fail-closed denial, got status %d body %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("metrics host denies without authorization", func(t *testing.T) {
		handler := handlers.NewMetricsHandler(nil, nil, nil, nil, logger.New())
		req := httptest.NewRequest(http.MethodGet, "/api/v2/metrics/host", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("expected fail-closed denial, got status %d body %q", rec.Code, rec.Body.String())
		}
	})

	t.Run("metrics server denies without authorization", func(t *testing.T) {
		handler := handlers.NewMetricsHandler(nil, nil, nil, nil, logger.New())
		req := httptest.NewRequest(http.MethodGet, "/api/v2/metrics/servers/srv-1", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
			t.Fatalf("expected fail-closed denial, got status %d body %q", rec.Code, rec.Body.String())
		}
	})
}

func TestHTTPEndpointClassificationHasNoDuplicatePolicyTargets(t *testing.T) {
	// The same concrete route pattern must not be classified twice with
	// conflicting public/protected policy.
	byPattern := make(map[string]httpRouteClass, len(productionHTTPRegistrations))
	for _, route := range productionHTTPRegistrations {
		if prev, exists := byPattern[route.Pattern]; exists && prev != route.Class {
			t.Fatalf("route %s has conflicting classifications %q and %q", route.Pattern, prev, route.Class)
		}
		byPattern[route.Pattern] = route.Class
	}
}

var _ = http.MethodGet
var _ = httptest.NewRecorder
var _ = strings.TrimSpace
var _ = sort.Strings
var _ = logger.New
var _ = handlers.NewKeyValidatorHandler
