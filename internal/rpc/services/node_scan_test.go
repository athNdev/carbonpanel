// SPDX-License-Identifier: MPL-2.0
package services

import (
	"connectrpc.com/connect"
	"context"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/proto"
	"testing"
)

func TestNodeScanServiceUsesSharedApplicationResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	expected := &v1.ScanNodesResponse{KnownNodes: 2, Candidates: []*v1.DockerCandidate{{Host: "fixture", LatencyMs: 42}}}
	service := NewNodeService(nil, nil, nil)
	service.scan = func(actual context.Context) *v1.ScanNodesResponse {
		if actual.Err() != context.Canceled {
			t.Fatal("operation context lost")
		}
		return expected
	}
	response, err := service.ScanNodes(ctx, connect.NewRequest(&v1.ScanNodesRequest{}))
	if err != nil || !proto.Equal(response.Msg, expected) {
		t.Fatalf("shared response changed: %v,%v", response, err)
	}
}
