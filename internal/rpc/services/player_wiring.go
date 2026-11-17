// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
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

type adapterShim struct {
	playeradapters.Adapter
}

func (a adapterShim) List(ctx context.Context) ([]observedPlayer, error) {
	listed, err := a.Adapter.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]observedPlayer, 0, len(listed))
	for _, p := range listed {
		out = append(out, observedPlayer{Name: p.Name, UUID: p.UUID})
	}
	return out, nil
}

// negotiatorShim builds adapter inputs from controller configuration. It never
// writes controller state into a world and never logs secrets.
type negotiatorShim struct {
	cfg     *config.Config
	sender  *command.Sender
	mu      sync.Mutex
	closers []interface{ Close() }
}

func newNegotiatorShim(cfg *config.Config, sender *command.Sender) *negotiatorShim {
	return &negotiatorShim{cfg: cfg, sender: sender}
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
	adapter, caps, err := playeradapters.Negotiate(ctx, info, exec)
	if closer, ok := adapter.(interface{ Close() }); ok {
		n.mu.Lock()
		n.closers = append(n.closers, closer)
		n.mu.Unlock()
	}
	unsupported := make([]unsupportedInfo, 0, len(caps.Unsupported))
	for _, item := range caps.Unsupported {
		unsupported = append(unsupported, unsupportedInfo{Name: item.Name, Reason: item.Reason})
	}
	return adapterShim{adapter}, capabilityInfo{
		Management: caps.Management, RCON: caps.RCON, Exec: caps.Exec,
		ChosenSource: caps.ChosenSource, Detail: caps.Detail, Unsupported: unsupported,
	}, err
}

// Close stops any management-protocol background reader this process owns.
func (n *negotiatorShim) Close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, closer := range n.closers {
		closer.Close()
	}
	n.closers = nil
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
	service.SetSkinProvider(skinShim{cache: playerskin.NewCache(skinDir, 12*time.Hour, nil)})
	service.SetDeepDataReader(deepShim{reader: playerdata.NewReader(store, resolver)})
	return service
}
