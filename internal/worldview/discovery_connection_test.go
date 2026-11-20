// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// OSS-388 renderer/Discovery connection qualification (fixture only).
//
// These tests drive the PUBLIC service seams (ListWorldViewTargets and
// GetWorldViewSession) with an owned, disposable httptest renderer standing in
// for the PRIVATE node-local endpoint. They pin the fail-closed behavior of the
// renderer connection when its authoritative /settings.json map list cannot be
// trusted (401/403, malformed JSON) and when the probe context is cancelled.
// No live renderer, container, Docker socket, host port, world or browser is
// involved; the test name suffix "Fixture" makes that boundary explicit.

// rendererConnectionService builds a WorldViewService whose discovered module's
// private endpoint is an owned httptest fixture with the supplied handler.
func rendererConnectionService(t *testing.T, handler http.HandlerFunc) (*Service, *moduleFixture) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	m := installedModuleFixture()
	store := newFakeStore()
	store.addServer(m.serverID, m.nodeID)
	store.setLevelName(m.serverID, m.worldName)
	enforcer := &fakeEnforcer{allow: func(roles []string, _, _, _ string) (bool, error) {
		return len(roles) > 0 && roles[0] == "admin", nil
	}}
	discovery := &fakeDiscovery{facts: readyFacts(m, srv.URL)}
	resolver := NewResolverWithDiscovery(Config{}, &fakeLocator{local: map[string]bool{m.nodeID: true}}, discovery)
	service := NewService(ServiceDeps{Store: store, Authorizer: enforcer, Resolver: resolver})
	return service, m
}

// settingsResponseFixture models a private BlueMap endpoint where "/" is a
// healthy listener but /settings.json returns the supplied authoritative map
// list (or an error/malformed body). A valid list proves the fixture is wired.
func settingsResponseFixture(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/settings.json" {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
			return
		}
		serveBlueMapFixture(w, r)
	}
}

func TestDiscoveredBlueMapChecksEveryServedMapTextureFixture(t *testing.T) {
	textures := []byte(`[{"name":"block/atlas.png"}]`)
	gzipped := gzipBytes(t, textures)
	for _, tc := range []struct {
		name      string
		status    int
		plain     []byte
		gzip      []byte
		wantReady bool
	}{
		{name: "both maps ready", status: http.StatusOK, plain: textures, wantReady: true},
		{name: "gzip sibling ready", status: http.StatusNotFound, gzip: gzipped, wantReady: true},
		{name: "plain asset denied despite available gzip sibling", status: http.StatusForbidden, gzip: gzipped},
		{name: "secondary map missing", status: http.StatusNotFound},
		{name: "secondary map serves its HTML fallback", status: http.StatusOK, plain: []byte("<html>BlueMap</html>")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service, m := rendererConnectionService(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/settings.json":
					_, _ = w.Write([]byte(`{"maps":["world","world_nether"]}`))
				case "/maps/world/textures.json":
					_, _ = w.Write(textures)
				case "/maps/world_nether/textures.json":
					w.WriteHeader(tc.status)
					_, _ = w.Write(tc.plain)
				case "/maps/world_nether/textures.json.gz":
					if len(tc.gzip) != 0 {
						_, _ = w.Write(tc.gzip)
						return
					}
					http.NotFound(w, r)
				default:
					serveBlueMapFixture(w, r)
				}
			})
			target := listTarget(t, service, m.serverID)
			session, err := service.GetWorldViewSession(svcCtx("admin", "admin"), connect.NewRequest(&v1.GetWorldViewSessionRequest{
				ServerId: m.serverID,
			}))
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantReady {
				if target.GetRendererState() != v1.RendererState_RENDERER_STATE_READY || session.Msg.GetProxyPath() == "" {
					t.Fatalf("expected READY with a session, got state=%s session=%v", target.GetRendererState(), session.Msg)
				}
				return
			}
			if target.GetRendererState() != v1.RendererState_RENDERER_STATE_ERROR ||
				session.Msg.GetState() != v1.RendererState_RENDERER_STATE_ERROR || session.Msg.GetProxyPath() != "" {
				t.Fatalf("expected renderer ERROR without a session, got state=%s session=%v", target.GetRendererState(), session.Msg)
			}
			if detail := target.GetError(); !strings.Contains(detail, "map textures could not be read") || session.Msg.GetReason() != detail {
				t.Fatalf("expected the same actionable textures diagnostic for the target and session, got %q and %q", detail, session.Msg.GetReason())
			}
		})
	}
}

