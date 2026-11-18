// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type serviceFixture struct {
	service  *Service
	store    *fakeStore
	auditor  *fakeAuditor
	overlays *OverlayStore
}

// newServiceFixture uses a local httptest renderer fixture. It is not a live
// renderer: the tests assert projection/authz/audit behavior only.
func newServiceFixture(t *testing.T, playerFeed bool) *serviceFixture {
	t.Helper()
	renderer := httptest.NewServer(http.HandlerFunc(serveBlueMapFixture))
	t.Cleanup(renderer.Close)

	store := newFakeStore()
	store.addServer("srv-a", "default")
	store.addServer("srv-b", "default")
	store.setLevelName("srv-a", "custom_world")
	enforcer := &fakeEnforcer{allow: func(roles []string, resource, action, objectID string) (bool, error) {
		if len(roles) > 0 && roles[0] == "admin" {
			return true, nil
		}
		return len(roles) > 0 && roles[0] == "user" && resource == rbac.ResourceWorldView &&
			action == rbac.ActionRead && objectID == "srv-a", nil
	}}
	auditor := &fakeAuditor{}
	overlays := NewOverlayStore()
	cfg := Config{Endpoints: []Endpoint{endpointForFixture(t, renderer.URL, "", RendererBlueMap)}}
	cfg.Endpoints[0].PlayerFeed = playerFeed
	service := NewService(ServiceDeps{
		Store: store, Authorizer: enforcer,
		Resolver: NewResolver(cfg, &fakeLocator{local: map[string]bool{"default": true}}),
		Auditor:  auditor, Overlays: overlays,
	})
	return &serviceFixture{service: service, store: store, auditor: auditor, overlays: overlays}
}

func svcCtx(userID string, roles ...string) context.Context {
	return auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: userID, Username: userID, Roles: roles,
	})
}

// TestListWorldViewTargetsFiltersAndDisclosesFixture checks per-server RBAC
// filtering and that unknown/absent facts stay unknown instead of guessed.
func TestListWorldViewTargetsFiltersAndDisclosesFixture(t *testing.T) {
	f := newServiceFixture(t, false)
	resp, err := f.service.ListWorldViewTargets(svcCtx("user-a", "user"), connect.NewRequest(&v1.ListWorldViewTargetsRequest{}))
	if err != nil {
		t.Fatalf("ListWorldViewTargets: %v", err)
	}
	targets := resp.Msg.GetTargets()
	if len(targets) != 1 || targets[0].GetServerId() != "srv-a" {
		t.Fatalf("targets = %+v, want only the granted server", targets)
	}
	target := targets[0]
	if target.GetWorldRef().GetKind() != v1.WorldRef_KIND_SERVER_LOCAL || target.GetWorldRef().GetLevelName() != "custom_world" {
		t.Fatalf("world_ref = %+v, want configured level name", target.GetWorldRef())
	}
	if target.GetRendererState() != v1.RendererState_RENDERER_STATE_READY ||
		target.GetRendererType() != v1.RendererType_RENDERER_TYPE_BLUEMAP ||
		target.GetRenderer().GetVersion() != "5.29" {
		t.Fatalf("renderer projection = %+v", target)
	}
	if target.GetLastRender() == nil || target.GetFreshness().GetConfidence() != "detected" {
		t.Fatalf("freshness = %+v, last_render = %v", target.GetFreshness(), target.GetLastRender())
	}
	if target.GetRenderer().GetWebBasePath() != ProxyPrefix || strings.Contains(target.GetRenderer().GetWebBasePath(), "://") {
		t.Fatalf("web_base_path = %q, want same-origin prefix only", target.GetRenderer().GetWebBasePath())
	}

	// A server with no endpoint is onboarding: empty level name, ABSENT state,
	// unknown confidence and no fabricated timestamp.
	f.store.addServer("srv-b", "default")
	enforcer := &fakeEnforcer{allow: func(roles []string, _, _, objectID string) (bool, error) {
		return objectID == "srv-b" && len(roles) > 0 && roles[0] == "user", nil
	}}
	absentService := NewService(ServiceDeps{
		Store: f.store, Authorizer: enforcer,
		Resolver: NewResolver(Config{}, &fakeLocator{local: map[string]bool{"default": true}}),
	})
	absentResp, err := absentService.ListWorldViewTargets(svcCtx("user-a", "user"), connect.NewRequest(&v1.ListWorldViewTargetsRequest{}))
	if err != nil || len(absentResp.Msg.GetTargets()) != 1 {
		t.Fatalf("absent targets = %+v, %v", absentResp, err)
	}
	absent := absentResp.Msg.GetTargets()[0]
	if absent.GetWorldRef().GetLevelName() != "" || absent.GetLastRender() != nil ||
		absent.GetRendererState() != v1.RendererState_RENDERER_STATE_ABSENT ||
		absent.GetFreshness().GetConfidence() != "unknown" || absent.GetError() == "" {
		t.Fatalf("absent target = %+v", absent)
	}
}

