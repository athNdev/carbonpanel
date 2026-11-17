// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/metrics"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var _ carbonpanelv1connect.PlayerServiceHandler = (*PlayerService)(nil)

// Local ports keep PlayerService independent of any one adapter package. The
// composition root injects implementations (see player_wiring.go).
type observedPlayer struct {
	Name string
	UUID string
}

type playerSource interface {
	Source() string
	List(ctx context.Context) ([]observedPlayer, error)
	Kick(ctx context.Context, name, reason string) error
	Ban(ctx context.Context, name, reason string) error
	Pardon(ctx context.Context, name string) error
	SetOperator(ctx context.Context, name string, level int32, remove bool) error
	SetWhitelist(ctx context.Context, name string, remove bool) error
}

type capabilityInfo struct {
	Management, RCON, Exec bool
	ChosenSource, Detail   string
	Unsupported            []unsupportedInfo
}

type unsupportedInfo struct{ Name, Reason string }

type sourceNegotiator interface {
	Negotiate(ctx context.Context, server *storage.Server, cfg *storage.ServerConfig) (playerSource, capabilityInfo, error)
}

type skinAsset struct {
	Data                        []byte
	ContentType                 string
	FromCache                   bool
	Source, Confidence, Note    string
	ObservedAt                  time.Time
}

type skinProvider interface {
	Get(ctx context.Context, uuid, kind string) (skinAsset, error)
	Refresh(ctx context.Context, uuid, kind string) (skinAsset, error)
	Close()
}

type deepField struct {
	Name, Value, Unit, Source, Confidence, ConflictNote string
	ObservedAt                                          time.Time
}

type deepResult struct {
	Fields       []deepField
	SnapshotNote string
}

type deepProvider interface {
	Read(ctx context.Context, server *storage.Server, uuid, kind string) (deepResult, error)
}

// PlayerService serves the rich player product over the generated RPC. It is a
// cache/index plus adapter orchestrator: world files and live gameplay state
// stay owned by the game server.
type PlayerService struct {
	store      *storage.Store
	log        *logger.Logger
	metrics    *metrics.Collector
	negotiator sourceNegotiator
	skins      skinProvider
	deep       deepProvider
}

func NewPlayerService(store *storage.Store, log *logger.Logger, collector *metrics.Collector) *PlayerService {
	return &PlayerService{store: store, log: log, metrics: collector}
}

func (s *PlayerService) SetSourceNegotiator(n sourceNegotiator) { s.negotiator = n }
func (s *PlayerService) SetSkinProvider(p skinProvider)         { s.skins = p }
func (s *PlayerService) SetDeepDataReader(r deepProvider)       { s.deep = r }

// Stop releases long-lived owned resources (skin cache janitor).
func (s *PlayerService) Stop() {
	if s.skins != nil {
		s.skins.Close()
	}
}

func (s *PlayerService) server(ctx context.Context, id string) (*storage.Server, error) {
	server, err := s.store.GetServer(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}
	return server, nil
}

func (s *PlayerService) identity(ctx context.Context, key string) (*storage.PlayerIdentity, error) {
	identity, err := s.store.GetPlayerIdentity(ctx, key)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("player identity not found"))
	}
	return identity, nil
}

func observation(src v1.PlayerDataSource, confidence v1.PlayerConfidence, at time.Time, note string) *v1.Observation {
	o := &v1.Observation{
		Source:     src,
		Confidence: confidence,
	}
	if !at.IsZero() {
		o.ObservedAt = timestamppb.New(at)
	}
	if note != "" {
		o.ConflictNote = note
	}
	return o
}

func toProtoObservation(src string, confidence string, at time.Time, note string) *v1.Observation {
	return observation(playerSourceEnum(src), playerConfidenceEnum(confidence), at, note)
}

func playerSourceEnum(src string) v1.PlayerDataSource {
	switch strings.ToLower(strings.TrimSpace(src)) {
	case "management_protocol", "management":
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_MANAGEMENT_PROTOCOL
	case "rcon":
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_RCON
	case "exec":
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_EXEC
	case "on_disk_nbt":
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_ON_DISK_NBT
	case "on_disk_json":
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_ON_DISK_JSON
	case "identity_service":
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_IDENTITY_SERVICE
	case "bridge":
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_BRIDGE
	case "panel_db":
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_PANEL_DB
	default:
		return v1.PlayerDataSource_PLAYER_DATA_SOURCE_PRESENCE
	}
}

func playerConfidenceEnum(value string) v1.PlayerConfidence {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "detected":
		return v1.PlayerConfidence_PLAYER_CONFIDENCE_DETECTED
	case "inferred":
		return v1.PlayerConfidence_PLAYER_CONFIDENCE_INFERRED
	case "stale":
		return v1.PlayerConfidence_PLAYER_CONFIDENCE_STALE
	case "conflict":
		return v1.PlayerConfidence_PLAYER_CONFIDENCE_CONFLICT
	default:
		return v1.PlayerConfidence_PLAYER_CONFIDENCE_UNKNOWN
	}
}

func playerRef(identity *storage.PlayerIdentity) *v1.PlayerRef {
	return &v1.PlayerRef{
		IdentityKey: identity.ID,
		Name:        identity.CurrentName,
		Uuid:        identity.PrimaryUUID,
		Xuid:        identity.XUID,
		OfflineUuid: identity.OfflineUUID,
	}
}

