// SPDX-License-Identifier: MPL-2.0

package services

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/internal/serverarchive"
	"github.com/athNdev/carbon-panel/pkg/files"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"google.golang.org/protobuf/proto"
)

// keyedMutex serializes operations that share a key without blocking other
// keys. Entries are dropped when the last holder releases them.
type keyedMutex struct {
	mu      sync.Mutex
	entries map[string]*keyedMutexEntry
}

type keyedMutexEntry struct {
	mu   sync.Mutex
	refs int
}

func (k *keyedMutex) Lock(key string) func() {
	k.mu.Lock()
	if k.entries == nil {
		k.entries = make(map[string]*keyedMutexEntry)
	}
	entry := k.entries[key]
	if entry == nil {
		entry = &keyedMutexEntry{}
		k.entries[key] = entry
	}
	entry.refs++
	k.mu.Unlock()

	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		k.mu.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(k.entries, key)
		}
		k.mu.Unlock()
	}
}

func (s *ServerService) SetServerArchiveUploads(uploads *upload.Manager) {
	s.archiveUploads = uploads
}

type archiveImport struct {
	file       *os.File
	verified   serverarchive.Archive
	digest     string
	replayKey  string
	replayKind string
	// claim is set when a prior submission left a durable destination. The
	// caller must recover it (or prove it absent) before placing fresh data.
	claim *storage.ArchiveImport
}

// archiveContentIdentity is a deterministic digest of the independently
// verified archive content: format, manifest (including per-file SHA-256),
// expanded size and the allowlisted gameplay settings. Upload ids and byte
// layout are deliberately excluded so a fresh upload of identical content is
// accepted after a restart, while changed content is refused.
func archiveContentIdentity(verified serverarchive.Archive) (string, error) {
	body, err := json.Marshal(struct {
		Format        serverarchive.Format
		Manifest      serverarchive.Manifest
		ExpandedBytes int64
		Gameplay      serverarchive.Gameplay
	}{
		Format:        verified.Format,
		Manifest:      verified.Manifest,
		ExpandedBytes: verified.ExpandedBytes,
		Gameplay:      verified.Gameplay,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(body)), nil
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
	digest, err := archiveContentIdentity(verified)
	if err != nil {
		_ = file.Close()
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not identify the server archive content"))
	}
	return &archiveImport{file: file, verified: verified, digest: digest}, nil
}

// verifyArchiveClaimIdentity refuses a replacement upload unless it matches the
// archive identity recorded with the durable submission.
func verifyArchiveClaimIdentity(imported *archiveImport, intent *storage.ArchiveImport) error {
	if imported == nil || intent == nil {
		return connect.NewError(connect.CodeInternal, errors.New("archive import identity is unavailable"))
	}
	if intent.ArchiveDigest == "" {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("an interrupted archive submission has no recorded content identity; use a new request id and inspect the destination node's server storage"))
	}
	if imported.digest == "" || imported.digest != intent.ArchiveDigest {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("the uploaded archive does not match the interrupted submission; upload the original archive or use a new request id"))
	}
	return nil
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

