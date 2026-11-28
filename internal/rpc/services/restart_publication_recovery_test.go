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

func stageRestartTestFile(t *testing.T, svc *ServerService, server *storage.Server, name, body string) *storage.StagedContentChange {
	t.Helper()
	stagePath := ".carbon-panel/staged/" + name + "/" + name
	require.NoError(t, os.MkdirAll(filepath.Join(server.DataPath, filepath.FromSlash(filepath.Dir(stagePath))), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(server.DataPath, filepath.FromSlash(stagePath)), []byte(body), 0o644))
	row, err := svc.store.AddStagedContent(context.Background(), &storage.StagedContentChange{
		ServerID: server.ID, Kind: storage.StagedKindFile, Action: storage.StagedActionInstall,
		TargetPath: name, StagePath: stagePath, FileName: name,
	})
	require.NoError(t, err)
	return row
}

func newRestartPublicationTestServer(t *testing.T, id string) (*ServerService, *storage.Server) {
	t.Helper()
	svc, _ := newDockerPrivilegeTestService(t)
	_ = svc.docker.Close()
	ensureLocalTestNode(t, svc.store, "test-node")
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	server := &storage.Server{
		ID: id, Name: id, NodeID: "test-node", ContainerID: "original",
		Port: 25751, Memory: 1024, Status: storage.StatusRunning,
		DataPath: t.TempDir(), LastStarted: &started,
	}
	require.NoError(t, svc.store.CreateServer(context.Background(), server))
	require.NoError(t, svc.store.SaveServerConfig(context.Background(), svc.store.CreateDefaultServerConfig(server.ID)))
	return svc, server
}

func serveRestartAPI(t *testing.T, svc *ServerService, handler http.HandlerFunc) {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	client, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), svc.log)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	svc.docker, svc.pool = client, nil
}

