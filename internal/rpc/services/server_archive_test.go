// SPDX-License-Identifier: MPL-2.0

package services

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/serverarchive"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/athNdev/carbon-panel/pkg/download"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"github.com/stretchr/testify/require"
)

func TestServerArchiveRoundTripThroughCreateServer(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		format v1.ServerArchiveFormat
	}{
		{"zip", v1.ServerArchiveFormat_SERVER_ARCHIVE_FORMAT_ZIP},
		{"tar.gz", v1.ServerArchiveFormat_SERVER_ARCHIVE_FORMAT_TAR_GZ},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			svc, _ := newDockerPrivilegeTestService(t)
			svc.hostPortBound = func(int) bool { return false }
			nodeRoot := t.TempDir()
			phantomRoot := filepath.Join(t.TempDir(), "remote-daemon-storage")
			registerRemoteCreateNode(t, svc, "archive-node", phantomRoot)
			transport := &archiveFixtureTransport{root: nodeRoot}
			svc.SetStorageResolver(nodestorage.NewResolver(transport))
			uploads := upload.NewManager(t.TempDir(), time.Hour, 32<<20, svc.log)
			t.Cleanup(uploads.Stop)
			downloads := download.NewManager(t.TempDir(), time.Hour, svc.log)
			t.Cleanup(downloads.Stop)
			svc.SetServerArchiveUploads(uploads)
			svc.SetServerArchiveDownloads(downloads)

			ctx := ctxWithRole("operator")
			other := auth.WithUser(context.Background(), &auth.AuthenticatedUser{
				ID: "other-operator", Username: "other-operator", Roles: []string{"operator"},
			})
			original := &db.Server{
				ID: "archive-source", Name: "Original", NodeID: "archive-node",
				Status: db.StatusRunning, ModLoader: db.ModLoaderPaper, MCVersion: "1.21.1",
				Memory: 2048, MaxPlayers: 12, Port: 25671,
				DataPath: filepath.Join(svc.config.Storage.DataDir, "servers", "archive-source"),
			}
			require.NoError(t, svc.store.CreateServer(ctx, original))
			want := map[string]string{
				"existing-world/level.dat": "existing-world-data",
				"mods/fixture.jar":         "mod data",
				"plugins/fixture.jar":      "plugin data",
				"config/fixture.yml":       "preserved: true\n",
				"server.properties":        "level-name=existing-world\nmotd=Restored fixture\n",
			}
			sourceRoot := filepath.Join(nodeRoot, filepath.Base(original.DataPath))
			for rel, body := range want {
				writeArchiveFixture(t, filepath.Join(sourceRoot, filepath.FromSlash(rel)), body)
			}
			export := &v1.ExportServerArchiveRequest{
				ServerId: original.ID, Format: fixture.format,
				Consistency: v1.ServerArchiveConsistency_SERVER_ARCHIVE_CONSISTENCY_STOPPED,
			}
			_, err := svc.ExportServerArchive(ctx, connect.NewRequest(export))
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			require.NoError(t, svc.store.UpdateServerStatus(ctx, original.ID, db.StatusStopped))
			exported, err := svc.ExportServerArchive(ctx, connect.NewRequest(export))
			require.NoError(t, err)
			require.True(t, strings.HasSuffix(exported.Msg.Filename, "."+fixture.name))
			session, err := downloads.GetSession(exported.Msg.SessionId)
			require.NoError(t, err)
			require.Equal(t, original.ID, session.ServerID)
			require.Equal(t, uploadOwnerID(ctx), session.CreatorUserID)
			data, err := os.ReadFile(session.FilePath)
			require.NoError(t, err)

			uploadSession, err := uploads.InitSessionFor(exported.Msg.Filename, int64(len(data)), int32(len(data)), uploadOwnerID(ctx))
			require.NoError(t, err)
			done, err := uploads.WriteChunkFor(uploadSession.ID, uploadOwnerID(ctx), 0, data)
			require.NoError(t, err)
			require.True(t, done)

			legacy, err := uploads.InitSession("legacy-archive", int64(len(data)), int32(len(data)))
			require.NoError(t, err)
			done, err = uploads.WriteChunk(legacy.ID, 0, data)
			require.NoError(t, err)
			require.True(t, done)
			_, err = svc.PreviewServerArchive(ctx, connect.NewRequest(&v1.PreviewServerArchiveRequest{UploadId: legacy.ID}))
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

			preview := &v1.PreviewServerArchiveRequest{UploadId: uploadSession.ID, NodeId: original.NodeID}
			_, err = svc.PreviewServerArchive(other, connect.NewRequest(preview))
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
			report, err := svc.PreviewServerArchive(ctx, connect.NewRequest(preview))
			require.NoError(t, err)
			require.Equal(t, fixture.format, report.Msg.Format)
			require.Equal(t, int32(len(want)), report.Msg.FileCount)
			require.Empty(t, report.Msg.Findings)
			require.Positive(t, report.Msg.GetAvailableBytes())

			create := &v1.CreateServerRequest{
				Name: "Imported", NodeId: original.NodeID, Port: 25672,
				ArchiveUploadId: uploadSession.ID, RequestId: "archive-round-trip",
			}
			_, err = svc.CreateServer(other, connect.NewRequest(create))
			require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

			created, err := svc.CreateServer(ctx, connect.NewRequest(create))
			require.NoError(t, err)
			require.NotEqual(t, original.ID, created.Msg.Server.Id)
			require.Equal(t, int32(25672), created.Msg.Server.Port)
			targetRoot := filepath.Join(nodeRoot, filepath.Base(created.Msg.Server.DataPath))
			for rel, body := range want {
				for _, root := range []string{sourceRoot, targetRoot} {
					got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
					require.NoError(t, err, rel)
					require.Equal(t, body, string(got), rel)
				}
			}
			config, err := svc.store.GetServerConfig(ctx, created.Msg.Server.Id)
			require.NoError(t, err)
			require.NotNil(t, config.Level)
			require.Equal(t, "existing-world", *config.Level)
			reset, err := svc.CreateServer(ctx, connect.NewRequest(create))
			require.NoError(t, err)
			require.Equal(t, created.Msg.Server.Id, reset.Msg.Server.Id)
			assertNoControllerLocalTree(t, phantomRoot, created.Msg.Server.DataPath)

			duplicate := &v1.CreateServerRequest{
				Name: " imported ", NodeId: original.NodeID, Port: 25673,
				ArchiveUploadId: uploadSession.ID, RequestId: "different-request",
			}
			_, err = svc.CreateServer(ctx, connect.NewRequest(duplicate))
			require.Equal(t, connect.CodeAlreadyExists, connect.CodeOf(err))
			interrupted := &v1.CreateServerRequest{
				Name: "Interrupted", NodeId: original.NodeID, Port: 25674,
				ArchiveUploadId: uploadSession.ID, RequestId: "interrupted-request",
			}
			interruptedContext, cancel := context.WithCancel(ctx)
			transport.interruptMu.Lock()
			transport.interruptWrite = cancel
			transport.interruptMu.Unlock()
			_, err = svc.CreateServer(interruptedContext, connect.NewRequest(interrupted))
			cancel()
			require.Equal(t, connect.CodeCanceled, connect.CodeOf(err))
			require.NoError(t, svc.store.CheckArchiveServerName(ctx, interrupted.Name))

			roots, err := os.ReadDir(nodeRoot)
			require.NoError(t, err)
			require.Len(t, roots, 2)
		})
	}
}

