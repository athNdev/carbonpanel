// SPDX-License-Identifier: MPL-2.0

package services

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
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
			svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
			req := env.request(t, svc, archiveData)
			imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
			require.NoError(t, err)
			require.Nil(t, prior)
			defer func() { _ = imported.file.Close() }()

			serverID := uuid.NewString()
			dataPath := svc.archiveDataPath(req.Name, serverID)
			intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath, imported.digest, imported.uploadID)
			require.NoError(t, err)
			require.True(t, claimed)
			env.setAllocation(t, store, intent, env.allocationRoot(dataPath))
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
			defer func() { _ = store.Close() }()
			svc = newArchiveRecoveryService(t, store, &env, t.TempDir())
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

			receipt, err := store.GetIdempotencyKey(ctxWithRole("operator"), imported.replayKey)
			require.NoError(t, err)
			require.Equal(t, req.ArchiveUploadId, receipt.ArchiveUploadID)
			require.Equal(t, retry.ArchiveUploadId, receipt.ArchiveCompletionUploadID)
			require.NoError(t, svc.archiveUploads.CleanupSessionFor(retry.ArchiveUploadId, uploadOwnerID(ctxWithRole("operator"))))
			for _, expired := range []*v1.CreateServerRequest{req, retry} {
				replayed, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(protoCloneRequest(expired)))
				require.NoError(t, err)
				require.Equal(t, serverID, replayed.Msg.Server.Id)
			}

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

// The requested provisioning intent is durable with publication: an import
// created with StartImmediately=false is recorded stopped, so a crash before
// the asynchronous container provision cannot make the reconciler start it.
func TestArchiveCreateWithoutImmediateStartPublishesStoppedIntent(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	archiveData := buildArchiveFixtureBytes(t)
	store := openArchiveRecoveryStore(t, env.dbPath)
	defer func() { _ = store.Close() }()
	svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
	req := env.request(t, svc, archiveData)
	req.StartImmediately = false

	created, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(req))
	require.NoError(t, err)
	require.Equal(t, v1.ServerStatus_SERVER_STATUS_STOPPED, created.Msg.Server.Status)

	// A successful handoff clears the intent after publication, while the
	// idempotency row remains the durable replay record.
	_, err = store.GetArchiveImport(ctxWithRole("operator"), archiveReplayKey(uploadOwnerID(ctxWithRole("operator")), req.RequestId))
	require.ErrorIs(t, err, db.ErrArchiveImportUnproven)
}

func TestInterruptedArchiveSubmissionFailsVisiblyWhenOwnershipIsUnproven(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	archiveData := buildArchiveFixtureBytes(t)
	store := openArchiveRecoveryStore(t, env.dbPath)
	svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
	req := env.request(t, svc, archiveData)
	imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
	require.NoError(t, err)
	require.Nil(t, prior)
	defer func() { _ = imported.file.Close() }()

	serverID := uuid.NewString()
	dataPath := svc.archiveDataPath(req.Name, serverID)
	intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath, imported.digest, imported.uploadID)
	require.NoError(t, err)
	require.True(t, claimed)
	env.setAllocation(t, store, intent, env.allocationRoot(dataPath))
	require.NoError(t, store.SetArchiveImportPhase(ctxWithRole("operator"), intent.RequestKey, db.ArchiveImportStaged))
	destination := filepath.Join(env.fixtureRoot, filepath.Base(dataPath))
	unowned := filepath.Join(destination, "world", "level.dat")
	writeArchiveFixture(t, unowned, "operator data that must survive")
	// No ownership marker: the controller cannot prove it created this tree.
	require.NoError(t, store.Close())

	store = openArchiveRecoveryStore(t, env.dbPath)
	defer func() { _ = store.Close() }()
	svc = newArchiveRecoveryService(t, store, &env, t.TempDir())
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
	defer func() { _ = store.Close() }()
	svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
	req := env.request(t, svc, archiveData)
	imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
	require.NoError(t, err)
	require.Nil(t, prior)
	defer func() { _ = imported.file.Close() }()

	serverID := uuid.NewString()
	dataPath := svc.archiveDataPath(req.Name, serverID)
	intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath, imported.digest, imported.uploadID)
	require.NoError(t, err)
	require.True(t, claimed)
	env.setAllocation(t, store, intent, env.allocationRoot(dataPath))
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
	defer func() { _ = store.Close() }()
	svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
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

