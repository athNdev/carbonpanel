// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

// newReviewedModServerService builds an UpdateServer fixture whose fake Docker
// API can complete a recreation, with one reviewed mod candidate staged against
// the concrete fabric/1.21.1 target.
func newReviewedModServerService(t *testing.T, id string, port int32) (*ServerService, *storage.Server) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = w.Write([]byte(`{"MemTotal":17179869184}`))
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			_, _ = w.Write([]byte(`{"Id":"sha256:fixture"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"replacement"}`))
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)

	svc, _ := newDockerPrivilegeTestService(t)
	svc.hostPortBound = func(int) bool { return false }
	if svc.docker != nil {
		_ = svc.docker.Close()
	}
	client, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), svc.log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	svc.docker, svc.pool = client, nil
	ensureLocalTestNode(t, svc.store, "test-node")

	ctx := context.Background()
	server := &storage.Server{
		ID: id, Name: id, NodeID: "test-node", Port: int(port), Memory: 1024,
		MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric,
		Status: storage.StatusStopped, DataPath: t.TempDir(),
	}
	require.NoError(t, svc.store.CreateServer(ctx, server))
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))
	_, err = svc.store.AddStagedContent(ctx, stagedReviewedModCandidate(server.ID))
	require.NoError(t, err)
	return svc, server
}

func stagedReviewedModCandidate(serverID string) *storage.StagedContentChange {
	return &storage.StagedContentChange{
		ServerID:           serverID,
		Kind:               storage.StagedKindMod,
		Action:             storage.StagedActionReplace,
		TargetPath:         "mods/mod.jar",
		StagePath:          ".carbon-panel/staged/" + serverID + "/mod.jar",
		FileName:           "mod.jar",
		ExpectedLiveSha512: strings.Repeat("ab", 64),
		ApprovedSha512:     strings.Repeat("cd", 64),
		ApprovedProvider:   "modrinth",
		ApprovedProjectID:  "project",
		ApprovedVersionID:  "release",
		ApprovedFilename:   "mod.jar",
	}
}

// TestUpdateServerRefusedRuntimeTargetMoveLeavesConsistentPair is the B2
// regression: a refused runtime-target move must not commit the server row
// before the config sync guard refuses, which left every later config read and
// lifecycle call failing with CodeInternal.
func TestUpdateServerRefusedRuntimeTargetMoveLeavesConsistentPair(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(serverID string) *v1.UpdateServerRequest
	}{
		{
			name: "minecraft version",
			build: func(serverID string) *v1.UpdateServerRequest {
				return &v1.UpdateServerRequest{Id: serverID, McVersion: "1.21.4"}
			},
		},
		{
			name: "mod loader",
			build: func(serverID string) *v1.UpdateServerRequest {
				return &v1.UpdateServerRequest{Id: serverID, ModLoader: "forge"}
			},
		},
		{
			name: "docker image",
			build: func(serverID string) *v1.UpdateServerRequest {
				return &v1.UpdateServerRequest{Id: serverID, DockerImage: "itzg/minecraft-bedrock-server"}
			},
		},
		{
			name: "docker override",
			build: func(serverID string) *v1.UpdateServerRequest {
				return &v1.UpdateServerRequest{Id: serverID, DockerOverrides: &v1.DockerOverrides{
					Environment: map[string]string{"VERSION": "1.21.4"},
				}}
			},
		},
		{
			name: "modpack",
			build: func(serverID string) *v1.UpdateServerRequest {
				return &v1.UpdateServerRequest{Id: serverID, ModpackId: "modrinth-runtime-target"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := ctxWithRole("operator")
			svc, server := newReviewedModServerService(t, "runtime-update-refused-"+strings.ReplaceAll(tc.name, " ", "-"), 25901)
			if tc.name == "modpack" {
				require.NoError(t, svc.store.DB().Create(&storage.IndexedModpack{
					ID: "modrinth-runtime-target", Indexer: "modrinth", IndexerID: "runtime-pack",
					Name: "Runtime Pack", GameVersions: `["1.21.1"]`,
				}).Error)
			}

			resp, err := svc.UpdateServer(ctx, connect.NewRequest(tc.build(server.ID)))
			require.Nil(t, resp)
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "%v", err)
			require.Contains(t, err.Error(), "apply or discard the staged reviewed mod update")
			require.Contains(t, err.Error(), "review and stage the mod update again")

			got, err := svc.store.GetServer(context.Background(), server.ID)
			require.NoError(t, err)
			require.Equal(t, "1.21.1", got.MCVersion, "a refused update must not save the server row")
			require.Equal(t, storage.ModLoaderFabric, got.ModLoader)
			require.Equal(t, server.DockerImage, got.DockerImage)
			cfg, err := svc.store.GetServerConfig(context.Background(), server.ID)
			require.NoError(t, err)
			require.Equal(t, "1.21.1", *cfg.Version, "a refused update must not save the config row")
			require.True(t, strings.EqualFold("fabric", *cfg.Type))
			require.Nil(t, cfg.CFPageURL)
			require.Nil(t, cfg.ModrinthModpack)

			// The read path stays usable for the consistent pair.
			configSvc := NewConfigService(svc.store, nil, nil, nil, logger.New())
			_, err = configSvc.GetServerConfig(ctx, connect.NewRequest(&v1.GetServerConfigRequest{ServerId: server.ID}))
			require.NoError(t, err, "GetServerConfig must still succeed after a refused target move")
			_, err = svc.StopServer(ctx, connect.NewRequest(&v1.StopServerRequest{Id: server.ID}))
			require.NoError(t, err, "StopServer must still succeed after a refused target move")
		})
	}
}

