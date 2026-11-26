// SPDX-License-Identifier: MPL-2.0

package rpc

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/internal/serverarchive"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"github.com/stretchr/testify/require"
)

// TestCreateOnlyPrincipalCanChooseArchiveDestinationWithoutListNodes is the
// restricted-role fixture for OSS-428. A principal that may create servers but
// may not read node inventory must still preview an archive, receive a narrow
// enabled-destination list, preview that exact node's capacity and submit the
// import to it. ListNodes stays denied.
func TestCreateOnlyPrincipalCanChooseArchiveDestinationWithoutListNodes(t *testing.T) {
	store := newTestStore(t)
	defer func() { _ = store.Close() }()
	ctx := context.Background()
	log := logger.New()

	enforcer, err := rbac.NewEnforcer(store.DB())
	require.NoError(t, err)
	// Only servers/create: no nodes/read and no broader server grants.
	require.NoError(t, enforcer.SetPermissionsForRole("anonymous", []rbac.Permission{
		{Resource: rbac.ResourceServers, Action: rbac.ActionCreate, ObjectID: "*"},
	}))

	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte("[]"))
		case strings.HasSuffix(r.URL.Path, "/info"):
			_, _ = w.Write([]byte(`{"MemTotal":17179869184}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer dockerAPI.Close()
	dockerHost := "tcp://" + strings.TrimPrefix(dockerAPI.URL, "http://")
	sharedRoot := t.TempDir()
	require.NoError(t, store.CreateNode(ctx, &storage.Node{
		ID: "archive-dest", Name: "Archive Destination", Host: dockerHost,
		Enabled: true, Status: storage.NodeStatusOnline, StorageRoot: sharedRoot,
	}))

	cfg := &config.Config{Storage: config.StorageConfig{DataDir: t.TempDir()}}
	cfg.Docker.Host = dockerHost
	dockerClient, err := docker.NewClient(dockerHost, log)
	require.NoError(t, err)
	svc := services.NewServerService(store, dockerClient, nil, cfg, nil, nil, nil, nil, nil, log, nil, nil, enforcer)
	svc.SetStorageResolver(nodestorage.NewResolver(nil))
	uploads := upload.NewManager(t.TempDir(), time.Hour, 32<<20, log)
	t.Cleanup(uploads.Stop)
	svc.SetServerArchiveUploads(uploads)

	source := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(source, "server.properties"), []byte("level-name=fixture-world\n"), 0o644))
	accessor, err := nodestorage.NewLocal(source)
	require.NoError(t, err)
	defer func() { _ = accessor.Close() }()
	var archive bytes.Buffer
	require.NoError(t, serverarchive.Write(ctx, accessor, &archive, serverarchive.ZIP, serverarchive.Runtime{
		MinecraftVersion: "1.21.1", ModLoader: string(storage.ModLoaderPaper), MemoryMB: 1024, MaxPlayers: 8,
	}, "fixture-exporter"))
	session, err := uploads.InitSessionFor("fixture.zip", int64(archive.Len()), int32(archive.Len()), "anonymous")
	require.NoError(t, err)
	done, err := uploads.WriteChunkFor(session.ID, "anonymous", 0, archive.Bytes())
	require.NoError(t, err)
	require.True(t, done)

	authManager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local: config.LocalConfig{Enabled: true}, AnonymousAccess: true,
	})
	require.NoError(t, err)
	server := &Server{store: store, authManager: authManager, enforcer: enforcer, log: log}
	mux := http.NewServeMux()
	serverPath, serverHandler := carbonpanelv1connect.NewServerServiceHandler(
		svc, connect.WithInterceptors(server.authInterceptor()))
	mux.Handle(serverPath, serverHandler)
	nodePath, nodeHandler := carbonpanelv1connect.NewNodeServiceHandler(
		services.NewNodeService(store, nil, log), connect.WithInterceptors(server.authInterceptor()))
	mux.Handle(nodePath, nodeHandler)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()

	nodeClient := carbonpanelv1connect.NewNodeServiceClient(httpServer.Client(), httpServer.URL)
	if _, err := nodeClient.ListNodes(ctx, connect.NewRequest(&v1.ListNodesRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("ListNodes for a create-only principal = %v, want permission denied", err)
	}

	serverClient := carbonpanelv1connect.NewServerServiceClient(httpServer.Client(), httpServer.URL)
	inspection, err := serverClient.PreviewServerArchive(ctx, connect.NewRequest(&v1.PreviewServerArchiveRequest{
		UploadId: session.ID,
	}))
	require.NoError(t, err, "archive preview must not require nodes/read")
	require.Nil(t, inspection.Msg.AvailableBytes)
	require.Len(t, inspection.Msg.Destinations, 1)
	destination := inspection.Msg.Destinations[0]
	require.Equal(t, "archive-dest", destination.Id)
	require.Equal(t, "Archive Destination", destination.Name)

	preview, err := serverClient.PreviewServerArchive(ctx, connect.NewRequest(&v1.PreviewServerArchiveRequest{
		UploadId: session.ID, NodeId: destination.Id,
	}))
	require.NoError(t, err)
	require.NotNil(t, preview.Msg.AvailableBytes, "the concrete destination must run the capacity check")

	created, err := serverClient.CreateServer(ctx, connect.NewRequest(&v1.CreateServerRequest{
		Name: "Create Only Import", NodeId: destination.Id, Port: 25690,
		ArchiveUploadId: session.ID, RequestId: "create-only-archive",
	}))
	require.NoError(t, err)
	require.NotNil(t, created.Msg.Server)
	require.Equal(t, "archive-dest", created.Msg.Server.NodeId)
	require.Equal(t, "1.21.1", created.Msg.Server.McVersion)
}
