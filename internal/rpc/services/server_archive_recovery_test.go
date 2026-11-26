// SPDX-License-Identifier: MPL-2.0

package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/serverarchive"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// A controller crash after the durable request claim but before the server row
// commit must be recoverable from durable state alone. These tests reconstruct
// the store and service against a file-backed database and the fixture the
// interrupted attempt left behind.
func TestInterruptedArchiveSubmissionRecoversProvenDestination(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		remote bool
	}{
		{name: "local-node"},
		{name: "registered-node", remote: true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			env := newArchiveRecoveryEnv(t, fixture.remote)
			archiveData := buildArchiveFixtureBytes(t)

			// First controller: claim the request and place a partial
			// destination, then disappear before the server row is committed.
			store := openArchiveRecoveryStore(t, env.dbPath)
			svc := newArchiveRecoveryService(t, store, env, t.TempDir())
			req := env.request(t, svc, archiveData)
			imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
			require.NoError(t, err)
			require.Nil(t, prior)
			defer func() { _ = imported.file.Close() }()

			serverID := uuid.NewString()
			dataPath := svc.archiveDataPath(req.Name, serverID)
			intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath)
			require.NoError(t, err)
			require.True(t, claimed)
			require.NoError(t, store.SetArchiveImportNode(ctxWithRole("operator"), intent.RequestKey, env.node.ID))
			require.NoError(t, store.SetArchiveImportPhase(ctxWithRole("operator"), intent.RequestKey, db.ArchiveImportStaged))
			child := filepath.Base(dataPath)
			destination := filepath.Join(env.fixtureRoot, child)
			writeArchiveFixture(t, filepath.Join(destination, "world", "partial.dat"), "incomplete")
			require.NoError(t, os.WriteFile(filepath.Join(destination, archiveOwnershipMarker), []byte(intent.ClaimToken+"\n"), 0o644))
			require.NoError(t, store.Close())

			// Second controller: the durable claim, intent and marker are the
			// only inputs. The upload id is deliberately new because upload
			// sessions do not survive a restart.
			store = openArchiveRecoveryStore(t, env.dbPath)
			defer store.Close()
			svc = newArchiveRecoveryService(t, store, env, t.TempDir())
			retry := protoCloneRequest(req)
			retry.ArchiveUploadId = env.upload(t, svc, archiveData)
			created, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(retry))
			require.NoError(t, err)
			require.Equal(t, serverID, created.Msg.Server.Id)

			// The proven partial destination was removed and replaced by the
			// verified archive. The marker never becomes server data.
			_, err = os.Stat(filepath.Join(destination, "world", "partial.dat"))
			require.ErrorIs(t, err, os.ErrNotExist)
			_, err = os.Stat(filepath.Join(destination, "world", "level.dat"))
			require.NoError(t, err)
			_, err = os.Stat(filepath.Join(destination, archiveOwnershipMarker))
			require.ErrorIs(t, err, os.ErrNotExist)

			entries, err := os.ReadDir(env.fixtureRoot)
			require.NoError(t, err)
			require.Len(t, entries, 1, "recovery must not leave a duplicate destination")
			require.Equal(t, child, entries[0].Name())

			committed, err := store.GetServer(ctxWithRole("operator"), serverID)
			require.NoError(t, err)
			require.Equal(t, req.Name, committed.Name)
			_, err = store.GetArchiveImport(ctxWithRole("operator"), intent.RequestKey)
			require.ErrorIs(t, err, db.ErrArchiveImportUnproven)

			// A third upload retry is a normal idempotent replay and still
			// leaves exactly one committed destination.
			again := protoCloneRequest(req)
			again.ArchiveUploadId = env.upload(t, svc, archiveData)
			replayed, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(again))
			require.NoError(t, err)
			require.Equal(t, serverID, replayed.Msg.Server.Id)
			entries, err = os.ReadDir(env.fixtureRoot)
			require.NoError(t, err)
			require.Len(t, entries, 1)
		})
	}
}

func TestInterruptedArchiveSubmissionFailsVisiblyWhenOwnershipIsUnproven(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	archiveData := buildArchiveFixtureBytes(t)
	store := openArchiveRecoveryStore(t, env.dbPath)
	svc := newArchiveRecoveryService(t, store, env, t.TempDir())
	req := env.request(t, svc, archiveData)
	imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
	require.NoError(t, err)
	require.Nil(t, prior)
	defer func() { _ = imported.file.Close() }()

	serverID := uuid.NewString()
	dataPath := svc.archiveDataPath(req.Name, serverID)
	intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.SetArchiveImportNode(ctxWithRole("operator"), intent.RequestKey, env.node.ID))
	require.NoError(t, store.SetArchiveImportPhase(ctxWithRole("operator"), intent.RequestKey, db.ArchiveImportStaged))
	destination := filepath.Join(env.fixtureRoot, filepath.Base(dataPath))
	unowned := filepath.Join(destination, "world", "level.dat")
	writeArchiveFixture(t, unowned, "operator data that must survive")
	// No ownership marker: the controller cannot prove it created this tree.
	require.NoError(t, store.Close())

	store = openArchiveRecoveryStore(t, env.dbPath)
	defer store.Close()
	svc = newArchiveRecoveryService(t, store, env, t.TempDir())
	retry := protoCloneRequest(req)
	retry.ArchiveUploadId = env.upload(t, svc, archiveData)
	_, err = svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(retry))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "cannot prove it created")

	body, err := os.ReadFile(unowned)
	require.NoError(t, err)
	require.Equal(t, "operator data that must survive", string(body))
	_, err = store.GetServer(ctxWithRole("operator"), serverID)
	require.Error(t, err)
	if _, lookupErr := store.LookupIdempotencyKey(ctxWithRole("operator"), imported.replayKey, imported.replayKind); lookupErr != nil {
		t.Fatalf("lookup idempotency key: %v", lookupErr)
	}
	_, err = store.GetArchiveImport(ctxWithRole("operator"), imported.replayKey)
	require.NoError(t, err)
}