// prepareArchiveImport is read-only with respect to the destination. It
// resolves a committed prior submission before touching the upload so retries
// work after upload expiry, and returns the durable intent of an interrupted
// submission so the caller can prove and reclaim only its own destination.
func (s *ServerService) prepareArchiveImport(ctx context.Context, req *v1.CreateServerRequest) (*archiveImport, *storage.Server, error) {
	requestID := strings.TrimSpace(req.GetRequestId())
	if requestID == "" {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("request_id is required for server archive import"))
	}
	if err := operation.ValidateRequestID(requestID); err != nil {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	// The upload session is not durable across a controller restart. Bind the
	// replay identity to everything except the upload id so the same
	// submission id can be retried with a freshly uploaded copy of the archive.
	hashed := proto.CloneOf(req)
	hashed.ArchiveUploadId = ""
	body, err := proto.MarshalOptions{Deterministic: true}.Marshal(hashed)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid server creation request"))
	}
	legacyBody, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return nil, nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid server creation request"))
	}
	replayKey := fmt.Sprintf("%x", sha256.Sum256([]byte("server-archive-create\x00"+uploadOwnerID(ctx)+"\x00"+requestID)))
	replayKind := fmt.Sprintf("server-archive-create:%x", sha256.Sum256(body))
	legacyKind := fmt.Sprintf("server-archive-create:%x", sha256.Sum256(legacyBody))
	existingID, err := s.store.LookupIdempotencyKey(ctx, replayKey, replayKind)
	if err != nil {
		if !errors.Is(err, storage.ErrRequestIDReused) {
			return nil, nil, archiveReplayError(err)
		}
		// Before durable archive intents, the replay identity included the
		// upload id. Keep a committed server replayable across that upgrade
		// without accepting a genuinely different submission.
		record, recordErr := s.store.GetIdempotencyKey(ctx, replayKey)
		if recordErr != nil || record == nil || record.Kind != legacyKind {
			return nil, nil, archiveReplayError(err)
		}
		existingID = record.ServerID
	}
	if existingID != "" {
		exists, existsErr := s.store.ServerExists(ctx, existingID)
		if existsErr != nil {
			return nil, nil, connect.NewError(connect.CodeInternal, errors.New("could not verify the previous server archive submission"))
		}
		if exists {
			prior, getErr := s.store.GetServer(ctx, existingID)
			if getErr != nil {
				return nil, nil, connect.NewError(connect.CodeInternal, errors.New("could not read the previous server archive submission"))
			}
			s.settleCommittedArchiveImport(ctx, replayKey)
			return nil, prior, nil
		}
		intent, intentErr := s.store.GetArchiveImport(ctx, replayKey)
		if intentErr != nil {
			if errors.Is(intentErr, storage.ErrArchiveImportUnproven) {
				return nil, nil, connect.NewError(connect.CodeFailedPrecondition,
					errors.New("an earlier submission was interrupted before its destination was recorded, so the controller cannot prove which files it created; use a new request id and inspect the destination node's server storage"))
			}
			return nil, nil, connect.NewError(connect.CodeInternal, errors.New("could not look up the interrupted server archive submission"))
		}
		if intent.ServerID != existingID || intent.Kind != replayKind {
			return nil, nil, connect.NewError(connect.CodeFailedPrecondition,
				errors.New("an interrupted server archive submission does not match this request; use a new request id"))
		}
		return &archiveImport{replayKey: replayKey, replayKind: replayKind, claim: intent}, nil, nil
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

// committedArchiveServer distinguishes a committed row from a database
// failure. Recovery must never proceed on an unreadable database.
func (s *ServerService) committedArchiveServer(ctx context.Context, id string) (*storage.Server, bool, error) {
	exists, err := s.store.ServerExists(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	server, err := s.store.GetServer(ctx, id)
	if err != nil {
		return nil, false, err
	}
	return server, true, nil
}

// settleCommittedArchiveImport clears a marker left by a crash between the
// server row commit and the download handoff. A committed server's data is
// never removed, even when its marker survives.
func (s *ServerService) settleCommittedArchiveImport(ctx context.Context, replayKey string) {
	intent, err := s.store.GetArchiveImport(ctx, replayKey)
	if err != nil {
		return
	}
	exists, err := s.store.ServerExists(ctx, intent.ServerID)
	if err != nil || !exists {
		return
	}
	if err := s.clearArchiveMarker(ctx, intent); err == nil {
		_ = s.store.DeleteArchiveImport(ctx, replayKey)
	}
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

// archiveGameplaySeed copies validated archive settings into the initial
// config. Missing values fall through to the panel defaults; destination RCON,
// ports, routing, auth, image and ownership are never derived from the archive.
func archiveGameplaySeed(gameplay serverarchive.Gameplay) storage.ArchiveGameplay {
	seed := storage.ArchiveGameplay{
		LevelName:  gameplay.LevelName,
		Difficulty: gameplay.Difficulty,
		GameMode:   gameplay.GameMode,
	}
	if gameplay.SyncChunkWrites != nil {
		value := *gameplay.SyncChunkWrites
		seed.SyncChunkWrites = &value
	}
	if gameplay.PauseWhenEmptySeconds != nil {
		value := *gameplay.PauseWhenEmptySeconds
		seed.PauseWhenEmptySeconds = &value
	}
	return seed
}

func archiveReplayError(err error) error {
	if errors.Is(err, storage.ErrRequestIDReused) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("request_id already refers to a different server archive submission"))
	}
	return connect.NewError(connect.CodeInternal, errors.New("could not look up the server archive submission"))
}

func (s *ServerService) placeArchive(ctx context.Context, imported *archiveImport, server *storage.Server) (func() error, error) {
	if imported == nil || imported.claim == nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("archive import has no durable destination"))
	}
	intent := imported.claim
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
	// Record the exact validated allocation root before creating anything.
	// Cleanup after a restart must resolve this same root; a changed node
	// storage root is a visible mismatch, never a redirect.
	if err := s.store.SetArchiveImportAllocation(ctx, intent.RequestKey, server.NodeID, root.Root()); err != nil {
		s.log.Error("Record archive allocation root for server %s: %v", server.ID, err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not record the archive destination"))
	}
	intent.StorageRoot = root.Root()
	child := filepath.Base(server.DataPath)
	if err := root.MkdirExclusive(child); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("destination directory already exists"))
		}
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("could not allocate a fresh destination directory"))
	}
	// The marker is the durable proof that this submission, not another
	// process, created the directory. It is written before any archive bytes
	// and is excluded from the archive format.
	cleanup := func() error {
		return s.removeArchiveDestination(ctx, intent, true)
	}
	if err := root.WriteFile(path.Join(child, archiveOwnershipMarker), []byte(intent.ClaimToken+"\n")); err != nil {
		s.log.Error("Write archive ownership marker for server %s: %v", server.ID, err)
		if removeErr := cleanup(); removeErr != nil {
			return cleanup, connect.NewError(connect.CodeInternal, errors.New("could not prepare the destination directory; retry the submission"))
		}
		return nil, connect.NewError(connect.CodeInternal, errors.New("could not prepare the destination directory"))
	}
	if err := s.store.SetArchiveImportPhase(ctx, intent.RequestKey, storage.ArchiveImportStaged); err != nil {
		s.log.Error("Record archive destination for server %s: %v", server.ID, err)
		return cleanup, connect.NewError(connect.CodeInternal, errors.New("could not record the archive destination"))
	}
	if err := serverarchive.CopyTo(ctx, imported.file, imported.verified, root, child); err != nil {
		s.log.Error("Import archive into newly allocated server %s: %v", server.ID, err)
		if ctx.Err() != nil {
			return cleanup, connect.NewError(connect.CodeCanceled, ctx.Err())
		}
		return cleanup, connect.NewError(connect.CodeFailedPrecondition, errors.New("could not restore verified data to the destination node; check storage and try again"))
	}
	return cleanup, nil
}