// A direct in-place restart that fails before Docker touches the container and
// a fallback recreation that fails its typed preflight prove that neither
// attempt mutated the runtime. The staged file must be restaged, not sealed
// into an uncertain publication.
func TestRestartInPlacePreflightThenFallbackPreflightCompensates(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "inplace-preflight")
	row := stageRestartTestFile(t, svc, server, "staged.txt", "new")
	serveRestartAPI(t, svc, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = w.Write([]byte(`{"MemTotal":17179869184}`))
		case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/images/create"):
			http.Error(w, `{"message":"registry unavailable"}`, http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[]`))
		case strings.HasSuffix(r.URL.Path, "/containers/original/stop"):
			http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	_, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "inplace-preflight"}))
	require.Error(t, err)

	got, err := svc.store.GetStagedContent(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, got.Status, "a fully preflight attempt must restage its row")
	require.NoFileExists(t, filepath.Join(server.DataPath, "staged.txt"))
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Nil(t, open.SealedAt, "full compensation releases admission")
	require.Nil(t, open.PublishedAt, "a failed attempt must not be acknowledged")
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.NotEqual(t, storage.PublicationDispatched, record.Phase)
}

// A persistence failure after the replacement was created must not remove the
// only container that may already contain the staged bytes. The recorded exact
// identity is what lets a retry reconcile that container and finalize.
func TestRestartCreatePersistenceFailureRetainsContainerAndRetryFinalizes(t *testing.T) {
	ctx := context.Background()
	svc, _ := newDockerPrivilegeTestService(t)
	_ = svc.docker.Close()
	ensureLocalTestNode(t, svc.store, "test-node")
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	server := &storage.Server{
		ID: "create-persist", Name: "create-persist", NodeID: "test-node",
		Port: 25751, Memory: 1024, Status: storage.StatusRunning,
		DataPath: t.TempDir(), LastStarted: &started,
	}
	require.NoError(t, svc.store.CreateServer(ctx, server))
	require.NoError(t, svc.store.SaveServerConfig(ctx, svc.store.CreateDefaultServerConfig(server.ID)))
	cfg, err := svc.store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "retained"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)

	var creates, deletes atomic.Int32
	var created atomic.Bool
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
			if created.Load() {
				_, _ = w.Write([]byte(`[{"Id":"created","Names":["/carbon-panel-server-create-persist"],"Labels":{"carbon-panel.server.id":"create-persist"}}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates.Add(1)
			created.Store(true)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"created"}`))
		case r.Method == http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	require.NoError(t, svc.store.DB().Exec(
		`CREATE TRIGGER fail_container_identity BEFORE UPDATE OF container_id ON servers
		 WHEN NEW.container_id = 'created' BEGIN SELECT RAISE(ABORT, 'container id unavailable'); END`).Error)
	_, err = svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "create-persist"}))
	require.Error(t, err)
	require.EqualValues(t, 1, creates.Load())
	require.Zero(t, deletes.Load(), "the created container must be retained on a persistence failure")

	row, err := svc.store.GetStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusApplied, row.Status)
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt, "a persistence failure retains the batch for identity verification")
	require.Nil(t, open.PublishedAt)
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, record.Phase)
	require.Equal(t, "created", record.ExpectedContainerID)

	require.NoError(t, svc.store.DB().Exec(`DROP TRIGGER fail_container_identity`).Error)
	response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "create-persist-retry"}))
	require.NoError(t, err)
	require.Equal(t, "restarting", response.Msg.GetStatus())
	require.EqualValues(t, 1, creates.Load(), "the retry must verify the recorded replacement, not create another")
	require.Zero(t, deletes.Load())

	stored, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "created", stored.ContainerID)
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open, "the identity-verifying retry finalizes the batch")
}

// A persistence failure after StartServer created the replacement must retain
// the only container that may already contain the staged bytes. The recorded
// exact identity is what lets a retry reconcile that container and finalize.
func TestStartCreatePersistenceFailureRetainsContainerAndRetryFinalizes(t *testing.T) {
	ctx := context.Background()
	svc, _ := newDockerPrivilegeTestService(t)
	_ = svc.docker.Close()
	ensureLocalTestNode(t, svc.store, "test-node")
	started := time.Now().Add(-time.Hour).Truncate(time.Second)
	server := &storage.Server{
		ID: "start-create-persist", Name: "start-create-persist", NodeID: "test-node",
		Port: 25752, Memory: 1024, Status: storage.StatusStopped,
		DataPath: t.TempDir(), LastStarted: &started,
	}
	require.NoError(t, svc.store.CreateServer(ctx, server))
	require.NoError(t, svc.store.SaveServerConfig(ctx, svc.store.CreateDefaultServerConfig(server.ID)))
	cfg, err := svc.store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "retained"},
		storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)

	var creates, deletes atomic.Int32
	var created atomic.Bool
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
			if created.Load() {
				_, _ = w.Write([]byte(`[{"Id":"created","Names":["/carbon-panel-server-start-create-persist"],"Labels":{"carbon-panel.server.id":"start-create-persist"}}]`))
			} else {
				_, _ = w.Write([]byte(`[]`))
			}
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates.Add(1)
			created.Store(true)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"created"}`))
		case r.Method == http.MethodDelete:
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	require.NoError(t, svc.store.DB().Exec(
		`CREATE TRIGGER fail_start_container_identity BEFORE UPDATE OF container_id ON servers
		 WHEN NEW.container_id = 'created' BEGIN SELECT RAISE(ABORT, 'container id unavailable'); END`).Error)
	_, err = svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-create-persist"}))
	require.Error(t, err)
	require.EqualValues(t, 1, creates.Load())
	require.Zero(t, deletes.Load(), "the created container must be retained on a persistence failure")

	row, err := svc.store.GetStagedConfigChange(ctx, cfg.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusApplied, row.Status)
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt, "a persistence failure retains the batch for identity verification")
	require.Nil(t, open.PublishedAt)
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, record.Phase)
	require.Equal(t, "created", record.ExpectedContainerID)

	require.NoError(t, svc.store.DB().Exec(`DROP TRIGGER fail_start_container_identity`).Error)
	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-create-persist-retry"}))
	require.NoError(t, err)
	require.Equal(t, "starting", response.Msg.GetStatus())
	require.EqualValues(t, 1, creates.Load(), "the retry must verify the recorded replacement, not create another")
	require.Zero(t, deletes.Load())

	stored, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, "created", stored.ContainerID)
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open, "the identity-verifying retry finalizes the batch")
}