func TestCommittedArchiveServerIsNeverRemovedByRecovery(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	archiveData := buildArchiveFixtureBytes(t)
	store := openArchiveRecoveryStore(t, env.dbPath)
	defer store.Close()
	svc := newArchiveRecoveryService(t, store, env, t.TempDir())
	req := env.request(t, svc, archiveData)
	imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
	require.NoError(t, err)
	require.Nil(t, prior)
	defer func() { _ = imported.file.Close() }()

	serverID := uuid.NewString()
	dataPath := svc.archiveDataPath(req.Name, serverID)
	intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.SetArchiveImportNode(ctxWithRole("operator"), intent.RequestKey, env.node.ID))
	require.NoError(t, store.CreateServer(ctxWithRole("operator"), &db.Server{
		ID: serverID, Name: req.Name, NodeID: env.node.ID, Status: db.StatusStopped,
		ModLoader: db.ModLoaderPaper, MCVersion: "1.21.1", DataPath: dataPath,
	}))
	committedFile := filepath.Join(env.fixtureRoot, filepath.Base(dataPath), "world", "level.dat")
	writeArchiveFixture(t, committedFile, "committed world")
	// A crash after the row commit can leave the marker behind.
	markerPath := filepath.Join(env.fixtureRoot, filepath.Base(dataPath), archiveOwnershipMarker)
	require.NoError(t, os.WriteFile(markerPath, []byte(intent.ClaimToken+"\n"), 0o644))

	replay := protoCloneRequest(req)
	replay.ArchiveUploadId = env.upload(t, svc, archiveData)
	response, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(replay))
	require.NoError(t, err)
	require.Equal(t, serverID, response.Msg.Server.Id)
	body, err := os.ReadFile(committedFile)
	require.NoError(t, err)
	require.Equal(t, "committed world", string(body))
	_, err = os.Stat(markerPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = store.GetArchiveImport(ctxWithRole("operator"), intent.RequestKey)
	require.ErrorIs(t, err, db.ErrArchiveImportUnproven)
}

func TestLegacyIdempotencyClaimStillReplaysCommittedServer(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	archiveData := buildArchiveFixtureBytes(t)
	store := openArchiveRecoveryStore(t, env.dbPath)
	defer store.Close()
	svc := newArchiveRecoveryService(t, store, env, t.TempDir())
	req := env.request(t, svc, archiveData)

	// A pre-intent controller stored a kind that included the upload id.
	legacyBody, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	require.NoError(t, err)
	legacyKind := "server-archive-create:" + hexSum(legacyBody)
	replayKey := archiveReplayKey(uploadOwnerID(ctxWithRole("operator")), req.RequestId)
	const serverID = "legacy-committed-server"
	_, claimed, err := store.ClaimIdempotencyKey(ctxWithRole("operator"), replayKey, serverID, legacyKind)
	require.NoError(t, err)
	require.True(t, claimed)
	dataPath := svc.archiveDataPath(req.Name, serverID)
	require.NoError(t, store.CreateServer(ctxWithRole("operator"), &db.Server{
		ID: serverID, Name: req.Name, NodeID: env.node.ID, Status: db.StatusStopped,
		ModLoader: db.ModLoaderPaper, MCVersion: "1.21.1", DataPath: dataPath,
	}))
	committedFile := filepath.Join(env.fixtureRoot, filepath.Base(dataPath), "world", "level.dat")
	writeArchiveFixture(t, committedFile, "legacy world")

	response, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(req))
	require.NoError(t, err)
	require.Equal(t, serverID, response.Msg.Server.Id)
	body, err := os.ReadFile(committedFile)
	require.NoError(t, err)
	require.Equal(t, "legacy world", string(body))
}

// --- fixtures -----------------------------------------------------------------

type archiveRecoveryEnv struct {
	dbPath      string
	dataDir     string
	fixtureRoot string
	node        *db.Node
	dockerHost  string
	log         *logger.Logger
}

