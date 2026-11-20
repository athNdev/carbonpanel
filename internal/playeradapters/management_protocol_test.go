// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// protocolFixture is intentionally strict: it accepts only the wire shapes
// emitted by a real vanilla 1.21.9 management endpoint. Every mismatch is
// recorded as a violation so the test fails on the pre-fix adapter instead of
// a permissive fixture silently accepting a wrong frame.
type protocolFixture struct {
	server     *httptest.Server
	url        string
	mu         sync.Mutex
	rawFrames  []string
	violations []string
	conns      []*protocolFixtureConn
	allowlist  map[string]bool
	bans       map[string]string
	ops        map[string]int32
	kicked     []string
}

type protocolFixtureConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (c *protocolFixtureConn) writeJSON(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(v)
}

func (c *protocolFixtureConn) writeRaw(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.TextMessage, b)
}

func newProtocolFixture(t *testing.T) *protocolFixture {
	t.Helper()
	f := &protocolFixture{
		allowlist: make(map[string]bool),
		bans:      make(map[string]string),
		ops:       make(map[string]int32),
	}
	upgrader := websocket.Upgrader{
		CheckOrigin:  func(*http.Request) bool { return true },
		Subprotocols: []string{"minecraft-v1", fixtureManagementSecret},
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fixtureManagementSecret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck // test fixture
		fc := &protocolFixtureConn{conn: conn}
		f.mu.Lock()
		f.conns = append(f.conns, fc)
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			for i, candidate := range f.conns {
				if candidate == fc {
					f.conns = append(f.conns[:i], f.conns[i+1:]...)
					break
				}
			}
			f.mu.Unlock()
		}()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.rawFrames = append(f.rawFrames, string(data))
			f.mu.Unlock()
			var req rpcRequest
			if json.Unmarshal(data, &req) != nil {
				f.violate("malformed JSON-RPC request: %s", data)
				continue
			}
			result, rpcErr := f.respond(req)
			reply := map[string]any{"jsonrpc": jsonRPCVersion, "id": req.ID}
			if rpcErr != nil {
				reply["error"] = rpcErr
			} else {
				reply["result"] = result
			}
			if err := fc.writeJSON(reply); err != nil {
				return
			}
		}
	}))
	f.url = "ws" + strings.TrimPrefix(f.server.URL, "http")
	t.Cleanup(f.server.Close)
	return f
}

func (f *protocolFixture) violate(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.violations = append(f.violations, fmt.Sprintf(format, args...))
}

// requireParams checks the named top-level parameter keys exactly. The real
// server rejects unknown by-name params, so an extra key is a wire break.
func (f *protocolFixture) requireParams(req rpcRequest, keys ...string) map[string]json.RawMessage {
	var obj map[string]json.RawMessage
	if len(req.Params) == 0 || json.Unmarshal(req.Params, &obj) != nil {
		f.violate("%s: missing or malformed params %q", req.Method, req.Params)
		return nil
	}
	if len(obj) != len(keys) {
		f.violate("%s: params keys %v, want %v", req.Method, obj, keys)
	}
	for _, key := range keys {
		if _, ok := obj[key]; !ok {
			f.violate("%s: missing param %q in %s", req.Method, key, req.Params)
		}
	}
	return obj
}

func (f *protocolFixture) forbid(req rpcRequest, tokens ...string) {
	for _, token := range tokens {
		if strings.Contains(string(req.Params), token) {
			f.violate("%s: legacy token %q in %s", req.Method, token, req.Params)
		}
	}
}

