// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package playeradapters

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// JSON-RPC method and notification names for the vanilla Minecraft Server
// Management Protocol. These are the documented names, confirmed against a
// real pinned 1.21.9 server in the OSS-350 qualification run. Note the
// notification namespace is "minecraft:notification/...", not
// "minecraft:players/...", and the list/list-like endpoints have no trailing
// slash (minecraft:players, minecraft:allowlist, minecraft:bans,
// minecraft:operators).
const (
	MethodPlayersList     = "minecraft:players"
	MethodPlayersKick     = "minecraft:players/kick"
	MethodAllowlistAdd    = "minecraft:allowlist/add"
	MethodAllowlistRemove = "minecraft:allowlist/remove"
	MethodBansAdd         = "minecraft:bans/add"
	MethodBansRemove      = "minecraft:bans/remove"
	MethodOperatorsAdd    = "minecraft:operators/add"
	MethodOperatorsRemove = "minecraft:operators/remove"

	NotificationPlayerJoined     = "minecraft:notification/players/joined"
	NotificationPlayerLeft       = "minecraft:notification/players/left"
	NotificationAllowlistAdded   = "minecraft:notification/allowlist/added"
	NotificationAllowlistRemoved = "minecraft:notification/allowlist/removed"
	NotificationBanAdded         = "minecraft:notification/bans/added"
	NotificationBanRemoved       = "minecraft:notification/bans/removed"
	NotificationOperatorAdded    = "minecraft:notification/operators/added"
	NotificationOperatorRemoved  = "minecraft:notification/operators/removed"
)

const (
	managementDialTimeout        = 3 * time.Second
	managementCallTimeout        = 5 * time.Second
	managementMaxFrameBytes      = 1 << 20
	managementNotificationBuffer = 128
	managementReconnectInterval  = 200 * time.Millisecond
	managementMaxPlayers         = 256
	jsonRPCVersion               = "2.0"
)

// Params is raw JSON so a no-argument method can omit the field entirely.
// Vanilla 1.21.9 rejects an empty object ("Expected no params, or an empty
// array") for minecraft:players; an omitted field is accepted.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcErrorPayload struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// rpcFrame handles both responses (id) and push notifications (method) on the
// same WebSocket. Unrelated fields are ignored, never trusted as roster data.
type rpcFrame struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *uint64          `json:"id"`
	Method  string           `json:"method"`
	Params  json.RawMessage  `json:"params"`
	Result  json.RawMessage  `json:"result"`
	Error   *rpcErrorPayload `json:"error"`
}

type rpcReply struct {
	result json.RawMessage
	err    error
}

