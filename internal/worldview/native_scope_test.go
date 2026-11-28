// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"gorm.io/gorm"

	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type nativeScopeFixture struct {
	*fakeStore
	node               *storage.Node
	binding            *storage.WorldBinding
	busy               bool
	restoreID          string
	worldBindingReads  int
	onWorldBindingRead func(read int)
}

func (f *nativeScopeFixture) GetNode(context.Context, string) (*storage.Node, error) {
	if f.node == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return f.node, nil
}

func (f *nativeScopeFixture) GetWorldBinding(context.Context, string, string) (*storage.WorldBinding, error) {
	f.worldBindingReads++
	if f.onWorldBindingRead != nil {
		f.onWorldBindingRead(f.worldBindingReads)
	}
	if f.binding == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return f.binding, nil
}

func (f *nativeScopeFixture) WorldViewOperationState(context.Context, string) (string, bool, error) {
	return f.restoreID, f.busy, nil
}

func TestNativeScopeUsesActualLevelAndCurrentNode(t *testing.T) {
	ctx := context.Background()
	server := &storage.Server{ID: "fixture-server", DataPath: t.TempDir(), NodeID: "registered-controller"}
	f := &nativeScopeFixture{
		fakeStore: newFakeStore(),
		node:      &storage.Node{ID: server.NodeID, IsLocal: true, Enabled: true, EndpointGeneration: 2},
		binding:   &storage.WorldBinding{ID: "active-binding", RevisionID: "current-revision", NodeID: server.NodeID, Generation: 2},
	}
	f.addServer(server.ID, server.NodeID)
	f.setLevelName(server.ID, "a-custom-level")

	ref := &v1.WorldRef{
		Kind: v1.WorldRef_KIND_LIBRARY_BINDING, ServerId: server.ID, LevelName: "a-custom-level", BindingId: f.binding.ID,
	}
	first, err := resolveNativeScope(ctx, f, server, ref)
	if err != nil || first.dataRoot != server.DataPath || first.level != ref.LevelName ||
		first.node.ID != server.NodeID || first.nodeGeneration != 2 || first.revisionID != f.binding.RevisionID {
		t.Fatalf("resolved scope = %+v, error = %v", first, err)
	}

	for _, tc := range []struct {
		name string
		ref  *v1.WorldRef
	}{
		{"another server", &v1.WorldRef{ServerId: "different-server"}},
		{"another level", &v1.WorldRef{LevelName: "other-level"}},
		{"another revision binding", &v1.WorldRef{Kind: v1.WorldRef_KIND_LIBRARY_BINDING, BindingId: "old"}},
		{"a static snapshot", &v1.WorldRef{Kind: v1.WorldRef_KIND_STATIC_IMPORT, SnapshotId: "archive"}},
		{"a binding disguised as server local", &v1.WorldRef{Kind: v1.WorldRef_KIND_SERVER_LOCAL, BindingId: f.binding.ID}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if scope, err := resolveNativeScope(ctx, f, server, tc.ref); !errors.Is(err, errNativeWorldMismatch) || scope != (nativeScope{}) {
				t.Fatalf("unexpected asset scope %+v (%v)", scope, err)
			}
		})
	}

	f.binding.RevisionID = "replacement-revision"
	second, err := resolveNativeScope(ctx, f, server, ref)
	if err != nil || second == first {
		t.Fatalf("same-folder replacement retained previous scope: %+v (%v)", second, err)
	}
	// A new binding event can publish a revision with the same identity.
	f.binding.RevisionID = first.revisionID
	f.binding.BoundAt = time.Unix(12345, 0)
	rebound, err := resolveNativeScope(ctx, f, server, ref)
	if err != nil || rebound == first {
		t.Fatalf("rebinding the same revision retained previous scope: %+v (%v)", rebound, err)
	}
	f.node.EndpointGeneration++
	if _, err := resolveNativeScope(ctx, f, server, ref); !errors.Is(err, errNativeWorldMismatch) {
		t.Fatalf("stale binding should be refused, got %v", err)
	}
}

