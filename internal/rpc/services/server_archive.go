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
	"github.com/athNdev/carbon-panel/internal/module"
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

	// Module containers are mutated by paths that never take the server
	// transition (create, start, update, hooks and their async workers). The
	// module gate is the choke point all of them use. Hold it for the entire
	// read so no panel-driven module lifecycle can start a writer mid-export.
	moduleFence, err := module.TryBeginServerModuleArchive(msg.GetServerId())
	if err != nil {
		message := "a module lifecycle operation is in progress; wait for it to finish, then export again"
		if errors.Is(err, module.ErrServerDataWrite) {
			message = "a panel file write is in progress; wait for it to finish, then export again"
		}
		return nil, connect.NewError(connect.CodeAborted, errors.New(message))
	}
	defer moduleFence.Release()

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
	var serverConfig *storage.ServerConfig
	if len(modules) > 0 {
		serverConfig, err = s.store.GetServerConfig(ctx, server.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("could not verify attached module mounts before export"))
		}
	}
	for _, module := range modules {
		if err := s.verifyArchiveModule(ctx, module, server, serverConfig, archiveSiblingModules(modules, module.ID)); err != nil {
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
	defer func() { _ = tree.Close() }()

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

// archiveContainerObservation is the live state the export consumes from the
// owning node: container status plus the daemon's effective bind mounts.
type archiveContainerObservation struct {
	Status storage.ServerStatus
	Mounts []docker.ContainerBindMount
}

func (s *ServerService) observeArchiveContainer(ctx context.Context, nodeID, containerID string) (archiveContainerObservation, error) {
	if s.inspectArchiveContainer != nil {
		return s.inspectArchiveContainer(ctx, nodeID, containerID)
	}
	cli, err := s.getDockerClientStrict(nodeID)
	if err != nil || cli == nil {
		if err == nil {
			err = errors.New("no Docker client available")
		}
		return archiveContainerObservation{}, err
	}
	status, err := cli.GetContainerStatus(ctx, containerID)
	if err != nil {
		return archiveContainerObservation{}, err
	}
	mounts, err := cli.ContainerBindMounts(ctx, containerID)
	if err != nil {
		return archiveContainerObservation{}, err
	}
	return archiveContainerObservation{Status: status, Mounts: mounts}, nil
}

func (s *ServerService) verifyArchiveContainerStopped(ctx context.Context, nodeID, containerID string) error {
	if containerID == "" {
		return nil
	}
	observation, err := s.observeArchiveContainer(ctx, nodeID, containerID)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("could not verify container state on the owning node"))
	}
	if observation.Status != storage.StatusStopped {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the owning node reports that a container is still active"))
	}
	return nil
}

func archiveSiblingModules(modules []*storage.Module, currentID string) map[string]*storage.Module {
	siblings := make(map[string]*storage.Module, len(modules))
	for _, module := range modules {
		if module == nil || module.ID == currentID {
			continue
		}
		siblings[module.Name] = module
	}
	return siblings
}

// verifyArchiveModule proves the module cannot write the server data directory
// and that the live container agrees with the stored mount configuration. A
// stopped status alone is not enough: an asynchronous recreate may have left
// an older, writable mount set on the daemon.
func (s *ServerService) verifyArchiveModule(ctx context.Context, module *storage.Module, server *storage.Server, serverConfig *storage.ServerConfig, siblings map[string]*storage.Module) error {
	if module == nil {
		return nil
	}
	if module.Detached || module.Status != storage.ModuleStatusStopped {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("stop attached modules before exporting the server"))
	}

	writable, err := docker.WritableModuleDataSources(module, server, serverConfig, s.config)
	if err != nil {
		s.log.Error("Verify module %s mounts before archive export: %v", module.ID, err)
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("could not verify attached module mounts before export"))
	}
	if len(writable) > 0 {
		return archiveModuleWritableError(module.Name)
	}
	if module.ContainerID == "" {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("module %q has no recorded container, so its effective mounts cannot be verified; recreate or remove it before exporting", module.Name))
	}

	cli, err := s.getDockerClientStrict(module.NodeID)
	if err != nil || cli == nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("owning node is unavailable"))
	}
	expected, err := cli.ModuleDataMounts(module, server, serverConfig, s.config, siblings)
	if err != nil {
		s.log.Error("Resolve module %s effective mounts before archive export: %v", module.ID, err)
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("could not verify attached module mounts before export"))
	}
	for _, mount := range expected {
		if !mount.ReadOnly {
			return archiveModuleWritableError(module.Name)
		}
	}

	observation, err := s.observeArchiveContainer(ctx, module.NodeID, module.ContainerID)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("could not verify the module container on the owning node"))
	}
	if observation.Status != storage.StatusStopped {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the owning node reports that a container is still active"))
	}
	dataRoot, ok := cli.ServerDataHostPath(server.DataPath)
	if !ok {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("could not resolve the server data directory on the module node"))
	}
	var actual []docker.ContainerBindMount
	for _, mount := range observation.Mounts {
		// A mount whose source is an ancestor of the data directory can write
		// it just as effectively as a nested mount. Include both directions in
		// the proven overlap so a writable ancestor is refused.
		if archiveMountOverlapsDataRoot(dataRoot, mount.Source) {
			actual = append(actual, mount)
		}
	}
	for _, mount := range actual {
		if !mount.ReadOnly {
			return archiveModuleWritableError(module.Name)
		}
	}
	if err := compareArchiveModuleMounts(expected, actual); err != nil {
		return connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("module %q: %w", module.Name, err))
	}
	return nil
}

func archiveModuleWritableError(name string) error {
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Errorf("module %q can still write the server data directory; make its bind mounts read-only and recreate it, or remove it, before exporting", name))
}

// compareArchiveModuleMounts compares the daemon's effective data-directory
// mounts with the mounts the stored configuration would create. Any extra,
// missing or read-only-flipped mount is an uncertain attachment state.
func compareArchiveModuleMounts(expected []docker.ModuleBindMount, actual []docker.ContainerBindMount) error {
	want := make(map[string]struct{}, len(expected))
	for _, mount := range expected {
		want[archiveMountKey(mount.Source, mount.Target, mount.ReadOnly)] = struct{}{}
	}
	got := make(map[string]struct{}, len(actual))
	for _, mount := range actual {
		got[archiveMountKey(mount.Source, mount.Target, mount.ReadOnly)] = struct{}{}
	}
	if len(want) != len(got) {
		return errors.New("the container's effective data-directory mounts do not match the stored module configuration; recreate the module so the configuration is applied, then export")
	}
	for key := range want {
		if _, ok := got[key]; !ok {
			return errors.New("the container's effective data-directory mounts do not match the stored module configuration; recreate the module so the configuration is applied, then export")
		}
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			return errors.New("the container's effective data-directory mounts do not match the stored module configuration; recreate the module so the configuration is applied, then export")
		}
	}
	return nil
}

func archiveMountOverlapsDataRoot(dataRoot, source string) bool {
	return docker.DataPathOverlaps(dataRoot, source)
}

func archiveMountKey(source, target string, readOnly bool) string {
	mode := "rw"
	if readOnly {
		mode = "ro"
	}
	return filepath.ToSlash(filepath.Clean(source)) + "\x00" + filepath.ToSlash(filepath.Clean(target)) + "\x00" + mode
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