func protoAliases(aliases []storage.PlayerAlias) []*v1.PlayerAlias {
	out := make([]*v1.PlayerAlias, 0, len(aliases))
	for _, a := range aliases {
		alias := &v1.PlayerAlias{
			Name:           a.Name,
			SourceServerId: a.SourceServerID,
			Source:         playerSourceEnum(a.Source),
		}
		if !a.ObservedAt.IsZero() {
			alias.ObservedAt = timestamppb.New(a.ObservedAt)
		}
		out = append(out, alias)
	}
	return out
}

// aliasesFor loads observed aliases. There is no store list helper yet, so the
// bounded query lives here beside the proto mapping.
func (s *PlayerService) aliasesFor(ctx context.Context, identityID string) []storage.PlayerAlias {
	var out []storage.PlayerAlias
	if err := s.store.DB().WithContext(ctx).Where("identity_id = ?", identityID).
		Order("observed_at DESC").Limit(50).Find(&out).Error; err != nil {
		s.log.Debug("Failed to load aliases for %s: %v", identityID, err)
	}
	return out
}

func (s *PlayerService) presenceFor(identity *storage.PlayerIdentity, serverID string) (v1.PlayerPresence, *v1.Observation, time.Time) {
	now := time.Now().UTC()
	lastSeen := identity.LastSeenAt
	var source v1.PlayerDataSource = v1.PlayerDataSource_PLAYER_DATA_SOURCE_PRESENCE
	confidence := v1.PlayerConfidence_PLAYER_CONFIDENCE_STALE
	presence := v1.PlayerPresence_PLAYER_PRESENCE_UNOBSERVED

	if s.metrics != nil && serverID != "" {
		if m := s.metrics.GetMetrics(serverID); m != nil {
			name := strings.ToLower(identity.CurrentName)
			for _, online := range m.PlayerSample {
				if strings.ToLower(online) == name {
					presence = v1.PlayerPresence_PLAYER_PRESENCE_ONLINE
					confidence = v1.PlayerConfidence_PLAYER_CONFIDENCE_DETECTED
					if !m.LastUpdated.IsZero() {
						lastSeen = m.LastUpdated
					}
					break
				}
			}
		}
	}
	if presence == v1.PlayerPresence_PLAYER_PRESENCE_UNOBSERVED {
		switch {
		case lastSeen.IsZero():
			confidence = v1.PlayerConfidence_PLAYER_CONFIDENCE_UNKNOWN
		case now.Sub(lastSeen) <= 15*time.Minute:
			presence = v1.PlayerPresence_PLAYER_PRESENCE_RECENTLY_SEEN
			source = v1.PlayerDataSource_PLAYER_DATA_SOURCE_PANEL_DB
		default:
			presence = v1.PlayerPresence_PLAYER_PRESENCE_OFFLINE
			source = v1.PlayerDataSource_PLAYER_DATA_SOURCE_PANEL_DB
		}
	}
	o := observation(source, confidence, lastSeen, "")
	if lastSeen.IsZero() {
		o.ConflictNote = "No fresh observation; absence of data is not offline."
	}
	return presence, o, lastSeen
}

// ListPlayers returns a bounded, sorted roster. Filters are applied over
// observed identities only; no fabricated zero values are produced.
func (s *PlayerService) ListPlayers(ctx context.Context, req *connect.Request[v1.ListPlayersRequest]) (*connect.Response[v1.ListPlayersResponse], error) {
	msg := req.Msg
	if msg.ServerId != "" {
		if _, err := s.server(ctx, msg.ServerId); err != nil {
			return nil, err
		}
	}
	limit := int(msg.Limit)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	identities, err := s.store.ListPlayerIdentities(ctx, msg.ServerId, msg.Query, limit)
	if err != nil {
		s.log.Error("Failed to list players: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list players"))
	}

	serverName := map[string]string{}
	access := map[string]storage.PlayerAccessState{}
	out := make([]*v1.PlayerSummary, 0, len(identities))
	for _, identity := range identities {
		presence, obs, lastSeen := s.presenceFor(&identity, msg.ServerId)
		serverID := msg.ServerId
		if serverID == "" {
			var session storage.PlayerSession
			if err := s.store.DB().WithContext(ctx).Where("identity_id = ?", identity.ID).
				Order("joined_at DESC").First(&session).Error; err == nil {
				serverID = session.ServerID
			}
		}
		name := "unassigned"
		var state storage.PlayerAccessState
		if serverID != "" {
			if cached, ok := serverName[serverID]; ok {
				name = cached
			} else if server, err := s.store.GetServer(ctx, serverID); err == nil {
				name = server.Name
				serverName[serverID] = name
			}
			key := identity.ID + "|" + serverID
			if cachedState, ok := access[key]; ok {
				state = cachedState
			} else {
				states, err := s.store.ListPlayerAccess(ctx, identity.ID)
				if err == nil {
					for _, candidate := range states {
						if candidate.ServerID == serverID {
							state = candidate
							break
						}
					}
				}
				access[key] = state
			}
		}
		band := "unverified"
		if state.Banned {
			band = "banned"
		} else if state.Operator {
			band = "operator"
		} else if state.Whitelisted {
			band = "whitelisted"
		}

		if msg.StateFilter != "" && !strings.EqualFold(msg.StateFilter, presenceStateName(presence)) {
			continue
		}
		if msg.AccessFilter != "" && !strings.EqualFold(msg.AccessFilter, band) {
			continue
		}

		summary := &v1.PlayerSummary{
			Ref:                 playerRef(&identity),
			ServerId:            serverID,
			ServerName:          name,
			Presence:            presence,
			PresenceObservation: obs,
			Aliases:             protoAliases(s.aliasesFor(ctx, identity.ID)),
			Whitelisted:         state.Whitelisted,
			Operator:            state.Operator,
			Banned:              state.Banned,
			SkinRef:             identity.ID,
			SkinAvailable:       identity.PrimaryUUID != "",
			AccessBand:          band,
		}
		if !lastSeen.IsZero() {
			summary.LastSeen = timestamppb.New(lastSeen)
		}
		out = append(out, summary)
	}

	sort.SliceStable(out, func(i, j int) bool {
		less := false
		switch msg.SortBy {
		case "last_seen":
			less = stampBefore(out[i].LastSeen, out[j].LastSeen)
		case "server":
			less = out[i].ServerName < out[j].ServerName
		case "presence":
			less = out[i].Presence < out[j].Presence
		default:
			less = strings.ToLower(out[i].Ref.Name) < strings.ToLower(out[j].Ref.Name)
		}
		if msg.SortDescending {
			return !less && out[i].Ref.IdentityKey != out[j].Ref.IdentityKey
		}
		return less
	})
	if len(out) > limit {
		out = out[:limit]
	}

	sourceDetail := "metrics + panel_db"
	if msg.ServerId != "" && s.negotiator != nil {
		if server, err := s.server(ctx, msg.ServerId); err == nil {
			var cfg *storage.ServerConfig
			if stored, cfgErr := s.store.GetServerConfig(ctx, server.ID); cfgErr == nil {
				cfg = stored
			}
			if _, caps, err := s.negotiator.Negotiate(ctx, server, cfg); err == nil {
				sourceDetail = caps.ChosenSource + ": " + caps.Detail
			}
		}
	}
	return connect.NewResponse(&v1.ListPlayersResponse{Players: out, SourceDetail: sourceDetail}), nil
}

