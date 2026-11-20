// SPDX-License-Identifier: MPL-2.0

package services

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/internal/serverarchive"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"google.golang.org/protobuf/proto"
)

func (s *ServerService) SetServerArchiveUploads(uploads *upload.Manager) {
	s.archiveUploads = uploads
}

type archiveImport struct {
	file       *os.File
	verified   serverarchive.Archive
	replayKey  string
	replayKind string
}

func (s *ServerService) PreviewServerArchive(ctx context.Context, req *connect.Request[v1.PreviewServerArchiveRequest]) (*connect.Response[v1.PreviewServerArchiveResponse], error) {
	ctx, cancel := s.beginArchiveRequest(ctx)
	defer cancel()
	imported, err := s.openServerArchive(ctx, req.Msg.GetUploadId())
	if err != nil {
		return nil, err
	}
	defer func() { _ = imported.file.Close() }()

	manifest := imported.verified.Manifest
	var format v1.ServerArchiveFormat
	switch imported.verified.Format {
	case serverarchive.ZIP:
		format = v1.ServerArchiveFormat_SERVER_ARCHIVE_FORMAT_ZIP
	case serverarchive.TarGzip:
		format = v1.ServerArchiveFormat_SERVER_ARCHIVE_FORMAT_TAR_GZ
	}
	runtime := manifest.Runtime
	out := &v1.PreviewServerArchiveResponse{
		Format:          format,
		SchemaVersion:   int32(manifest.SchemaVersion),
		ExporterVersion: manifest.ExporterVersion,
		McVersion:       runtime.MinecraftVersion,
		ModLoader:       dbModLoaderToProto(storage.ModLoader(runtime.ModLoader)),
		LoaderVersion:   runtime.LoaderVersion,
		Memory:          int32(runtime.MemoryMB),
		MaxPlayers:      int32(runtime.MaxPlayers),
		FileCount:       int32(len(manifest.Files)),
		ExpandedBytes:   imported.verified.ExpandedBytes,
		RequiredBytes:   imported.requiredBytes(),
		Findings:        archiveRuntimeFindings(runtime),
	}
	if nodeID := req.Msg.GetNodeId(); nodeID != "" {
		free, err := s.archiveAvailableSpace(ctx, nodeID)
		switch {
		case err != nil:
			out.Findings = append(out.Findings, archiveBlocker(v1.ServerArchiveFindingCode_SERVER_ARCHIVE_FINDING_CODE_STORAGE_UNAVAILABLE,
				"Destination node storage is unavailable. Check the node and its storage root, or select another node."))
		default:
			out.AvailableBytes = &free
			if free < out.RequiredBytes {
				out.Findings = append(out.Findings, archiveBlocker(v1.ServerArchiveFindingCode_SERVER_ARCHIVE_FINDING_CODE_INSUFFICIENT_SPACE,
					"The destination node has insufficient free storage to import this archive."))
			}
		}
	}
	return connect.NewResponse(out), nil
}

func archiveRuntimeFindings(runtime serverarchive.Runtime) []*v1.ServerArchiveFinding {
	var findings []*v1.ServerArchiveFinding
	loader := storage.ModLoader(runtime.ModLoader)
	if protoModLoaderToDB(dbModLoaderToProto(loader)) != loader {
		findings = append(findings, archiveBlocker(v1.ServerArchiveFindingCode_SERVER_ARCHIVE_FINDING_CODE_UNSUPPORTED_LOADER,
			"The archive uses a loader that this controller does not support."))
	}
	if runtime.RequiresProviderSetup || providerArchiveLoader(loader) {
		findings = append(findings, archiveBlocker(v1.ServerArchiveFindingCode_SERVER_ARCHIVE_FINDING_CODE_PROVIDER_SETUP,
			"The source depends on a managed modpack provider or attached module setup. This archive cannot recreate those dependencies."))
	}
	if runtime.RequiresCustomRuntime || loader == storage.ModLoaderCustom {
		findings = append(findings, archiveBlocker(v1.ServerArchiveFindingCode_SERVER_ARCHIVE_FINDING_CODE_CUSTOM_RUNTIME,
			"The source used an image or runtime override that must be recreated separately. This archive cannot provision it."))
	}
	return findings
}