type managementPlayer struct {
	ID       string `json:"id"`
	UUID     string `json:"uuid"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

// Minecraft 1.21.11 introduced protocol v2. Select the v1 release
// explicitly so snapshots and later wire formats use the configured fallback.
func selectManagementProfile(version string) (string, error) {
	switch strings.TrimSpace(version) {
	case "1.21.9", "1.21.10":
		return "minecraft-v1/1.21.9", nil
	default:
		return "", fmt.Errorf("no verified management protocol signature for Minecraft version %q", version)
	}
}

// ManagementAdapter is a bounded JSON-RPC 2.0 client. It lazily connects on
// first use, reconnects on later calls, and fails outstanding calls when a
// connection generation dies. Owner must call Close.
type ManagementAdapter struct {
	info       ServerInfo
	url        string
	dialer     websocket.Dialer
	header     http.Header
	profile    string
	profileErr error

	mu         sync.Mutex
	conn       *websocket.Conn
	generation uint64
	pending    map[uint64]chan rpcReply
	nextID     uint64
	closed     bool
	roster     []ObservedPlayer
	rosterAt   time.Time
	lastDial   time.Time
	dropped    uint64

	writeMu  sync.Mutex
	notifyCh chan Notification
}

func newManagementAdapter(info ServerInfo) *ManagementAdapter {
	header := http.Header{}
	header.Set("Authorization", "Bearer "+info.ManagementSecret)
	dialer := websocket.Dialer{
		HandshakeTimeout:  managementDialTimeout,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
		EnableCompression: false,
	}
	profile, profileErr := selectManagementProfile(info.MCVersion)
	return &ManagementAdapter{
		info:       info,
		url:        info.ManagementURL,
		dialer:     dialer,
		header:     header,
		profile:    profile,
		profileErr: profileErr,
		pending:    make(map[uint64]chan rpcReply),
		notifyCh:   make(chan Notification, managementNotificationBuffer),
	}
}

// ProtocolProfile reports the verified wire signature this adapter uses. It is
// empty when no signature matches the configured version; such an adapter
// refuses every call instead of guessing a shape.
func (m *ManagementAdapter) ProtocolProfile() string { return m.profile }

func (m *ManagementAdapter) Source() string { return SourceManagementProtocol }

// Notifications exposes joined/left push signals. It never closes while the
// adapter is in use; consumers must select on their own context/shutdown.
func (m *ManagementAdapter) Notifications() <-chan Notification { return m.notifyCh }

// DroppedNotifications counts advisory signals dropped because a slow
// consumer did not drain Notifications. It is a diagnostic, not roster data.
func (m *ManagementAdapter) DroppedNotifications() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.dropped
}

// Snapshot returns the last roster obtained from a successful List, together
// with the time the response was observed and its source. Notifications never
// modify this snapshot; a consumer that sees a newer notification should
// re-list before treating presence as authoritative.
func (m *ManagementAdapter) Snapshot() ([]ObservedPlayer, time.Time, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	roster := append([]ObservedPlayer(nil), m.roster...)
	source := ""
	if !m.rosterAt.IsZero() {
		source = SourceManagementProtocol
	}
	return roster, m.rosterAt, source
}

// Close stops the background reader and fails all outstanding requests.
func (m *ManagementAdapter) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	conn := m.conn
	m.conn = nil
	m.generation++
	failPendingLocked(m.pending, errors.New("playeradapters: management: adapter closed"))
	m.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func failPendingLocked(pending map[uint64]chan rpcReply, err error) {
	for id, ch := range pending {
		select {
		case ch <- rpcReply{err: err}:
		default:
		}
		delete(pending, id)
	}
}

func (m *ManagementAdapter) ensureConnected(ctx context.Context) (uint64, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, errors.New("playeradapters: management: adapter is closed")
	}
	if m.conn != nil {
		gen := m.generation
		m.mu.Unlock()
		return gen, nil
	}
	gen := m.generation
	if !m.lastDial.IsZero() {
		if wait := managementReconnectInterval - time.Since(m.lastDial); wait > 0 {
			m.mu.Unlock()
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return 0, ctx.Err()
			}
			timer.Stop()
			m.mu.Lock()
			if m.closed {
				m.mu.Unlock()
				return 0, errors.New("playeradapters: management: adapter is closed")
			}
			if m.conn != nil {
				gen = m.generation
				m.mu.Unlock()
				return gen, nil
			}
			gen = m.generation
		}
	}
	m.lastDial = time.Now()
	m.mu.Unlock()

	dialCtx, cancel := context.WithTimeout(ctx, managementDialTimeout)
	conn, _, err := m.dialer.DialContext(dialCtx, m.url, m.header)
	cancel()
	if err != nil {
		return 0, m.info.sourceError(SourceManagementProtocol, err)
	}
	conn.SetReadLimit(managementMaxFrameBytes)

	m.mu.Lock()
	if m.closed || m.generation != gen {
		m.mu.Unlock()
		_ = conn.Close()
		return 0, errors.New("playeradapters: management: connection generation changed during connect")
	}
	m.conn = conn
	connectedGen := m.generation
	m.mu.Unlock()

	go m.readLoop(conn, connectedGen)
	return connectedGen, nil
}

func (m *ManagementAdapter) failConnection(gen uint64, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if gen != m.generation || m.conn == nil {
		return
	}
	m.generation++
	m.conn = nil
	failPendingLocked(m.pending, m.info.sourceError(SourceManagementProtocol, cause))
}

func (m *ManagementAdapter) readLoop(conn *websocket.Conn, gen uint64) {
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			m.failConnection(gen, err)
			return
		}
		var frame rpcFrame
		if json.Unmarshal(data, &frame) != nil {
			continue // malformed frames are ignored, never treated as roster data
		}
		if frame.ID != nil {
			m.deliverReply(*frame.ID, frame)
			continue
		}
		if frame.Method != "" {
			m.recordNotification(frame.Method, frame.Params)
		}
	}
}

func (m *ManagementAdapter) deliverReply(id uint64, frame rpcFrame) {
	m.mu.Lock()
	ch := m.pending[id]
	m.mu.Unlock()
	if ch == nil {
		return
	}
	reply := rpcReply{result: frame.Result}
	if frame.Error != nil {
		message := frame.Error.Message
		if message == "" {
			message = "json-rpc error"
		}
		reply.err = m.info.sourceError(SourceManagementProtocol, errors.New(message))
	}
	select {
	case ch <- reply:
	default:
	}
}

func notificationKind(method string) (string, bool) {
	switch method {
	case NotificationPlayerJoined:
		return "joined", true
	case NotificationPlayerLeft:
		return "left", true
	case NotificationAllowlistAdded:
		return "allowlist_added", true
	case NotificationAllowlistRemoved:
		return "allowlist_removed", true
	case NotificationBanAdded:
		return "ban_added", true
	case NotificationBanRemoved:
		return "ban_removed", true
	case NotificationOperatorAdded:
		return "operator_added", true
	case NotificationOperatorRemoved:
		return "operator_removed", true
	default:
		return "", false
	}
}

func (m *ManagementAdapter) recordNotification(method string, params json.RawMessage) {
	kind, known := notificationKind(method)
	if !known {
		return
	}
	for _, player := range decodeNotificationPlayers(params) {
		if player.Name == "" {
			continue
		}
		notification := Notification{
			Kind:       kind,
			Name:       player.Name,
			UUID:       player.UUID,
			Source:     SourceManagementProtocol,
			ObservedAt: time.Now().UTC(),
		}
		select {
		case m.notifyCh <- notification:
		default:
			m.mu.Lock()
			m.dropped++
			m.mu.Unlock()
		}
	}
}

// decodeNotificationPlayers understands the real 1.21.9 notification params,
// which are a bare JSON array of players. Allowlist/ban/operator notifications
// may nest the player under "player" with additional fields beside it. The
// {"player": ...} wrapper and a flat object are also accepted so older
// fixtures still exercise the same adapter path.
func decodeNotificationPlayers(params json.RawMessage) []ObservedPlayer {
	if len(params) == 0 {
		return nil
	}
	if params[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(params, &items) != nil {
			return nil
		}
		out := make([]ObservedPlayer, 0, len(items))
		for _, item := range items {
			if player, ok := decodeNotificationPlayer(item); ok {
				out = append(out, player)
			}
		}
		return out
	}
	if player, ok := decodeNotificationPlayer(params); ok {
		return []ObservedPlayer{player}
	}
	return nil
}

func decodeNotificationPlayer(raw json.RawMessage) (ObservedPlayer, bool) {
	var wrapped struct {
		Player *managementPlayer `json:"player"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Player != nil {
		player := normaliseManagementPlayer(*wrapped.Player)
		return player, player.Name != ""
	}
	var flat managementPlayer
	if json.Unmarshal(raw, &flat) != nil {
		return ObservedPlayer{}, false
	}
	player := normaliseManagementPlayer(flat)
	return player, player.Name != ""
}