const archiveOwnershipMarker = ".carbon-archive-import"

// errArchiveDestinationUnproven means a directory exists at the claimed
// destination but its marker does not prove this submission created it.
var errArchiveDestinationUnproven = errors.New("archive destination ownership cannot be proven")

// archiveDataPath is the canonical destination for a new server. Keeping the
// formula in one place lets a retry resume the exact path recorded durably.
func (s *ServerService) archiveDataPath(name, serverID string) string {
	return filepath.Join(s.config.Storage.DataDir, "servers", fmt.Sprintf("%s_%s", files.SanitizePathName(name), serverID))
}

// recoverArchiveImport reclaims a destination allocated by an interrupted
// submission. It removes files only when the durable intent, the exact path
// and the ownership marker all agree. A committed server row always wins:
// recovery never removes data of a server that already exists.
func (s *ServerService) recoverArchiveImport(ctx context.Context, intent *storage.ArchiveImport) error {
	if intent == nil {
		return nil
	}
	exists, err := s.store.ServerExists(ctx, intent.ServerID)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, errors.New("could not verify whether the archive destination was already committed"))
	}
	if exists {
		return nil
	}
	// A committed intent is durable publication evidence even when the server
	// row is gone, for example after a normal keep_data delete whose marker
	// cleanup was interrupted. Its data belongs to the operator and must never
	// be reclaimed by a retry.
	if intent.Phase == storage.ArchiveImportCommitted {
		return connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this archive submission was already published and its retained server data must not be reclaimed; use a new request id or remove the retained data explicitly"))
	}
	if intent.NodeID == "" {
		// No node was recorded, so placeArchive could not have allocated a
		// destination for this intent.
		return nil
	}
	if err := s.removeArchiveDestination(ctx, intent, false); err != nil {
		if errors.Is(err, errArchiveDestinationUnproven) {
			return connect.NewError(connect.CodeFailedPrecondition,
				errors.New("an interrupted archive import left a destination that this controller cannot prove it created; inspect the destination node's server storage before retrying"))
		}
		s.log.Error("Reclaim interrupted archive destination for server %s: %v", intent.ServerID, err)
		return connect.NewError(connect.CodeUnavailable, errors.New("could not reclaim the interrupted archive destination"))
	}
	return nil
}

