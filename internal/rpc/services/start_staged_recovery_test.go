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

// stageMixedNextRestartRows creates the content and config of one
// next-restart batch through the normal admission path.
func stageMixedNextRestartRows(t *testing.T, svc *ServerService, server *storage.Server) {
	t.Helper()
	ctx := context.Background()
	_, err := svc.store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "mixed"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	stagePath := ".carbon-panel/staged/mixed/mixed.txt"
	require.NoError(t, os.MkdirAll(filepath.Join(server.DataPath, filepath.FromSlash(".carbon-panel/staged/mixed")), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(server.DataPath, filepath.FromSlash(stagePath)), []byte("mixed"), 0o644))
	_, err = svc.store.AddStagedContent(ctx, &storage.StagedContentChange{
		ServerID: server.ID, Kind: storage.StagedKindFile, Action: storage.StagedActionInstall,
		TargetPath: "mixed.txt", StagePath: stagePath, FileName: "mixed.txt",
	})
	require.NoError(t, err)
}

func newStagedLifecycleTestServer(t *testing.T, id string, started time.Time) (*ServerService, *storage.Server) {
	t.Helper()
	svc, _ := newDockerPrivilegeTestService(t)
	_ = svc.docker.Close()
	ensureLocalTestNode(t, svc.store, "test-node")
	server := &storage.Server{
		ID: id, Name: id, NodeID: "test-node",
		ContainerID: "original", Port: 25751, Memory: 1024,
		Status: storage.StatusRunning, DataPath: t.TempDir(), LastStarted: &started,
	}
	require.NoError(t, svc.store.CreateServer(context.Background(), server))
	require.NoError(t, svc.store.SaveServerConfig(context.Background(), svc.store.CreateDefaultServerConfig(server.ID)))
	return svc, server
}

func attachDockerAPI(t *testing.T, svc *ServerService, handler http.HandlerFunc) {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	client, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), svc.log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	svc.docker, svc.pool = client, nil
}

// TestStartAcknowledgedMixedRetryDoesNotRecreate reproduces an acknowledged
// content-plus-config batch whose PersistStarted failed. The acknowledgement is
// valid (the runtime accepted the batch) but LastStarted is still old. The retry
// must finalize the batch and perform the user-requested start without treating
// the already applied owned config as new staged work.
func TestStartAcknowledgedMixedRetryDoesNotRecreate(t *testing.T) {
	ctx := context.Background()
	var creates, starts atomic.Int32
	svc, server := newStagedLifecycleTestServer(t, "ack-mixed-retry", time.Now().Add(-time.Hour).Truncate(time.Second))
	attachDockerAPI(t, svc, func(w http.ResponseWriter, r *http.Request) {
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
	})
	stageMixedNextRestartRows(t, svc, server)
	started := *server.LastStarted

	require.NoError(t, svc.store.DB().Exec(
		`CREATE TRIGGER fail_starting BEFORE UPDATE OF status ON servers
		 WHEN NEW.status = 'starting' BEGIN SELECT RAISE(ABORT, 'status save unavailable'); END`).Error)
	_, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.Error(t, err)
	require.EqualValues(t, 1, creates.Load())
	require.EqualValues(t, 1, starts.Load())
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.PublishedAt, "the accepted publication was acknowledged")
	stored, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.True(t, stored.LastStarted.Equal(started), "the failed status save left LastStarted old")

	require.NoError(t, svc.store.DB().Exec(`DROP TRIGGER fail_starting`).Error)
	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 1, creates.Load(), "an acknowledged batch must not be published again")
	require.EqualValues(t, 2, starts.Load(), "the user-requested start still runs")
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
	stored, err = svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.True(t, stored.LastStarted.After(started))
	body, err := os.ReadFile(filepath.Join(server.DataPath, "mixed.txt"))
	require.NoError(t, err)
	require.Equal(t, "mixed", string(body))
}

// TestStartIdentityPersistenceFailureIsNotAcknowledged reproduces a verified
// Docker replacement whose new ID could not be saved. The batch must stay sealed
// and unacknowledged; the retry reconciles the canonical container, records it,
// and then finalizes instead of starting or recreating against the stale ID.
func TestStartIdentityPersistenceFailureIsNotAcknowledged(t *testing.T) {
	ctx := context.Background()
	var creates, starts atomic.Int32
	created := atomic.Bool{}
	svc, server := newStagedLifecycleTestServer(t, "identity-retry", time.Now().Add(-time.Hour).Truncate(time.Second))
	attachDockerAPI(t, svc, func(w http.ResponseWriter, r *http.Request) {
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
			if created.Load() {
				_, _ = w.Write([]byte(`[{"Id":"replacement","Names":["/carbon-panel-server-identity-retry"],"State":"running"}]`))
				return
			}
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
			_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`))
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates.Add(1)
			created.Store(true)
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
	})
	_, err := svc.store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "applied"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)
	started := *server.LastStarted

	require.NoError(t, svc.store.DB().Exec(
		`CREATE TRIGGER fail_identity BEFORE UPDATE OF container_id ON servers
		 WHEN NEW.container_id = 'replacement' BEGIN SELECT RAISE(ABORT, 'identity save unavailable'); END`).Error)
	_, err = svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.ErrorContains(t, err, "ID could not be saved")
	require.EqualValues(t, 1, creates.Load())
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt)
	require.Nil(t, open.PublishedAt, "a replacement without a persisted identity is not an acknowledgement")
	stored, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "original", stored.ContainerID)
	require.True(t, stored.LastStarted.Equal(started))

	require.NoError(t, svc.store.DB().Exec(`DROP TRIGGER fail_identity`).Error)
	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 1, creates.Load(), "the reconciled replacement must not be recreated")
	require.EqualValues(t, 2, starts.Load(), "the user-requested start runs on the reconciled container")
	stored, err = svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "replacement", stored.ContainerID)
	require.True(t, stored.LastStarted.After(started))
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}