func (m *ManagementAdapter) call(ctx context.Context, method string, params any, out any) error {
	if m.profileErr != nil {
		return m.info.sourceError(SourceManagementProtocol, m.profileErr)
	}
	callCtx, cancel := context.WithTimeout(ctx, managementCallTimeout)
	defer cancel()

	gen, err := m.ensureConnected(callCtx)
	if err != nil {
		return err
	}
	id := atomic.AddUint64(&m.nextID, 1)
	ch := make(chan rpcReply, 1)
	m.mu.Lock()
	if m.closed || m.generation != gen || m.conn == nil {
		m.mu.Unlock()
		return errors.New("playeradapters: management: connection generation changed")
	}
	conn := m.conn
	m.pending[id] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.pending, id)
		m.mu.Unlock()
	}()

	// A nil params means "no parameters": omit the field rather than sending
	// {}, which vanilla 1.21.9 rejects for minecraft:players.
	var rawParams json.RawMessage
	if params != nil {
		rawParams, err = json.Marshal(params)
		if err != nil {
			return m.info.sourceError(SourceManagementProtocol, err)
		}
	}
	payload, err := json.Marshal(rpcRequest{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: rawParams})
	if err != nil {
		return m.info.sourceError(SourceManagementProtocol, err)
	}
	m.writeMu.Lock()
	_ = conn.SetWriteDeadline(time.Now().Add(managementCallTimeout))
	err = conn.WriteMessage(websocket.TextMessage, payload)
	m.writeMu.Unlock()
	if err != nil {
		m.failConnection(gen, err)
		return m.info.sourceError(SourceManagementProtocol, err)
	}

	select {
	case reply := <-ch:
		if reply.err != nil {
			return reply.err
		}
		if out != nil && len(reply.result) > 0 {
			if err := json.Unmarshal(reply.result, out); err != nil {
				return m.info.sourceError(SourceManagementProtocol, err)
			}
		}
		return nil
	case <-callCtx.Done():
		return callCtx.Err()
	}
}

func normaliseManagementPlayer(player managementPlayer) ObservedPlayer {
	name := player.Name
	if name == "" {
		name = player.Username
	}
	identifier := player.ID
	if identifier == "" {
		identifier = player.UUID
	}
	uuid := ""
	if canonical, ok := canonicalUUID(identifier); ok {
		uuid = canonical
	}
	return ObservedPlayer{Name: name, UUID: uuid}
}