// A committed intent is publication evidence in its own right. A normal
// keep_data delete removes the server row but may leave the destination and its
// marker; a later retry must never reclaim that retained operator data.
func TestCommittedArchiveIntentIsNotReclaimedAfterKeepDataDelete(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	archiveData := buildArchiveFixtureBytes(t)
	store := openArchiveRecoveryStore(t, env.dbPath)
	defer func() { _ = store.Close() }()
	svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
	req := env.request(t, svc, archiveData)
	imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
	require.NoError(t, err)
	require.Nil(t, prior)
	defer func() { _ = imported.file.Close() }()

	serverID := uuid.NewString()
	dataPath := svc.archiveDataPath(req.Name, serverID)
	intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath, imported.digest, imported.uploadID)
	require.NoError(t, err)
	require.True(t, claimed)
	env.setAllocation(t, store, intent, env.allocationRoot(dataPath))
	require.NoError(t, store.SetArchiveImportPhase(ctxWithRole("operator"), intent.RequestKey, db.ArchiveImportStaged))

	server := &db.Server{
		ID: serverID, Name: req.Name, NodeID: env.node.ID, Status: db.StatusStopped,
		ModLoader: db.ModLoaderPaper, MCVersion: "1.21.1", DataPath: dataPath,
	}
	require.NoError(t, store.CreateArchiveServer(ctxWithRole("operator"), server, db.ArchiveGameplay{},
		intent.RequestKey, imported.digest, imported.uploadID))
	published := filepath.Join(env.fixtureRoot, filepath.Base(dataPath), "world", "level.dat")
	writeArchiveFixture(t, published, "published world")
	marker := filepath.Join(env.fixtureRoot, filepath.Base(dataPath), archiveOwnershipMarker)
	require.NoError(t, os.WriteFile(marker, []byte(intent.ClaimToken+"\n"), 0o644))

	// Normal keep_data delete: the panel row goes away, the world and an
	// interrupted marker cleanup remain.
	_, err = svc.DeleteServer(ctxWithRole("operator"), connect.NewRequest(&v1.DeleteServerRequest{
		Id: serverID, KeepData: true,
	}))
	require.NoError(t, err)
	_, err = store.GetServer(ctxWithRole("operator"), serverID)
	require.Error(t, err)

	retry := protoCloneRequest(req)
	retry.ArchiveUploadId = env.upload(t, svc, archiveData)
	_, err = svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(retry))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "must not be reclaimed")
	body, readErr := os.ReadFile(published)
	require.NoError(t, readErr)
	require.Equal(t, "published world", string(body))
	_, markerErr := os.Stat(marker)
	require.NoError(t, markerErr, "recovery must not touch a published destination")
}

