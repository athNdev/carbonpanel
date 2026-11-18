// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const fixtureManagementSecret = "fixture-management-secret-9f3a"

// deferredReply tells the fixture that the responder will write its own reply
// later. It is used to exercise response correlation when replies arrive out
// of order.
type deferredReply struct{}

type fixtureResponder func(req rpcRequest, conn *websocket.Conn, write func(any) error) (result any, rpcErr *rpcErrorPayload)

type mgmtFixture struct {
	server      *httptest.Server
	url         string
	mu          sync.Mutex
	requests    []rpcRequest
	authHeaders []string
	subprotos   []string
	connections int
}

// newManagementFixture starts an in-process WebSocket JSON-RPC fixture. It is
// not a Minecraft server; see the D-MGMT notice in doc.go.
func newManagementFixture(t *testing.T, secret string, respond fixtureResponder, onConnect func(*websocket.Conn)) *mgmtFixture {
	t.Helper()
	fixture := &mgmtFixture{}
	upgrader := websocket.Upgrader{
		CheckOrigin:  func(*http.Request) bool { return true },
		Subprotocols: []string{secret},
	}
	fixture.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		fixture.mu.Lock()
		fixture.authHeaders = append(fixture.authHeaders, auth)
		fixture.mu.Unlock()
		if auth != "Bearer "+secret {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized token "+secret, http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func(closer func() error) { _ = closer() }(conn.Close)
		fixture.mu.Lock()
		fixture.connections++
		fixture.subprotos = append(fixture.subprotos, conn.Subprotocol())
		fixture.mu.Unlock()
		if onConnect != nil {
			onConnect(conn)
		}
		var writeMu sync.Mutex
		write := func(payload any) error {
			writeMu.Lock()
			defer writeMu.Unlock()
			return conn.WriteJSON(payload)
		}
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var req rpcRequest
			if json.Unmarshal(data, &req) != nil {
				continue
			}
			fixture.mu.Lock()
			fixture.requests = append(fixture.requests, req)
			fixture.mu.Unlock()
			result, rpcErr := respond(req, conn, write)
			if _, ok := result.(deferredReply); ok {
				continue
			}
			reply := map[string]any{"jsonrpc": jsonRPCVersion, "id": req.ID}
			if rpcErr != nil {
				reply["error"] = rpcErr
			} else {
				reply["result"] = result
			}
			if err := write(reply); err != nil {
				return
			}
		}
	}))
	fixture.url = "ws" + strings.TrimPrefix(fixture.server.URL, "http")
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *mgmtFixture) recorded() ([]rpcRequest, []string, []string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rpcRequest(nil), f.requests...), append([]string(nil), f.authHeaders...), append([]string(nil), f.subprotos...), f.connections
}

func fixtureServerInfo(url string) ServerInfo {
	return ServerInfo{
		ServerID:          "srv-1",
		NodeID:            "node-a",
		Edition:           "java",
		MCVersion:         "1.21.9",
		ManagementEnabled: true,
		ManagementURL:     url,
		ManagementSecret:  fixtureManagementSecret,
	}
}

func assertNoSecret(t *testing.T, text string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			t.Fatalf("secret leaked into output: %q", secret)
		}
	}
}