func (f *protocolFixture) respond(req rpcRequest) (any, *rpcErrorPayload) {
	switch req.Method {
	case MethodPlayersList:
		if len(req.Params) != 0 && string(req.Params) != "[]" {
			f.violate("%s: params must be omitted or an empty array, got %s", req.Method, req.Params)
		}
		return []map[string]any{{"id": "f865b2ce-4755-3723-bea5-c87ecc602a4d", "name": "qualplayer1"}}, nil
	case MethodAllowlistAdd:
		f.requireParams(req, "add")
		f.forbid(req, `"player"`, "username")
		var p addPlayersParams
		if json.Unmarshal(req.Params, &p) != nil || len(p.Add) != 1 || p.Add[0].Name != "QualBot1" {
			f.violate("%s: unexpected add payload %s", req.Method, req.Params)
		}
		f.mu.Lock()
		f.allowlist["qualbot1"] = true
		f.mu.Unlock()
		return []map[string]any{{"id": "81766b88-199d-35ad-bd4b-2857ef0c4e1a", "name": "qualbot1"}}, nil
	case MethodAllowlistRemove:
		f.requireParams(req, "remove")
		f.forbid(req, `"player"`, "username")
		var p removePlayersParams
		if json.Unmarshal(req.Params, &p) != nil || len(p.Remove) != 1 || p.Remove[0].Name != "QualBot1" {
			f.violate("%s: unexpected remove payload %s", req.Method, req.Params)
		}
		f.mu.Lock()
		delete(f.allowlist, "qualbot1")
		f.mu.Unlock()
		return []map[string]any{}, nil
	case MethodBansAdd:
		f.requireParams(req, "add")
		f.forbid(req, "permission_level")
		var p addBansParams
		if json.Unmarshal(req.Params, &p) != nil || len(p.Add) != 1 || p.Add[0].Player.Name != "QualBan1" || p.Add[0].Reason != "qualban" {
			f.violate("%s: unexpected ban payload %s", req.Method, req.Params)
		}
		f.mu.Lock()
		f.bans["qualban1"] = "qualban"
		f.mu.Unlock()
		return []map[string]any{{"player": map[string]any{"id": "ea5397f7-6e74-3952-bb43-7051b5c86478", "name": "qualban1"}, "reason": "qualban", "source": "Management server"}}, nil
	case MethodBansRemove:
		f.requireParams(req, "remove")
		f.forbid(req, `"player"`, "reason")
		var p removePlayersParams
		if json.Unmarshal(req.Params, &p) != nil || len(p.Remove) != 1 || p.Remove[0].Name != "QualBan1" {
			f.violate("%s: unexpected pardon payload %s", req.Method, req.Params)
		}
		f.mu.Lock()
		delete(f.bans, "qualban1")
		f.mu.Unlock()
		return []map[string]any{}, nil
	case MethodOperatorsAdd:
		f.requireParams(req, "add")
		f.forbid(req, "permission_level", "username")
		if !strings.Contains(string(req.Params), `"permissionLevel":4`) {
			f.violate("%s: camelCase permissionLevel missing in %s", req.Method, req.Params)
		}
		var p addOperatorsParams
		if json.Unmarshal(req.Params, &p) != nil || len(p.Add) != 1 || p.Add[0].Player.Name != "QualOp1" || p.Add[0].PermissionLevel != 4 {
			f.violate("%s: unexpected operator payload %s", req.Method, req.Params)
		}
		f.mu.Lock()
		f.ops["qualop1"] = 4
		f.mu.Unlock()
		return []map[string]any{{"player": map[string]any{"id": "8b648cde-c3d2-3568-b0e7-33c93239721d", "name": "qualop1"}, "permissionLevel": 4}}, nil
	case MethodOperatorsRemove:
		f.requireParams(req, "remove")
		f.forbid(req, "permission_level", `"player"`)
		var p removePlayersParams
		if json.Unmarshal(req.Params, &p) != nil || len(p.Remove) != 1 || p.Remove[0].Name != "QualOp1" {
			f.violate("%s: unexpected deop payload %s", req.Method, req.Params)
		}
		f.mu.Lock()
		delete(f.ops, "qualop1")
		f.mu.Unlock()
		return []map[string]any{}, nil
	case MethodPlayersKick:
		f.requireParams(req, "kick")
		f.forbid(req, `"message":"`, "username")
		if !strings.Contains(string(req.Params), `"message":{"literal":"qual kick"}`) {
			f.violate("%s: structured literal message missing in %s", req.Method, req.Params)
		}
		var p kickPlayersParams
		if json.Unmarshal(req.Params, &p) != nil || len(p.Kick) != 1 || p.Kick[0].Player.Name != "QualBot1" || p.Kick[0].Message.Literal != "qual kick" {
			f.violate("%s: unexpected kick payload %s", req.Method, req.Params)
		}
		f.mu.Lock()
		f.kicked = append(f.kicked, p.Kick[0].Player.Name)
		f.mu.Unlock()
		return []map[string]any{{"name": "qualbot1"}}, nil
	default:
		return nil, &rpcErrorPayload{Code: -32601, Message: "Method not found: " + req.Method}
	}
}

func (f *protocolFixture) emit(t *testing.T, frame string) {
	t.Helper()
	f.mu.Lock()
	conns := append([]*protocolFixtureConn(nil), f.conns...)
	f.mu.Unlock()
	if len(conns) == 0 {
		t.Fatal("no management connection to emit notifications on")
	}
	for _, c := range conns {
		if err := c.writeRaw([]byte(frame)); err != nil {
			t.Fatalf("emit notification: %v", err)
		}
	}
}