func TestNativeScopeRefusesUnsafeOrUnavailableStorage(t *testing.T) {
	ctx := context.Background()
	server := &storage.Server{ID: "fixture-server", DataPath: t.TempDir(), NodeID: "default"}
	f := &nativeScopeFixture{fakeStore: newFakeStore()}
	f.addServer(server.ID, server.NodeID)
	f.setLevelName(server.ID, "world")
	got, err := resolveNativeScope(ctx, f, server, nil)
	if err != nil || !got.node.IsLocal || got.nodeGeneration != 1 {
		t.Fatalf("legacy server should resolve controller storage: %+v (%v)", got, err)
	}
	f.busy = true
	if _, err := resolveNativeScope(ctx, f, server, nil); !errors.Is(err, errNativeWorldBusy) {
		t.Fatalf("active operation should refuse rendering, got %v", err)
	}
	f.busy = false
	f.restoreID = "same-level-restore"
	restored, err := resolveNativeScope(ctx, f, server, nil)
	if err != nil || restored == got || restored.restoreID != f.restoreID {
		t.Fatalf("a restore kept the old native scope: %+v (%v)", restored, err)
	}

	f.setLevelName(server.ID, "../../outside")
	if _, err := resolveNativeScope(ctx, f, server, nil); !errors.Is(err, errNativeSourceAbsent) {
		t.Fatalf("invalid server level should refuse rendering, got %v", err)
	}
	f.setLevelName(server.ID, "world")
	server.NodeID = "registered-node"

	for _, tc := range []*storage.Node{
		nil,
		{ID: server.NodeID, Enabled: true, IsLocal: false, EndpointGeneration: 1},
		{ID: server.NodeID, Enabled: false, IsLocal: true, EndpointGeneration: 1},
		{ID: server.NodeID, Enabled: true, IsLocal: true, EndpointGeneration: 0},
	} {
		f.node = tc
		if _, err := resolveNativeScope(ctx, f, server, nil); !errors.Is(err, errNativeSourceAbsent) {
			t.Fatalf("unqualified owner %+v should refuse rendering, got %v", tc, err)
		}
	}
}

func nativeProbeServiceFixture(t *testing.T, servers int) *Service {
	t.Helper()
	f := &nativeScopeFixture{fakeStore: newFakeStore()}
	const level = "actual-world"
	nbt, err := worldlib.WriteFreshLevelData(map[string]string{"level-name": level}, "3953")
	if err != nil {
		t.Fatal(err)
	}
	for i := range servers {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, level), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, level, "level.dat"), nbt, 0o644); err != nil {
			t.Fatal(err)
		}
		id := fmt.Sprintf("native-server-%d", i)
		f.addServer(id, "default")
		f.servers[id].DataPath = root
		f.setLevelName(id, level)
	}
	return NewService(ServiceDeps{
		Store:      f,
		Authorizer: &fakeEnforcer{allow: func([]string, string, string, string) (bool, error) { return true, nil }},
		Native:     NewNativeMap(f, nodestorage.NewResolver(nil)),
	})
}

func TestNativeInventoryDefersUnexaminedWorldsFixture(t *testing.T) {
	const serverCount = nativeInventoryProbeLimit + 3
	service := nativeProbeServiceFixture(t, serverCount)
	ctx := svcCtx("native-viewer", "user")
	list, err := service.ListWorldViewTargets(ctx, connect.NewRequest(&v1.ListWorldViewTargetsRequest{}))
	if err != nil || len(list.Msg.GetTargets()) != serverCount {
		t.Fatalf("all authorized worlds must stay discoverable: %v (%v)", list, err)
	}
	qualified := 0
	var deferred *v1.WorldViewTarget
	for _, target := range list.Msg.GetTargets() {
		switch target.GetRendererState() {
		case v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE:
			qualified++
		case v1.RendererState_RENDERER_STATE_UNSPECIFIED:
			if target.GetWorldRef().GetServerId() != target.GetServerId() ||
				target.GetWorldRef().GetLevelName() == "" || !strings.Contains(target.GetError(), "open this world") {
				t.Fatalf("deferred inventory row lost its active world link or action: %+v", target)
			}
			deferred = target
		default:
			t.Fatalf("inventory returned an unexpected state: %+v", target)
		}
	}
	if qualified == 0 || qualified > nativeInventoryProbeLimit || deferred == nil {
		t.Fatalf("inventory probe budget violated: qualified %d of %d", qualified, serverCount)
	}

	status, err := service.GetWorldViewStatus(ctx, connect.NewRequest(&v1.GetWorldViewStatusRequest{
		ServerId: deferred.GetServerId(), WorldRef: deferred.GetWorldRef(),
	}))
	if err != nil || status.Msg.GetTarget().GetRendererState() != v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE {
		t.Fatalf("direct status could not check the deferred world: %v (%v)", status, err)
	}
}

