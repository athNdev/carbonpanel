// SPDX-License-Identifier: MPL-2.0

package services

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/activity"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestBindWorldRequestReplayRequiresOriginalRevision(t *testing.T) {
	for _, mode := range []string{"new", "legacy"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			store := newWorldRPCStore(t)
			root := t.TempDir()
			libraryRoot := filepath.Join(root, "library")
			server, original := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
			other := *original
			other.SHA256 = strings.Repeat("b", 64)
			other.ID = worldlib.RevisionID(other.SHA256)
			if err := store.PutWorldRevision(ctx, &other); err != nil {
				t.Fatal(err)
			}
			svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
			bind := func(revision, key string) (*connect.Response[v1.BindWorldResponse], error) {
				return svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
					ServerId: server.ID, LevelName: "world", RevisionId: revision, RequestId: key,
				}))
			}
			first, err := bind(original.ID, "key-a")
			if err != nil || first.Msg.Outcome != "success" {
				t.Fatalf("first bind: %+v, %v", first, err)
			}
			if mode == "legacy" {
				if err := store.DB().Model(&storage.ServerOperation{}).
					Where("id = ?", first.Msg.OperationId).
					UpdateColumn("payload_hash", operation.Digest("world-bind", server)).Error; err != nil {
					t.Fatal(err)
				}
			}
			replay, err := bind(original.ID, "key-a")
			if err != nil || replay.Msg.OperationId != first.Msg.OperationId {
				t.Fatalf("same bind replay: %+v, %v", replay, err)
			}
			if response, err := bind(other.ID, "key-a"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("changed revision replay = %+v, %v", response, err)
			}
			if response, err := bind(other.ID, "key-b"); err != nil || response.Msg.Outcome != "success" {
				t.Fatalf("valid second bind = %+v, %v", response, err)
			}
			if response, err := bind(original.ID, "key-a"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("replay after supersession = %+v, %v", response, err)
			}
			if response, err := bind(other.ID, "key-a"); connect.CodeOf(err) != connect.CodeFailedPrecondition {
				t.Fatalf("key from another bind against current revision = %+v, %v", response, err)
			}
			binding, err := store.GetWorldBinding(ctx, server.ID, "world")
			if err != nil || binding.RevisionID != other.ID {
				t.Fatalf("binding changed after refused replays: %+v, %v", binding, err)
			}
			if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "key-a"); got != 1 {
				t.Fatalf("first request produced %d success events, want 1", got)
			}
		})
	}
}