// Recovery must use the allocation root that was validated when the
// destination was created. Repointing a node's storage root (or the local data
// directory) at a same-name unrelated tree must not redirect removal.
func TestArchiveRecoveryUsesOriginalAllocationRoot(t *testing.T) {
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
			store := openArchiveRecoveryStore(t, env.dbPath)
			defer func() { _ = store.Close() }()
			svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
			req := env.request(t, svc, archiveData)
			imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
			require.NoError(t, err)
			require.Nil(t, prior)
			defer func() { _ = imported.file.Close() }()

			serverID := uuid.NewString()
			dataPath := svc.archiveDataPath(req.Name, serverID)
			intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath, imported.digest, imported.uploadID)
			require.NoError(t, err)
			require.True(t, claimed)
			originalRoot := env.allocationRoot(dataPath)
			env.setAllocation(t, store, intent, originalRoot)
			require.NoError(t, store.SetArchiveImportPhase(ctxWithRole("operator"), intent.RequestKey, db.ArchiveImportStaged))
			child := filepath.Base(dataPath)
			partial := filepath.Join(originalRoot, child, "world", "partial.dat")
			writeArchiveFixture(t, partial, "incomplete")
			require.NoError(t, os.WriteFile(filepath.Join(originalRoot, child, archiveOwnershipMarker), []byte(intent.ClaimToken+"\n"), 0o644))

			// An unrelated tree with the same server directory name appears at
			// the node's newly resolved root.
			remappedRoot := filepath.Join(t.TempDir(), "remapped")
			unrelated := filepath.Join(remappedRoot, child, "world", "level.dat")
			writeArchiveFixture(t, unrelated, "unrelated operator data")
			if fixture.remote {
				env.transport.root = remappedRoot
				remappedNode := *env.node
				remappedNode.StorageRoot = remappedRoot
				require.NoError(t, store.UpdateNode(ctxWithRole("operator"), &remappedNode))
			} else {
				// Local allocation is derived from the durable path, not the
				// current controller data directory. Repointing the directory
				// must not redirect cleanup.
				svc.config.Storage.DataDir = remappedRoot
			}

			retry := protoCloneRequest(req)
			retry.ArchiveUploadId = env.upload(t, svc, archiveData)
			_, err = svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(retry))
			if fixture.remote {
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
				require.Contains(t, err.Error(), "cannot prove it created")
			} else {
				require.NoError(t, err)
			}
			body, readErr := os.ReadFile(unrelated)
			require.NoError(t, readErr)
			require.Equal(t, "unrelated operator data", string(body))
		})
	}
}

// A replacement upload is validated against the durable archive identity before
// the interrupted destination is touched.
func TestArchiveRecoveryRefusesDifferentContentBeforeReclaim(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	original := buildArchiveFixtureBytesWith(t, "original world")
	replacement := buildArchiveFixtureBytesWith(t, "different world")
	store := openArchiveRecoveryStore(t, env.dbPath)
	defer func() { _ = store.Close() }()
	svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
	req := env.request(t, svc, original)
	imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
	require.NoError(t, err)
	require.Nil(t, prior)
	defer func() { _ = imported.file.Close() }()

	serverID := uuid.NewString()
	dataPath := svc.archiveDataPath(req.Name, serverID)
	intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey, imported.replayKind, serverID, dataPath, imported.digest, imported.uploadID)
	require.NoError(t, err)
	require.True(t, claimed)
	root := env.allocationRoot(dataPath)
	env.setAllocation(t, store, intent, root)
	require.NoError(t, store.SetArchiveImportPhase(ctxWithRole("operator"), intent.RequestKey, db.ArchiveImportStaged))
	child := filepath.Base(dataPath)
	partial := filepath.Join(root, child, "world", "partial.dat")
	writeArchiveFixture(t, partial, "belongs to the interrupted submission")
	require.NoError(t, os.WriteFile(filepath.Join(root, child, archiveOwnershipMarker), []byte(intent.ClaimToken+"\n"), 0o644))

	retry := protoCloneRequest(req)
	retry.ArchiveUploadId = env.upload(t, svc, replacement)
	_, err = svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(retry))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "does not match the interrupted submission")
	body, readErr := os.ReadFile(partial)
	require.NoError(t, readErr)
	require.Equal(t, "belongs to the interrupted submission", string(body))
	require.FileExists(t, filepath.Join(root, child, archiveOwnershipMarker))

	// The rejected replacement must not become the binding for this request:
	// the original archive still recovers the same destination.
	retry.ArchiveUploadId = env.upload(t, svc, original)
	created, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(retry))
	require.NoError(t, err)
	require.Equal(t, serverID, created.Msg.Server.Id)
	_, err = os.Stat(partial)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.FileExists(t, filepath.Join(root, child, "world", "level.dat"))
}