func (f *protocolFixture) check(t *testing.T) {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, violation := range f.violations {
		t.Errorf("WIRE SHAPE VIOLATION: %s", violation)
	}
}

func (f *protocolFixture) frames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rawFrames...)
}

func TestManagementAdapterQualifiedWireShapes(t *testing.T) {
	fixture := newProtocolFixture(t)
	info := fixtureServerInfo(fixture.url)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	adapter, caps, err := Negotiate(ctx, info, nil)
	if err != nil {
		t.Fatalf("Negotiate: %v", err)
	}
	m, ok := adapter.(*ManagementAdapter)
	if !ok {
		t.Fatalf("expected *ManagementAdapter, got %T", adapter)
	}
	defer m.Close() //nolint:errcheck // test fixture
	if caps.ChosenSource != SourceManagementProtocol || m.ProtocolProfile() != "minecraft-v1/1.21.9" {
		t.Fatalf("profile = %q, caps = %+v", m.ProtocolProfile(), caps)
	}

	players, err := m.List(ctx)
	if err != nil || len(players) != 1 || players[0].Name != "qualplayer1" || players[0].UUID != "f865b2ce-4755-3723-bea5-c87ecc602a4d" {
		t.Fatalf("List: %+v / %v", players, err)
	}
	for _, action := range []struct {
		name string
		run  func() error
	}{
		{"kick", func() error { return m.Kick(ctx, "QualBot1", "qual kick") }},
		{"ban", func() error { return m.Ban(ctx, "QualBan1", "qualban") }},
		{"pardon", func() error { return m.Pardon(ctx, "QualBan1") }},
		{"op", func() error { return m.SetOperator(ctx, "QualOp1", 4, false) }},
		{"deop", func() error { return m.SetOperator(ctx, "QualOp1", 4, true) }},
		{"allowlist add", func() error { return m.SetWhitelist(ctx, "QualBot1", false) }},
		{"allowlist remove", func() error { return m.SetWhitelist(ctx, "QualBot1", true) }},
	} {
		if err := action.run(); err != nil {
			t.Fatalf("%s: %v", action.name, err)
		}
	}
	fixture.check(t)

	frames := fixture.frames()
	if len(frames) != 8 {
		t.Fatalf("got %d frames, want 8: %v", len(frames), frames)
	}
	if strings.Contains(frames[0], `"params"`) {
		t.Fatalf("minecraft:players must omit params: %s", frames[0])
	}
	wantExact := map[int]string{
		1: `{"jsonrpc":"2.0","id":2,"method":"minecraft:players/kick","params":{"kick":[{"player":{"name":"QualBot1"},"message":{"literal":"qual kick"}}]}}`,
		2: `{"jsonrpc":"2.0","id":3,"method":"minecraft:bans/add","params":{"add":[{"player":{"name":"QualBan1"},"reason":"qualban"}]}}`,
		4: `{"jsonrpc":"2.0","id":5,"method":"minecraft:operators/add","params":{"add":[{"player":{"name":"QualOp1"},"permissionLevel":4}]}}`,
	}
	for index, want := range wantExact {
		if frames[index] != want {
			t.Errorf("frame %d =\n%s\nwant\n%s", index, frames[index], want)
		}
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.kicked) != 1 || fixture.kicked[0] != "QualBot1" {
		t.Errorf("kick state = %v", fixture.kicked)
	}
	if len(fixture.allowlist) != 0 || len(fixture.bans) != 0 || len(fixture.ops) != 0 {
		t.Errorf("moderation state not restored: allowlist=%v bans=%v ops=%v", fixture.allowlist, fixture.bans, fixture.ops)
	}
}

