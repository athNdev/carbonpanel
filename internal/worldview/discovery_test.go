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
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// These fixtures own every fact: a local httptest renderer stands in for the
// PRIVATE backend transport and an in-memory ModuleDiscovery stands in for the
// installed-module registry. No live container, Docker socket, port probe, host
// port or world is touched. Test names say "Fixture" so results are never read
// as live renderer evidence.

type fakeDiscovery struct {
	facts *InstalledRenderer
	err   error
	calls int
}

func (f *fakeDiscovery) FindRenderer(context.Context, string, string) (*InstalledRenderer, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	if f.facts == nil {
		return nil, nil
	}
	clone := *f.facts
	return &clone, nil
}

// moduleFixture is the task-owned installed-module fact set for OSS-388.
type moduleFixture struct {
	id, serverID, nodeID, templateID string
	status                           ModuleLifecycle
	worldName                        string
}

func installedModuleFixture() *moduleFixture {
	return &moduleFixture{
		id:         "mod-blue-1",
		serverID:   "srv-blue",
		nodeID:     "default",
		templateID: BuiltinBlueMapTemplateID,
		status:     ModuleRunning,
		worldName:  "actual_world",
	}
}

// readyFacts models the live evidence: web server on the owned network, NULL
// host-port bindings, read-only /app/world, configured target world and scoped
// tiles. The httptest URL is the private endpoint for the fixture only.
func readyFacts(m *moduleFixture, privateURL string) *InstalledRenderer {
	return &InstalledRenderer{
		ModuleID: m.id, ServerID: m.serverID, NodeID: m.nodeID,
		TemplateID: m.templateID, Lifecycle: m.status,
		BaseURL: privateURL, ContainerPort: 8100,
		IdentityVerified: true, NetworkVerified: true, MountVerified: true,
		MapID:     "world",
		WorldName: m.worldName, WorldMatchesServer: true,
		AcceptDownload: true, WebUp: true, Rendered: true,
		ReadOnlyWorldMount: true, Version: "5.29",
	}
}

func privateRendererFixture(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(serveBlueMapFixture))
	t.Cleanup(srv.Close)
	return srv.URL
}

func discoveryFixture(t *testing.T, m *moduleFixture, factsFor func(string) *InstalledRenderer) (*Service, *fakeDiscovery, string) {
	t.Helper()
	privateURL := privateRendererFixture(t)
	store := newFakeStore()
	store.addServer(m.serverID, m.nodeID)
	store.setLevelName(m.serverID, m.worldName) // ACTUAL level name, not "world"
	enforcer := &fakeEnforcer{allow: func(roles []string, _, _, _ string) (bool, error) {
		return len(roles) > 0 && roles[0] == "admin", nil
	}}
	discovery := &fakeDiscovery{facts: factsFor(privateURL)}
	locator := &fakeLocator{local: map[string]bool{m.nodeID: true}}
	resolver := NewResolverWithDiscovery(Config{}, locator, discovery)
	service := NewService(ServiceDeps{Store: store, Authorizer: enforcer, Resolver: resolver})
	return service, discovery, privateURL
}

func listTarget(t *testing.T, service *Service, serverID string) *v1.WorldViewTarget {
	t.Helper()
	resp, err := service.ListWorldViewTargets(svcCtx("admin", "admin"), connect.NewRequest(&v1.ListWorldViewTargetsRequest{}))
	if err != nil {
		t.Fatalf("ListWorldViewTargets: %v", err)
	}
	for _, target := range resp.Msg.GetTargets() {
		if target.GetServerId() == serverID {
			return target
		}
	}
	t.Fatalf("no target for server %s", serverID)
	return nil
}

