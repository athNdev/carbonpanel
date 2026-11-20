// SPDX-License-Identifier: MPL-2.0

package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/buildinfo"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/serverarchive"
	"github.com/athNdev/carbon-panel/pkg/download"
	"github.com/athNdev/carbon-panel/pkg/files"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"google.golang.org/protobuf/proto"
)

func (s *ServerService) SetServerArchiveDownloads(downloads *download.Manager) {
	s.archiveDownloads = downloads
}

// ExportServerArchive snapshots stopped server data through the owning node.
// The binary download handler reauthorizes access to the resulting session.
func (s *ServerService) ExportServerArchive(ctx context.Context, req *connect.Request[v1.ExportServerArchiveRequest]) (*connect.Response[v1.ExportServerArchiveResponse], error) {
	msg := req.Msg
	var format serverarchive.Format
	switch msg.GetFormat() {
	case v1.ServerArchiveFormat_SERVER_ARCHIVE_FORMAT_ZIP:
		format = serverarchive.ZIP
	case v1.ServerArchiveFormat_SERVER_ARCHIVE_FORMAT_TAR_GZ:
		format = serverarchive.TarGzip
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("select ZIP or tar.gz"))
	}
	if msg.GetConsistency() != v1.ServerArchiveConsistency_SERVER_ARCHIVE_CONSISTENCY_STOPPED {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("select stopped-server consistency"))
	}
	if s.archiveDownloads == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("server archive downloads are unavailable"))
	}

	ctx, cancel := s.beginArchiveRequest(ctx)
	defer cancel()

	transition, err := beginServerLifecycleTransition(msg.GetServerId())
	if err != nil {
		return nil, err
	}
	defer transition.Release()

	server, err := s.store.GetServer(ctx, msg.GetServerId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}
	if server.Detached || server.Status != storage.StatusStopped {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("stop the managed server before exporting it"))
	}
	if err := s.verifyArchiveContainerStopped(ctx, server.NodeID, server.ContainerID); err != nil {
		return nil, err
	}

	modules, err := s.store.ListServerModules(ctx, server.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not verify server modules before export"))
	}
	for _, module := range modules {
		if module.Detached || module.Status != storage.ModuleStatusStopped {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("stop attached modules before exporting the server"))
		}
		if err := s.verifyArchiveContainerStopped(ctx, module.NodeID, module.ContainerID); err != nil {
			return nil, err
		}
	}

	// Validate the mapped path without creating a missing directory on a shared
	// mount. For transport-only storage, the accessor is the sole reader.
	root, err := dataRootFor(ctx, s.store, server, false, s.storageResolver)
	if err != nil {
		return nil, storageError(err)
	}
	if unsafeWorldExport(root) || unsafeWorldExport(server.DataPath) ||
		sameArchiveRoot(root, s.config.Storage.DataDir) ||
		sameArchiveRoot(root, filepath.Join(s.config.Storage.DataDir, "servers")) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("server data directory is unavailable"))
	}
	info, err := storageNodeInfo(ctx, s.store, server.NodeID)
	if err != nil {
		return nil, storageError(err)
	}
	if !info.IsLocal && (sameArchiveRoot(root, info.StorageRoot) || sameArchiveRoot(root, info.ControllerStorageRoot)) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("server data directory must be below the storage root"))
	}
	tree, err := s.storageResolver.ForExisting(ctx, info, server.DataPath)
	if err != nil {
		return nil, storageError(err)
	}
	defer tree.Close()

	tempDir := s.archiveDownloads.TempDir()
	if !nodestorage.RequiresRemoteTransport(info) && files.WithinReal(root, tempDir) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("download staging overlaps the server data directory"))
	}
	tmp, err := os.CreateTemp(tempDir, "server-archive-*")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not stage the server archive"))
	}
	path := tmp.Name()
	published := false
	defer func() {
		_ = tmp.Close()
		if !published {
			_ = os.Remove(path)
		}
	}()

	runtime := archiveRuntime(server, len(modules) != 0)
	if err := serverarchive.Write(ctx, tree, tmp, format, runtime, buildinfo.Version); err != nil {
		s.log.Error("Export server archive %s: %v", server.ID, err)
		if ctx.Err() != nil {
			return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("could not archive the server data; check the controller log"))
	}
	stat, err := tmp.Stat()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not verify the server archive file"))
	}
	if err := tmp.Close(); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not finish the server archive"))
	}
	filename := fmt.Sprintf("%s-server.%s", files.SanitizePathName(server.Name), format)
	session := s.archiveDownloads.InitSessionFor(path, filename, stat.Size(), true, server.ID, uploadOwnerID(ctx))
	published = true

	return connect.NewResponse(&v1.ExportServerArchiveResponse{
		SessionId: session.ID,
		Filename:  filename,
		TotalSize: stat.Size(),
	}), nil
}

func archiveRuntime(server *storage.Server, hasModules bool) serverarchive.Runtime {
	loader := server.ModLoader
	if loader == "" {
		loader = storage.ModLoaderVanilla
	}
	runtime := serverarchive.Runtime{
		MinecraftVersion:      server.MCVersion,
		ModLoader:             string(loader),
		LoaderVersion:         server.LoaderVersion,
		MemoryMB:              server.Memory,
		MaxPlayers:            server.MaxPlayers,
		RequiresCustomRuntime: loader == storage.ModLoaderCustom || proto.Size(server.DockerOverrides) != 0,
		RequiresProviderSetup: hasModules,
	}
	runtime.RequiresProviderSetup = runtime.RequiresProviderSetup || providerArchiveLoader(loader)
	image, err := docker.ResolveServerImage(server.DockerImage)
	defaultImage, _ := docker.ResolveServerImage(docker.GetOptimalDockerTag(server.MCVersion, loader, false))
	if server.DockerImage != "" && (err != nil || image != defaultImage) {
		runtime.RequiresCustomRuntime = true
	}
	return runtime
}

func sameArchiveRoot(a, b string) bool {
	return b != "" && filepath.Clean(a) == filepath.Clean(b)
}

func (s *ServerService) verifyArchiveContainerStopped(ctx context.Context, nodeID, containerID string) error {
	if containerID == "" {
		return nil
	}
	cli, err := s.getDockerClientStrict(nodeID)
	if err != nil || cli == nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("owning node is unavailable"))
	}
	status, err := cli.GetContainerStatus(ctx, containerID)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("could not verify container state on the owning node"))
	}
	if status != storage.StatusStopped {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the owning node reports that a container is still active"))
	}
	return nil
}

type archiveDeadlineContextKey struct{}

// WithArchiveResponseDeadline lets the mounted Connect handler extend the
// HTTP write deadline only after it identifies an archive operation.
func WithArchiveResponseDeadline(ctx context.Context, extend func(time.Time) error) context.Context {
	return context.WithValue(ctx, archiveDeadlineContextKey{}, extend)
}

func (s *ServerService) beginArchiveRequest(ctx context.Context) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(time.Hour)
	if extend, ok := ctx.Value(archiveDeadlineContextKey{}).(func(time.Time) error); ok {
		if err := extend(deadline); err != nil {
			s.log.Warn("Set server archive response deadline: %v", err)
		}
	}
	return context.WithDeadline(ctx, deadline)
}