func TestManagementAdapterRealNotificationShapes(t *testing.T) {
	fixture := newProtocolFixture(t)
	m := newManagementAdapter(fixtureServerInfo(fixture.url))
	defer m.Close() //nolint:errcheck // test fixture
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := m.List(ctx); err != nil {
		t.Fatalf("List to open socket: %v", err)
	}
	before, beforeAt, beforeSource := m.Snapshot()

	frames := []string{
		`{"jsonrpc":"2.0","method":"minecraft:notification/players/joined","params":[{"id":"f865b2ce-4755-3723-bea5-c87ecc602a4d","name":"QualPlayer1"}]}`,
		`{"jsonrpc":"2.0","method":"minecraft:notification/players/left","params":[{"id":"f865b2ce-4755-3723-bea5-c87ecc602a4d","name":"QualPlayer1"}]}`,
		`{"jsonrpc":"2.0","method":"minecraft:notification/allowlist/added","params":[{"id":"81766b88-199d-35ad-bd4b-2857ef0c4e1a","name":"QualBot1"}]}`,
		`{"jsonrpc":"2.0","method":"minecraft:notification/bans/added","params":[{"player":{"id":"ea5397f7-6e74-3952-bb43-7051b5c86478","name":"QualBan1"},"reason":"qual"}]}`,
		`{"jsonrpc":"2.0","method":"minecraft:notification/operators/removed","params":[{"player":{"id":"8b648cde-c3d2-3568-b0e7-33c93239721d","name":"QualOp1"},"permissionLevel":4}]}`,
	}
	for _, frame := range frames {
		fixture.emit(t, frame)
	}

	wantKinds := []string{"joined", "left", "allowlist_added", "ban_added", "operator_removed"}
	wantNames := []string{"QualPlayer1", "QualPlayer1", "QualBot1", "QualBan1", "QualOp1"}
	wantUUIDs := []string{
		"f865b2ce-4755-3723-bea5-c87ecc602a4d", "f865b2ce-4755-3723-bea5-c87ecc602a4d",
		"81766b88-199d-35ad-bd4b-2857ef0c4e1a", "ea5397f7-6e74-3952-bb43-7051b5c86478",
		"8b648cde-c3d2-3568-b0e7-33c93239721d",
	}
	for i := range wantKinds {
		select {
		case n := <-m.Notifications():
			if n.Kind != wantKinds[i] || n.Name != wantNames[i] || n.UUID != wantUUIDs[i] || n.Source != SourceManagementProtocol || n.ObservedAt.IsZero() {
				t.Errorf("notification %d = %+v, want %q/%q/%q", i, n, wantKinds[i], wantNames[i], wantUUIDs[i])
			}
		case <-ctx.Done():
			t.Fatalf("missing notification %d", i)
		}
	}
	after, afterAt, afterSource := m.Snapshot()
	if len(after) != len(before) || !afterAt.Equal(beforeAt) || afterSource != beforeSource {
		t.Fatalf("notifications mutated authoritative roster: %+v/%v/%q -> %+v/%v/%q", before, beforeAt, beforeSource, after, afterAt, afterSource)
	}
}

func TestManagementAdapterVersionProfileMismatch(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{version: "1.21.9", want: true},
		{version: "1.21.9-rc1", want: false},
		{version: "1.21.10", want: true},
		{version: "1.21.11", want: false},
		{version: "1.21.9.custom", want: false},
		{version: "1.21.8", want: false},
		{version: "1.22", want: false},
		{version: "26.3", want: false},
		{version: "snapshot", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.version, func(t *testing.T) {
			info := fixtureServerInfo("ws://127.0.0.1:1")
			info.MCVersion = tc.version
			profile, err := selectManagementProfile(tc.version)
			if (err == nil) != tc.want {
				t.Fatalf("selectManagementProfile(%q) = %+v / %v, want ok=%v", tc.version, profile, err, tc.want)
			}
			m := newManagementAdapter(info)
			defer m.Close() //nolint:errcheck // test fixture
			if got := m.ProtocolProfile(); (got != "") != tc.want {
				t.Fatalf("ProtocolProfile(%q) = %q, want qualified=%v", tc.version, got, tc.want)
			}
			if tc.want {
				return
			}
			_, listErr := m.List(context.Background())
			if listErr == nil || !strings.Contains(listErr.Error(), "no verified management protocol signature") {
				t.Fatalf("unverified version must fail closed before dialing, got %v", listErr)
			}
			if !m.lastDial.IsZero() {
				t.Fatal("unverified version attempted a network dial")
			}
			// Negotiation must report the same gate, not advertise a broken capability.
			info.RCONEnabled = false
			_, caps, negotiateErr := Negotiate(context.Background(), info, nil)
			if caps.Management || caps.ChosenSource != "" {
				t.Fatalf("caps for %q = %+v", tc.version, caps)
			}
			if negotiateErr == nil || !strings.Contains(negotiateErr.Error(), "No source is usable") {
				t.Fatalf("expected fail-closed negotiation for %q, got %v", tc.version, negotiateErr)
			}
		})
	}
}