func presenceStateName(p v1.PlayerPresence) string {
	switch p {
	case v1.PlayerPresence_PLAYER_PRESENCE_ONLINE:
		return "online"
	case v1.PlayerPresence_PLAYER_PRESENCE_OFFLINE:
		return "offline"
	case v1.PlayerPresence_PLAYER_PRESENCE_RECENTLY_SEEN:
		return "recently_seen"
	default:
		return "unobserved"
	}
}

func stampBefore(a, b interface{ AsTime() time.Time }) bool {
	if a == nil {
		return b != nil
	}
	if b == nil {
		return false
	}
	return a.AsTime().Before(b.AsTime())
}

// GetPlayer returns grouped detail with provenance on every non-authoritative
// group. Character/progress are read-only snapshots, never live NBT edits.
func (s *PlayerService) GetPlayer(ctx context.Context, req *connect.Request[v1.GetPlayerRequest]) (*connect.Response[v1.GetPlayerResponse], error) {
	msg := req.Msg
	identity, err := s.identity(ctx, msg.IdentityKey)
	if err != nil {
		return nil, err
	}
	presence, obs, lastSeen := s.presenceFor(identity, msg.ServerId)
	detail := &v1.PlayerDetail{
		Ref:                  playerRef(identity),
		Aliases:              protoAliases(s.aliasesFor(ctx, identity.ID)),
		Presence:             presence,
		PresenceObservation:  obs,
		PrimaryServerId:      msg.ServerId,
		NameHistoryNote:      "Mojang removed public name history on 2022-09-13; only names observed by this panel are shown.",
	}
	if !identity.FirstSeenAt.IsZero() {
		detail.FirstSeen = timestamppb.New(identity.FirstSeenAt)
	}
	if !lastSeen.IsZero() {
		detail.LastSeen = timestamppb.New(lastSeen)
	}

	states, err := s.store.ListPlayerAccess(ctx, identity.ID)
	if err == nil {
		for _, state := range states {
			if msg.ServerId != "" && state.ServerID != msg.ServerId {
				continue
			}
			detail.Access = append(detail.Access,
				&v1.PlayerField{Name: "server", Value: state.ServerID, Observation: observation(v1.PlayerDataSource_PLAYER_DATA_SOURCE_PANEL_DB, v1.PlayerConfidence_PLAYER_CONFIDENCE_DETECTED, state.ObservedAt, "")},
				&v1.PlayerField{Name: "whitelisted", Value: boolLabel(state.Whitelisted), Observation: toProtoObservation(state.Source, state.Confidence, state.ObservedAt, "")},
				&v1.PlayerField{Name: "operator", Value: boolLabel(state.Operator), Observation: toProtoObservation(state.Source, state.Confidence, state.ObservedAt, "")},
				&v1.PlayerField{Name: "banned", Value: boolLabel(state.Banned), Observation: toProtoObservation(state.Source, state.Confidence, state.ObservedAt, "")},
			)
		}
	}
	if s.deep != nil && msg.ServerId != "" {
		uuid := identity.PrimaryUUID
		if uuid == "" {
			uuid = "00000000-0000-0000-0000-000000000000"
		}
		server, err := s.server(ctx, msg.ServerId)
		if err == nil {
			for _, kind := range []string{"character", "progress"} {
				result, readErr := s.deep.Read(ctx, server, uuid, kind)
				if readErr != nil {
					continue
				}
				fields := deepFieldsProto(result.Fields)
				if kind == "character" {
					detail.Character = fields
				} else {
					detail.Progress = fields
				}
			}
		}
	}
	detail.Capabilities = s.capabilityStatuses(ctx, msg.ServerId)
	return connect.NewResponse(&v1.GetPlayerResponse{Detail: detail}), nil
}