// removeArchiveDestination removes the destination only when justCreated is
// true (this call allocated it) or the durable marker proves ownership.
func (s *ServerService) removeArchiveDestination(ctx context.Context, intent *storage.ArchiveImport, justCreated bool) error {
	if intent == nil || intent.NodeID == "" || intent.DataPath == "" {
		return errArchiveDestinationUnproven
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	info, err := s.archiveNode(cleanupCtx, intent.NodeID)
	if err != nil {
		return err
	}
	parent := filepath.Dir(intent.DataPath)
	if info.IsLocal {
		if _, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	root, err := s.storageResolver.ForAllocation(cleanupCtx, info, parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if intent.StorageRoot == "" || !sameArchiveRoot(root.Root(), intent.StorageRoot) {
		return errArchiveDestinationUnproven
	}
	child := filepath.Base(intent.DataPath)
	if !justCreated {
		exists, proven, err := archiveDestinationProof(root, child, intent.ClaimToken)
		if err != nil {
			return err
		}
		if !exists {
			return nil
		}
		if !proven {
			return errArchiveDestinationUnproven
		}
	}
	return root.RemoveAll(child)
}

// archiveDestinationProof reports whether the directory exists and whether its
// marker matches the durable claim token.
func archiveDestinationProof(root nodestorage.AllocationRoot, child, token string) (bool, bool, error) {
	info, err := root.Stat(child)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if !info.IsDir() {
		return true, false, nil
	}
	data, err := root.ReadFile(path.Join(child, archiveOwnershipMarker))
	if errors.Is(err, os.ErrNotExist) {
		return true, false, nil
	}
	if err != nil {
		return true, false, err
	}
	return true, strings.TrimSpace(string(data)) == token, nil
}

// clearArchiveMarker removes only the marker from a committed server. It never
// removes server data and does nothing when the marker is absent or unproven.
func (s *ServerService) clearArchiveMarker(ctx context.Context, intent *storage.ArchiveImport) error {
	if intent == nil || intent.NodeID == "" || intent.DataPath == "" {
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	info, err := s.archiveNode(cleanupCtx, intent.NodeID)
	if err != nil {
		return err
	}
	parent := filepath.Dir(intent.DataPath)
	if info.IsLocal {
		if _, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	root, err := s.storageResolver.ForAllocation(cleanupCtx, info, parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if intent.StorageRoot == "" || !sameArchiveRoot(root.Root(), intent.StorageRoot) {
		return errArchiveDestinationUnproven
	}
	child := filepath.Base(intent.DataPath)
	exists, proven, err := archiveDestinationProof(root, child, intent.ClaimToken)
	if err != nil {
		return err
	}
	if !exists || !proven {
		return nil
	}
	return root.Remove(path.Join(child, archiveOwnershipMarker))
}

// finishArchiveImport clears the marker of an intent whose committed phase was
// written in the same transaction as the server row. The intent is retained if
// marker cleanup fails so a replay can retry it; it never downgrades the phase.
func (s *ServerService) finishArchiveImport(ctx context.Context, intent *storage.ArchiveImport) {
	if intent == nil {
		return
	}
	bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.clearArchiveMarker(bgCtx, intent); err != nil {
		s.log.Error("Clear archive ownership marker for committed server %s: %v", intent.ServerID, err)
		return
	}
	if err := s.store.DeleteArchiveImport(bgCtx, intent.RequestKey); err != nil {
		s.log.Error("Delete committed archive import %s: %v", intent.ServerID, err)
	}
}

func archiveNameError(err error) error {
	if errors.Is(err, storage.ErrServerNameInUse) {
		return connect.NewError(connect.CodeAlreadyExists, err)
	}
	return connect.NewError(connect.CodeInternal, errors.New("could not commit the new server"))
}
