// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/gorilla/websocket"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const mgmtServiceSecret = "MgmtQualOSS350FixtureSecret0123456789abc"

// mgmtServiceFixture speaks the real vanilla 1.21.9 JSON-RPC shapes. It is
// deliberately strict so the panel's own service path is qualified, not a
// permissive stand-in that accepts the pre-fix frames.
type mgmtServiceFixture struct {
	server     *httptest.Server
	url        string
	mu         sync.Mutex
	frames     []string
	violations []string
}

func newMgmtServiceFixture(t *testing.T) *mgmtServiceFixture {
	t.Helper()
	f := &mgmtServiceFixture{}
	upgrader := websocket.Upgrader{
		CheckOrigin:  func(*http.Request) bool { return true },
		Subprotocols: []string{"minecraft-v1", mgmtServiceSecret},
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+mgmtServiceSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck // test fixture
		var writeMu sync.Mutex
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.frames = append(f.frames, string(data))
			f.mu.Unlock()
			var req struct {
				ID     uint64          `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			_ = json.Unmarshal(data, &req)
			var result any
			switch req.Method {
			case "minecraft:players":
				if len(req.Params) != 0 && string(req.Params) != "[]" {
					f.violate("minecraft:players must omit params, got %s", req.Params)
				}
				result = []map[string]any{{"id": "f865b2ce-4755-3723-bea5-c87ecc602a4d", "name": "qualplayer1"}}
			case "minecraft:players/kick":
				want := `{"kick":[{"player":{"name":"qualplayer1"},"message":{"literal":"qual kick"}}]}`
				if string(req.Params) != want {
					f.violate("kick params = %s, want %s", req.Params, want)
				}
				result = []map[string]any{{"name": "qualplayer1"}}
			default:
				result = []map[string]any{}
			}
			writeMu.Lock()
			err = conn.WriteJSON(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
			writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}))
	f.url = "ws" + strings.TrimPrefix(f.server.URL, "http")
	t.Cleanup(f.server.Close)
	return f
}

func (f *mgmtServiceFixture) violate(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.violations = append(f.violations, fmt.Sprintf(format, args...))
}

func (f *mgmtServiceFixture) check(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, violation := range f.violations {
		t.Errorf("SERVICE WIRE VIOLATION: %s", violation)
	}
}

func (f *mgmtServiceFixture) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.frames...)
}

func newMgmtServiceFixtureService(t *testing.T, f *mgmtServiceFixture, version string) (*PlayerService, *storage.Store, *negotiatorShim) {
	t.Helper()
	store, err := storage.NewSQLiteStore(&config.Config{
		Database: config.DatabaseConfig{Path: filepath.Join(t.TempDir(), "players.db"), AutoMigrate: true},
		Docker:   config.DockerConfig{Host: "unix:///var/run/docker.sock"},
	})
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.DB().Create(&storage.Server{
		ID: "srv-1", Name: "Mgmt", NodeID: "default", MCVersion: version,
		DataPath: filepath.Join(t.TempDir(), "data"), ContainerID: "container-1",
	}).Error; err != nil {
		t.Fatalf("create server: %v", err)
	}
	cfg := &config.Config{Minecraft: config.MinecraftConfig{GlobalConfig: map[string]any{
		"player_edition":           "java",
		"management_server_url":    f.url,
		"management_server_secret": mgmtServiceSecret,
	}}}
	shim := newNegotiatorShim(cfg, nil)
	t.Cleanup(shim.Close)
	svc := NewPlayerService(store, logger.New(), nil)
	svc.SetSourceNegotiator(shim)
	svc.SetSkinProvider(&fakeSkin{})
	svc.SetDeepDataReader(&fakeDeep{})
	return svc, store, shim
}

func TestPlayerServiceManagementProtocolIntegration(t *testing.T) {
	f := newMgmtServiceFixture(t)
	svc, store, shim := newMgmtServiceFixtureService(t, f, "1.21.9")
	ctx := context.Background()
	server, err := store.GetServer(ctx, "srv-1")
	if err != nil {
		t.Fatalf("get server: %v", err)
	}
	source, caps, err := shim.Negotiate(ctx, server, nil)
	if err != nil || caps.ChosenSource != "management_protocol" {
		t.Fatalf("shim negotiation: %+v / %v", caps, err)
	}

	roster, err := svc.ListPlayers(ctx, connect.NewRequest(&v1.ListPlayersRequest{ServerId: "srv-1"}))
	if err != nil {
		t.Fatalf("ListPlayers: %v", err)
	}
	if !strings.Contains(roster.Msg.SourceDetail, "management_protocol") {
		t.Fatalf("source detail = %q", roster.Msg.SourceDetail)
	}
	foundPlayer := false
	for _, p := range roster.Msg.Players {
		if p.Ref.Name == "qualplayer1" {
			foundPlayer = true
		}
	}
	if !foundPlayer {
		t.Fatalf("roster did not ingest the live management player: %+v", roster.Msg.Players)
	}

	capabilities, err := svc.ListPlayerCapabilities(ctx, connect.NewRequest(&v1.ListPlayerCapabilitiesRequest{ServerId: "srv-1"}))
	if err != nil {
		t.Fatalf("ListPlayerCapabilities: %v", err)
	}
	managementSupported := false
	for _, status := range capabilities.Msg.Capabilities {
		if status.Capability == v1.PlayerCapability_PLAYER_CAPABILITY_MANAGEMENT_PROTOCOL &&
			status.State == v1.PlayerSupportState_PLAYER_SUPPORT_STATE_SUPPORTED {
			managementSupported = true
		}
	}
	if !managementSupported {
		t.Fatalf("capability RPC did not report management protocol supported: %+v", capabilities.Msg.Capabilities)
	}

	identity := seedIdentity(t, store, "qualplayer1", "f865b2ce-4755-3723-bea5-c87ecc602a4d", "srv-1", time.Now())
	authCtx := auth.WithUser(ctx, &auth.AuthenticatedUser{ID: "actor-1", Username: "mod"})
	if _, err := svc.KickPlayer(authCtx, connect.NewRequest(&v1.KickPlayerRequest{
		ServerId: "srv-1", IdentityKey: identity.ID, Reason: "qual kick", RequestId: "oss350-realkick-1",
	})); err != nil {
		t.Fatalf("KickPlayer through service: %v", err)
	}

	f.check(t)
	frames := f.recorded()
	if len(frames) == 0 || strings.Contains(frames[0], `"params"`) {
		t.Fatalf("first frame must omit params: %v", frames)
	}
	var kickFrame string
	for _, frame := range frames {
		if strings.Contains(frame, "minecraft:players/kick") {
			kickFrame = frame
		}
	}
	wantKick := `{"jsonrpc":"2.0","id":2,"method":"minecraft:players/kick","params":{"kick":[{"player":{"name":"qualplayer1"},"message":{"literal":"qual kick"}}]}}`
	if kickFrame != wantKick {
		t.Fatalf("kick frame =\n%s\nwant\n%s", kickFrame, wantKick)
	}

	// The service owns the adapter's background reader: Stop must retire it.
	svc.Stop()
	if _, err := source.List(ctx); err == nil {
		t.Fatal("adapter remained usable after PlayerService.Stop")
	}
}

func TestPlayerServiceManagementVersionMismatchFailsClosed(t *testing.T) {
	f := newMgmtServiceFixture(t)
	svc, _, _ := newMgmtServiceFixtureService(t, f, "1.21.8")
	ctx := context.Background()

	roster, err := svc.ListPlayers(ctx, connect.NewRequest(&v1.ListPlayersRequest{ServerId: "srv-1"}))
	if err != nil {
		t.Fatalf("ListPlayers: %v", err)
	}
	if strings.Contains(roster.Msg.SourceDetail, "management_protocol") {
		t.Fatalf("unqualified version advertised management protocol: %q", roster.Msg.SourceDetail)
	}
	capabilities, err := svc.ListPlayerCapabilities(ctx, connect.NewRequest(&v1.ListPlayerCapabilitiesRequest{ServerId: "srv-1"}))
	if err != nil {
		t.Fatalf("ListPlayerCapabilities: %v", err)
	}
	for _, status := range capabilities.Msg.Capabilities {
		if status.Capability == v1.PlayerCapability_PLAYER_CAPABILITY_MANAGEMENT_PROTOCOL &&
			status.State == v1.PlayerSupportState_PLAYER_SUPPORT_STATE_SUPPORTED {
			t.Fatalf("unqualified version reported management protocol supported: %+v", status)
		}
	}
	f.check(t)
	if frames := f.recorded(); len(frames) != 0 {
		t.Fatalf("unqualified version sent management frames: %v", frames)
	}
}