func boolLabel(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func deepFieldsProto(fields []deepField) []*v1.PlayerField {
	out := make([]*v1.PlayerField, 0, len(fields))
	for _, f := range fields {
		out = append(out, &v1.PlayerField{
			Name: f.Name, Value: f.Value, Unit: f.Unit,
			Observation: toProtoObservation(f.Source, f.Confidence, f.ObservedAt, f.ConflictNote),
		})
	}
	return out
}

func (s *PlayerService) ListPlayerSessions(ctx context.Context, req *connect.Request[v1.ListPlayerSessionsRequest]) (*connect.Response[v1.ListPlayerSessionsResponse], error) {
	msg := req.Msg
	if _, err := s.identity(ctx, msg.IdentityKey); err != nil {
		return nil, err
	}
	sessions, err := s.store.ListPlayerSessions(ctx, msg.IdentityKey, msg.ServerId, 200)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list sessions"))
	}
	out := make([]*v1.PlayerSession, 0, len(sessions))
	for _, session := range sessions {
		item := &v1.PlayerSession{
			IdentityKey: session.IdentityID,
			ServerId:    session.ServerID,
			Source:      session.Source,
			Estimate:    session.Estimate,
		}
		if !session.JoinedAt.IsZero() {
			item.JoinedAt = timestamppb.New(session.JoinedAt)
		}
		if session.LeftAt != nil {
			item.LeftAt = timestamppb.New(*session.LeftAt)
		}
		out = append(out, item)
	}
	return connect.NewResponse(&v1.ListPlayerSessionsResponse{Sessions: out}), nil
}

func (s *PlayerService) ListPlayerAccess(ctx context.Context, req *connect.Request[v1.ListPlayerAccessRequest]) (*connect.Response[v1.ListPlayerAccessResponse], error) {
	msg := req.Msg
	if _, err := s.identity(ctx, msg.IdentityKey); err != nil {
		return nil, err
	}
	states, err := s.store.ListPlayerAccess(ctx, msg.IdentityKey)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list access state"))
	}
	out := make([]*v1.PlayerAccessState, 0, len(states))
	for _, state := range states {
		if msg.ServerId != "" && state.ServerID != msg.ServerId {
			continue
		}
		out = append(out, &v1.PlayerAccessState{
			ServerId:    state.ServerID,
			Whitelisted: state.Whitelisted,
			Operator:    state.Operator,
			OpLevel:     int32(state.OpLevel),
			Banned:      state.Banned,
			BanReason:   state.BanReason,
			Mute:        state.Mute,
			Observation: toProtoObservation(state.Source, state.Confidence, state.ObservedAt, ""),
		})
	}
	return connect.NewResponse(&v1.ListPlayerAccessResponse{States: out}), nil
}

func (s *PlayerService) ListPlayerHistory(ctx context.Context, req *connect.Request[v1.ListPlayerHistoryRequest]) (*connect.Response[v1.ListPlayerHistoryResponse], error) {
	msg := req.Msg
	limit := int(msg.Limit)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.store.ListPlayerHistory(ctx, msg.IdentityKey, msg.ServerId, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list player history"))
	}
	out := make([]*v1.PlayerHistoryEntry, 0, len(rows))
	for _, row := range rows {
		entry := &v1.PlayerHistoryEntry{
			Id: row.ID, Action: row.Action, ActorName: row.ActorName,
			ServerId: row.ServerID, Reason: row.Reason, RequestId: row.RequestID,
		}
		if !row.CreatedAt.IsZero() {
			entry.OccurredAt = timestamppb.New(row.CreatedAt)
		}
		out = append(out, entry)
	}
	return connect.NewResponse(&v1.ListPlayerHistoryResponse{Entries: out}), nil
}