func TestInstalledBlueMapUpgradesAndFallsBackOnActiveWorld(t *testing.T) {
	service := nativeProbeServiceFixture(t, 1)
	m := installedModuleFixture()
	m.serverID, m.worldName = "native-server-0", "actual-world"
	discovery := &fakeDiscovery{facts: readyFacts(m, privateRendererFixture(t))}
	service.deps.Resolver = NewResolverWithDiscovery(Config{}, &fakeLocator{local: map[string]bool{m.nodeID: true}}, discovery)
	ctx := svcCtx("native-viewer", "user")
	var embeddedKey string
	for _, tc := range []struct {
		name      string
		lifecycle ModuleLifecycle
		rendered  bool
		wantType  v1.RendererType
		wantState v1.RendererState
	}{
		{"installed but waiting for tiles", ModuleRunning, false, v1.RendererType_RENDERER_TYPE_NATIVE, v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE},
		{"verified first render", ModuleRunning, true, v1.RendererType_RENDERER_TYPE_BLUEMAP, v1.RendererState_RENDERER_STATE_READY},
		{"renderer stopped", ModuleStopped, true, v1.RendererType_RENDERER_TYPE_NATIVE, v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE},
	} {
		t.Run(tc.name, func(t *testing.T) {
			discovery.facts.Lifecycle, discovery.facts.Rendered = tc.lifecycle, tc.rendered
			status, err := service.GetWorldViewStatus(ctx, connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: m.serverID}))
			if err != nil {
				t.Fatal(err)
			}
			target := status.Msg.GetTarget()
			if target.GetRendererState() != tc.wantState || target.GetRendererType() != tc.wantType ||
				target.GetWorldRef().GetLevelName() != m.worldName {
				t.Fatalf("status did not select the active world's expected renderer: %v", target)
			}
			if (target.GetError() != "") != (tc.wantType == v1.RendererType_RENDERER_TYPE_NATIVE) {
				t.Fatalf("unexpected fallback reason: %q", target.GetError())
			}
			caps, err := service.ListCapabilities(ctx, connect.NewRequest(&v1.ListCapabilitiesRequest{ServerId: m.serverID}))
			if err != nil || caps.Msg.GetRendererType() != tc.wantType {
				t.Fatalf("capabilities do not match active renderer: %v (%v)", caps, err)
			}
			session, err := service.GetWorldViewSession(ctx, connect.NewRequest(&v1.GetWorldViewSessionRequest{
				ServerId: m.serverID, WorldRef: target.GetWorldRef(),
			}))
			if err != nil || session.Msg.GetState() != tc.wantState || session.Msg.GetProxyPath() == "" {
				t.Fatalf("session does not match active renderer: %v (%v)", session, err)
			}
			if tc.wantState == v1.RendererState_RENDERER_STATE_READY {
				embeddedKey = strings.Trim(strings.TrimPrefix(session.Msg.GetProxyPath(), ProxyPrefix), "/")
			}
		})
	}

	discovery.facts.Lifecycle = ModuleRunning
	for _, ref := range []*v1.WorldRef{
		{Kind: v1.WorldRef_KIND_SERVER_LOCAL, LevelName: "previous-level"},
		{Kind: v1.WorldRef_KIND_STATIC_IMPORT, SnapshotId: "saved", LevelName: m.worldName},
	} {
		session, err := service.GetWorldViewSession(ctx, connect.NewRequest(&v1.GetWorldViewSessionRequest{ServerId: m.serverID, WorldRef: ref}))
		if err != nil || session.Msg.GetProxyPath() != "" {
			t.Fatalf("active BlueMap must not serve a different world reference: %v (%v)", session, err)
		}
	}

	discovery.facts.WorldName = "newly-active-level"
	handler := NewProxyHandler(ProxyDeps{
		Auth:       &fakeAuth{users: map[string]*auth.AuthenticatedUser{"viewer-token": auth.GetUserFromContext(ctx)}},
		Authorizer: service.deps.Authorizer, Servers: service.deps.Store,
		Resolver: service.deps.Resolver, Sessions: service.SessionRegistry(),
	})
	if response := callProxy(handler, embeddedKey, "viewer-token", http.MethodGet); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("previous BlueMap session followed the changed world: %d %s", response.Code, response.Body.String())
	}
}