// An interruption after a completed restart unpause leaves the database PAUSED
// over a container that is already running. This recovery covers the case with
// no dispatched publication: the completed request ID stays refused, but a new
// request ID observes the live container, records RUNNING and restarts exactly
// once instead of failing the unpause.
func TestRestartUnpauseCrashStateWithNoDispatchedPublicationRecoversWithNewRequest(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "unpause-crash")
	var starts, stops, unpauses atomic.Int32
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
		case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
			_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`))
		case strings.HasSuffix(r.URL.Path, "/unpause"):
			unpauses.Add(1)
			http.Error(w, `{"message":"Container is not paused"}`, http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stops.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/start"):
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	op, _, err := svc.store.BeginServerOperation(ctx, server.ID, "test-node", "original",
		"unpause-crash:restart_unpause", "restart_unpause", "digest")
	require.NoError(t, err)
	require.NoError(t, svc.store.AdvanceServerOperation(ctx, op.ID, storage.ServerOperationPrepared, storage.ServerOperationExecuting, ""))
	require.NoError(t, svc.store.AdvanceServerOperation(ctx, op.ID, storage.ServerOperationExecuting, storage.ServerOperationComplete, ""))
	require.NoError(t, svc.store.UpdateServerStatus(ctx, server.ID, storage.StatusPaused))

	_, err = svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "unpause-crash"}))
	require.ErrorContains(t, err, "already completed")
	require.Zero(t, unpauses.Load(), "a completed request ID must not attempt the unpause")
	require.Zero(t, stops.Load())
	require.Zero(t, starts.Load())

	response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "unpause-crash-retry"}))
	require.NoError(t, err)
	require.Equal(t, "restarting", response.Msg.GetStatus())
	require.EqualValues(t, 1, unpauses.Load(), "the retry attempts the unpause once")
	require.EqualValues(t, 1, stops.Load(), "the retry restarts the running container exactly once")
	require.EqualValues(t, 1, starts.Load())

	stored, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StatusStarting, stored.Status)
}

// A successful unpause whose RUNNING status cannot be persisted fails closed:
// it must not claim success and must not restart under a still-PAUSED intent.
// With no dispatched publication, dropping the persistence failure lets a new
// request ID recover the running container and finish the restart.
func TestRestartUnpauseStatusPersistFailureWithNoDispatchedPublicationFailsClosedAndRetryRecovers(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "unpause-status-fail")
	var starts, stops, unpauses atomic.Int32
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
		case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
			_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`))
		case strings.HasSuffix(r.URL.Path, "/unpause"):
			unpauses.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stops.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/start"):
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	require.NoError(t, svc.store.UpdateServerStatus(ctx, server.ID, storage.StatusPaused))

	require.NoError(t, svc.store.DB().Exec(
		`CREATE TRIGGER fail_unpause_status BEFORE UPDATE OF status ON servers
		 WHEN NEW.status = 'running' BEGIN SELECT RAISE(ABORT, 'status unavailable'); END`).Error)
	_, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "unpause-status-fail"}))
	require.ErrorContains(t, err, "status could not be saved")
	require.EqualValues(t, 1, unpauses.Load())
	require.Zero(t, stops.Load(), "a failed status write must not claim the restart")
	require.Zero(t, starts.Load())

	current, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StatusPaused, current.Status)

	require.NoError(t, svc.store.DB().Exec(`DROP TRIGGER fail_unpause_status`).Error)
	response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "unpause-status-fail-retry"}))
	require.NoError(t, err)
	require.Equal(t, "restarting", response.Msg.GetStatus())
	require.EqualValues(t, 2, unpauses.Load())
	require.EqualValues(t, 1, stops.Load())
	require.EqualValues(t, 1, starts.Load())

	current, err = svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StatusStarting, current.Status)
}