func (s *PlayerService) capabilityStatuses(ctx context.Context, serverID string) []*v1.PlayerCapabilityStatus {
	statuses := []*v1.PlayerCapabilityStatus{
		{Capability: v1.PlayerCapability_PLAYER_CAPABILITY_BRIDGE_VITALS, State: v1.PlayerSupportState_PLAYER_SUPPORT_STATE_DECISION_PENDING, Detail: "Optional Paper/Fabric/NeoForge bridge requires capability + licence verification before it can ship."},
		{Capability: v1.PlayerCapability_PLAYER_CAPABILITY_BRIDGE_INVENTORY, State: v1.PlayerSupportState_PLAYER_SUPPORT_STATE_DECISION_PENDING, Detail: "Live inventory requires a verified bridge; on-disk snapshots are read-only."},
		{Capability: v1.PlayerCapability_PLAYER_CAPABILITY_BRIDGE_GROUPS, State: v1.PlayerSupportState_PLAYER_SUPPORT_STATE_DECISION_PENDING, Detail: "Permission groups are plugin-specific and unverified."},
		{Capability: v1.PlayerCapability_PLAYER_CAPABILITY_BRIDGE_MUTE, State: v1.PlayerSupportState_PLAYER_SUPPORT_STATE_DECISION_PENDING, Detail: "Mute is not a vanilla Java concept; requires a verified bridge."},
		{Capability: v1.PlayerCapability_PLAYER_CAPABILITY_BEDROCK_XUID, State: v1.PlayerSupportState_PLAYER_SUPPORT_STATE_DECISION_PENDING, Detail: "Bedrock identity parity (XUID/Floodgate) is pending an explicit scope decision."},
	}
	if serverID == "" || s.negotiator == nil {
		statuses = append(statuses,
			&v1.PlayerCapabilityStatus{Capability: v1.PlayerCapability_PLAYER_CAPABILITY_MANAGEMENT_PROTOCOL, State: v1.PlayerSupportState_PLAYER_SUPPORT_STATE_UNKNOWN, Detail: "No server selected to negotiate capabilities."},
			&v1.PlayerCapabilityStatus{Capability: v1.PlayerCapability_PLAYER_CAPABILITY_RCON_EXEC, State: v1.PlayerSupportState_PLAYER_SUPPORT_STATE_UNKNOWN, Detail: "No server selected to negotiate capabilities."},
			&v1.PlayerCapabilityStatus{Capability: v1.PlayerCapability_PLAYER_CAPABILITY_ON_DISK_READ, State: v1.PlayerSupportState_PLAYER_SUPPORT_STATE_UNKNOWN, Detail: "No server selected to negotiate capabilities."},
		)
		return statuses
	}
	server, err := s.server(ctx, serverID)
	if err != nil {
		return statuses
	}
	var cfg *storage.ServerConfig
	if stored, cfgErr := s.store.GetServerConfig(ctx, server.ID); cfgErr == nil {
		cfg = stored
	}
	_, caps, err := s.negotiator.Negotiate(ctx, server, cfg)
	if err != nil {
		statuses = append(statuses, &v1.PlayerCapabilityStatus{
			Capability: v1.PlayerCapability_PLAYER_CAPABILITY_MANAGEMENT_PROTOCOL,
			State:      v1.PlayerSupportState_PLAYER_SUPPORT_STATE_UNSUPPORTED,
			Detail:     caps.Detail,
		})
		return statuses
	}
	statusFor := func(available bool, cap v1.PlayerCapability, detail string) *v1.PlayerCapabilityStatus {
		state := v1.PlayerSupportState_PLAYER_SUPPORT_STATE_UNSUPPORTED
		if available {
			state = v1.PlayerSupportState_PLAYER_SUPPORT_STATE_SUPPORTED
		}
		return &v1.PlayerCapabilityStatus{Capability: cap, State: state, Detail: detail}
	}
	statuses = append(statuses,
		statusFor(caps.Management, v1.PlayerCapability_PLAYER_CAPABILITY_MANAGEMENT_PROTOCOL, "Native management protocol requires Java 1.21.9+ and controller-side configuration."),
		statusFor(caps.RCON, v1.PlayerCapability_PLAYER_CAPABILITY_RCON_EXEC, "RCON is Java-only, plaintext and parsed defensively."),
		statusFor(caps.Exec, v1.PlayerCapability_PLAYER_CAPABILITY_RCON_EXEC, "Node-scoped docker exec fallback for remote nodes."),
		statusFor(true, v1.PlayerCapability_PLAYER_CAPABILITY_ON_DISK_READ, "Read-only snapshots through the node resolver."),
		statusFor(s.skins != nil, v1.PlayerCapability_PLAYER_CAPABILITY_SKIN_PROXY, "Same-origin bounded skin cache; no third-party client requests."),
	)
	for _, unsupported := range caps.Unsupported {
		statuses = append(statuses, &v1.PlayerCapabilityStatus{
			Capability: v1.PlayerCapability_PLAYER_CAPABILITY_UNSPECIFIED,
			State:      v1.PlayerSupportState_PLAYER_SUPPORT_STATE_UNSUPPORTED,
			Detail:     unsupported.Name + ": " + unsupported.Reason,
		})
	}
	return statuses
}

func (s *PlayerService) ListPlayerCapabilities(ctx context.Context, req *connect.Request[v1.ListPlayerCapabilitiesRequest]) (*connect.Response[v1.ListPlayerCapabilitiesResponse], error) {
	return connect.NewResponse(&v1.ListPlayerCapabilitiesResponse{
		Capabilities: s.capabilityStatuses(ctx, req.Msg.ServerId),
	}), nil
}

func (s *PlayerService) GetPlayerSkin(ctx context.Context, req *connect.Request[v1.GetPlayerSkinRequest]) (*connect.Response[v1.GetPlayerSkinResponse], error) {
	return s.skin(ctx, req.Msg.IdentityKey, req.Msg.SkinRef, req.Msg.Kind, false)
}

func (s *PlayerService) RefreshPlayerSkin(ctx context.Context, req *connect.Request[v1.RefreshPlayerSkinRequest]) (*connect.Response[v1.RefreshPlayerSkinResponse], error) {
	asset, err := s.loadSkin(ctx, req.Msg.IdentityKey, "face", true)
	if err != nil {
		return nil, err
	}
	if req.Msg.ServerId != "" {
		s.auditPlayer(ctx, "", activity.EventPlayerSkinRefresh, req.Msg.ServerId, req.Msg.IdentityKey, "", map[string]any{"action": "skin.refresh"})
	}
	return connect.NewResponse(&v1.RefreshPlayerSkinResponse{Asset: asset}), nil
}

func (s *PlayerService) skin(ctx context.Context, identityKey, skinRef, kind string, refresh bool) (*connect.Response[v1.GetPlayerSkinResponse], error) {
	asset, err := s.loadSkin(ctx, identityKey, kind, refresh)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.GetPlayerSkinResponse{Asset: asset}), nil
}

func (s *PlayerService) loadSkin(ctx context.Context, identityKey, kind string, refresh bool) (*v1.PlayerSkinAsset, error) {
	if s.skins == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("skin proxy is not configured"))
	}
	if kind != "face" && kind != "skin" && kind != "cape" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("skin kind must be face, skin or cape"))
	}
	identity, err := s.identity(ctx, identityKey)
	if err != nil {
		return nil, err
	}
	uuid := identity.PrimaryUUID
	if uuid == "" {
		uuid = "00000000-0000-0000-0000-000000000000"
	}
	var asset skinAsset
	if refresh {
		asset, err = s.skins.Refresh(ctx, uuid, kind)
	} else {
		asset, err = s.skins.Get(ctx, uuid, kind)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	out := &v1.PlayerSkinAsset{
		Data: asset.Data, ContentType: asset.ContentType, FromCache: asset.FromCache,
		Observation: toProtoObservation(asset.Source, asset.Confidence, asset.ObservedAt, asset.Note),
	}
	return out, nil
}