func writeArchiveFixture(t *testing.T, destination, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0o755))
	require.NoError(t, os.WriteFile(destination, []byte(content), 0o644))
}

func newArchiveUpload(t *testing.T, ctx context.Context, svc *ServerService) (*upload.Manager, string) {
	t.Helper()
	source := t.TempDir()
	writeArchiveFixture(t, filepath.Join(source, "server.properties"), "level-name=fixture-world\nmotd=fixture\n")
	accessor, err := nodestorage.NewLocal(source)
	require.NoError(t, err)
	defer func() { _ = accessor.Close() }()
	var archive bytes.Buffer
	require.NoError(t, serverarchive.Write(ctx, accessor, &archive, serverarchive.ZIP, serverarchive.Runtime{
		MinecraftVersion: "1.21.1",
		ModLoader:        string(db.ModLoaderPaper),
		MemoryMB:         1024,
		MaxPlayers:       8,
	}, "fixture-exporter"))
	uploads := upload.NewManager(t.TempDir(), time.Hour, 32<<20, svc.log)
	t.Cleanup(uploads.Stop)
	svc.SetServerArchiveUploads(uploads)
	session, err := uploads.InitSessionFor("fixture.zip", int64(archive.Len()), int32(archive.Len()), uploadOwnerID(ctx))
	require.NoError(t, err)
	done, err := uploads.WriteChunkFor(session.ID, uploadOwnerID(ctx), 0, archive.Bytes())
	require.NoError(t, err)
	require.True(t, done)
	return uploads, session.ID
}