// TestInstalledBlueMapIsDiscoveredWithoutManualEndpoint is the original
// red-capable loop: it failed with ABSENT before discovery was wired and must
// now resolve to READY with only an empty world_view.endpoints config.
func TestInstalledBlueMapIsDiscoveredWithoutManualEndpoint(t *testing.T) {
	m := installedModuleFixture()
	service, _, privateURL := discoveryFixture(t, m, func(u string) *InstalledRenderer { return readyFacts(m, u) })

	target := listTarget(t, service, m.serverID)
	if target.GetRendererState() == v1.RendererState_RENDERER_STATE_ABSENT {
		t.Fatalf("EXACT SYMPTOM: installed builtin-bluemap module %s on server %s/node %s is ABSENT with empty config; target = %+v",
			m.id, m.serverID, m.nodeID, target)
	}
	if target.GetRendererState() != v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("installed module should resolve to READY, got %s; target = %+v", target.GetRendererState(), target)
	}
	if target.GetWorldRef().GetLevelName() != m.worldName {
		t.Fatalf("level name = %q, want actual world %q", target.GetWorldRef().GetLevelName(), m.worldName)
	}
	if target.GetRenderer().GetVersion() != "5.29" {
		t.Fatalf("version = %q, want pinned 5.29", target.GetRenderer().GetVersion())
	}
	if strings.Contains(target.String(), privateURL) || strings.Contains(target.String(), "carbon-panel-module-") {
		t.Fatalf("private renderer address leaked into client target: %s", target.String())
	}
}

// H1 (explicit precedence): an operator endpoint must always win and discovery
// must not even be consulted.
func TestExplicitEndpointAlwaysWinsOverDiscovery(t *testing.T) {
	m := installedModuleFixture()
	service, discovery, privateURL := discoveryFixture(t, m, func(u string) *InstalledRenderer { return readyFacts(m, u) })
	store := service.deps.Store
	enforcer := service.deps.Authorizer
	resolver := NewResolverWithDiscovery(
		Config{Endpoints: []Endpoint{endpointForFixture(t, privateURL, "", RendererBlueMap)}},
		&fakeLocator{local: map[string]bool{m.nodeID: true}}, discovery,
	)
	service = NewService(ServiceDeps{Store: store, Authorizer: enforcer, Resolver: resolver})
	discovery.facts = nil // would yield ABSENT if discovery were used

	target := listTarget(t, service, m.serverID)
	if target.GetRendererState() != v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("explicit endpoint should be READY, got %s; target = %+v", target.GetRendererState(), target)
	}
	if discovery.calls != 0 {
		t.Fatalf("discovery was consulted %d times despite an explicit endpoint", discovery.calls)
	}
}

// H5 (no cross-server fallback): identity binding is exact.
func TestDiscoveryBindsExactServerNodeModuleIdentity(t *testing.T) {
	m := installedModuleFixture()
	cases := []struct {
		name   string
		mutate func(*InstalledRenderer)
	}{
		{"other server", func(f *InstalledRenderer) { f.ServerID = "srv-other" }},
		{"other node", func(f *InstalledRenderer) { f.NodeID = "node-other" }},
		{"other template", func(f *InstalledRenderer) { f.TemplateID = "builtin-geyser" }},
		{"empty module id", func(f *InstalledRenderer) { f.ModuleID = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, _, _ := discoveryFixture(t, m, func(u string) *InstalledRenderer {
				facts := readyFacts(m, u)
				tc.mutate(facts)
				return facts
			})
			target := listTarget(t, service, m.serverID)
			if target.GetRendererState() != v1.RendererState_RENDERER_STATE_ABSENT {
				t.Fatalf("%s must fail closed to ABSENT, got %s", tc.name, target.GetRendererState())
			}
		})
	}
}