func providerArchiveLoader(loader storage.ModLoader) bool {
	switch loader {
	case storage.ModLoaderAutoCurseForge, storage.ModLoaderCurseForge,
		storage.ModLoaderFTBA, storage.ModLoaderModrinth:
		return true
	default:
		return false
	}
}

func archiveBlocker(code v1.ServerArchiveFindingCode, message string) *v1.ServerArchiveFinding {
	return &v1.ServerArchiveFinding{
		Code:     code,
		Severity: v1.ServerArchiveFindingSeverity_SERVER_ARCHIVE_FINDING_SEVERITY_BLOCKER,
		Message:  message,
	}
}

func (s *ServerService) openServerArchive(ctx context.Context, uploadID string) (*archiveImport, error) {
	if strings.TrimSpace(uploadID) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a completed upload is required"))
	}
	if s.archiveUploads == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("server archive uploads are unavailable"))
	}
	path, _, err := s.archiveUploads.GetOwnedTempPathFor(uploadID, uploadOwnerID(ctx))
	if err != nil {
		if denied := ownerMismatchError(err); denied != nil {
			return nil, denied
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("upload is incomplete, expired or unavailable"))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("completed upload is unavailable; upload the archive again"))
	}
	verified, err := serverarchive.Inspect(ctx, file)
	if err != nil {
		_ = file.Close()
		switch {
		case errors.Is(err, context.Canceled):
			return nil, connect.NewError(connect.CodeCanceled, err)
		case errors.Is(err, context.DeadlineExceeded):
			return nil, connect.NewError(connect.CodeDeadlineExceeded, err)
		default:
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid server archive: %w", err))
		}
	}
	return &archiveImport{file: file, verified: verified}, nil
}

func (i *archiveImport) requiredBytes() int64 {
	// Allow a filesystem block per file plus room for directory metadata and
	// first-boot writes. It is a preflight estimate, not a disk reservation.
	return i.verified.ExpandedBytes + int64(len(i.verified.Manifest.Files))*4096 + 64<<20
}

func (s *ServerService) archiveAvailableSpace(ctx context.Context, nodeID string) (int64, error) {
	info, err := s.archiveNode(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	parent := filepath.Join(s.config.Storage.DataDir, "servers")
	root, err := s.storageResolver.ForAllocation(ctx, info, parent)
	if errors.Is(err, os.ErrNotExist) && info.IsLocal {
		// A fresh controller can preview before its first server directory.
		root, err = s.storageResolver.ForAllocation(ctx, info, s.config.Storage.DataDir)
	}
	if err != nil {
		return 0, err
	}
	defer func() { _ = root.Close() }()
	return root.FreeBytes()
}

func (s *ServerService) archiveNode(ctx context.Context, nodeID string) (nodestorage.NodeInfo, error) {
	node, err := s.store.GetNode(ctx, nodeID)
	if err != nil || !node.Enabled {
		return nodestorage.NodeInfo{}, errors.New("destination node is disabled or unavailable")
	}
	return node.StorageInfo(), nil
}

// prepareArchiveImport is read-only. It resolves a prior submission before
// touching the upload so retries work after upload expiry.
func (s *ServerService) prepareArchiveImport(ctx context.Context, req *v1.CreateServerRequest) (*archiveImport, *storage.Server, error) {
	requestID := strings.TrimSpace(req.GetRequestId())
	if requestID == "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("request_id is required for server archive import"))
	}
	if err := operation.ValidateRequestID(requestID); err != nil {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid server creation request"))
	}
	replayKey := fmt.Sprintf("%x", sha256.Sum256([]byte("server-archive-create\x00"+uploadOwnerID(ctx)+"\x00"+requestID)))
	replayKind := fmt.Sprintf("server-archive-create:%x", sha256.Sum256(body))
	existingID, err := s.store.LookupIdempotencyKey(ctx, replayKey, replayKind)
	if err != nil {
		return nil, nil, archiveReplayError(err)
	}
	if existingID != "" {
		prior, err := s.archiveReplay(ctx, existingID)
		return nil, prior, err
	}

	imported, err := s.openServerArchive(ctx, req.GetArchiveUploadId())
	if err != nil {
		return nil, nil, err
	}
	if err := applyArchiveDefaults(req, imported.verified.Manifest.Runtime); err != nil {
		_ = imported.file.Close()
		return nil, nil, err
	}
	imported.replayKey = replayKey
	imported.replayKind = replayKind
	return imported, nil, nil
}