// An options-only committed row without a receipt cannot prove which archive
// was verified. It must fail closed rather than returning the prior server for
// an arbitrary upload.
func TestCommittedArchiveWithoutReceiptFailsClosed(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	archiveData := buildArchiveFixtureBytes(t)
	store := openArchiveRecoveryStore(t, env.dbPath)
	defer func() { _ = store.Close() }()
	svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
	ctx := ctxWithRole("operator")
	req := env.request(t, svc, archiveData)

	hashed := protoCloneRequest(req)
	hashed.ArchiveUploadId = ""
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(hashed)
	require.NoError(t, err)
	kind := "server-archive-create:" + hexSum(body)
	replayKey := archiveReplayKey(uploadOwnerID(ctx), req.RequestId)
	const serverID = "legacy-options-only-server"
	_, claimed, err := store.ClaimIdempotencyKey(ctx, replayKey, serverID, kind)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, store.CreateServer(ctx, &db.Server{
		ID: serverID, Name: req.Name, NodeID: env.node.ID, Status: db.StatusStopped,
		ModLoader: db.ModLoaderPaper, MCVersion: "1.21.1", DataPath: svc.archiveDataPath(req.Name, serverID),
	}))

	_, err = svc.CreateServer(ctx, connect.NewRequest(protoCloneRequest(req)))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "no durable archive identity")
}

// A committed archive import persists its replay receipt independently of the
// ownership marker. The exact original upload may replay after expiry, a fresh
// upload must verify and match the recorded digest, and different or unknown
// uploads fail clearly instead of returning the committed server.
func TestCommittedArchiveReplayIdentity(t *testing.T) {
	env := newArchiveRecoveryEnv(t, false)
	original := buildArchiveFixtureBytesWith(t, "replay world")
	store := openArchiveRecoveryStore(t, env.dbPath)
	defer func() { _ = store.Close() }()
	svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
	ctx := ctxWithRole("operator")
	req := env.request(t, svc, original)

	created, err := svc.CreateServer(ctx, connect.NewRequest(protoCloneRequest(req)))
	require.NoError(t, err)
	serverID := created.Msg.Server.Id
	originalUpload := req.ArchiveUploadId

	// The intent is gone after marker cleanup, so the idempotency receipt is
	// the only durable replay evidence.
	_, err = store.GetArchiveImport(ctx, archiveReplayKey(uploadOwnerID(ctx), req.RequestId))
	require.ErrorIs(t, err, db.ErrArchiveImportUnproven)
	receipt, err := store.GetIdempotencyKey(ctx, archiveReplayKey(uploadOwnerID(ctx), req.RequestId))
	require.NoError(t, err)
	require.NotNil(t, receipt)
	require.NotEmpty(t, receipt.ArchiveDigest)
	require.Equal(t, originalUpload, receipt.ArchiveUploadID)

	// The exact original upload id replays even after the upload session and
	// its temp file have expired.
	require.NoError(t, svc.archiveUploads.CleanupSessionFor(originalUpload, uploadOwnerID(ctx)))
	exact, err := svc.CreateServer(ctx, connect.NewRequest(protoCloneRequest(req)))
	require.NoError(t, err)
	require.Equal(t, serverID, exact.Msg.Server.Id)

	// A fresh upload of the same content is verified against the recorded
	// digest and replays.
	fresh := protoCloneRequest(req)
	fresh.ArchiveUploadId = env.upload(t, svc, original)
	same, err := svc.CreateServer(ctx, connect.NewRequest(fresh))
	require.NoError(t, err)
	require.Equal(t, serverID, same.Msg.Server.Id)

	// A different valid archive cannot borrow the committed result.
	different := protoCloneRequest(req)
	different.ArchiveUploadId = env.upload(t, svc, buildArchiveFixtureBytesWith(t, "different replay world"))
	_, err = svc.CreateServer(ctx, connect.NewRequest(different))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "does not match the archive of the committed submission")

	// An unknown upload id fails clearly without returning the prior server.
	bogus := protoCloneRequest(req)
	bogus.ArchiveUploadId = uuid.NewString()
	_, err = svc.CreateServer(ctx, connect.NewRequest(bogus))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.NotContains(t, err.Error(), serverID)
}