// H3/H4 (honest states): a listening web server alone is never READY.
func TestDiscoveryHonestStates(t *testing.T) {
	m := installedModuleFixture()
	cases := []struct {
		name      string
		mutate    func(*InstalledRenderer)
		wantState v1.RendererState
		wantNote  string
	}{
		{"pending", func(f *InstalledRenderer) { f.Lifecycle = ModulePending }, v1.RendererState_RENDERER_STATE_INSTALLING, "being created/started"},
		{"stopped", func(f *InstalledRenderer) { f.Lifecycle = ModuleStopped }, v1.RendererState_RENDERER_STATE_STOPPED, "module is stopped"},
		{"failed", func(f *InstalledRenderer) { f.Lifecycle = ModuleFailed }, v1.RendererState_RENDERER_STATE_ERROR, "could not be verified"},
		{"identity mismatch", func(f *InstalledRenderer) { f.IdentityVerified = false }, v1.RendererState_RENDERER_STATE_ERROR, "identity, pinned version or privilege"},
		{"off network", func(f *InstalledRenderer) { f.NetworkVerified = false }, v1.RendererState_RENDERER_STATE_ERROR, "not on the owned network"},
		{"wrong world mount", func(f *InstalledRenderer) { f.MountVerified = false }, v1.RendererState_RENDERER_STATE_ABSENT, "world mount does not match"},
		{"writable world mount", func(f *InstalledRenderer) { f.ReadOnlyWorldMount = false }, v1.RendererState_RENDERER_STATE_ABSENT, "not read-only"},
		{"web down", func(f *InstalledRenderer) { f.WebUp = false }, v1.RendererState_RENDERER_STATE_ERROR, "unreachable"},
		{"consent missing", func(f *InstalledRenderer) { f.AcceptDownload = false }, v1.RendererState_RENDERER_STATE_ABSENT, "accept the download/license"},
		{"no world configured", func(f *InstalledRenderer) { f.WorldName = "" }, v1.RendererState_RENDERER_STATE_ABSENT, "no configured map"},
		{"world mismatch", func(f *InstalledRenderer) { f.WorldMatchesServer = false }, v1.RendererState_RENDERER_STATE_ABSENT, "actual world"},
		{"not rendered", func(f *InstalledRenderer) { f.Rendered = false }, v1.RendererState_RENDERER_STATE_RENDERING, "first render"},
		{"map not served", func(f *InstalledRenderer) { f.MapID = "missing" }, v1.RendererState_RENDERER_STATE_ABSENT, "does not serve a configured map"},
		{"legacy verified adoption", func(f *InstalledRenderer) { f.LegacyTag = true; f.ProvenanceVerified = true }, v1.RendererState_RENDERER_STATE_READY, "answered the configured private health probe"},
		{"legacy unverified", func(f *InstalledRenderer) { f.LegacyTag = true }, v1.RendererState_RENDERER_STATE_ERROR, "provenance could not be established"},
		{"ready", func(*InstalledRenderer) {}, v1.RendererState_RENDERER_STATE_READY, "answered the configured private health probe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, _, _ := discoveryFixture(t, m, func(u string) *InstalledRenderer {
				facts := readyFacts(m, u)
				tc.mutate(facts)
				return facts
			})
			target := listTarget(t, service, m.serverID)
			if target.GetRendererState() != tc.wantState {
				t.Fatalf("state = %s, want %s; target = %+v", target.GetRendererState(), tc.wantState, target)
			}
			note := target.GetError() + " " + target.GetFreshness().GetNote()
			if !strings.Contains(note, tc.wantNote) {
				t.Fatalf("note %q does not contain next action %q", note, tc.wantNote)
			}
		})
	}
}

// H2 (no host-port dependency, read-only world mount): the owned network path
// and template-declared container port are sufficient; no host binding exists.
func TestDiscoveryNeedsNoHostPortAndToleratesReadOnlyWorldMount(t *testing.T) {
	// No host-port field exists in InstalledRenderer; the renderer base URL is
	// fixture-owned and uses only the inspected private IP + container port.
	m := installedModuleFixture()
	service, _, _ := discoveryFixture(t, m, func(u string) *InstalledRenderer {
		facts := readyFacts(m, u)
		facts.ContainerPort = 8100
		facts.ReadOnlyWorldMount = true // both live mounts are read-only
		return facts
	})
	target := listTarget(t, service, m.serverID)
	if target.GetRendererState() != v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("read-only world mount with no host port should still be READY, got %s", target.GetRendererState())
	}
}

// H2 (local only): a remote node has no private transport and must stay ABSENT
// unless an explicit operator endpoint is configured.
func TestDiscoveryRequiresLocalNode(t *testing.T) {
	m := installedModuleFixture()
	store := newFakeStore()
	store.addServer(m.serverID, m.nodeID)
	store.setLevelName(m.serverID, m.worldName)
	enforcer := &fakeEnforcer{allow: func(roles []string, _, _, _ string) (bool, error) {
		return len(roles) > 0 && roles[0] == "admin", nil
	}}
	discovery := &fakeDiscovery{facts: readyFacts(m, privateRendererFixture(t))}
	resolver := NewResolverWithDiscovery(Config{}, &fakeLocator{local: map[string]bool{m.nodeID: false}}, discovery)
	service := NewService(ServiceDeps{Store: store, Authorizer: enforcer, Resolver: resolver})
	target := listTarget(t, service, m.serverID)
	if target.GetRendererState() != v1.RendererState_RENDERER_STATE_ABSENT {
		t.Fatalf("remote node must stay ABSENT without explicit endpoint, got %s", target.GetRendererState())
	}
	if discovery.calls != 0 {
		t.Fatalf("discovery must not run for a remote node; calls = %d", discovery.calls)
	}
}