func (s *PlayerService) GetPlayerDeepData(ctx context.Context, req *connect.Request[v1.GetPlayerDeepDataRequest]) (*connect.Response[v1.GetPlayerDeepDataResponse], error) {
	msg := req.Msg
	if s.deep == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("deep data reader is not configured"))
	}
	if msg.Kind != "stats" && msg.Kind != "advancements" && msg.Kind != "character" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("kind must be stats, advancements or character"))
	}
	identity, err := s.identity(ctx, msg.IdentityKey)
	if err != nil {
		return nil, err
	}
	server, err := s.server(ctx, msg.ServerId)
	if err != nil {
		return nil, err
	}
	uuid := identity.PrimaryUUID
	if uuid == "" {
		uuid = "00000000-0000-0000-0000-000000000000"
	}
	result, err := s.deep.Read(ctx, server, uuid, msg.Kind)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, connect.NewError(connect.CodeCanceled, err)
		}
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	return connect.NewResponse(&v1.GetPlayerDeepDataResponse{
		Fields: deepFieldsProto(result.Fields), SnapshotNote: result.SnapshotNote,
	}), nil
}

// auditPlayer writes one append-only audit row. Audit failure never breaks the
// operation being audited.
func (s *PlayerService) auditPlayer(ctx context.Context, ip, event, serverID, identityKey, requestID string, props map[string]any) {
	actorID, actorName := "", ""
	if user := auth.GetUserFromContext(ctx); user != nil {
		actorID, actorName = user.ID, user.Username
	}
	encoded := ""
	if len(props) > 0 {
		if raw, err := json.Marshal(props); err == nil {
			encoded = string(raw)
		}
	}
	if err := activity.Log(s.store.DB(), activity.Entry{
		ActorID: actorID, ActorName: actorName, IP: ip, Event: event,
		SubjectTyp: "player", SubjectID: identityKey,
		Properties: encoded, RequestID: strings.TrimSpace(requestID),
	}); err != nil {
		s.log.Error("Audit log failed for %s: %v", event, err)
	}
}

// actionContext resolves the server, identity and adapter for a mutation.
func (s *PlayerService) actionContext(ctx context.Context, serverID, identityKey, name string) (*storage.Server, *storage.PlayerIdentity, playerSource, string, error) {
	server, err := s.server(ctx, serverID)
	if err != nil {
		return nil, nil, nil, "", err
	}
	identity, err := s.identity(ctx, identityKey)
	if err != nil {
		return nil, nil, nil, "", err
	}
	if s.negotiator == nil {
		return nil, nil, nil, "", connect.NewError(connect.CodeUnavailable, errors.New("player source negotiation is not configured"))
	}
	var cfg *storage.ServerConfig
	if stored, cfgErr := s.store.GetServerConfig(ctx, server.ID); cfgErr == nil {
		cfg = stored
	}
	source, caps, err := s.negotiator.Negotiate(ctx, server, cfg)
	if err != nil {
		return nil, nil, nil, caps.Detail, connect.NewError(connect.CodeUnavailable, fmt.Errorf("no player source available: %s", caps.Detail))
	}
	if name == "" {
		name = identity.CurrentName
	}
	if name == "" {
		return nil, nil, nil, "", connect.NewError(connect.CodeInvalidArgument, errors.New("target player name is required"))
	}
	return server, identity, source, source.Source(), nil
}

// runAction persists intent, performs one external mutation and finalizes the
// journal. An uncertain outcome keeps the journal executing: no blind retry.
func (s *PlayerService) runAction(ctx context.Context, ip, serverID, identityKey, name, action, event, reason, requestID string, undoable bool, mutate func(playerSource) (string, error)) (*v1.PlayerActionOutcome, error) {
	if err := operation.ValidateRequestID(requestID); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	actorID, actorName := "", ""
	if user := auth.GetUserFromContext(ctx); user != nil {
		actorID, actorName = user.ID, user.Username
	}
	row, claimed, err := s.store.BeginPlayerAction(ctx, requestID, serverID, identityKey, action, actorID, actorName, reason, "", 1)
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrPlayerActionChanged):
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		case errors.Is(err, storage.ErrPlayerActionInProgress):
			return nil, connect.NewError(connect.CodeAborted, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	if !claimed && row.Phase == storage.PlayerActionComplete {
		return &v1.PlayerActionOutcome{RequestId: requestID, JournalId: row.ID, Results: []*v1.PlayerActionResult{{IdentityKey: identityKey, Success: true, Message: "already completed"}}}, nil
	}
	_, _, source, sourceName, err := s.actionContext(ctx, serverID, identityKey, name)
	if err != nil {
		return nil, err
	}
	if err := s.store.AdvancePlayerAction(ctx, row.ID, storage.PlayerActionPrepared, storage.PlayerActionExecuting, ""); err != nil {
		return nil, connect.NewError(connect.CodeAborted, err)
	}
	prior, mutateErr := mutate(source)
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if mutateErr != nil {
		_ = s.store.AdvancePlayerAction(finalCtx, row.ID, storage.PlayerActionExecuting, storage.PlayerActionFailed, mutateErr.Error())
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("%s failed: %w", action, mutateErr))
	}
	if err := s.store.AdvancePlayerAction(finalCtx, row.ID, storage.PlayerActionExecuting, storage.PlayerActionComplete, ""); err != nil {
		s.log.Error("Failed to finalize %s journal %s: %v", action, row.ID, err)
	}
	if prior == "" {
		prior = "{}"
	}
	_ = s.store.DB().WithContext(finalCtx).Model(&storage.PlayerModerationAction{}).Where("id = ?", row.ID).
		Update("prior_value", prior).Error
	s.auditPlayer(ctx, ip, event, serverID, identityKey, requestID, map[string]any{
		"action": action, "reason": reason, "source": sourceName, "outcome": "success",
	})
	result := &v1.PlayerActionResult{IdentityKey: identityKey, Success: true, Message: action + " applied via " + sourceName}
	if undoable {
		result.UndoSupported = true
		result.UndoToken = row.UndoToken
	}
	return &v1.PlayerActionOutcome{RequestId: requestID, JournalId: row.ID, Results: []*v1.PlayerActionResult{result}}, nil
}