// TestTargetErrorsDoNotLeakEndpointFixture checks client-visible diagnostics
// never carry a renderer host or port, even for an invalid endpoint.
func TestTargetErrorsDoNotLeakEndpointFixture(t *testing.T) {
	store := newFakeStore()
	store.addServer("srv-a", "default")
	service := NewService(ServiceDeps{
		Store:      store,
		Authorizer: &fakeEnforcer{allow: func([]string, string, string, string) (bool, error) { return true, nil }},
		Resolver: NewResolver(Config{Endpoints: []Endpoint{{
			NodeID: "default", Kind: RendererBlueMap, Host: "10.9.8.7", Port: 12345,
		}}}, &fakeLocator{local: map[string]bool{"default": true}}),
	})
	resp, err := service.ListWorldViewTargets(svcCtx("user-a", "user"), connect.NewRequest(&v1.ListWorldViewTargetsRequest{}))
	if err != nil || len(resp.Msg.GetTargets()) != 1 {
		t.Fatalf("targets = %+v, %v", resp, err)
	}
	target := resp.Msg.GetTargets()[0]
	for _, leaked := range []string{"10.9.8.7", "12345", "http://"} {
		if strings.Contains(target.GetError(), leaked) || strings.Contains(target.GetFreshness().GetNote(), leaked) {
			t.Fatalf("target leaked %q: %+v", leaked, target)
		}
	}
}

// TestGetWorldViewSessionFixture issues only same-origin paths and refuses to
// issue a session when the renderer is not ready.
func TestGetWorldViewSessionFixture(t *testing.T) {
	ready := newServiceFixture(t, false)
	resp, err := ready.service.GetWorldViewSession(svcCtx("user-a", "user"), connect.NewRequest(&v1.GetWorldViewSessionRequest{
		ServerId: "srv-a", WorldRef: fixtureRef("srv-a", "custom_world"),
	}))
	if err != nil {
		t.Fatalf("GetWorldViewSession: %v", err)
	}
	got := resp.Msg
	if !strings.HasPrefix(got.GetProxyPath(), ProxyPrefix) || strings.Contains(got.GetProxyPath(), "://") {
		t.Fatalf("proxy_path = %q, want same-origin path only", got.GetProxyPath())
	}
	if time.Until(got.GetExpiresAt().AsTime()) < 4*time.Minute || got.GetState() != v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("session = %+v", got)
	}

	absent := NewService(ServiceDeps{
		Store: ready.store, Authorizer: &fakeEnforcer{allow: func([]string, string, string, string) (bool, error) { return true, nil }},
		Resolver: NewResolver(Config{}, &fakeLocator{local: map[string]bool{"default": true}}),
	})
	unready, err := absent.GetWorldViewSession(svcCtx("user-a", "user"), connect.NewRequest(&v1.GetWorldViewSessionRequest{
		ServerId: "srv-b", WorldRef: fixtureRef("srv-b", ""),
	}))
	if err != nil {
		t.Fatalf("unready session: %v", err)
	}
	if unready.Msg.GetProxyPath() != "" || unready.Msg.GetReason() == "" ||
		unready.Msg.GetState() != v1.RendererState_RENDERER_STATE_ABSENT {
		t.Fatalf("unready session = %+v", unready.Msg)
	}

	// A static snapshot is honestly STALE but has no reachable tile proxy: it
	// must keep the stale state and freshness without offering a blank iframe.
	staticService := NewService(ServiceDeps{
		Store: ready.store, Authorizer: &fakeEnforcer{allow: func([]string, string, string, string) (bool, error) { return true, nil }},
		Resolver: NewResolver(Config{Endpoints: []Endpoint{{
			NodeID: "default", Kind: RendererStatic, Host: "127.0.0.1", Port: 8100,
		}}}, &fakeLocator{local: map[string]bool{"default": true}}),
	})
	staticResp, err := staticService.GetWorldViewSession(svcCtx("user-a", "user"), connect.NewRequest(&v1.GetWorldViewSessionRequest{
		ServerId: "srv-a", WorldRef: fixtureRef("srv-a", "custom_world"),
	}))
	if err != nil {
		t.Fatalf("static session: %v", err)
	}
	if staticResp.Msg.GetProxyPath() != "" || staticResp.Msg.GetState() != v1.RendererState_RENDERER_STATE_STALE ||
		staticResp.Msg.GetReason() == "" {
		t.Fatalf("static stale session = %+v", staticResp.Msg)
	}
}

