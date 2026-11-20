// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/athNdev/carbon-panel/internal/command"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/metrics"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/playeradapters"
	"github.com/athNdev/carbon-panel/internal/playerdata"
	"github.com/athNdev/carbon-panel/internal/playerskin"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// This file owns the seam between PlayerService's small local ports and the
// concrete adapter packages. Keeping the translation here means neither the
// service nor the adapters depend on each other's package shape.

// adapterEntry owns one adapter per server with a reference count. A retired
// entry is closed only after its in-flight calls finish, so a roster load can
// never tear down a moderation adapter that is mid-mutation.
type adapterEntry struct {
	adapter playeradapters.Adapter
	// ManagementAdapter.Close returns error; a plain Close() assertion would
	// silently match nothing and leak its background WebSocket reader.
	closer  interface{ Close() error }
	info    playeradapters.ServerInfo
	caps    capabilityInfo
	refs    int
	retired bool
}

// negotiatorShim builds adapter inputs from controller configuration. It never
// writes controller state into a world and never logs secrets.
type negotiatorShim struct {
	cfg     *config.Config
	sender  *command.Sender
	mu      sync.Mutex
	entries map[string]*adapterEntry
}

func newNegotiatorShim(cfg *config.Config, sender *command.Sender) *negotiatorShim {
	return &negotiatorShim{cfg: cfg, sender: sender, entries: make(map[string]*adapterEntry)}
}

func globalString(cfg *config.Config, key string) string {
	if cfg == nil || cfg.Minecraft.GlobalConfig == nil {
		return ""
	}
	value, _ := cfg.Minecraft.GlobalConfig[key].(string)
	return strings.TrimSpace(value)
}

func (n *negotiatorShim) Negotiate(ctx context.Context, server *storage.Server, serverCfg *storage.ServerConfig) (playerSource, capabilityInfo, error) {
	info := playeradapters.ServerInfo{
		ServerID:    server.ID,
		NodeID:      server.NodeID,
		ContainerID: server.ContainerID,
		DataPath:    server.DataPath,
		Edition:     globalString(n.cfg, "player_edition"),
		MCVersion:   server.MCVersion,
	}
	if info.Edition == "" {
		info.Edition = "java"
	}
	if serverCfg != nil {
		if serverCfg.EnableRCON != nil {
			info.RCONEnabled = *serverCfg.EnableRCON
		}
		if serverCfg.RCONPort != nil {
			info.RCONPort = *serverCfg.RCONPort
		}
		if serverCfg.RCONPassword != nil {
			info.RCONPassword = *serverCfg.RCONPassword
		}
	}
	// A controller-reachable RCON host is an explicit operator setting. When it
	// is absent, the node-scoped exec path handles local RCON and remote exec
	// with correct host resolution and password redaction.
	info.RCONHost = globalString(n.cfg, "player_rcon_host")
	if info.RCONHost == "" {
		info.RCONEnabled = false
	}
	info.ManagementURL = globalString(n.cfg, "management_server_url")
	info.ManagementSecret = globalString(n.cfg, "management_server_secret")
	if info.ManagementURL != "" {
		info.ManagementEnabled = true
	}

	var exec playeradapters.ExecFunc
	if n.sender != nil {
		sender := n.sender
		exec = func(ctx context.Context, command string) (string, error) {
			return sender.SendCommand(ctx, server.ID, command)
		}
	}
	n.mu.Lock()
	entry := n.entries[server.ID]
	if entry != nil && entry.info != info {
		entry.retired = true
		if entry.refs == 0 {
			closeAdapterEntry(entry)
			delete(n.entries, server.ID)
		}
		entry = nil
	}
	if entry == nil {
		adapter, caps, err := playeradapters.Negotiate(ctx, info, exec)
		if err != nil {
			n.mu.Unlock()
			return nil, toCapabilityInfo(caps), err
		}
		var closer interface{ Close() error }
		if candidate, ok := adapter.(interface{ Close() error }); ok {
			closer = candidate
		}
		entry = &adapterEntry{adapter: adapter, closer: closer, info: info, caps: toCapabilityInfo(caps)}
		n.entries[server.ID] = entry
	}
	caps := entry.caps
	n.mu.Unlock()
	return &refCountedSource{shim: n, serverID: server.ID, caps: caps}, caps, nil
}

func toCapabilityInfo(caps playeradapters.CapabilitySet) capabilityInfo {
	unsupported := make([]unsupportedInfo, 0, len(caps.Unsupported))
	for _, item := range caps.Unsupported {
		unsupported = append(unsupported, unsupportedInfo{Name: item.Name, Reason: item.Reason})
	}
	return capabilityInfo{
		Management: caps.Management, RCON: caps.RCON, Exec: caps.Exec,
		ChosenSource: caps.ChosenSource, Detail: caps.Detail, Unsupported: unsupported,
	}
}

func closeAdapterEntry(entry *adapterEntry) {
	if entry.closer != nil {
		_ = entry.closer.Close()
	}
}

func (n *negotiatorShim) release(serverID string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	entry := n.entries[serverID]
	if entry == nil {
		return
	}
	entry.refs--
	if entry.retired && entry.refs <= 0 {
		closeAdapterEntry(entry)
		delete(n.entries, serverID)
	}
}