func (s *PlayerService) KickPlayer(ctx context.Context, req *connect.Request[v1.KickPlayerRequest]) (*connect.Response[v1.KickPlayerResponse], error) {
	msg := req.Msg
	out, err := s.runAction(ctx, clientIP(req.Peer().Addr), msg.ServerId, msg.IdentityKey, msg.PlayerName, "kick", activity.EventPlayerKick, msg.Reason, msg.RequestId, false,
		func(source playerSource) (string, error) { return "{}", source.Kick(ctx, nameOr(msg.PlayerName, msg.IdentityKey), msg.Reason) })
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.KickPlayerResponse{Outcome: out}), nil
}

func (s *PlayerService) BanPlayer(ctx context.Context, req *connect.Request[v1.BanPlayerRequest]) (*connect.Response[v1.BanPlayerResponse], error) {
	msg := req.Msg
	out, err := s.runAction(ctx, clientIP(req.Peer().Addr), msg.ServerId, msg.IdentityKey, msg.PlayerName, "ban", activity.EventPlayerBan, msg.Reason, msg.RequestId, true,
		func(source playerSource) (string, error) { return `{"inverse":"pardon"}`, source.Ban(ctx, nameOr(msg.PlayerName, msg.IdentityKey), msg.Reason) })
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.BanPlayerResponse{Outcome: out}), nil
}

func (s *PlayerService) PardonPlayer(ctx context.Context, req *connect.Request[v1.PardonPlayerRequest]) (*connect.Response[v1.PardonPlayerResponse], error) {
	msg := req.Msg
	out, err := s.runAction(ctx, clientIP(req.Peer().Addr), msg.ServerId, msg.IdentityKey, msg.PlayerName, "pardon", activity.EventPlayerPardon, "", msg.RequestId, true,
		func(source playerSource) (string, error) { return `{"inverse":"ban"}`, source.Pardon(ctx, nameOr(msg.PlayerName, msg.IdentityKey)) })
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.PardonPlayerResponse{Outcome: out}), nil
}

func (s *PlayerService) SetOperator(ctx context.Context, req *connect.Request[v1.SetOperatorRequest]) (*connect.Response[v1.SetOperatorResponse], error) {
	msg := req.Msg
	// No self-elevation: the actor may not raise their own game permission.
	if user := auth.GetUserFromContext(ctx); user != nil && strings.EqualFold(user.Username, msg.PlayerName) && !msg.Remove {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("an actor cannot op themselves"))
	}
	action, event := "op", activity.EventPlayerOp
	inverse := `{"inverse":"deop"}`
	if msg.Remove {
		action, event = "deop", activity.EventPlayerDeop
		inverse = `{"inverse":"op"}`
	}
	out, err := s.runAction(ctx, clientIP(req.Peer().Addr), msg.ServerId, msg.IdentityKey, msg.PlayerName, action, event, "", msg.RequestId, true,
		func(source playerSource) (string, error) { return inverse, source.SetOperator(ctx, nameOr(msg.PlayerName, msg.IdentityKey), msg.Level, msg.Remove) })
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.SetOperatorResponse{Outcome: out}), nil
}

func (s *PlayerService) SetWhitelistEntry(ctx context.Context, req *connect.Request[v1.SetWhitelistEntryRequest]) (*connect.Response[v1.SetWhitelistEntryResponse], error) {
	msg := req.Msg
	action, event := "allowlist_add", activity.EventPlayerWhitelistAdd
	inverse := `{"inverse":"allowlist_remove"}`
	if msg.Remove {
		action, event = "allowlist_remove", activity.EventPlayerWhitelistRemove
		inverse = `{"inverse":"allowlist_add"}`
	}
	out, err := s.runAction(ctx, clientIP(req.Peer().Addr), msg.ServerId, msg.IdentityKey, msg.PlayerName, action, event, "", msg.RequestId, true,
		func(source playerSource) (string, error) { return inverse, source.SetWhitelist(ctx, nameOr(msg.PlayerName, msg.IdentityKey), msg.Remove) })
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&v1.SetWhitelistEntryResponse{Outcome: out}), nil
}

func nameOr(name, key string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	return key
}

