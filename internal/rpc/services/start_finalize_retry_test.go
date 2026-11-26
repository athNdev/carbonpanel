// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

// TestStartFinalizeRetryAfterAcknowledgedPublicationDoesNotRecreate exercises
// the normal StartServer path twice. The first lifecycle applies and publishes
// the staged file and persists the acknowledgement, but finalization cannot
// close the batch. The retry owes only metadata/cleanup; it must not recreate
// the container again.
func TestStartFinalizeRetryAfterAcknowledgedPublicationDoesNotRecreate(t *testing.T) {
	ctx := context.Background()
	var creates, starts atomic.Int32
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
		case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
			_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`))
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"replacement"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/replacement/start"):
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/wait"):
			_, _ = w.Write([]byte(`{"StatusCode":0}`))
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	svc, _ := newDockerPrivilegeTestService(t)
	_ = svc.docker.Close()
	client, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), svc.log)
	require.NoError(t, err)
	defer func() { _ = client.Close() }()
	svc.docker, svc.pool = client, nil
	ensureLocalTestNode(t, svc.store, "test-node")

	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	server := &storage.Server{
		ID: "finalize-retry", Name: "Finalize Retry", NodeID: "test-node",
		ContainerID: "original", Port: 25751, Memory: 1024,
		Status: storage.StatusRunning, DataPath: t.TempDir(), LastStarted: &started,
	}
	require.NoError(t, svc.store.CreateServer(ctx, server))
	require.NoError(t, svc.store.SaveServerConfig(ctx, svc.store.CreateDefaultServerConfig(server.ID)))

	stagePath := ".carbon-panel/staged/finalize/keep.txt"
	require.NoError(t, os.MkdirAll(filepath.Join(server.DataPath, filepath.FromSlash(".carbon-panel/staged/finalize")), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(server.DataPath, filepath.FromSlash(stagePath)), []byte("published"), 0o644))
	row, err := svc.store.AddStagedContent(ctx, &storage.StagedContentChange{
		ServerID: server.ID, Kind: storage.StagedKindFile, Action: storage.StagedActionInstall,
		TargetPath: "keep.txt", StagePath: stagePath, FileName: "keep.txt",
	})
	require.NoError(t, err)

	// Finalization fails after the lifecycle accepted the publication and the
	// recovery cleanup ran. That leaves an open batch with applied rows and a
	// persisted acknowledgement.
	require.NoError(t, svc.store.DB().Exec(
		`CREATE TRIGGER fail_batch_close BEFORE UPDATE OF status ON server_update_batches
		 WHEN NEW.status = 'applied' BEGIN SELECT RAISE(ABORT, 'batch close unavailable'); END`).Error)
	_, err = svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.Error(t, err)
	require.EqualValues(t, 1, creates.Load(), "the first lifecycle publishes once")
	require.EqualValues(t, 1, starts.Load())

	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.PublishedAt, "the accepted publication is durable")
	require.NotNil(t, open.SealedAt)
	applied, err := svc.store.CountBatchContentByStatus(ctx, row.BatchID, storage.StagedStatusApplied)
	require.NoError(t, err)
	require.EqualValues(t, 1, applied)
	require.FileExists(t, filepath.Join(server.DataPath, "keep.txt"))

	require.NoError(t, svc.store.DB().Exec(`DROP TRIGGER fail_batch_close`).Error)
	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 1, creates.Load(), "a finalize retry must not recreate the container")
	require.EqualValues(t, 2, starts.Load(), "the user-requested start still starts the existing container")

	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open, "the retry finalizes and closes the batch")
	body, err := os.ReadFile(filepath.Join(server.DataPath, "keep.txt"))
	require.NoError(t, err)
	require.Equal(t, "published", string(body))
}