// A failed runtime gate must never cause an HTTP request, even if a stale
// WebUp fact is present. No port probing on invalid identity/network/mount.
func TestDiscoveryDoesNotProbeHttpOnFailedRuntimeGate(t *testing.T) {
	m := installedModuleFixture()
	for _, tc := range []struct {
		name   string
		mutate func(*InstalledRenderer)
	}{
		{"identity", func(f *InstalledRenderer) { f.IdentityVerified = false }},
		{"network", func(f *InstalledRenderer) { f.NetworkVerified = false }},
		{"mount", func(f *InstalledRenderer) { f.MountVerified = false }},
		{"stopped", func(f *InstalledRenderer) { f.Lifecycle = ModuleStopped }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeStore()
			store.addServer(m.serverID, m.nodeID)
			store.setLevelName(m.serverID, m.worldName)
			enforcer := &fakeEnforcer{allow: func(roles []string, _, _, _ string) (bool, error) {
				return len(roles) > 0 && roles[0] == "admin", nil
			}}
			facts := readyFacts(m, "http://127.0.0.1:1") // deliberately unreachable
			facts.WebUp = true                           // stale claim must be ignored
			tc.mutate(facts)
			doer := &funcDoer{fn: func(req *http.Request) (*http.Response, error) {
				t.Fatalf("unexpected HTTP probe to %s on failed runtime gate", req.URL)
				return nil, errors.New("unreachable")
			}}
			resolver := NewResolverWithDiscovery(Config{}, &fakeLocator{local: map[string]bool{m.nodeID: true}}, &fakeDiscovery{facts: facts})
			resolver.hc = doer
			service := NewService(ServiceDeps{Store: store, Authorizer: enforcer, Resolver: resolver})
			_ = listTarget(t, service, m.serverID)
			if doer.hits() != 0 {
				t.Fatalf("HTTP doer called %d times on failed runtime gate", doer.hits())
			}
		})
	}
}

// A discovered renderer is reachable ONLY through the authenticated same-origin
// scoped proxy. The browser never receives the private address.
func TestDiscoveredRendererThroughSameOriginProxyFixture(t *testing.T) {
	m := installedModuleFixture()
	assetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("tile-bytes"))
	}))
	t.Cleanup(assetServer.Close)

	store := newFakeStore()
	store.addServer(m.serverID, m.nodeID)
	users := &fakeAuth{users: map[string]*auth.AuthenticatedUser{
		"tok": {ID: "user-a", Username: "alice", Roles: []string{"user"}},
	}}
	enforcer := &fakeEnforcer{allow: func(roles []string, resource, action, objectID string) (bool, error) {
		return resource == rbac.ResourceWorldView && action == rbac.ActionRead && objectID == m.serverID, nil
	}}
	discovery := &fakeDiscovery{facts: readyFacts(m, assetServer.URL)}
	resolver := NewResolverWithDiscovery(Config{}, &fakeLocator{local: map[string]bool{m.nodeID: true}}, discovery)
	upstream := newCountingDoer(assetServer.Client())
	sessions := NewSessionRegistry()
	handler := NewProxyHandler(ProxyDeps{
		Auth: users, Authorizer: enforcer, Subusers: store, Servers: store,
		Resolver: resolver, Sessions: sessions, Client: upstream,
	})
	key, _ := sessions.Issue(m.serverID, "world", "user-a", time.Minute)

	rec := callProxy(handler, key, "tok", http.MethodGet)
	if rec.Code != http.StatusOK || rec.Body.String() != "tile-bytes" {
		t.Fatalf("proxy status/body = %d/%q; headers = %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if upstream.hits() != 1 {
		t.Fatalf("upstream hits = %d, want 1 (private asset request)", upstream.hits())
	}
	if strings.Contains(rec.Body.String(), assetServer.URL) {
		t.Fatal("private renderer address leaked into proxied asset")
	}
	if rec.Header().Get("Cache-Control") != "private, no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("protected cache policy missing: %v", rec.Header())
	}
}