func TestUpdateServerRuntimeTargetMoveSucceedsAfterDiscard(t *testing.T) {
	ctx := ctxWithRole("operator")
	svc, server := newReviewedModServerService(t, "runtime-update-discard", 25902)
	rows, err := svc.store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	_, err = svc.store.DiscardStagedContent(ctx, rows[0].ID)
	require.NoError(t, err)

	resp, err := svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, McVersion: "1.21.4",
	}))
	require.NoError(t, err, "the same update succeeds once no reviewed candidate blocks it")
	require.Equal(t, "1.21.4", resp.Msg.GetServer().GetMcVersion())

	got, err := svc.store.GetServer(context.Background(), server.ID)
	require.NoError(t, err)
	require.Equal(t, "1.21.4", got.MCVersion)
	cfg, err := svc.store.GetServerConfig(context.Background(), server.ID)
	require.NoError(t, err)
	require.Equal(t, got.MCVersion, *cfg.Version)
	require.Equal(t, string(storage.NormalizeModLoaderForType(got.ModLoader)), *cfg.Type)
	effectiveLoader, effectiveVersion, err := cfg.EffectiveRuntimeTarget(got)
	require.NoError(t, err)
	require.Equal(t, storage.ModLoaderFabric, effectiveLoader)
	require.Equal(t, "1.21.4", effectiveVersion, "the effective target must move once the candidate is gone")
}

func TestUpdateServerNonRuntimeChangeAllowedWithReviewedModStaged(t *testing.T) {
	ctx := ctxWithRole("operator")
	svc, server := newReviewedModServerService(t, "runtime-update-nonruntime", 25903)

	resp, err := svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, Name: "renamed", Memory: 2048,
	}))
	require.NoError(t, err, "a non-runtime update must not be blocked by a staged reviewed mod")
	require.Equal(t, "renamed", resp.Msg.GetServer().GetName())

	got, err := svc.store.GetServer(context.Background(), server.ID)
	require.NoError(t, err)
	require.Equal(t, "renamed", got.Name)
	require.Equal(t, 2048, got.Memory)
	require.Equal(t, "1.21.1", got.MCVersion, "the runtime target must not move")
	cfg, err := svc.store.GetServerConfig(context.Background(), server.ID)
	require.NoError(t, err)
	require.Equal(t, "1.21.1", *cfg.Version)

	// The staged candidate is still publishable against the unchanged target.
	rows, err := svc.store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedStatusStaged, rows[0].Status)
}

// TestPublicationStillRefusesMovedTargetThroughStartRPC proves the A invariant
// survives the UpdateServer preflight: an out-of-band durable target move is
// still refused at publication, before any live write.
func TestPublicationStillRefusesMovedTargetThroughStartRPC(t *testing.T) {
	ctx := ctxWithRole("operator")
	svc, server := newReviewedModServerService(t, "runtime-publication-moved", 25904)
	stagedPath := filepath.Join(server.DataPath, ".carbon-panel", "staged", server.ID, "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(stagedPath), 0o700))
	require.NoError(t, os.WriteFile(stagedPath, []byte("candidate"), 0o600))

	// A writer that bypasses the store guard still cannot publish the reviewed
	// candidate against the moved target.
	require.NoError(t, svc.store.DB().Model(&storage.ServerConfig{}).
		Where("server_id = ?", server.ID).Update("version", "1.21.4").Error)

	_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "%v", err)
	require.ErrorIs(t, err, storage.ErrStagedModRuntimeTargetChanged)
	require.Contains(t, err.Error(), "review and stage the update again")

	live, err := os.ReadFile(filepath.Join(server.DataPath, "mods", "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "installed", string(live), "a moved target must preserve live bytes")
}