func TestPreviewServerArchiveExposesOnlyEnabledDestinations(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	svc.hostPortBound = func(int) bool { return false }
	registerRemoteCreateNode(t, svc, "archive-enabled", filepath.Join(t.TempDir(), "remote-storage"))
	require.NoError(t, svc.store.CreateNode(context.Background(), &db.Node{
		ID: "archive-disabled", Name: "Disabled Node", Host: "tcp://198.51.100.7:2375",
		Enabled: true, Status: db.NodeStatusOnline,
	}))
	disabled := &db.Node{
		ID: "archive-disabled", Name: "Disabled Node", Host: "tcp://198.51.100.7:2375",
		Enabled: false, Status: db.NodeStatusOnline,
	}
	require.NoError(t, svc.store.UpdateNode(context.Background(), disabled))
	ctx := ctxWithRole("operator")
	_, uploadID := newArchiveUpload(t, ctx, svc)

	report, err := svc.PreviewServerArchive(ctx, connect.NewRequest(&v1.PreviewServerArchiveRequest{UploadId: uploadID}))
	require.NoError(t, err)
	require.Nil(t, report.Msg.AvailableBytes, "capacity is only checked for a concrete node")

	destinations := map[string]string{}
	for _, destination := range report.Msg.Destinations {
		destinations[destination.Id] = destination.Name
	}
	require.Contains(t, destinations, "archive-enabled")
	require.Equal(t, "archive-enabled", destinations["archive-enabled"])
	require.NotContains(t, destinations, "archive-disabled", "disabled nodes must not be offered")
	require.Len(t, destinations, 2, "only the fixture local node and the enabled remote node qualify")
}

func TestPreviewAndCreateRefuseArchiveOnInsufficientDestinationSpace(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	svc.hostPortBound = func(int) bool { return false }
	registerRemoteCreateNode(t, svc, "archive-small", filepath.Join(t.TempDir(), "remote-storage"))
	svc.SetStorageResolver(nodestorage.NewResolver(&lowSpaceTransport{free: 1 << 20}))
	ctx := ctxWithRole("operator")
	_, uploadID := newArchiveUpload(t, ctx, svc)

	preview, err := svc.PreviewServerArchive(ctx, connect.NewRequest(&v1.PreviewServerArchiveRequest{
		UploadId: uploadID,
		NodeId:   "archive-small",
	}))
	require.NoError(t, err)
	require.NotNil(t, preview.Msg.AvailableBytes)
	require.Less(t, preview.Msg.GetAvailableBytes(), preview.Msg.RequiredBytes)
	require.True(t, hasArchiveFinding(preview.Msg.Findings, v1.ServerArchiveFindingCode_SERVER_ARCHIVE_FINDING_CODE_INSUFFICIENT_SPACE))

	_, err = svc.CreateServer(ctx, connect.NewRequest(&v1.CreateServerRequest{
		Name: "Too Large", NodeId: "archive-small", Port: 25691,
		ArchiveUploadId: uploadID, RequestId: "archive-low-space",
	}))
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err), err)
}