// A paused server whose container is not live must never be reported as a
// successful start. Docker refuses the unpause, the observed state is stopped,
// and StartServer must return that error without writing RUNNING or starting a
// container.
func TestStartPausedContainerWithExitedContainerFailsClosed(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "start-unpause-stopped")
	var starts, stops, unpauses atomic.Int32
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
		case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
			_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":false,"Status":"exited"}}`))
		case strings.HasSuffix(r.URL.Path, "/unpause"):
			unpauses.Add(1)
			http.Error(w, `{"message":"Container is not paused"}`, http.StatusInternalServerError)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stops.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/start"):
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	require.NoError(t, svc.store.UpdateServerStatus(ctx, server.ID, storage.StatusPaused))

	response, err := svc.StartServer(ctx, connect.NewRequest(&v1.StartServerRequest{Id: server.ID, RequestId: "start-unpause-stopped"}))
	require.Error(t, err)
	require.Nil(t, response, "a stopped container is not a successful start")
	require.ErrorContains(t, err, "failed to resume server")
	require.ErrorContains(t, err, "not paused")
	require.EqualValues(t, 1, unpauses.Load())
	require.Zero(t, starts.Load(), "a stopped container must not be started as if it resumed")
	require.Zero(t, stops.Load())

	stored, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StatusPaused, stored.Status, "the false success must not persist RUNNING")
}