// TestListCapabilitiesFixture verifies the capability matrix gates controls:
// players require the explicit feed flag and unverified features stay false.
func TestListCapabilitiesFixture(t *testing.T) {
	withFeed := newServiceFixture(t, true)
	resp, err := withFeed.service.ListCapabilities(svcCtx("user-a", "user"), connect.NewRequest(&v1.ListCapabilitiesRequest{ServerId: "srv-a"}))
	if err != nil {
		t.Fatalf("ListCapabilities: %v", err)
	}
	caps := resp.Msg.GetCapabilities()
	if got := lookupCapability(caps, CapabilityPlayers); got == nil || !got.GetSupported() {
		t.Fatalf("players capability with fixture feed = %+v", got)
	}
	for _, name := range []string{CapabilityTimeWeather, CapabilityTelemetry} {
		if got := lookupCapability(caps, name); got == nil || got.GetSupported() || got.GetDetail() == "" {
			t.Errorf("%s = %+v, want unsupported with detail", name, got)
		}
	}

	noFeed := newServiceFixture(t, false)
	noFeedResp, err := noFeed.service.ListCapabilities(svcCtx("user-a", "user"), connect.NewRequest(&v1.ListCapabilitiesRequest{ServerId: "srv-a"}))
	if err != nil {
		t.Fatalf("ListCapabilities (no feed): %v", err)
	}
	if got := lookupCapability(noFeedResp.Msg.GetCapabilities(), CapabilityPlayers); got == nil || got.GetSupported() {
		t.Fatalf("players capability without feed = %+v, want unsupported", got)
	}

	absent := NewService(ServiceDeps{
		Store: noFeed.store, Authorizer: &fakeEnforcer{allow: func([]string, string, string, string) (bool, error) { return true, nil }},
		Resolver: NewResolver(Config{}, &fakeLocator{local: map[string]bool{"default": true}}),
	})
	absentResp, err := absent.ListCapabilities(svcCtx("user-a", "user"), connect.NewRequest(&v1.ListCapabilitiesRequest{ServerId: "srv-b"}))
	if err != nil {
		t.Fatalf("absent capabilities: %v", err)
	}
	for _, c := range absentResp.Msg.GetCapabilities() {
		if c.GetSupported() || c.GetDetail() == "" {
			t.Errorf("absent renderer capability %s = %+v", c.GetName(), c)
		}
	}
}

