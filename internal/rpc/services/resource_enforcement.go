// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const daemonEnforcementCacheTTL = 30 * time.Second

type daemonEnforcementCache struct {
	at        time.Time
	value     docker.DaemonEnforcement
	qualified bool
}

func (s *ServerService) toProtoServer(ctx context.Context, server *storage.Server) *v1.Server {
	proto := dbServerToProto(server)
	if proto == nil {
		return nil
	}
	proto.ResourceEnforcement = s.resourceEnforcementProto(ctx, server)
	return proto
}

func (s *ServerService) resourceEnforcementProto(ctx context.Context, server *storage.Server) *v1.ResourceEnforcement {
	if server == nil {
		return nil
	}
	writableMB, worldQuotaMB := s.storageLimits()
	requestedMB := int64(server.Memory)
	if server.DockerOverrides != nil && server.DockerOverrides.GetMemoryLimit() > 0 {
		requestedMB = server.DockerOverrides.GetMemoryLimit()
	}
	daemon, qualified := s.cachedDaemonEnforcement(ctx, server.NodeID)
	profile := docker.DescribeConfiguredResourceLimits(requestedMB, writableMB, worldQuotaMB, server.WorldSize, daemon)
	view := docker.ViewResourceEnforcement(profile, qualified, worldQuotaMB)
	return &v1.ResourceEnforcement{
		RequestedMemoryBytes:     view.RequestedMemoryBytes,
		EffectiveMemoryBytes:     view.EffectiveMemoryBytes,
		RequestedWritableLayerMb: view.RequestedWritableLayerMB,
		EffectiveWritableLayerMb: view.EffectiveWritableLayerMB,
		WorldDiskEnforced:        view.WorldDiskEnforced,
		WorldQuotaAdmission:      view.WorldQuotaAdmission,
		MemoryEnforced:           view.MemoryEnforced,
		Warnings:                 view.Warnings,
	}
}

func (s *ServerService) storageLimits() (writableMB, worldQuotaMB int64) {
	if s == nil || s.config == nil {
		return 0, 0
	}
	return s.config.Storage.ContainerWritableMB, s.config.Storage.WorldQuotaMB
}

func (s *ServerService) cachedDaemonEnforcement(ctx context.Context, nodeID string) (*docker.DaemonEnforcement, bool) {
	if s == nil {
		return nil, false
	}
	if nodeID == "" {
		nodeID = "local"
	}
	now := time.Now()
	s.daemonInfoMu.Lock()
	if entry, ok := s.daemonInfo[nodeID]; ok && now.Sub(entry.at) < daemonEnforcementCacheTTL {
		value := entry.value
		qualified := entry.qualified
		s.daemonInfoMu.Unlock()
		if !qualified {
			return nil, false
		}
		return &value, true
	}
	s.daemonInfoMu.Unlock()

	cli := s.getDockerClient(nodeID)
	if cli == nil {
		s.storeDaemonEnforcement(nodeID, now, docker.DaemonEnforcement{}, false)
		return nil, false
	}
	infoCtx, cancel := context.WithTimeout(ctx, statusRefreshTimeout)
	defer cancel()
	enforcement, err := cli.DaemonEnforcement(infoCtx)
	if err != nil {
		s.storeDaemonEnforcement(nodeID, now, docker.DaemonEnforcement{}, false)
		return nil, false
	}
	s.storeDaemonEnforcement(nodeID, now, enforcement, true)
	return &enforcement, true
}

func (s *ServerService) storeDaemonEnforcement(nodeID string, at time.Time, value docker.DaemonEnforcement, qualified bool) {
	s.daemonInfoMu.Lock()
	defer s.daemonInfoMu.Unlock()
	if s.daemonInfo == nil {
		s.daemonInfo = make(map[string]daemonEnforcementCache)
	}
	s.daemonInfo[nodeID] = daemonEnforcementCache{at: at, value: value, qualified: qualified}
}