func parseManagementPlayers(raw json.RawMessage) ([]ObservedPlayer, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return []ObservedPlayer{}, nil
	}
	var list []managementPlayer
	if raw[0] == '[' {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
	} else {
		var wrapped struct {
			Players []managementPlayer `json:"players"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return nil, err
		}
		list = wrapped.Players
	}
	players := make([]ObservedPlayer, 0, len(list))
	for _, entry := range list {
		player := normaliseManagementPlayer(entry)
		if player.Name == "" {
			continue
		}
		players = append(players, player)
		if len(players) >= managementMaxPlayers {
			break
		}
	}
	return players, nil
}

func (m *ManagementAdapter) List(ctx context.Context) ([]ObservedPlayer, error) {
	var raw json.RawMessage
	// minecraft:players takes no parameters; sending {} is a -32602 on a real
	// 1.21.9 server. nil omits the params field from the frame.
	if err := m.call(ctx, MethodPlayersList, nil, &raw); err != nil {
		return nil, err
	}
	players, err := parseManagementPlayers(raw)
	if err != nil {
		return nil, m.info.sourceError(SourceManagementProtocol, err)
	}
	m.mu.Lock()
	m.roster = players
	m.rosterAt = time.Now().UTC()
	m.mu.Unlock()
	return append([]ObservedPlayer(nil), players...), nil
}

// The 1.21.9 signature uses named array parameters, not a single "player"
// object: {"add":[{"name":"..."}]}, {"remove":[{"name":"..."}]},
// {"kick":[{"player":{"name":"..."},"message":{"literal":"..."}}]},
// {"add":[{"player":{"name":"..."},"permissionLevel":4}]}. Note the camelCase
// permissionLevel and the structured message. All names are validated before
// a request is built, so no caller string is ever interpolated into JSON.
type playerSpec struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
}

func namedPlayer(name string) playerSpec { return playerSpec{Name: name} }

type addPlayersParams struct {
	Add []playerSpec `json:"add"`
}

type removePlayersParams struct {
	Remove []playerSpec `json:"remove"`
}

type displayMessage struct {
	Literal string `json:"literal"`
}

type kickSpec struct {
	Player  playerSpec      `json:"player"`
	Message *displayMessage `json:"message,omitempty"`
}

type kickPlayersParams struct {
	Kick []kickSpec `json:"kick"`
}

type banSpec struct {
	Player playerSpec `json:"player"`
	Reason string     `json:"reason,omitempty"`
}

type addBansParams struct {
	Add []banSpec `json:"add"`
}

type operatorSpec struct {
	Player          playerSpec `json:"player"`
	PermissionLevel int32      `json:"permissionLevel"`
}

type addOperatorsParams struct {
	Add []operatorSpec `json:"add"`
}

func (m *ManagementAdapter) Kick(ctx context.Context, name, reason string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := validReason(reason); err != nil {
		return err
	}
	kick := kickSpec{Player: namedPlayer(name)}
	if reason != "" {
		kick.Message = &displayMessage{Literal: reason}
	}
	var removed []managementPlayer
	if err := m.call(ctx, MethodPlayersKick, kickPlayersParams{Kick: []kickSpec{kick}}, &removed); err != nil {
		return err
	}
	for _, player := range removed {
		if strings.EqualFold(player.Name, name) {
			return nil
		}
	}
	return m.info.sourceError(SourceManagementProtocol, errors.New("the server did not report kicking the requested player"))
}

func (m *ManagementAdapter) Ban(ctx context.Context, name, reason string) error {
	if err := validName(name); err != nil {
		return err
	}
	if err := validReason(reason); err != nil {
		return err
	}
	return m.call(ctx, MethodBansAdd, addBansParams{Add: []banSpec{{
		Player: namedPlayer(name),
		Reason: reason,
	}}}, nil)
}

func (m *ManagementAdapter) Pardon(ctx context.Context, name string) error {
	if err := validName(name); err != nil {
		return err
	}
	return m.call(ctx, MethodBansRemove, removePlayersParams{Remove: []playerSpec{namedPlayer(name)}}, nil)
}

func (m *ManagementAdapter) SetOperator(ctx context.Context, name string, level int32, remove bool) error {
	if err := validName(name); err != nil {
		return err
	}
	if level < 0 || level > 4 {
		return errors.New("playeradapters: invalid operator level")
	}
	if remove {
		return m.call(ctx, MethodOperatorsRemove, removePlayersParams{Remove: []playerSpec{namedPlayer(name)}}, nil)
	}
	return m.call(ctx, MethodOperatorsAdd, addOperatorsParams{Add: []operatorSpec{{
		Player:          namedPlayer(name),
		PermissionLevel: level,
	}}}, nil)
}

func (m *ManagementAdapter) SetWhitelist(ctx context.Context, name string, remove bool) error {
	if err := validName(name); err != nil {
		return err
	}
	if remove {
		return m.call(ctx, MethodAllowlistRemove, removePlayersParams{Remove: []playerSpec{namedPlayer(name)}}, nil)
	}
	return m.call(ctx, MethodAllowlistAdd, addPlayersParams{Add: []playerSpec{namedPlayer(name)}}, nil)
}