func TestNativeInspectionCongestionPreservesIssuedSessionFixture(t *testing.T) {
	service := nativeProbeServiceFixture(t, 1)
	ctx := svcCtx("native-viewer", "user")
	const serverID = "native-server-0"
	request := func() *connect.Request[v1.GetWorldViewSessionRequest] {
		return connect.NewRequest(&v1.GetWorldViewSessionRequest{ServerId: serverID})
	}
	session, err := service.GetWorldViewSession(ctx, request())
	if err != nil || session.Msg.GetProxyPath() == "" {
		t.Fatalf("initial native session unavailable: %v (%v)", session, err)
	}
	native := service.deps.Native
	for range nativeWorkSlots {
		native.slots <- struct{}{}
	}
	status := connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: serverID})
	if _, err := service.GetWorldViewStatus(ctx, status); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("busy qualification must be a retryable status error: %v", err)
	}
	if _, err := service.GetWorldViewSession(ctx, request()); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("busy qualification must be a retryable renewal error: %v", err)
	}

	proxy := NewProxyHandler(ProxyDeps{
		Auth:       &fakeAuth{users: map[string]*auth.AuthenticatedUser{"fixture-token": auth.GetUserFromContext(ctx)}},
		Authorizer: service.deps.Authorizer, Servers: service.deps.Store,
		Sessions: service.SessionRegistry(), Native: native,
	})
	manifest := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, session.Msg.GetProxyPath()+nativeManifestPath, nil)
		req.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "fixture-token"})
		response := httptest.NewRecorder()
		proxy.ServeHTTP(response, req)
		return response
	}
	if response := manifest(); response.Code != http.StatusTooManyRequests {
		t.Fatalf("asset did not share the same work budget: %d", response.Code)
	}
	for range nativeWorkSlots {
		<-native.slots
	}
	if response := manifest(); response.Code != http.StatusOK {
		t.Fatalf("congestion invalidated the original session: %d %s", response.Code, response.Body)
	}
}

func TestNativeQualificationDefersWhileWorldChangesFixture(t *testing.T) {
	service := nativeProbeServiceFixture(t, 1)
	ctx := svcCtx("native-viewer", "user")
	service.deps.Store.(*nativeScopeFixture).busy = true
	const serverID = "native-server-0"
	_, statusErr := service.GetWorldViewStatus(ctx, connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: serverID}))
	_, sessionErr := service.GetWorldViewSession(ctx, connect.NewRequest(&v1.GetWorldViewSessionRequest{ServerId: serverID}))
	_, capsErr := service.ListCapabilities(ctx, connect.NewRequest(&v1.ListCapabilitiesRequest{ServerId: serverID}))
	for _, err := range []error{statusErr, sessionErr, capsErr} {
		if connect.CodeOf(err) != connect.CodeUnavailable || !errors.Is(err, errNativeWorldBusy) {
			t.Fatalf("a live server operation did not return its temporary reason: %v", err)
		}
	}
	list, err := service.ListWorldViewTargets(ctx, connect.NewRequest(&v1.ListWorldViewTargetsRequest{}))
	if err != nil || len(list.Msg.GetTargets()) != 1 {
		t.Fatalf("temporarily changing world disappeared from inventory: %v (%v)", list, err)
	}
	target := list.Msg.GetTargets()[0]
	if target.GetRendererState() != v1.RendererState_RENDERER_STATE_UNSPECIFIED ||
		target.GetError() != errNativeWorldBusy.Error() || target.GetWorldRef().GetLevelName() == "" {
		t.Fatalf("inventory lost the changing world or its reason: %+v", target)
	}
}

func TestNativeQualificationPreservesExplicitEndpointSelectionFixture(t *testing.T) {
	service := nativeProbeServiceFixture(t, 1)
	ctx := svcCtx("native-viewer", "user")
	const serverID = "native-server-0"
	config := Config{Endpoints: []Endpoint{{ServerID: serverID, Kind: RendererBlueMap, Host: "127.0.0.1", Port: 8100}}}
	// A node classification error wraps ErrRendererUnavailable even for a
	// matching explicit renderer. It must never trigger native fallback.
	service.deps.Resolver = NewResolver(config, &fakeLocator{err: errors.New("node classification unavailable")})
	get := func() (*v1.WorldViewTarget, error) {
		status, err := service.GetWorldViewStatus(ctx, connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: serverID}))
		if err != nil {
			return nil, err
		}
		return status.Msg.GetTarget(), nil
	}
	target, err := get()
	if err != nil || target.GetRendererType() == v1.RendererType_RENDERER_TYPE_NATIVE ||
		target.GetRendererState() == v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE {
		t.Fatalf("explicit endpoint unexpectedly fell back to native: %v (%v)", target, err)
	}

	// An unavailable discovery adapter with no explicit endpoint can still
	// offer native terrain for the already-authorized controller-local world.
	service.deps.Resolver = NewResolver(Config{}, &fakeLocator{err: errors.New("discovery unavailable")})
	target, err = get()
	if err != nil || target.GetRendererState() != v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE {
		t.Fatalf("unidentified optional renderer blocked valid local terrain: %v (%v)", target, err)
	}
}
