// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

// serveRecreateFixture models a daemon that supports the full replace ladder:
// inspect a running container, stop and rename it, create the replacement, and
// start it. Created identities are distinct so a replay cannot masquerade as
// the baseline container.
func serveRecreateFixture(t *testing.T, svc *ServerService, creates, starts, unpauses *atomic.Int32) {
	t.Helper()
	serveRestartAPI(t, svc, func(w http.ResponseWriter, r *http.Request) {
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
		case strings.Contains(r.URL.Path, "/containers/") && strings.HasSuffix(r.URL.Path, "/json"):
			id := path.Base(strings.TrimSuffix(r.URL.Path, "/json"))
			if id != "original" && !strings.HasPrefix(id, "replacement-") {
				// Only the tracked identities exist. A name lookup for a
				// missing container must not invent a duplicate to adopt.
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":    id,
				"Name":  "/" + id,
				"State": map[string]any{"Running": true, "Status": "running"},
			})
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			id := fmt.Sprintf("replacement-%d", creates.Add(1))
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"Id":%q}`, id)
		case strings.HasSuffix(r.URL.Path, "/unpause"):
			unpauses.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/start"):
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/wait"):
			_, _ = w.Write([]byte(`{"StatusCode":0}`))
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
}

func requireOpenRetryableBatch(t *testing.T, svc *ServerService, serverID string) *storage.ServerUpdateBatch {
	t.Helper()
	ctx := context.Background()
	open, err := svc.store.OpenUpdateBatch(ctx, serverID)
	require.NoError(t, err)
	require.NotNil(t, open, "a suppressed replay must leave the batch open")
	require.Nil(t, open.SealedAt, "a suppressed replay must not seal fresh changes")
	require.Nil(t, open.PublishedAt, "a suppressed replay must not acknowledge the batch")
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.NotEqual(t, storage.PublicationDispatched, record.Phase, "a suppressed replay must not record a dispatch")
	require.Empty(t, record.ExpectedContainerID, "a suppressed replay must not record a replacement")
	return open
}

// A normal paused server is unpaused once and restarted in place.
func TestRestartPausedServerUnpausesAndRestarts(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "paused-normal")
	var creates, starts, unpauses atomic.Int32
	serveRecreateFixture(t, svc, &creates, &starts, &unpauses)
	require.NoError(t, svc.store.UpdateServerStatus(ctx, server.ID, storage.StatusPaused))
	stageRestartTestFile(t, svc, server, "paused.txt", "paused")

	response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "paused-normal"}))
	require.NoError(t, err)
	require.Equal(t, "restarting", response.Msg.GetStatus())
	require.EqualValues(t, 1, unpauses.Load(), "a paused server is unpaused exactly once")
	require.EqualValues(t, 1, starts.Load())
	require.Zero(t, creates.Load(), "an in-place restart must not replace the container")
	require.FileExists(t, filepath.Join(server.DataPath, "paused.txt"))
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

// A completed request ID replayed with fresh content on a paused server must
// not unpause the container, restage the fresh row, keep the batch retryable
// and publish nothing until a new request ID is used.
func TestRestartPausedServerCompletedReplayDoesNotUnpauseOrDispatch(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "paused-completed")
	var creates, starts, unpauses atomic.Int32
	serveRecreateFixture(t, svc, &creates, &starts, &unpauses)
	first := stageRestartTestFile(t, svc, server, "first.txt", "first")
	require.NoError(t, svc.store.UpdateServerStatus(ctx, server.ID, storage.StatusPaused))

	_, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "paused-request"}))
	require.NoError(t, err)
	require.EqualValues(t, 1, unpauses.Load())
	require.EqualValues(t, 1, starts.Load())
	require.FileExists(t, filepath.Join(server.DataPath, first.TargetPath))

	require.NoError(t, svc.store.UpdateServerStatus(ctx, server.ID, storage.StatusPaused))
	fresh := stageRestartTestFile(t, svc, server, "fresh.txt", "fresh")

	_, err = svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "paused-request"}))
	require.ErrorContains(t, err, "already completed")
	require.EqualValues(t, 1, unpauses.Load(), "a completed request ID must not unpause the container")
	require.EqualValues(t, 1, starts.Load(), "a completed request ID must not restart the container")
	require.Zero(t, creates.Load())

	stored, err := svc.store.GetStagedContent(ctx, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, stored.Status, "the fresh row must be restaged")
	require.NoFileExists(t, filepath.Join(server.DataPath, "fresh.txt"))
	requireOpenRetryableBatch(t, svc, server.ID)

	response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "paused-request-new"}))
	require.NoError(t, err)
	require.Equal(t, "restarting", response.Msg.GetStatus())
	require.EqualValues(t, 2, unpauses.Load())
	require.EqualValues(t, 2, starts.Load())
	require.FileExists(t, filepath.Join(server.DataPath, "fresh.txt"))
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open, "a new request ID publishes the retained batch")
}

// A completed StartServer request replayed with fresh content must not replace
// the running container or publish the fresh row.
func TestStartCompletedRequestReplayDoesNotRecreateFreshContent(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "start-replay-content")
	var creates, starts, unpauses atomic.Int32
	serveRecreateFixture(t, svc, &creates, &starts, &unpauses)
	first := stageRestartTestFile(t, svc, server, "first.txt", "first")

	_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-replay"}))
	require.NoError(t, err)
	require.EqualValues(t, 1, creates.Load())
	require.EqualValues(t, 1, starts.Load())
	require.FileExists(t, filepath.Join(server.DataPath, first.TargetPath))

	fresh := stageRestartTestFile(t, svc, server, "fresh.txt", "fresh")
	_, err = svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-replay"}))
	require.Error(t, err)
	require.EqualValues(t, 1, creates.Load(), "a completed request ID must not create a replacement")
	require.EqualValues(t, 1, starts.Load(), "a completed request ID must not start a replacement")

	stored, err := svc.store.GetStagedContent(ctx, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, stored.Status)
	require.NoFileExists(t, filepath.Join(server.DataPath, "fresh.txt"))
	requireOpenRetryableBatch(t, svc, server.ID)

	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-replay-new"}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 2, creates.Load())
	require.EqualValues(t, 2, starts.Load())
	require.FileExists(t, filepath.Join(server.DataPath, "fresh.txt"))
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

// A completed StartServer request replayed after the tracked container ID was
// lost must not create or start a container for freshly applied rows.
func TestStartCompletedRequestReplayDoesNotCreateFreshContent(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "start-replay-create")
	var creates, starts, unpauses atomic.Int32
	serveRecreateFixture(t, svc, &creates, &starts, &unpauses)
	require.NoError(t, svc.store.UpdateServerFields(ctx, server.ID, map[string]any{
		"container_id": "", "status": string(storage.StatusStopped),
	}))
	server.ContainerID = ""
	server.Status = storage.StatusStopped
	first := stageRestartTestFile(t, svc, server, "first.txt", "first")

	_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-create-replay"}))
	require.NoError(t, err)
	require.EqualValues(t, 1, creates.Load())
	require.EqualValues(t, 1, starts.Load())
	require.FileExists(t, filepath.Join(server.DataPath, first.TargetPath))

	// The panel lost the tracked identity again; the canonical lookup finds
	// nothing, so the create path is the only way a replay could mutate Docker.
	require.NoError(t, svc.store.UpdateServerFields(ctx, server.ID, map[string]any{"container_id": ""}))
	fresh := stageRestartTestFile(t, svc, server, "fresh.txt", "fresh")
	_, err = svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-create-replay"}))
	require.Error(t, err)
	require.EqualValues(t, 1, creates.Load(), "a completed request ID must not create a container")
	require.EqualValues(t, 1, starts.Load(), "a completed request ID must not start a container")

	stored, err := svc.store.GetStagedContent(ctx, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, stored.Status)
	require.NoFileExists(t, filepath.Join(server.DataPath, "fresh.txt"))
	requireOpenRetryableBatch(t, svc, server.ID)

	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-create-replay-new"}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 2, creates.Load())
	require.EqualValues(t, 2, starts.Load())
	require.FileExists(t, filepath.Join(server.DataPath, "fresh.txt"))
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

// A completed RestartServer request replayed with fresh config must not replace
// the container or apply the fresh config to the live tree.
func TestRestartCompletedRequestReplayDoesNotRecreateFreshConfig(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "restart-replay-config")
	var creates, starts, unpauses atomic.Int32
	serveRecreateFixture(t, svc, &creates, &starts, &unpauses)
	_, err := svc.store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "first"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)

	_, err = svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "restart-config-replay"}))
	require.NoError(t, err)
	require.EqualValues(t, 1, creates.Load())
	require.EqualValues(t, 1, starts.Load())

	fresh, err := svc.store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "fresh"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	_, err = svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "restart-config-replay"}))
	require.Error(t, err)
	require.EqualValues(t, 1, creates.Load(), "a completed request ID must not create a replacement")
	require.EqualValues(t, 1, starts.Load(), "a completed request ID must not start a replacement")

	stored, err := svc.store.GetStagedConfigChange(ctx, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, stored.Status, "the fresh config must be restaged")
	config, err := svc.store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, config.MOTD)
	require.Equal(t, "first", *config.MOTD, "the fresh config must not reach the live config")
	requireOpenRetryableBatch(t, svc, server.ID)

	response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "restart-config-replay-new"}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 2, creates.Load())
	require.EqualValues(t, 2, starts.Load())
	config, err = svc.store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, config.MOTD)
	require.Equal(t, "fresh", *config.MOTD)
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}