// refCountedSource acquires the current adapter for the duration of one call.
// Each method releases after it finishes, so re-negotiation can never close an
// adapter that is still in flight.
type refCountedSource struct {
	shim     *negotiatorShim
	serverID string
	caps     capabilityInfo
}

func (r *refCountedSource) Source() string { return r.caps.ChosenSource }

func (r *refCountedSource) with(fn func(playeradapters.Adapter) error) error {
	r.shim.mu.Lock()
	entry := r.shim.entries[r.serverID]
	if entry == nil || entry.retired {
		r.shim.mu.Unlock()
		return fmt.Errorf("player adapter for %s is no longer active", r.serverID)
	}
	entry.refs++
	adapter := entry.adapter
	r.shim.mu.Unlock()
	defer r.shim.release(r.serverID)
	return fn(adapter)
}

func (r *refCountedSource) List(ctx context.Context) ([]observedPlayer, error) {
	var out []observedPlayer
	err := r.with(func(adapter playeradapters.Adapter) error {
		listed, listErr := adapter.List(ctx)
		if listErr != nil {
			return listErr
		}
		out = make([]observedPlayer, 0, len(listed))
		for _, p := range listed {
			out = append(out, observedPlayer{Name: p.Name, UUID: p.UUID})
		}
		return nil
	})
	return out, err
}

func (r *refCountedSource) Kick(ctx context.Context, name, reason string) error {
	return r.with(func(a playeradapters.Adapter) error { return a.Kick(ctx, name, reason) })
}
func (r *refCountedSource) Ban(ctx context.Context, name, reason string) error {
	return r.with(func(a playeradapters.Adapter) error { return a.Ban(ctx, name, reason) })
}
func (r *refCountedSource) Pardon(ctx context.Context, name string) error {
	return r.with(func(a playeradapters.Adapter) error { return a.Pardon(ctx, name) })
}
func (r *refCountedSource) SetOperator(ctx context.Context, name string, level int32, remove bool) error {
	return r.with(func(a playeradapters.Adapter) error { return a.SetOperator(ctx, name, level, remove) })
}
func (r *refCountedSource) SetWhitelist(ctx context.Context, name string, remove bool) error {
	return r.with(func(a playeradapters.Adapter) error { return a.SetWhitelist(ctx, name, remove) })
}

// Close stops any management-protocol background reader this process owns.
// Calls in flight keep their adapter alive until they release it.
func (n *negotiatorShim) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for id, entry := range n.entries {
		if entry.refs == 0 {
			closeAdapterEntry(entry)
			delete(n.entries, id)
		} else {
			entry.retired = true
		}
	}
}

type skinShim struct{ cache *playerskin.Cache }

func (s skinShim) Get(ctx context.Context, uuid, kind string) (skinAsset, error) {
	asset, err := s.cache.Get(ctx, uuid, kind)
	return convertSkin(asset), err
}

func (s skinShim) Refresh(ctx context.Context, uuid, kind string) (skinAsset, error) {
	asset, err := s.cache.Refresh(ctx, uuid, kind)
	return convertSkin(asset), err
}

func (s skinShim) Lookup(ctx context.Context, query string) (identityLookup, error) {
	result, err := s.cache.Lookup(ctx, query)
	return identityLookup{
		Status: string(result.Status),
		UUID:   result.UUID,
		Name:   result.Name,
		Note:   result.Note,
		Source: result.Source,
	}, err
}

func (s skinShim) Close() { s.cache.Close() }

func convertSkin(asset playerskin.Asset) skinAsset {
	return skinAsset{
		Data: asset.Data, ContentType: asset.ContentType, FromCache: asset.FromCache,
		Source: asset.Source, Confidence: asset.Confidence, Note: asset.Note,
		ObservedAt: asset.ObservedAt,
	}
}

type deepShim struct{ reader *playerdata.Reader }

func (d deepShim) Read(ctx context.Context, server *storage.Server, uuid, kind string) (deepResult, error) {
	result, err := d.reader.Read(ctx, server, uuid, kind)
	if err != nil {
		return deepResult{}, err
	}
	fields := make([]deepField, 0, len(result.Fields))
	for _, f := range result.Fields {
		fields = append(fields, deepField{
			Name: f.Name, Value: f.Value, Unit: f.Unit, Source: f.Source,
			Confidence: f.Confidence, ConflictNote: f.ConflictNote, ObservedAt: f.ObservedAt,
		})
	}
	return deepResult{Fields: fields, SnapshotNote: result.SnapshotNote}, nil
}

// NewConfiguredPlayerService wires the concrete adapters at the composition
// root. The cache directory is supplied by configuration so no workstation-
// specific path is embedded here. Both owned resources are released by Stop.
func NewConfiguredPlayerService(
	store *storage.Store,
	cfg *config.Config,
	sender *command.Sender,
	resolver *nodestorage.Resolver,
	collector *metrics.Collector,
	log *logger.Logger,
	skinDir string,
) *PlayerService {
	service := NewPlayerService(store, log, collector)
	negotiator := newNegotiatorShim(cfg, sender)
	service.SetSourceNegotiator(negotiator)
	service.SetEdition(globalString(cfg, "player_edition"))
	service.SetSkinProvider(skinShim{cache: playerskin.NewCache(skinDir, 12*time.Hour, nil)})
	service.SetDeepDataReader(deepShim{reader: playerdata.NewReader(store, resolver)})
	return service
}