func applyArchiveDefaults(req *v1.CreateServerRequest, runtime serverarchive.Runtime) error {
	if findings := archiveRuntimeFindings(runtime); len(findings) != 0 {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New(findings[0].Message))
	}
	loader := dbModLoaderToProto(storage.ModLoader(runtime.ModLoader))
	if (req.McVersion != "" && req.McVersion != runtime.MinecraftVersion) ||
		(req.ModLoader != v1.ModLoader_MOD_LOADER_UNSPECIFIED && req.ModLoader != loader) ||
		(req.LoaderVersion != "" && req.LoaderVersion != runtime.LoaderVersion) {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("keep the archive's Minecraft and loader versions when importing"))
	}
	if req.ModpackId != "" || req.ModpackVersionId != "" || req.Detached ||
		req.DockerImage != "" || proto.Size(req.DockerOverrides) != 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("server archives require the managed runtime and cannot be combined with a modpack or Docker overrides"))
	}
	req.McVersion = runtime.MinecraftVersion
	req.ModLoader = loader
	req.LoaderVersion = runtime.LoaderVersion
	req.Name = strings.TrimSpace(req.GetName())
	if req.Name == "" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("name is required"))
	}
	if req.Memory == 0 {
		req.Memory = int32(runtime.MemoryMB)
	}
	if req.MaxPlayers == 0 {
		req.MaxPlayers = int32(runtime.MaxPlayers)
	}
	if req.Memory < 0 || req.MaxPlayers < 0 {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("memory and player limit must be positive"))
	}
	return nil
}

func archiveReplayError(err error) error {
	if errors.Is(err, storage.ErrRequestIDReused) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("request_id already refers to a different server archive submission"))
	}
	return connect.NewError(connect.CodeInternal, errors.New("could not look up the server archive submission"))
}

func (s *ServerService) archiveReplay(ctx context.Context, id string) (*storage.Server, error) {
	prior, err := s.store.GetServer(ctx, id)
	if err != nil {
		return nil, connect.NewError(connect.CodeAborted, errors.New("server archive creation is already in progress or the original server was removed"))
	}
	return prior, nil
}

func (s *ServerService) placeArchive(ctx context.Context, imported *archiveImport, server *storage.Server) (func(), error) {
	info, err := s.archiveNode(ctx, server.NodeID)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, err)
	}
	parent := filepath.Dir(server.DataPath)
	if info.IsLocal {
		if err := os.MkdirAll(parent, 0o755); err != nil {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("could not open local server storage"))
		}
	}
	root, err := s.storageResolver.ForAllocation(ctx, info, parent)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("destination node storage is unavailable"))
	}
	defer func() { _ = root.Close() }()
	free, err := root.FreeBytes()
	if err != nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("could not verify free space on the destination node"))
	}
	if free < imported.requiredBytes() {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("insufficient free space on the destination node"))
	}
	child := filepath.Base(server.DataPath)
	if err := root.MkdirExclusive(child); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("destination directory already exists"))
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("could not allocate a fresh destination directory"))
	}

	remove := func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		cleanup, err := s.storageResolver.ForAllocation(ctx, info, parent)
		if err == nil {
			defer func() { _ = cleanup.Close() }()
			err = cleanup.RemoveAll(child)
		}
		if err != nil {
			s.log.Error("Cleanup for incomplete archive server %s: %v", server.ID, err)
		}
	}
	if err := serverarchive.CopyTo(ctx, imported.file, imported.verified, root, child); err != nil {
		_ = root.Close()
		remove()
		s.log.Error("Import archive into newly allocated server %s: %v", server.ID, err)
		if ctx.Err() != nil {
			return nil, connect.NewError(connect.CodeCanceled, ctx.Err())
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("could not restore verified data to the destination node; check storage and try again"))
	}
	return remove, nil
}

func archiveNameError(err error) error {
	if errors.Is(err, storage.ErrServerNameInUse) {
		return connect.NewError(connect.CodeAlreadyExists, err)
	}
	return connect.NewError(connect.CodeInternal, errors.New("could not commit the new server"))
}
