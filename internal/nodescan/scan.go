// SPDX-License-Identifier: MPL-2.0
// Package nodescan owns bounded node discovery shared by product transports.
package nodescan

import (
	"context"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type nodeStore interface {
	ListNodes(context.Context) ([]*db.Node, error)
}

type Scanner struct {
	store  nodeStore
	log    *logger.Logger
	detect func(context.Context) []docker.DockerCandidate
	now    func() time.Time
}

func New(store *db.Store, log *logger.Logger) *Scanner {
	scanner := &Scanner{log: log, detect: docker.DetectDockerDaemons, now: time.Now}
	if store != nil {
		scanner.store = store
	}
	return scanner
}

// Scan retains the legacy best-effort known-node count and twelve-second probe
// limit. Authorization belongs to the mapped transport boundary before scanning.
func (s *Scanner) Scan(ctx context.Context) *v1.ScanNodesResponse {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	result := &v1.ScanNodesResponse{}
	for _, candidate := range s.detect(ctx) {
		result.Candidates = append(result.Candidates, &v1.DockerCandidate{Host: candidate.Host, Port: int32(candidate.Port), Reachable: candidate.Reachable, LatencyMs: candidate.LatencyMs, Source: candidate.Source})
	}
	if s.store != nil {
		if nodes, err := s.store.ListNodes(ctx); err == nil {
			result.KnownNodes = int32(len(nodes))
		} else if s.log != nil {
			s.log.Warn("Node scan: failed to list known nodes: %v", err)
		}
	}
	result.ScannedAt = timestamppb.New(s.now().UTC().Truncate(time.Second))
	return result
}