// resumePausedContainer may ignore a refused unpause only when the container is
// observed live. Paused, stopped, unknown and absent containers keep the
// original failure, and a canceled request cannot be mistaken for a resume.
func TestResumePausedContainerAcceptsOnlyLiveObservedState(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	_ = svc.docker.Close()
	inspect := func(body string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}
	}
	tests := []struct {
		name    string
		inspect http.HandlerFunc
		cancel  bool
		wantErr bool
	}{
		{name: "running", inspect: inspect(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`)},
		{name: "unhealthy", inspect: inspect(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running","Health":{"Status":"unhealthy"}}}`)},
		{name: "paused", inspect: inspect(`{"Id":"original","Name":"/original","State":{"Running":false,"Status":"paused","Paused":true}}`), wantErr: true},
		{name: "stopped", inspect: inspect(`{"Id":"original","Name":"/original","State":{"Running":false,"Status":"exited"}}`), wantErr: true},
		{name: "unknown", inspect: inspect(`{"Id":"original","Name":"/original","State":{"Running":false,"Status":"unknown"}}`), wantErr: true},
		{name: "absent", inspect: func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }, wantErr: true},
		{name: "canceled", inspect: inspect(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`), cancel: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			serveRestartAPI(t, svc, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/unpause"):
					http.Error(w, `{"message":"Container is not paused"}`, http.StatusInternalServerError)
				case strings.HasSuffix(r.URL.Path, "/json"):
					tt.inspect(w, r)
				default:
					http.NotFound(w, r)
				}
			})
			ctx := context.Background()
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := resumePausedContainer(ctx, svc.docker, "original")
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
}

// A content-only staged batch is dispatched before its unpause, but an in-place
// unpause has no replacement identity. If the RUNNING status write then fails,
// the batch is retained as dispatched with an empty expected identity and a new
// request ID must be refused at the identity check rather than acknowledged.
// The dispatched in-place batch is not auto-recovered in this pass.
func TestRestartUnpauseStatusPersistFailureWithDispatchedContentStaysFailClosed(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "unpause-dispatched-content")
	var starts, stops, unpauses atomic.Int32
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
			_, _ = w.Write([]byte(`[{"Id":"original","Names":["/carbon-panel-server-unpause-dispatched-content"],"Labels":{"carbon-panel.server.id":"unpause-dispatched-content"},"State":"running","Status":"running"}]`))
		case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
			_, _ = w.Write([]byte(`{"Id":"original","Name":"/original","State":{"Running":true,"Status":"running"}}`))
		case strings.HasSuffix(r.URL.Path, "/unpause"):
			unpauses.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/stop"):
			stops.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/start"):
			starts.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	require.NoError(t, svc.store.UpdateServerStatus(ctx, server.ID, storage.StatusPaused))
	row := stageRestartTestFile(t, svc, server, "staged.txt", "new")

	require.NoError(t, svc.store.DB().Exec(
		`CREATE TRIGGER fail_unpause_dispatched_status BEFORE UPDATE OF status ON servers
		 WHEN NEW.status = 'running' BEGIN SELECT RAISE(ABORT, 'status unavailable'); END`).Error)
	_, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "unpause-dispatched-content"}))
	require.ErrorContains(t, err, "status could not be saved before restarting")
	require.EqualValues(t, 1, unpauses.Load())
	require.Zero(t, stops.Load(), "a failed status write must not claim the restart")
	require.Zero(t, starts.Load())

	current, err := svc.store.GetServer(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StatusPaused, current.Status)
	got, err := svc.store.GetStagedContent(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusApplied, got.Status)
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt, "the dispatched in-place batch is retained fail-closed")
	require.Nil(t, open.PublishedAt)
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, record.Phase)
	require.Empty(t, record.ExpectedContainerID, "an in-place unpause has no replacement identity")

	require.NoError(t, svc.store.DB().Exec(`DROP TRIGGER fail_unpause_dispatched_status`).Error)
	response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "unpause-dispatched-content-retry"}))
	require.Error(t, err)
	require.Nil(t, response)
	require.ErrorContains(t, err, `is not the recorded replacement ""`)
	require.EqualValues(t, 1, unpauses.Load(), "the retry must not silently acknowledge or re-unpause")
	require.Zero(t, stops.Load())
	require.Zero(t, starts.Load())

	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Nil(t, open.PublishedAt, "the identity refusal is not an acknowledgement")
	record, err = svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, record.Phase)
	require.Empty(t, record.ExpectedContainerID)
}

// A completed request ID must not write a durable dispatch for freshly staged
// bytes: the callback is suppressed, so no runtime mutation occurred and the
// batch must stay retryable with a new request ID.
func TestCompletedRestartRequestDoesNotDispatchFreshStagedChanges(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "completed-replay")
	first := stageRestartTestFile(t, svc, server, "first.txt", "first")
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
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	_, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "same-request"}))
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(server.DataPath, first.TargetPath))

	fresh := stageRestartTestFile(t, svc, server, "fresh.txt", "fresh")
	_, err = svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "same-request"}))
	require.ErrorContains(t, err, "already completed")

	got, err := svc.store.GetStagedContent(ctx, fresh.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, got.Status)
	require.NoFileExists(t, filepath.Join(server.DataPath, "fresh.txt"))
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.Nil(t, open.SealedAt, "a suppressed callback must not seal fresh changes")
	require.Nil(t, open.PublishedAt)
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	if record != nil {
		require.NotEqual(t, storage.PublicationDispatched, record.Phase, "a suppressed callback must not record a dispatch")
	}

	response, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "fresh-request"}))
	require.NoError(t, err)
	require.Equal(t, "restarting", response.Msg.GetStatus())
	require.FileExists(t, filepath.Join(server.DataPath, "fresh.txt"))
	open, err = svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open)
}

// A direct in-place preflight followed by a fallback that may have created a
// replacement is not compensable: the replacement may already contain the
// staged bytes, so the batch stays retained and sealed until a retry verifies
// the recorded identity.
func TestRestartInPlacePreflightThenUncertainFallbackSeals(t *testing.T) {
	ctx := context.Background()
	svc, server := newRestartPublicationTestServer(t, "inplace-uncertain")
	var creates atomic.Int32
	row := stageRestartTestFile(t, svc, server, "staged.txt", "new")
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
		case strings.HasSuffix(r.URL.Path, "/containers/original/stop"):
			http.Error(w, `{"message":"no such container"}`, http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/containers/original/json"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			creates.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"replacement"}`))
		case strings.HasSuffix(r.URL.Path, "/containers/replacement/start"):
			http.Error(w, `{"message":"replacement start failed"}`, http.StatusInternalServerError)
		case r.Method == http.MethodPost || r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})

	_, err := svc.RestartServer(ctx, connect.NewRequest(&v1.RestartServerRequest{Id: server.ID, RequestId: "inplace-uncertain"}))
	require.Error(t, err)
	require.EqualValues(t, 1, creates.Load(), "the fallback must attempt the replacement")

	got, err := svc.store.GetStagedContent(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusApplied, got.Status)
	body, err := os.ReadFile(filepath.Join(server.DataPath, "staged.txt"))
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	open, err := svc.store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, open)
	require.NotNil(t, open.SealedAt, "a possibly-mutated replacement seals admission")
	require.Nil(t, open.PublishedAt, "an unverified outcome is not an acknowledgement")
	record, err := svc.store.GetUpdatePublication(ctx, open.ID)
	require.NoError(t, err)
	require.Equal(t, storage.PublicationDispatched, record.Phase)
	require.Equal(t, "replacement", record.ExpectedContainerID)
}