func TestDiscoveredProxyRechecksRuntimeForExistingSession(t *testing.T) {
	m := installedModuleFixture()
	store := newFakeStore()
	store.addServer(m.serverID, m.nodeID)
	users := &fakeAuth{users: map[string]*auth.AuthenticatedUser{
		"tok": {ID: "user-a", Username: "alice", Roles: []string{"user"}},
	}}
	enforcer := &fakeEnforcer{allow: func([]string, string, string, string) (bool, error) {
		return true, nil
	}}
	discovery := &fakeDiscovery{}
	resolver := NewResolverWithDiscovery(Config{}, &fakeLocator{local: map[string]bool{m.nodeID: true}}, discovery)
	client := &funcDoer{fn: func(*http.Request) (*http.Response, error) {
		t.Fatal("unverified renderer received an asset request")
		return nil, errors.New("unreachable")
	}}
	sessions := NewSessionRegistry()
	handler := NewProxyHandler(ProxyDeps{
		Auth: users, Authorizer: enforcer, Servers: store, Resolver: resolver, Sessions: sessions, Client: client,
	})
	key, _ := sessions.Issue(m.serverID, "world", "user-a", time.Minute)
	for _, tc := range []struct {
		name   string
		mutate func(*InstalledRenderer)
	}{
		{"identity", func(f *InstalledRenderer) { f.IdentityVerified = false }},
		{"network", func(f *InstalledRenderer) { f.NetworkVerified = false }},
		{"mount", func(f *InstalledRenderer) { f.MountVerified = false }},
		{"writable mount", func(f *InstalledRenderer) { f.ReadOnlyWorldMount = false }},
		{"stopped", func(f *InstalledRenderer) { f.Lifecycle = ModuleStopped }},
		{"unverified legacy image", func(f *InstalledRenderer) { f.LegacyTag = true; f.ProvenanceVerified = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			discovery.facts = readyFacts(m, "http://127.0.0.1:1")
			tc.mutate(discovery.facts)
			if got := callProxy(handler, key, "tok", http.MethodGet); got.Code != http.StatusServiceUnavailable {
				t.Fatalf("proxy status = %d, want 503", got.Code)
			}
		})
	}
}

// Non-default world folder identity: explicit precedence and path-component
// validation (OSS-388). An invalid folder never becomes a guessed "world".
func TestConfiguredLevelNamePrecedenceAndValidationFixture(t *testing.T) {
	cases := []struct {
		name string
		cfg  *storage.ServerConfig
		want string
	}{
		{"level-name override wins", &storage.ServerConfig{
			Level: strPtr("panel_level"), CustomServerProperties: strPtr("level-name=actual_world\n"),
		}, "actual_world"},
		{"panel level field", &storage.ServerConfig{Level: strPtr("custom_world")}, "custom_world"},
		{"minecraft default", &storage.ServerConfig{}, "world"},
		{"nil config is unknown", nil, ""},
		{"invalid override fails closed", &storage.ServerConfig{
			Level: strPtr("safe"), CustomServerProperties: strPtr("level-name=../../etc"),
		}, ""},
		{"invalid level field fails closed", &storage.ServerConfig{Level: strPtr("a/b")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := configuredLevelName(tc.cfg); got != tc.want {
				t.Fatalf("configuredLevelName = %q, want %q", got, tc.want)
			}
		})
	}
}

// Discovery errors and no-module are honest ABSENT, never a fallback.
func TestDiscoveryFailureFailsClosed(t *testing.T) {
	m := installedModuleFixture()
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"no module", nil},
		{"lookup error", errors.New("fixture: transport failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, _, _ := discoveryFixture(t, m, func(string) *InstalledRenderer { return nil })
			service.deps.Resolver = NewResolverWithDiscovery(Config{},
				&fakeLocator{local: map[string]bool{m.nodeID: true}},
				&fakeDiscovery{err: tc.err})
			target := listTarget(t, service, m.serverID)
			if target.GetRendererState() != v1.RendererState_RENDERER_STATE_ABSENT {
				t.Fatalf("state = %s, want ABSENT", target.GetRendererState())
			}
		})
	}
}
