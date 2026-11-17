// SPDX-License-Identifier: MPL-2.0
package nodescan

import (
	"context"
	"errors"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"testing"
	"time"
)

type nodeStoreFunc func(context.Context) ([]*db.Node, error)

func (f nodeStoreFunc) ListNodes(ctx context.Context) ([]*db.Node, error) { return f(ctx) }

func TestScanPreservesBoundedProbesAndBestEffortKnownCount(t *testing.T) {
	for _, unavailable := range []bool{false, true} {
		scanner := New(nil, nil)
		scanner.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 99, time.UTC) }
		scanner.detect = func(ctx context.Context) []docker.DockerCandidate {
			deadline, ok := ctx.Deadline()
			if !ok || time.Until(deadline) > 12*time.Second {
				t.Fatal("missing bounded probe context")
			}
			return []docker.DockerCandidate{{Host: "fixture", Port: 2375, Reachable: true, LatencyMs: 42, Source: "interface"}, {Host: "unreachable", Port: 2376}}
		}
		scanner.store = nodeStoreFunc(func(context.Context) ([]*db.Node, error) {
			if unavailable {
				return nil, errors.New("unavailable")
			}
			return []*db.Node{{ID: "one"}, {ID: "two"}}, nil
		})
		result := scanner.Scan(context.Background())
		if len(result.Candidates) != 2 || result.Candidates[0].LatencyMs != 42 || result.Candidates[1].Reachable || result.ScannedAt.AsTime().Nanosecond() != 0 {
			t.Fatalf("probe mapping changed: %v", result)
		}
		expected := int32(2)
		if unavailable {
			expected = 0
		}
		if result.KnownNodes != expected {
			t.Fatalf("known nodes = %d", result.KnownNodes)
		}
	}
}

func TestScanPassesCancellationToBothDependencies(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scanner := New(nil, nil)
	scanner.detect = func(ctx context.Context) []docker.DockerCandidate {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("probe lost cancellation")
		}
		return nil
	}
	scanner.store = nodeStoreFunc(func(ctx context.Context) ([]*db.Node, error) {
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatal("store lost cancellation")
		}
		return nil, ctx.Err()
	})
	result := scanner.Scan(ctx)
	if result.KnownNodes != 0 || len(result.Candidates) != 0 {
		t.Fatalf("unexpected canceled evidence: %v", result)
	}
}