func TestManagementAdapterFixtureProtocolAndMutations(t *testing.T) {
	fixture := newManagementFixture(t, fixtureManagementSecret, func(req rpcRequest, _ *websocket.Conn, _ func(any) error) (any, *rpcErrorPayload) {
		if req.Method == MethodPlayersList {
			return map[string]any{"players": []map[string]any{
				{"id": "00000000-0000-0000-0000-000000000001", "name": "Steve"},
				{"uuid": "00000000000000000000000000000002", "name": "Alex"},
			}}, nil
		}
		return map[string]any{"ok": true}, nil
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	adapter, caps, err := Negotiate(ctx, fixtureServerInfo(fixture.url), nil)
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	if caps.ChosenSource != SourceManagementProtocol || !caps.Management || adapter.Source() != SourceManagementProtocol {
		t.Fatalf("unexpected negotiation result: %+v", caps)
	}
	m, ok := adapter.(*ManagementAdapter)
	if !ok {
		t.Fatalf("expected *ManagementAdapter, got %T", adapter)
	}
	t.Cleanup(func() { _ = m.Close() })

	players, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(players) != 2 || players[0].Name != "Steve" || players[0].UUID != "00000000-0000-0000-0000-000000000001" || players[1].UUID != "00000000-0000-0000-0000-000000000002" {
		t.Fatalf("unexpected roster: %+v", players)
	}
	roster, observedAt, source := m.Snapshot()
	if source != SourceManagementProtocol || observedAt.IsZero() || len(roster) != 2 {
		t.Fatalf("unexpected snapshot provenance: %+v %v %q", roster, observedAt, source)
	}

	for _, action := range []struct {
		name string
		run  func() error
	}{
		{"kick", func() error { return m.Kick(ctx, "Steve", "griefing") }},
		{"ban", func() error { return m.Ban(ctx, "Alex", "repeat offense") }},
		{"pardon", func() error { return m.Pardon(ctx, "Alex") }},
		{"op", func() error { return m.SetOperator(ctx, "Steve", 4, false) }},
		{"deop", func() error { return m.SetOperator(ctx, "Steve", 0, true) }},
		{"allowlist add", func() error { return m.SetWhitelist(ctx, "Steve", false) }},
		{"allowlist remove", func() error { return m.SetWhitelist(ctx, "Steve", true) }},
	} {
		if err := action.run(); err != nil {
			t.Fatalf("%s: %v", action.name, err)
		}
	}

	requests, authHeaders, subprotos, connections := fixture.recorded()
	if len(requests) != 8 || connections != 1 {
		t.Fatalf("expected 8 requests on one connection, got %d requests / %d connections", len(requests), connections)
	}
	if len(authHeaders) == 0 || authHeaders[0] != "Bearer "+fixtureManagementSecret {
		t.Fatalf("missing bearer auth header: %+v", authHeaders)
	}
	if len(subprotos) == 0 || subprotos[0] != fixtureManagementSecret {
		t.Fatalf("secret subprotocol was not negotiated: %+v", subprotos)
	}
	wantMethods := []string{
		MethodPlayersList, MethodPlayersKick, MethodBansAdd, MethodBansRemove,
		MethodOperatorsAdd, MethodOperatorsRemove, MethodAllowlistAdd, MethodAllowlistRemove,
	}
	for i, want := range wantMethods {
		if requests[i].Method != want || requests[i].JSONRPC != jsonRPCVersion || requests[i].ID == 0 {
			t.Fatalf("request %d = %+v, want method %q", i, requests[i], want)
		}
	}
	kickParams, _ := json.Marshal(requests[1].Params)
	if !strings.Contains(string(kickParams), "griefing") || !strings.Contains(string(kickParams), "Steve") {
		t.Fatalf("kick params = %s", kickParams)
	}
	opParams, _ := json.Marshal(requests[5].Params)
	if strings.Contains(string(opParams), "permission_level") {
		t.Fatalf("deop should not carry a permission level: %s", opParams)
	}
}

func TestManagementAdapterOutOfOrderResponses(t *testing.T) {
	gate := make(chan struct{})
	fixture := newManagementFixture(t, fixtureManagementSecret, func(req rpcRequest, _ *websocket.Conn, write func(any) error) (any, *rpcErrorPayload) {
		if req.Method == MethodPlayersList {
			go func(id uint64) {
				<-gate // write the list response after a later request completes
				_ = write(map[string]any{
					"jsonrpc": jsonRPCVersion,
					"id":      id,
					"result":  map[string]any{"players": []map[string]any{{"name": "Steve"}}},
				})
			}(req.ID)
			return deferredReply{}, nil
		}
		return map[string]any{"ok": true}, nil
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := newManagementAdapter(fixtureServerInfo(fixture.url))
	t.Cleanup(func() { _ = m.Close() })

	type outcome struct {
		players []ObservedPlayer
		err     error
	}
	listResult := make(chan outcome, 1)
	go func() {
		players, err := m.List(ctx)
		listResult <- outcome{players: players, err: err}
	}()

	// Wait until the fixture has received the List request before issuing the
	// second call, otherwise the gate would be closed too early.
	deadline := time.Now().Add(3 * time.Second)
	for {
		requests, _, _, _ := fixture.recorded()
		if len(requests) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not receive the list request")
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.Pardon(ctx, "Alex"); err != nil {
		t.Fatalf("Pardon while List is pending: %v", err)
	}
	close(gate)
	select {
	case result := <-listResult:
		if result.err != nil || len(result.players) != 1 || result.players[0].Name != "Steve" {
			t.Fatalf("List after out-of-order response: %+v / %v", result.players, result.err)
		}
	case <-ctx.Done():
		t.Fatal("List did not complete")
	}
}

func TestManagementAdapterAuthFailure(t *testing.T) {
	fixture := newManagementFixture(t, fixtureManagementSecret, func(rpcRequest, *websocket.Conn, func(any) error) (any, *rpcErrorPayload) {
		return nil, nil
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wrongSecretInfo := fixtureServerInfo(fixture.url)
	wrongSecretInfo.ManagementSecret = "wrong-secret-value"
	m := newManagementAdapter(wrongSecretInfo)
	defer func(closer func() error) { _ = closer() }(m.Close)
	_, err := m.List(ctx)
	if err == nil {
		t.Fatal("expected auth failure")
	}
	assertNoSecret(t, err.Error(), wrongSecretInfo.ManagementSecret)
	assertNoSecret(t, err.Error(), fixtureManagementSecret)
}

func TestManagementAdapterConnectFailure(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	deadURL := "ws" + strings.TrimPrefix(dead.URL, "http")
	dead.Close()

	info := fixtureServerInfo(deadURL)
	adapter, caps, err := Negotiate(context.Background(), info, nil)
	if err != nil || caps.ChosenSource != SourceManagementProtocol {
		t.Fatalf("negotiation should be configuration-only: %v / %+v", err, caps)
	}
	m := adapter.(*ManagementAdapter)
	defer func(closer func() error) { _ = closer() }(m.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = m.List(ctx)
	if err == nil {
		t.Fatal("expected connect failure")
	}
	assertNoSecret(t, err.Error(), info.ManagementSecret)
}

func TestManagementAdapterRPCAndSecretRedaction(t *testing.T) {
	secret := fixtureManagementSecret
	fixture := newManagementFixture(t, secret, func(req rpcRequest, _ *websocket.Conn, _ func(any) error) (any, *rpcErrorPayload) {
		return nil, &rpcErrorPayload{Code: -32000, Message: "denied for token " + secret}
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := newManagementAdapter(fixtureServerInfo(fixture.url))
	defer func(closer func() error) { _ = closer() }(m.Close)

	_, err := m.List(ctx)
	if err == nil || !strings.Contains(err.Error(), "denied for token") {
		t.Fatalf("expected json-rpc error, got %v", err)
	}
	assertNoSecret(t, err.Error(), secret)
	if !strings.Contains(err.Error(), redactionMarker) {
		t.Fatalf("expected redaction marker in %q", err.Error())
	}
}

// TestManagementAdapterNotificationRaceAndSource records pushed notifications
// with observed-at and source, and checks that neither a pre-list nor an
// interleaved notification silently changes the authoritative roster.
func TestManagementAdapterNotificationRaceAndSource(t *testing.T) {
	joined := NotificationPlayerJoined
	left := NotificationPlayerLeft
	joinedPlayer := `{"jsonrpc":"2.0","method":"` + joined + `","params":{"player":{"id":"00000000-0000-0000-0000-000000000001","name":"Steve"}}}`

	fixture := newManagementFixture(t, fixtureManagementSecret, func(req rpcRequest, _ *websocket.Conn, write func(any) error) (any, *rpcErrorPayload) {
		// A left notification delivered in the middle of a list response must
		// not alter the roster reported by List.
		_ = write(map[string]any{"jsonrpc": jsonRPCVersion, "method": left, "params": map[string]any{"name": "Alex", "uuid": "00000000000000000000000000000002"}})
		return map[string]any{"players": []map[string]any{{"name": "Steve"}}}, nil
	}, func(conn *websocket.Conn) {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(joinedPlayer))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := newManagementAdapter(fixtureServerInfo(fixture.url))
	defer func(closer func() error) { _ = closer() }(m.Close)

	before := time.Now().UTC().Add(-time.Second)
	if roster, at, source := m.Snapshot(); len(roster) != 0 || !at.IsZero() || source != "" {
		t.Fatalf("unexpected initial snapshot: %+v %v %q", roster, at, source)
	}

	players, err := m.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(players) != 1 || players[0].Name != "Steve" {
		t.Fatalf("roster changed by notification: %+v", players)
	}

	var seen []Notification
	for i := 0; i < 2; i++ {
		select {
		case n := <-m.Notifications():
			seen = append(seen, n)
		case <-ctx.Done():
			t.Fatalf("missing notification %d: %+v", i, seen)
		}
	}
	after := time.Now().UTC().Add(time.Second)
	wantKinds := map[string]bool{"joined": false, "left": false}
	for _, n := range seen {
		if _, ok := wantKinds[n.Kind]; !ok {
			t.Fatalf("unexpected notification kind %q", n.Kind)
		}
		wantKinds[n.Kind] = true
		if n.Source != SourceManagementProtocol {
			t.Fatalf("notification source = %q", n.Source)
		}
		if n.ObservedAt.Before(before) || n.ObservedAt.After(after) || n.ObservedAt.IsZero() {
			t.Fatalf("notification observed-at out of range: %v", n.ObservedAt)
		}
	}
	for kind, seenKind := range wantKinds {
		if !seenKind {
			t.Fatalf("missing %s notification", kind)
		}
	}
	roster, at, source := m.Snapshot()
	if len(roster) != 1 || roster[0].Name != "Steve" || at.IsZero() || source != SourceManagementProtocol {
		t.Fatalf("notifications mutated authoritative roster: %+v %v %q", roster, at, source)
	}
}

func TestManagementAdapterReconnectGenerationGuard(t *testing.T) {
	fixture := newManagementFixture(t, fixtureManagementSecret, func(req rpcRequest, _ *websocket.Conn, _ func(any) error) (any, *rpcErrorPayload) {
		return map[string]any{"players": []map[string]any{{"name": "Steve"}}}, nil
	}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m := newManagementAdapter(fixtureServerInfo(fixture.url))
	defer func(closer func() error) { _ = closer() }(m.Close)

	if _, err := m.List(ctx); err != nil {
		t.Fatalf("first List: %v", err)
	}
	firstGeneration := m.generation

	// Drop the connection behind the adapter's back; the next call must
	// reconnect rather than write to the dead socket.
	m.mu.Lock()
	staleConn := m.conn
	m.mu.Unlock()
	if staleConn != nil {
		_ = staleConn.Close()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		current := m.conn
		m.mu.Unlock()
		if current == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("adapter did not notice dropped connection")
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := m.List(ctx); err != nil {
		t.Fatalf("second List after reconnect: %v", err)
	}
	m.mu.Lock()
	currentGeneration := m.generation
	currentConn := m.conn
	m.mu.Unlock()
	if currentGeneration <= firstGeneration || currentConn == nil {
		t.Fatalf("reconnect did not create a new generation: %d -> %d", firstGeneration, currentGeneration)
	}
	_, _, _, connections := fixture.recorded()
	if connections < 2 {
		t.Fatalf("expected at least two fixture connections, got %d", connections)
	}

	// A late failure from an older generation must not invalidate the new one.
	m.failConnection(currentGeneration-1, errors.New("stale generation failure"))
	m.mu.Lock()
	afterStale := m.generation
	stillConnected := m.conn != nil
	m.mu.Unlock()
	if afterStale != currentGeneration || !stillConnected {
		t.Fatalf("stale generation clobbered live connection: %d/%v", afterStale, stillConnected)
	}
}

func TestManagementAdapterCloseFailsPendingCalls(t *testing.T) {
	gate := make(chan struct{})
	fixture := newManagementFixture(t, fixtureManagementSecret, func(rpcRequest, *websocket.Conn, func(any) error) (any, *rpcErrorPayload) {
		<-gate
		return map[string]any{"players": []map[string]any{}}, nil
	}, nil)
	m := newManagementAdapter(fixtureServerInfo(fixture.url))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := m.List(ctx)
		result <- err
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		requests, _, _, _ := fixture.recorded()
		if len(requests) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture did not receive request")
		}
		time.Sleep(time.Millisecond)
	}
	if err := m.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	close(gate)
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "adapter closed") {
			t.Fatalf("pending call error = %v", err)
		}
	case <-ctx.Done():
		t.Fatal("pending call did not fail on Close")
	}
}

func TestManagementAdapterCallerContextTimeout(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	fixture := newManagementFixture(t, fixtureManagementSecret, func(req rpcRequest, _ *websocket.Conn, _ func(any) error) (any, *rpcErrorPayload) {
		<-gate // deliberately never answer before the caller's deadline
		return map[string]any{"players": []map[string]any{}}, nil
	}, nil)
	m := newManagementAdapter(fixtureServerInfo(fixture.url))
	defer func(closer func() error) { _ = closer() }(m.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := m.List(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
}

func TestManagementAdapterBoundedFrame(t *testing.T) {
	fixture := newManagementFixture(t, fixtureManagementSecret, func(rpcRequest, *websocket.Conn, func(any) error) (any, *rpcErrorPayload) {
		return map[string]any{"players": []map[string]any{}}, nil
	}, func(conn *websocket.Conn) {
		_ = conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", managementMaxFrameBytes+1)))
	})
	m := newManagementAdapter(fixtureServerInfo(fixture.url))
	defer func(closer func() error) { _ = closer() }(m.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = m.List(ctx) // the oversized frame may win the race or arrive after the reply
	deadline := time.Now().Add(3 * time.Second)
	for {
		m.mu.Lock()
		connected := m.conn != nil
		m.mu.Unlock()
		if !connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("oversized frame did not fail the connection")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestManagementAdapterRejectsInjectionBeforeRPC(t *testing.T) {
	m := newManagementAdapter(fixtureServerInfo("ws://127.0.0.1:1"))
	defer func(closer func() error) { _ = closer() }(m.Close)
	ctx := context.Background()
	bad := []func() error{
		func() error { return m.Kick(ctx, "Steve\nstop", "x") },
		func() error { return m.Ban(ctx, "Steve", "x; stop") },
		func() error { return m.Pardon(ctx, "Steve\x00") },
		func() error { return m.SetOperator(ctx, "Steve", 9, false) },
		func() error { return m.SetWhitelist(ctx, "Steve\u2028", false) },
	}
	for i, action := range bad {
		if err := action(); err == nil {
			t.Errorf("action %d was not rejected", i)
		}
	}
	// No connection attempt should have been made for invalid input.
	m.mu.Lock()
	attempted := !m.lastDial.IsZero()
	m.mu.Unlock()
	if attempted {
		t.Fatal("invalid input reached the WebSocket dial")
	}
}