// BulkPlayerAction applies one typed action to a bounded set of targets. Each
// target gets its own result; a failure is never reported as overall success.
func (s *PlayerService) BulkPlayerAction(ctx context.Context, req *connect.Request[v1.BulkPlayerActionRequest]) (*connect.Response[v1.BulkPlayerActionResponse], error) {
	msg := req.Msg
	const maxTargets = 50
	if len(msg.IdentityKeys) == 0 || len(msg.IdentityKeys) > maxTargets {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("bulk actions require 1-%d targets", maxTargets))
	}
	if err := operation.ValidateRequestID(msg.RequestId); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	actorID, actorName := "", ""
	if user := auth.GetUserFromContext(ctx); user != nil {
		actorID, actorName = user.ID, user.Username
	}
	row, _, err := s.store.BeginPlayerAction(ctx, msg.RequestId, msg.ServerId, strings.Join(msg.IdentityKeys, ","), "bulk_"+msg.Kind, actorID, actorName, msg.Reason, "", len(msg.IdentityKeys))
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrPlayerActionInProgress):
			return nil, connect.NewError(connect.CodeAborted, err)
		case errors.Is(err, storage.ErrPlayerActionChanged):
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		default:
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	_, _, source, sourceName, err := s.actionContext(ctx, msg.ServerId, msg.IdentityKeys[0], "")
	if err != nil {
		return nil, err
	}
	results := make([]*v1.PlayerActionResult, 0, len(msg.IdentityKeys))
	failures := 0
	for _, key := range msg.IdentityKeys {
		identity, err := s.identity(ctx, key)
		if err != nil {
			results = append(results, &v1.PlayerActionResult{IdentityKey: key, Success: false, ErrorCode: "not_found", Message: "player identity not found"})
			failures++
			continue
		}
		name := identity.CurrentName
		var actionErr error
		switch msg.Kind {
		case "kick":
			actionErr = source.Kick(ctx, name, msg.Reason)
		case "ban":
			actionErr = source.Ban(ctx, name, msg.Reason)
		case "pardon":
			actionErr = source.Pardon(ctx, name)
		case "deop":
			actionErr = source.SetOperator(ctx, name, 0, true)
		case "allowlist_add":
			actionErr = source.SetWhitelist(ctx, name, false)
		case "allowlist_remove":
			actionErr = source.SetWhitelist(ctx, name, true)
		default:
			actionErr = fmt.Errorf("unsupported bulk action %q", msg.Kind)
		}
		if actionErr != nil {
			failures++
			results = append(results, &v1.PlayerActionResult{IdentityKey: key, Success: false, ErrorCode: "source_error", Message: actionErr.Error()})
			continue
		}
		results = append(results, &v1.PlayerActionResult{IdentityKey: key, Success: true, Message: "applied via " + sourceName})
	}
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	phase := storage.PlayerActionComplete
	if failures == len(msg.IdentityKeys) {
		phase = storage.PlayerActionFailed
	}
	if err := s.store.AdvancePlayerAction(finalCtx, row.ID, storage.PlayerActionPrepared, phase, fmt.Sprintf("%d of %d failed", failures, len(msg.IdentityKeys))); err != nil {
		s.log.Error("Failed to finalize bulk journal %s: %v", row.ID, err)
	}
	s.auditPlayer(ctx, clientIP(req.Peer().Addr), activity.EventPlayerBulk, msg.ServerId, strings.Join(msg.IdentityKeys, ","), msg.RequestId, map[string]any{
		"action": msg.Kind, "reason": msg.Reason, "targets": len(msg.IdentityKeys), "failures": failures, "source": sourceName,
	})
	return connect.NewResponse(&v1.BulkPlayerActionResponse{Outcome: &v1.PlayerActionOutcome{
		RequestId: msg.RequestId, JournalId: row.ID, Results: results,
	}}), nil
}

// UndoPlayerAction reverses only actions whose prior value was recorded. Kick
// is deliberately not reversible and never exposes an undo token.
func (s *PlayerService) UndoPlayerAction(ctx context.Context, req *connect.Request[v1.UndoPlayerActionRequest]) (*connect.Response[v1.UndoPlayerActionResponse], error) {
	msg := req.Msg
	row, err := s.store.GetPlayerActionByUndoToken(ctx, msg.UndoToken)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("undo token not found"))
	}
	if msg.ServerId != "" && row.ServerID != msg.ServerId {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("undo token belongs to another server"))
	}
	if strings.TrimSpace(row.PriorValue) == "" || row.PriorValue == "{}" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this action has no recorded prior value and cannot be undone"))
	}
	_, _, source, sourceName, err := s.actionContext(ctx, row.ServerID, row.IdentityKey, "")
	if err != nil {
		return nil, err
	}
	identity, err := s.identity(ctx, row.IdentityKey)
	if err != nil {
		return nil, err
	}
	name := identity.CurrentName
	switch row.Action {
	case "ban":
		err = source.Pardon(ctx, name)
	case "pardon":
		err = source.Ban(ctx, name, "undo of pardon")
	case "op":
		err = source.SetOperator(ctx, name, 0, true)
	case "deop":
		err = source.SetOperator(ctx, name, 4, false)
	case "allowlist_add":
		err = source.SetWhitelist(ctx, name, true)
	case "allowlist_remove":
		err = source.SetWhitelist(ctx, name, false)
	default:
		err = fmt.Errorf("action %q has no supported undo", row.Action)
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	s.auditPlayer(ctx, clientIP(req.Peer().Addr), activity.EventPlayerUndo, row.ServerID, row.IdentityKey, msg.RequestId, map[string]any{
		"undone_action": row.Action, "source": sourceName, "outcome": "success",
	})
	return connect.NewResponse(&v1.UndoPlayerActionResponse{Outcome: &v1.PlayerActionOutcome{
		RequestId: msg.RequestId,
		Results:   []*v1.PlayerActionResult{{IdentityKey: row.IdentityKey, Success: true, Message: "undo applied via " + sourceName}},
	}}), nil
}