// TestDiscoveredBlueMapUntrustedSettingsFailsClosedFixture pins that a running
// web server is NEVER treated as a ready renderer when its own map list cannot
// be read or trusted. 401/403 and malformed/invalid JSON shapes must fail closed
// to a non-ready state and must never issue a map session key (no iframe can be
// mounted). The configured map ID safeguard is preserved for v5.29 string-array
// settings.
func TestDiscoveredBlueMapUntrustedSettingsFailsClosedFixture(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      string
		wantReady bool
	}{
		{"settings 401", http.StatusUnauthorized, `{"maps":[{"id":"world"}]}`, false},
		{"settings 403", http.StatusForbidden, `{"maps":[{"id":"world"}]}`, false},
		{"malformed settings", http.StatusOK, `{"maps":`, false},
		{"mixed numeric invalid", http.StatusOK, `{"maps":[123,"world"]}`, false},
		{"valid control", http.StatusOK, `{"maps":[{"id":"world"}]}`, true},
		{"v5.29 string map id", http.StatusOK, `{"maps":["world"]}`, true},
		{"v5.29 string wrong map id", http.StatusOK, `{"maps":["other"]}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service, m := rendererConnectionService(t, settingsResponseFixture(tc.status, tc.body))
			target := listTarget(t, service, m.serverID)
			session, err := service.GetWorldViewSession(svcCtx("admin", "admin"), connect.NewRequest(&v1.GetWorldViewSessionRequest{
				ServerId: m.serverID,
			}))
			if err != nil {
				t.Fatalf("GetWorldViewSession: %v", err)
			}

			if tc.wantReady {
				if target.GetRendererState() != v1.RendererState_RENDERER_STATE_READY || session.Msg.GetProxyPath() == "" {
					t.Fatalf("expected READY with a session, got state=%s session=%+v target=%+v", target.GetRendererState(), session.Msg, target)
				}
				return
			}

			if target.GetRendererState() == v1.RendererState_RENDERER_STATE_READY {
				t.Fatalf("untrusted/invalid settings must not be READY: %+v", target)
			}
			if session.Msg.GetProxyPath() != "" || session.Msg.GetReason() == "" {
				t.Fatalf("untrusted/invalid settings must issue no map session: %+v", session.Msg)
			}
		})
	}
}

// TestDiscoveredBlueMapCancellationFailsClosedFixture proves the public service
// seam fails closed on an already-cancelled context: no READY state, no map
// session and a prompt return. The resolver short-circuits on ctx.Err() before
// discovery/probe (config.go Resolve); the companion probe-level test below
// covers context threading into the health request itself.
func TestDiscoveredBlueMapCancellationFailsClosedFixture(t *testing.T) {
	service, m := rendererConnectionService(t, settingsResponseFixture(http.StatusOK, `{"maps":[{"id":"world"}]}`))

	ctx, cancel := context.WithCancel(svcCtx("admin", "admin"))
	cancel() // pre-cancel: the probe must observe it before any fixture is read

	type result struct {
		state  v1.RendererState
		path   string
		reason string
		err    error
	}
	out := make(chan result, 1)
	go func() {
		resp, err := service.ListWorldViewTargets(ctx, connect.NewRequest(&v1.ListWorldViewTargetsRequest{}))
		state := v1.RendererState_RENDERER_STATE_UNSPECIFIED
		if err == nil {
			for _, target := range resp.Msg.GetTargets() {
				if target.GetServerId() == m.serverID {
					state = target.GetRendererState()
				}
			}
		}
		sess, serr := service.GetWorldViewSession(ctx, connect.NewRequest(&v1.GetWorldViewSessionRequest{ServerId: m.serverID}))
		path, reason := "", ""
		if serr == nil {
			path, reason = sess.Msg.GetProxyPath(), sess.Msg.GetReason()
		}
		out <- result{state: state, path: path, reason: reason, err: err}
	}()

	select {
	case got := <-out:
		if got.err != nil {
			t.Fatalf("ListWorldViewTargets with cancelled ctx: %v", got.err)
		}
		if got.state == v1.RendererState_RENDERER_STATE_READY {
			t.Fatalf("cancelled probe must not be READY, got %s", got.state)
		}
		if got.path != "" || got.reason == "" {
			t.Fatalf("cancelled probe must issue no map session: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("discovery health probe did not honour cancellation (hung)")
	}
}

// TestDiscoveredBlueMapProbeContextCancellationFixture exercises the adapter
// probe seam directly: a pre-cancelled context must reach the HTTP doer and
// fail closed with an error and a non-READY observation. All facts are the
// task-owned ready set; the doer is in-memory. No real transport or renderer.
func TestDiscoveredBlueMapProbeContextCancellationFixture(t *testing.T) {
	facts := readyFacts(installedModuleFixture(), "http://127.0.0.1:1")
	doer := &funcDoer{fn: func(req *http.Request) (*http.Response, error) {
		if err := req.Context().Err(); err == nil {
			t.Errorf("health probe did not carry the cancelled context")
			return nil, context.Canceled
		}
		return nil, req.Context().Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	h, err := newDiscoveredRenderer(facts, doer).Health(ctx)
	if err == nil {
		t.Fatal("Health with a cancelled context must return an error")
	}
	if h == nil || h.State == v1.RendererState_RENDERER_STATE_READY {
		t.Fatalf("cancelled probe must fail closed, got %+v", h)
	}
	if doer.hits() == 0 {
		t.Fatal("probe never reached the HTTP doer; cancellation threading not exercised")
	}
}
