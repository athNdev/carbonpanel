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
	"github.com/athNdev/carbon-panel/internal/staging"
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

// TestStartPreDispatchInterruptedNoContainerResumesStandardCreation simulates a
// crash after the staged files and config are on the live tree but before any
// Docker dispatch, for a server that has no container yet. The retry must not
// ask for identity reconciliation; it resumes ordinary creation, records the
// created identity, starts the server, and acknowledges the batch once.
func TestStartPreDispatchInterruptedNoContainerResumesStandardCreation(t *testing.T) {
	ctx := context.Background()
	var creates, starts atomic.Int32
	svc, server := newStagedLifecycleTestServer(t, "pre-dispatch-create", time.Now().Add(-time.Hour).Truncate(time.Second))
	require.NoError(t, svc.store.UpdateServerFields(ctx, server.ID, map[string]any{
		"container_id": "", "status": string(storage.StatusStopped),
	}))
	server.ContainerID = ""
	server.Status = storage.StatusStopped
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
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"created-container"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/created-container/start"):
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

	// The prepared snapshot and live writes exist; no dispatch intent does.
	commit, err := staging.CommitNextRestartFiles(ctx, svc.store, server, nil)
	require.NoError(t, err)
	require.NotNil(t, commit)
	_, err = commit.ApplyConfigs(ctx, svc.store)
	require.NoError(t, err)
	require.False(t, commit.NeedsIdentityVerification())
	commit.Finish() // crash before any Docker call

	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationApplying, record.Phase)
	require.Empty(t, record.ExpectedContainerID)

	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 1, creates.Load(), "the retry resumes standard creation")
	require.EqualValues(t, 1, starts.Load())
	stored, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "created-container", stored.ContainerID)
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
	record, err = svc.store.GetUpdatePublication(ctx, record.BatchID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationCleaned, record.Phase)
}

// TestStartUnrelatedCanonicalDriftRefusesAcknowledgement simulates a recorded
// dispatch whose expected replacement is known, while the daemon resolves an
// unrelated canonical container. Adoption may repair the tracked id, but it is
// not publication evidence: the batch stays sealed and no cleanup or start runs.
func TestStartUnrelatedCanonicalDriftRefusesAcknowledgement(t *testing.T) {
	ctx := context.Background()
	var creates, starts atomic.Int32
	svc, server := newStagedLifecycleTestServer(t, "drift-refused", time.Now().Add(-time.Hour).Truncate(time.Second))
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
			_, _ = w.Write([]byte(`[{"Id":"drift-container","Names":["/carbon-panel-server-drift-refused"],"State":"running"}]`))
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"unexpected"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/drift-container/start"):
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	stageMixedNextRestartRows(t, svc, server)

	commit, err := staging.CommitNextRestartFiles(ctx, svc.store, server, nil)
	require.NoError(t, err)
	applied, err := commit.ApplyConfigs(ctx, svc.store)
	require.NoError(t, err)
	first := &staging.RestartPublication{Commit: commit, Configs: applied, RuntimeAttempted: true}
	require.NoError(t, first.BeginDispatch(ctx))
	require.NoError(t, first.RecordReplacement(ctx, "expected-replacement"))
	commit.Finish() // crash before the runtime outcome was verified
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	recovery := filepath.Join(server.DataPath, ".carbon-panel", "undo", open.ID)
	require.DirExists(t, recovery)

	_, err = svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.ErrorContains(t, err, "not the recorded replacement")
	require.EqualValues(t, 0, creates.Load())
	require.EqualValues(t, 0, starts.Load(), "drift must not start or recreate the runtime")
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open.SealedAt)
	require.Nil(t, open.PublishedAt, "drift is identity repair, not an acknowledgement")
	require.DirExists(t, recovery, "a refused acknowledgement must retain recovery")
	require.FileExists(t, filepath.Join(server.DataPath, "mixed.txt"))
	stored, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "drift-container", stored.ContainerID, "adoption repairs the tracked id")
}

// TestStartAcknowledgementPersistenceFailureRetainsRecoveryThenFinalizes
// exercises the normal StartServer entry twice. The runtime accepts the
// replacement but the acknowledgement cannot persist; the batch must stay
// sealed with its recovery tree and must not close. Once persistence recovers,
// the retry verifies the recorded replacement, finalizes and still starts.
func TestStartAcknowledgementPersistenceFailureRetainsRecoveryThenFinalizes(t *testing.T) {
	ctx := context.Background()
	var creates, starts atomic.Int32
	created := atomic.Bool{}
	svc, server := newStagedLifecycleTestServer(t, "ack-persist-retry", time.Now().Add(-time.Hour).Truncate(time.Second))
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
				_, _ = w.Write([]byte(`[{"Id":"replacement","Names":["/carbon-panel-server-ack-persist-retry"],"State":"running"}]`))
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
	stageMixedNextRestartRows(t, svc, server)
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	recovery := filepath.Join(server.DataPath, ".carbon-panel", "undo", open.ID)

	require.NoError(t, svc.store.DB().Exec(
		`CREATE TRIGGER fail_published_at BEFORE UPDATE OF published_at ON server_update_batches
		 WHEN NEW.published_at IS NOT NULL BEGIN SELECT RAISE(ABORT, 'publication ack unavailable'); END`).Error)
	_, err = svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.ErrorContains(t, err, "publication ack unavailable")
	require.EqualValues(t, 1, creates.Load())
	require.EqualValues(t, 1, starts.Load())
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open, "a failed acknowledgement must not close the batch")
	require.NotNil(t, open.SealedAt)
	require.Nil(t, open.PublishedAt)
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, record.Phase)
	require.DirExists(t, recovery, "a failed acknowledgement must retain recovery")

	require.NoError(t, svc.store.DB().Exec(`DROP TRIGGER fail_published_at`).Error)
	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 1, creates.Load(), "the verified replacement must not be recreated")
	require.EqualValues(t, 2, starts.Load(), "the user-requested start still runs")
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
	require.NoDirExists(t, recovery)
}