// A remote Docker storage worker sees every host root under one constant
// mounted path. Recovery must therefore bind the allocation to the daemon
// endpoint and host source, not to Root() alone.
func TestArchiveRecoveryBindsRemoteHostIdentity(t *testing.T) {
	const workerRoot = "/carbon-storage-root"
	for _, remap := range []bool{false, true} {
		name := "original host proof is reclaimed"
		if remap {
			name = "remapped host root is not deleted"
		}
		t.Run(name, func(t *testing.T) {
			env := newArchiveRecoveryEnv(t, true)
			archiveData := buildArchiveFixtureBytes(t)
			store := openArchiveRecoveryStore(t, env.dbPath)
			defer func() { _ = store.Close() }()
			svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
			env.transport.workerRoot = workerRoot
			req := env.request(t, svc, archiveData)

			imported, prior, err := svc.prepareArchiveImport(ctxWithRole("operator"), protoCloneRequest(req))
			require.NoError(t, err)
			require.Nil(t, prior)
			defer func() { _ = imported.file.Close() }()

			serverID := uuid.NewString()
			dataPath := svc.archiveDataPath(req.Name, serverID)
			intent, claimed, err := store.ClaimArchiveImport(ctxWithRole("operator"), imported.replayKey,
				imported.replayKind, serverID, dataPath, imported.digest, imported.uploadID)
			require.NoError(t, err)
			require.True(t, claimed)
			env.setAllocation(t, store, intent, workerRoot)
			require.NoError(t, store.SetArchiveImportPhase(ctxWithRole("operator"), intent.RequestKey, db.ArchiveImportStaged))

			child := filepath.Base(dataPath)
			originalDestination := filepath.Join(env.fixtureRoot, child)
			partial := filepath.Join(originalDestination, "world", "partial.dat")
			writeArchiveFixture(t, partial, "interrupted on the original host")
			require.NoError(t, os.WriteFile(filepath.Join(originalDestination, archiveOwnershipMarker),
				[]byte(intent.ClaimToken+"\n"), 0o644))

			remappedRoot := filepath.Join(t.TempDir(), "remapped-host")
			unrelated := filepath.Join(remappedRoot, child, "world", "level.dat")
			writeArchiveFixture(t, unrelated, "unrelated data on the new host")

			if remap {
				env.transport.root = remappedRoot
				remappedNode := *env.node
				remappedNode.StorageRoot = filepath.Join(t.TempDir(), "remapped-node-storage")
				require.NoError(t, store.UpdateNode(ctxWithRole("operator"), &remappedNode))
			}

			retry := protoCloneRequest(req)
			retry.ArchiveUploadId = env.upload(t, svc, archiveData)
			_, err = svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(retry))
			if remap {
				require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
				require.Contains(t, err.Error(), "cannot prove it created")
				body, readErr := os.ReadFile(unrelated)
				require.NoError(t, readErr)
				require.Equal(t, "unrelated data on the new host", string(body))
				body, readErr = os.ReadFile(partial)
				require.NoError(t, readErr)
				require.Equal(t, "interrupted on the original host", string(body))
				return
			}

			require.NoError(t, err)
			_, err = os.Stat(partial)
			require.ErrorIs(t, err, os.ErrNotExist)
			require.FileExists(t, filepath.Join(originalDestination, "world", "level.dat"))
			_, err = os.Stat(filepath.Join(originalDestination, archiveOwnershipMarker))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestArchiveMarkerWriteFailureKeepsUnprovenAllocation(t *testing.T) {
	for _, remap := range []bool{false, true} {
		t.Run(map[bool]string{false: "stable", true: "remapped"}[remap], func(t *testing.T) {
			env := newArchiveRecoveryEnv(t, true)
			archiveData := buildArchiveFixtureBytes(t)
			store := openArchiveRecoveryStore(t, env.dbPath)
			defer func() { _ = store.Close() }()
			svc := newArchiveRecoveryService(t, store, &env, t.TempDir())
			env.transport.workerRoot = "/carbon-storage-root"
			ctx := ctxWithRole("operator")
			req := env.request(t, svc, archiveData)
			remappedRoot := t.TempDir()
			var original, unrelated string
			var fixtureErr error
			env.transport.failMarker = func(rel string) error {
				child := filepath.Base(filepath.Dir(rel))
				original = filepath.Join(env.fixtureRoot, child, "operator.txt")
				fixtureErr = os.WriteFile(original, []byte("original storage content"), 0o644)
				if fixtureErr != nil {
					return fixtureErr
				}
				if remap {
					unrelated = filepath.Join(remappedRoot, child, "operator.txt")
					if err := os.MkdirAll(filepath.Dir(unrelated), 0o755); err != nil {
						fixtureErr = err
						return err
					}
					if err := os.WriteFile(unrelated, []byte("replacement storage content"), 0o644); err != nil {
						fixtureErr = err
						return err
					}
					remappedNode := *env.node
					remappedNode.StorageRoot = remappedRoot
					fixtureErr = store.UpdateNode(ctx, &remappedNode)
					if fixtureErr != nil {
						return fixtureErr
					}
					env.transport.root = remappedRoot
				}
				return errors.New("injected ownership marker write failure")
			}

			_, err := svc.CreateServer(ctx, connect.NewRequest(protoCloneRequest(req)))
			require.NoError(t, fixtureErr)
			require.Equal(t, connect.CodeInternal, connect.CodeOf(err))
			require.Contains(t, err.Error(), "inspect the destination node")
			body, err := os.ReadFile(original)
			require.NoError(t, err)
			require.Equal(t, "original storage content", string(body))
			require.NoFileExists(t, filepath.Join(filepath.Dir(original), archiveOwnershipMarker))

			_, err = svc.CreateServer(ctx, connect.NewRequest(protoCloneRequest(req)))
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.Contains(t, err.Error(), "cannot prove it created")
			require.FileExists(t, original)
			if remap {
				body, err = os.ReadFile(unrelated)
				require.NoError(t, err)
				require.Equal(t, "replacement storage content", string(body))
			}
		})
	}
}

func TestArchiveAllocationEndpointOmitsCredentials(t *testing.T) {
	identity := archiveEndpointIdentity("tcp://operator:super-secret@10.2.0.11:2375?token=hidden")
	require.Equal(t, "tcp://10.2.0.11:2375", identity)
	require.NotContains(t, identity, "secret")
	require.NotContains(t, identity, "operator")
	require.NotContains(t, identity, "token")
	proxyA := archiveEndpointIdentity("tcp://10.2.0.11:2375/route-a")
	proxyB := archiveEndpointIdentity("tcp://10.2.0.11:2375/route-b")
	require.NotEqual(t, proxyA, proxyB)
	require.NotContains(t, proxyA, "route-a")
}

// --- fixtures -----------------------------------------------------------------

type archiveRecoveryEnv struct {
	dbPath      string
	dataDir     string
	fixtureRoot string
	node        *db.Node
	dockerHost  string
	log         *logger.Logger
	transport   *archiveFixtureTransport
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

func newArchiveRecoveryService(t *testing.T, store *db.Store, env *archiveRecoveryEnv, uploadDir string) *ServerService {
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
		env.transport = transport
		env.fixtureRoot = transport.root
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

// allocationRoot is the root the service records when placeArchive resolves
// the destination for this fixture.
func (e archiveRecoveryEnv) allocationRoot(dataPath string) string {
	if e.node.IsLocal {
		return filepath.Dir(dataPath)
	}
	return e.fixtureRoot
}

// setAllocation records the same accessor root, sanitised endpoint and host
// source that placeArchive would persist for this fixture node.
func (e archiveRecoveryEnv) setAllocation(t *testing.T, store *db.Store, intent *db.ArchiveImport, root string) {
	t.Helper()
	info := e.node.StorageInfo()
	require.NoError(t, store.SetArchiveImportAllocation(
		ctxWithRole("operator"), intent.RequestKey, e.node.ID, root,
		archiveEndpointIdentityFor(info), archiveHostRootFor(info, root),
	))
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
	return buildArchiveFixtureBytesWith(t, "recovered world")
}

func buildArchiveFixtureBytesWith(t *testing.T, world string) []byte {
	t.Helper()
	source := t.TempDir()
	writeArchiveFixture(t, filepath.Join(source, "world", "level.dat"), world)
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