// TestServiceMutationsAreAuditedAndScopedFixture covers overlay/visibility
// service-layer scoping, sensitive filtering and audit failure handling.
func TestServiceMutationsAreAuditedAndScopedFixture(t *testing.T) {
	f := newServiceFixture(t, false)
	ref := fixtureRef("srv-a", "custom_world")
	overlayReq := func(sensitive bool, id string) *connect.Request[v1.SetMapOverlayRequest] {
		return connect.NewRequest(&v1.SetMapOverlayRequest{
			ServerId: "srv-a",
			Overlay: &v1.MapOverlay{
				Id: id, ServerId: "attacker-controlled", WorldRef: fixtureRef("", "custom_world"),
				Dimension: "overworld", Kind: "poi", Sensitive: sensitive,
			},
		})
	}

	// world_view/read alone cannot edit overlays or disclose players.
	if _, err := f.service.SetMapOverlay(svcCtx("user-a", "user"), overlayReq(false, "")); err == nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read-only user SetMapOverlay error = %v", err)
	}
	if _, err := f.service.SetPlayerVisibility(svcCtx("user-a", "user"), connect.NewRequest(&v1.SetPlayerVisibilityRequest{
		ServerId: "srv-a", WorldRef: ref, Visible: true,
	})); err == nil || connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("read-only user SetPlayerVisibility error = %v", err)
	}

	admin := svcCtx("admin", "admin")
	resp, err := f.service.SetMapOverlay(admin, overlayReq(false, ""))
	if err != nil {
		t.Fatalf("admin SetMapOverlay: %v", err)
	}
	saved := resp.Msg.GetOverlay()
	if saved.GetServerId() != "srv-a" || saved.GetWorldRef().GetServerId() != "srv-a" || saved.GetId() == "" {
		t.Fatalf("stored overlay was not forced into the request scope: %+v", saved)
	}
	if _, err := f.service.SetMapOverlay(admin, overlayReq(true, "sensitive-id")); err != nil {
		t.Fatalf("sensitive overlay: %v", err)
	}

	listReq := connect.NewRequest(&v1.ListMapOverlaysRequest{ServerId: "srv-a", WorldRef: ref, Dimension: "overworld"})
	userList, err := f.service.ListMapOverlays(svcCtx("user-a", "user"), listReq)
	if err != nil || len(userList.Msg.GetOverlays()) != 1 {
		t.Fatalf("user overlays = %+v, %v; want only the non-sensitive one", userList, err)
	}
	adminList, err := f.service.ListMapOverlays(admin, listReq)
	if err != nil || len(adminList.Msg.GetOverlays()) != 2 {
		t.Fatalf("admin overlays = %+v, %v; want both", adminList, err)
	}

	visibility, err := f.service.SetPlayerVisibility(admin, connect.NewRequest(&v1.SetPlayerVisibilityRequest{
		ServerId: "srv-a", WorldRef: ref, Visible: true,
	}))
	if err != nil || !visibility.Msg.GetVisible() || visibility.Msg.GetObservation().GetConfidence() != "unknown" {
		t.Fatalf("visibility = %+v, %v", visibility, err)
	}
	events := f.auditor.all()
	if len(events) != 3 {
		t.Fatalf("audit events = %d, want 3", len(events))
	}
	if events[0].Event != "world_view.overlay.set" || events[0].SubjectID != "srv-a" ||
		events[2].Event != "world_view.observe_players" || events[2].ActorID != "admin" {
		t.Fatalf("audit events = %+v", events)
	}

	// Audit failure must fail the mutation, not be reported as success.
	f.auditor.err = errors.New("fixture: audit database down")
	before := len(f.overlays.List("srv-a", worldKey(ref), "overworld"))
	if _, err := f.service.SetMapOverlay(admin, overlayReq(false, "unaudited")); err == nil || connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("unaudited overlay error = %v, want internal", err)
	}
	if after := len(f.overlays.List("srv-a", worldKey(ref), "overworld")); after != before {
		t.Fatalf("unaudited overlay changed the store: %d -> %d", before, after)
	}
}