func hasArchiveFinding(findings []*v1.ServerArchiveFinding, code v1.ServerArchiveFindingCode) bool {
	for _, finding := range findings {
		if finding.Code == code && finding.Severity == v1.ServerArchiveFindingSeverity_SERVER_ARCHIVE_FINDING_SEVERITY_BLOCKER {
			return true
		}
	}
	return false
}

// lowSpaceTransport serves the allocation preflight for a transport-only node
// without touching a real daemon or a controller-local path.
type lowSpaceTransport struct {
	free int64
}

func (l *lowSpaceTransport) Open(context.Context, nodestorage.NodeInfo, string) (nodestorage.Accessor, error) {
	return nil, errors.New("unexpected data open")
}

func (l *lowSpaceTransport) OpenStorageRoot(context.Context, nodestorage.NodeInfo) (nodestorage.AllocationRoot, error) {
	return lowSpaceRoot{free: l.free}, nil
}

type lowSpaceRoot struct {
	nodestorage.AllocationRoot
	free int64
}

func (l lowSpaceRoot) FreeBytes() (int64, error) { return l.free, nil }
func (l lowSpaceRoot) Close() error              { return nil }

// The fixture uses the real storage worker protocol without mounting any
// controller path as the remote daemon's declared storage root.
type archiveFixtureTransport struct {
	root           string
	interruptMu    sync.Mutex
	interruptWrite context.CancelFunc
}

func (a *archiveFixtureTransport) Open(ctx context.Context, node nodestorage.NodeInfo, dataRoot string) (nodestorage.Accessor, error) {
	return a.OpenExisting(ctx, node, dataRoot)
}

func (a *archiveFixtureTransport) OpenExisting(ctx context.Context, _ nodestorage.NodeInfo, dataRoot string) (nodestorage.Accessor, error) {
	return a.open(ctx, filepath.Join(a.root, filepath.Base(dataRoot)))
}

func (a *archiveFixtureTransport) OpenStorageRoot(ctx context.Context, _ nodestorage.NodeInfo) (nodestorage.AllocationRoot, error) {
	return a.open(ctx, a.root)
}

func (a *archiveFixtureTransport) open(ctx context.Context, root string) (*storageworker.RemoteAccessor, error) {
	local, err := nodestorage.NewLocal(root)
	if err != nil {
		return nil, err
	}
	controller, worker := net.Pipe()
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		defer func() { _ = worker.Close() }()
		defer func() { _ = local.Close() }()
		_ = storageworker.ServeAccessor(worker, worker, archiveFixtureFS{AllocationRoot: local.(nodestorage.AllocationRoot), transport: a})
	}()
	closeWorker := func() error {
		err := controller.Close()
		<-workerDone
		if errors.Is(err, net.ErrClosed) {
			return nil
		}
		return err
	}
	session, err := storageworker.NewSessionContext(ctx, controller, controller, closeWorker)
	if err != nil {
		_ = closeWorker()
		return nil, err
	}
	return storageworker.NewRemoteAccessor(session), nil
}

// archiveFixtureFS cancels once at the first attempted bulk write.
type archiveFixtureFS struct {
	nodestorage.AllocationRoot
	transport *archiveFixtureTransport
}

func (f archiveFixtureFS) BeginWrite(ctx context.Context, path string, limit int64) (storageworker.AtomicWriter, error) {
	f.transport.interruptMu.Lock()
	cancel := f.transport.interruptWrite
	f.transport.interruptWrite = nil
	f.transport.interruptMu.Unlock()
	if cancel != nil {
		cancel()
		return nil, context.Canceled
	}
	return f.AllocationRoot.BeginWrite(ctx, path, limit)
}