func newArchiveRecoveryEnv(t *testing.T, remote bool) archiveRecoveryEnv {
	t.Helper()
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(dockerAPI.Close)
	dockerHost := "tcp://" + strings.TrimPrefix(dockerAPI.URL, "http://")

	env := archiveRecoveryEnv{
		dbPath:     filepath.Join(t.TempDir(), "panel.db"),
		dataDir:    t.TempDir(),
		dockerHost: dockerHost,
		log:        logger.New(),
	}
	if remote {
		// The node-side storage root is not visible to the controller, so the
		// accessor is reached through the storage transport.
		env.fixtureRoot = t.TempDir()
		env.node = &db.Node{
			ID: "recovery-remote", Name: "recovery-remote", Host: "tcp://10.2.0.11:2375",
			Enabled: true, Status: db.NodeStatusOnline,
			StorageRoot: filepath.Join(t.TempDir(), "remote-node-storage"),
		}
	} else {
		env.fixtureRoot = filepath.Join(env.dataDir, "servers")
		env.node = &db.Node{
			ID: "recovery-local", Name: "recovery-local", Host: dockerHost,
			Enabled: true, Status: db.NodeStatusOnline, IsLocal: true,
		}
	}
	return env
}

func openArchiveRecoveryStore(t *testing.T, path string) *db.Store {
	t.Helper()
	cfg := &config.Config{Database: config.DatabaseConfig{
		Path: path, AutoMigrate: true, MaxConnections: 5,
	}}
	store, err := db.NewSQLiteStore(cfg)
	require.NoError(t, err)
	return store
}

func newArchiveRecoveryService(t *testing.T, store *db.Store, env archiveRecoveryEnv, uploadDir string) *ServerService {
	t.Helper()
	cfg := &config.Config{Storage: config.StorageConfig{DataDir: env.dataDir}}
	cfg.Docker.Host = env.dockerHost
	dockerCli, err := docker.NewClient(env.dockerHost, env.log)
	require.NoError(t, err)
	svc := NewServerService(store, dockerCli, nil, cfg, nil, nil, nil, nil, nil, env.log, nil, nil, nil)
	svc.hostPortBound = func(int) bool { return false }
	svc.hostMemoryMB = func(context.Context, string) (int64, error) { return 16 * 1024, nil }
	uploads := upload.NewManager(uploadDir, time.Hour, 32<<20, env.log)
	t.Cleanup(uploads.Stop)
	svc.SetServerArchiveUploads(uploads)
	if env.node.IsLocal {
		svc.SetStorageResolver(nodestorage.NewResolver(nil))
	} else {
		transport := &archiveFixtureTransport{root: env.fixtureRoot}
		svc.SetStorageResolver(nodestorage.NewResolver(transport))
		pool := docker.NewClientPool(store, dockerCli, env.log)
		pool.RegisterClient(env.node.ID, dockerCli)
		svc.pool = pool
	}
	if _, err := store.GetNode(context.Background(), env.node.ID); err != nil {
		require.NoError(t, store.CreateNode(context.Background(), env.node))
	}
	return svc
}

func (e archiveRecoveryEnv) request(t *testing.T, svc *ServerService, archiveData []byte) *v1.CreateServerRequest {
	t.Helper()
	return &v1.CreateServerRequest{
		Name: "Interrupted", NodeId: e.node.ID, Port: 25801,
		ArchiveUploadId: e.upload(t, svc, archiveData), RequestId: "interrupted-submission",
	}
}

func (e archiveRecoveryEnv) upload(t *testing.T, svc *ServerService, archiveData []byte) string {
	t.Helper()
	if svc.archiveUploads == nil {
		t.Fatal("archive upload manager is not configured")
	}
	session, err := svc.archiveUploads.InitSessionFor("fixture.zip", int64(len(archiveData)), int32(len(archiveData)), uploadOwnerID(ctxWithRole("operator")))
	require.NoError(t, err)
	done, err := svc.archiveUploads.WriteChunkFor(session.ID, uploadOwnerID(ctxWithRole("operator")), 0, archiveData)
	require.NoError(t, err)
	require.True(t, done)
	return session.ID
}

func buildArchiveFixtureBytes(t *testing.T) []byte {
	t.Helper()
	source := t.TempDir()
	writeArchiveFixture(t, filepath.Join(source, "world", "level.dat"), "recovered world")
	writeArchiveFixture(t, filepath.Join(source, "server.properties"), "level-name=world\ndifficulty=hard\n")
	accessor, err := nodestorage.NewLocal(source)
	require.NoError(t, err)
	defer func() { _ = accessor.Close() }()
	var buf bytes.Buffer
	err = serverarchive.Write(context.Background(), accessor, &buf, serverarchive.ZIP, serverarchive.Runtime{
		MinecraftVersion: "1.21.1", ModLoader: "paper", MemoryMB: 1024, MaxPlayers: 10,
	}, "recovery-fixture")
	require.NoError(t, err)
	return buf.Bytes()
}

func protoCloneRequest(req *v1.CreateServerRequest) *v1.CreateServerRequest {
	return proto.CloneOf(req)
}

func hexSum(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func archiveReplayKey(owner, requestID string) string {
	return hexSum([]byte("server-archive-create\x00" + owner + "\x00" + requestID))
}
