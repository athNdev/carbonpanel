// SPDX-License-Identifier: MIT AND MPL-2.0
// This file includes DiscoPanel material under the MIT License,
// Copyright (c) 2025 Nicholas W. Heyer. The MIT license text is in LICENSE
// and still applies to that inherited material.
// Maintainer changes are licensed under the Mozilla Public License, v. 2.0.
// If a copy of the MPL was not distributed with this file, You can obtain
// one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/indexers"
	"github.com/athNdev/carbon-panel/internal/minecraft"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldlib/providers"
	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/athNdev/carbon-panel/pkg/upload"
	"github.com/athNdev/carbon-panel/pkg/utils"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Compile-time check that ModService implements the interface
var _ carbonpanelv1connect.ModServiceHandler = (*ModService)(nil)

// ModService implements the Mod service
type ModService struct {
	store            *storage.Store
	docker           *docker.Client
	log              *logger.Logger
	uploadManager    *upload.Manager
	storageResolver  *nodestorage.Resolver
	candidateFetcher modCandidateFetcher
	updateLookup     modUpdateLookup
	updateBudget     int64
	// optimizationMu serializes Fabric optimization installs per server so two
	// requests cannot interleave old-jar cleanup. optimizationFetch and
	// optimizationClient are fixture seams for the same install path.
	optimizationMu     keyedMutex
	optimizationFetch  func(context.Context, string, string) (*modrinthVersionItem, error)
	optimizationClient *http.Client
}

// SetModUpdateBatchBudget overrides the cumulative candidate-byte budget. It
// exists for bounded-space fixtures; production uses the package default.
func (s *ModService) SetModUpdateBatchBudget(budget int64) {
	s.updateBudget = budget
}

// SetModUpdateLookup replaces the provider lookup used by scheduled policies.
// Manual preview still uses the configured global provider adapter.
func (s *ModService) SetModUpdateLookup(lookup modUpdateLookup) {
	s.updateLookup = lookup
}

// SetStorageResolver supplies the instance-owned node storage resolver.
func (s *ModService) SetStorageResolver(resolver *nodestorage.Resolver) {
	s.storageResolver = resolver
}

func modInstallPath(modsDir, filename string) (string, error) {
	if filename == "" || filename == "." || filename == ".." || filepath.IsAbs(filename) ||
		filepath.VolumeName(filename) != "" || strings.ContainsAny(filename, `/\`) ||
		filepath.Base(filename) != filename {
		return "", fmt.Errorf("invalid mod filename %q", filename)
	}
	return filepath.Join(modsDir, filename), nil
}

// NewModService creates a new mod service
func NewModService(store *storage.Store, docker *docker.Client, uploadManager *upload.Manager, log *logger.Logger) *ModService {
	return &ModService{
		store:         store,
		docker:        docker,
		log:           log,
		uploadManager: uploadManager,
	}
}

// auditMod records a mod operation in the Activity tab, failures included.
// Mod failures used to surface only as toasts, leaving no trace of what was
// tried. Audit failures are logged, never returned.
func (s *ModService) auditMod(ctx context.Context, ip, event, serverID string, props map[string]any) {
	actorID, actorName := "", ""
	if u := auth.GetUserFromContext(ctx); u != nil {
		actorID, actorName = u.ID, u.Username
	}
	propsJSON, _ := json.Marshal(props)
	if err := activity.Log(s.store.DB(), activity.Entry{
		ActorID:    actorID,
		ActorName:  actorName,
		IP:         ip,
		Event:      event,
		SubjectTyp: "server",
		SubjectID:  serverID,
		Properties: string(propsJSON),
	}); err != nil {
		s.log.Error("Audit log failed for %s: %v", event, err)
	}
}

// ListMods lists mods for a server
func (s *ModService) ListMods(ctx context.Context, req *connect.Request[v1.ListModsRequest]) (*connect.Response[v1.ListModsResponse], error) {
	msg := req.Msg

	// Get server to find data path and mod loader
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	var runtimeErr error
	if msg.CheckUpdates {
		runtimeServer, err := s.reviewedModRuntimeServer(ctx, server)
		if err == nil {
			server = runtimeServer
		} else if !errors.Is(err, storage.ErrReviewedModRuntimeUnknown) {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		runtimeErr = err
	}

	root, err := dataRootFor(ctx, s.store, server, false, s.storageResolver)
	if err != nil {
		return nil, storageError(err)
	}

	// Get the mods directory path
	modsDir := minecraft.GetModsPath(root, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	// Check if mods directory exists
	mods := []*v1.Mod{}

	// Read mods from active directory
	if files, err := os.ReadDir(modsDir); err == nil {
		for _, file := range files {
			if file.IsDir() {
				continue
			}

			// Check if this is a valid mod file
			if !minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				continue
			}

			info, err := file.Info()
			if err != nil {
				continue
			}

			// Extract display name from filename (remove extension)
			displayName := file.Name()
			if ext := filepath.Ext(displayName); ext != "" {
				displayName = displayName[:len(displayName)-len(ext)]
			}

			// Create mod entry with consistent ID generation
			mod := &v1.Mod{
				Id:          uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+file.Name())).String(),
				ServerId:    msg.ServerId,
				FileName:    file.Name(),
				DisplayName: displayName,
				Enabled:     true,
				FileSize:    info.Size(),
				UploadedAt:  timestamppb.New(info.ModTime()),
			}

			mods = append(mods, mod)
		}
	}

	// Also check disabled mods directory
	disabledDir := modsDir + "_disabled"
	if files, err := os.ReadDir(disabledDir); err == nil {
		for _, file := range files {
			if file.IsDir() {
				continue
			}

			if !minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				continue
			}

			info, err := file.Info()
			if err != nil {
				continue
			}

			// Extract display name from filename
			displayName := file.Name()
			if ext := filepath.Ext(displayName); ext != "" {
				displayName = displayName[:len(displayName)-len(ext)]
			}

			mod := &v1.Mod{
				Id:          uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+file.Name())).String(),
				ServerId:    msg.ServerId,
				FileName:    file.Name(),
				DisplayName: displayName,
				Enabled:     false,
				FileSize:    info.Size(),
				UploadedAt:  timestamppb.New(info.ModTime()),
			}

			mods = append(mods, mod)
		}
	}

	if msg.CheckUpdates && len(mods) != 0 {
		if runtimeErr != nil {
			for _, mod := range mods {
				mod.UpdatePreview = &v1.InstalledModUpdate{State: "review_required", Reason: runtimeErr.Error()}
			}
		} else {
			settings, err := s.store.GetServerConfig(ctx, storage.GlobalSettingsID)
			if err != nil && !errors.Is(err, storage.ErrServerConfigNotFound) {
				return nil, connect.NewError(connect.CodeInternal, errors.New("failed to load provider settings"))
			}
			lookup := s.updateLookup
			if lookup == nil {
				lookup = modUpdateProvider(settings)
			}
			if err := previewServerModUpdates(ctx, root, modsDir, server, mods, lookup); err != nil {
				return nil, err
			}
		}
	}
	return connect.NewResponse(&v1.ListModsResponse{
		Mods: mods,
	}), nil
}

// GetMod gets a specific mod
func (s *ModService) GetMod(ctx context.Context, req *connect.Request[v1.GetModRequest]) (*connect.Response[v1.GetModResponse], error) {
	msg := req.Msg

	// Get server to validate and find mod
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	root, err := dataRootFor(ctx, s.store, server, false, s.storageResolver)
	if err != nil {
		return nil, storageError(err)
	}

	// Get the mods directory path
	modsDir := minecraft.GetModsPath(root, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	// Try to find the mod file in active directory
	if files, err := os.ReadDir(modsDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				// Generate the same ID as in ListMods to match
				fileID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+file.Name())).String()
				if fileID == msg.ModId {
					info, _ := file.Info()

					displayName := file.Name()
					if ext := filepath.Ext(displayName); ext != "" {
						displayName = displayName[:len(displayName)-len(ext)]
					}

					return connect.NewResponse(&v1.GetModResponse{
						Mod: &v1.Mod{
							Id:          fileID,
							ServerId:    msg.ServerId,
							FileName:    file.Name(),
							DisplayName: displayName,
							Enabled:     true,
							FileSize:    info.Size(),
							UploadedAt:  timestamppb.New(info.ModTime()),
						},
					}), nil
				}
			}
		}
	}

	// Try disabled directory
	disabledDir := modsDir + "_disabled"
	if files, err := os.ReadDir(disabledDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				fileID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+file.Name())).String()
				if fileID == msg.ModId {
					info, _ := file.Info()

					displayName := file.Name()
					if ext := filepath.Ext(displayName); ext != "" {
						displayName = displayName[:len(displayName)-len(ext)]
					}

					return connect.NewResponse(&v1.GetModResponse{
						Mod: &v1.Mod{
							Id:          fileID,
							ServerId:    msg.ServerId,
							FileName:    file.Name(),
							DisplayName: displayName,
							Enabled:     false,
							FileSize:    info.Size(),
							UploadedAt:  timestamppb.New(info.ModTime()),
						},
					}), nil
				}
			}
		}
	}

	return nil, connect.NewError(connect.CodeNotFound, errors.New("mod not found"))
}

// ImportUploadedMod imports a mod
func (s *ModService) ImportUploadedMod(ctx context.Context, req *connect.Request[v1.ImportUploadedModRequest]) (*connect.Response[v1.ImportUploadedModResponse], error) {
	msg := req.Msg
	peerIP := req.Peer().Addr

	// Validate upload session
	if msg.UploadSessionId == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload_session_id is required"))
	}

	// Get server to find data path and mod loader
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	// Get temp file path and original filename from upload manager. Ownership
	// is enforced here as well: knowing a session ID is not enough to import
	// another user's upload.
	ownerID := uploadOwnerID(ctx)
	tempPath, originalFilename, err := s.uploadManager.GetTempPathFor(msg.UploadSessionId, ownerID)
	if err != nil {
		if denied := ownerMismatchError(err); denied != nil {
			return nil, denied
		}
		s.log.Error("Failed to get upload session: %v", err)
		s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "error": "upload session not found or not completed"})
		return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found or not completed"))
	}

	// Validate file is appropriate for this mod loader
	if !minecraft.IsValidModFile(originalFilename, server.ModLoader) {
		s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "file": originalFilename, "error": "invalid file type for this mod loader"})
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid file type for this mod loader"))
	}

	// Reject client-only mods before installing: they crash a dedicated server
	// at class load. Only a confirmed client descriptor is rejected; unknown
	// metadata (Forge/NeoForge) is left to the runtime.
	if side, sideErr := minecraft.InspectModSide(tempPath); sideErr == nil && side == minecraft.SideClient {
		_ = os.Remove(tempPath)
		_ = s.uploadManager.CleanupSessionFor(msg.UploadSessionId, uploadOwnerID(ctx))
		s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "file": originalFilename, "error": "client-only mod rejected"})
		return nil, connect.NewError(
			connect.CodeInvalidArgument,
			errors.New("this mod is client-only and cannot run on a dedicated server"),
		)
	}

	if msg.GetStage() {
		stageResp, stageErr := s.stageUploadedMod(ctx, msg, server, tempPath, originalFilename)
		if stageErr != nil {
			s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "staged": true, "file": originalFilename, "error": stageErr.Error()})
			return nil, stageErr
		}
		s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "staged": true, "file": originalFilename})
		return stageResp, nil
	}

	root, err := dataRootFor(ctx, s.store, server, true, s.storageResolver)
	if err != nil {
		return nil, storageError(err)
	}

	// Get the correct mods directory based on mod loader
	modsDir := minecraft.GetModsPath(root, server.ModLoader)
	if modsDir == "" {
		s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "file": originalFilename, "error": "server type does not support mods"})
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	// Create mods directory if needed
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		s.log.Error("Failed to create mods directory: %v", err)
		s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "file": originalFilename, "error": "failed to create mods directory"})
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create mods directory"))
	}

	// Move file from temp location to mods dir
	modPath := filepath.Join(modsDir, originalFilename)
	if err := os.Rename(tempPath, modPath); err != nil {
		if err := files.CopyFile(tempPath, modPath); err != nil {
			s.log.Error("Failed to move mod file: %v", err)
			s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "file": originalFilename, "error": "failed to save mod"})
			return nil, connect.NewError(connect.CodeInternal, errors.New("failed to save mod"))
		}
		os.Remove(tempPath)
	}

	// Cleanup the upload session
	_ = s.uploadManager.CleanupSessionFor(msg.UploadSessionId, ownerID)

	// Get file info for the response
	info, err := os.Stat(modPath)
	if err != nil {
		s.log.Error("Failed to stat mod file: %v", err)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get mod info"))
	}

	// Use provided display name or derive from filename
	displayName := msg.DisplayName
	if displayName == "" {
		displayName = originalFilename
		if ext := filepath.Ext(displayName); ext != "" {
			displayName = displayName[:len(displayName)-len(ext)]
		}
	}

	// Create mod record
	mod := &v1.Mod{
		Id:          uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+originalFilename)).String(),
		ServerId:    msg.ServerId,
		FileName:    originalFilename,
		DisplayName: displayName,
		Description: msg.Description,
		Enabled:     true,
		FileSize:    info.Size(),
		UploadedAt:  timestamppb.New(info.ModTime()),
	}

	s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"method": "upload", "file": originalFilename, "display_name": displayName})
	return connect.NewResponse(&v1.ImportUploadedModResponse{
		Mod:     mod,
		Message: "Mod uploaded successfully",
	}), nil
}

// stageUploadedMod keeps a validated jar out of the live mods directory.
func (s *ModService) stageUploadedMod(ctx context.Context, msg *v1.ImportUploadedModRequest, server *storage.Server, tempPath, originalFilename string) (*connect.Response[v1.ImportUploadedModResponse], error) {
	info := minecraft.GetModLoaderInfo(server.ModLoader)
	if info.ModsDirectory == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}
	if _, err := modInstallPath(info.ModsDirectory, originalFilename); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	data, err := os.ReadFile(tempPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to read uploaded mod"))
	}
	fileInfo, err := os.Stat(tempPath)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to get mod info"))
	}
	target := info.ModsDirectory + "/" + filepath.Base(originalFilename)
	if _, err := stageContentBytes(ctx, s.store, server, s.storageResolver, storage.StagedKindMod, storage.StagedActionInstall, target, originalFilename, data); err != nil {
		return nil, stageContentError(err)
	}
	_ = os.Remove(tempPath)
	_ = s.uploadManager.CleanupSessionFor(msg.UploadSessionId, uploadOwnerID(ctx))

	displayName := msg.DisplayName
	if displayName == "" {
		displayName = originalFilename
		if ext := filepath.Ext(displayName); ext != "" {
			displayName = displayName[:len(displayName)-len(ext)]
		}
	}
	return connect.NewResponse(&v1.ImportUploadedModResponse{
		Mod: &v1.Mod{
			Id:          uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+originalFilename)).String(),
			ServerId:    msg.ServerId,
			FileName:    originalFilename,
			DisplayName: displayName,
			Description: msg.Description,
			Enabled:     true,
			FileSize:    fileInfo.Size(),
			UploadedAt:  timestamppb.New(fileInfo.ModTime()),
		},
		Message: "Mod staged. The server was not changed.",
		Staged:  true,
	}), nil
}

// UpdateMod updates a mod
func (s *ModService) UpdateMod(ctx context.Context, req *connect.Request[v1.UpdateModRequest]) (*connect.Response[v1.UpdateModResponse], error) {
	msg := req.Msg
	peerIP := req.Peer().Addr

	// OSS-424 manual apply. The same procedure and mods/update[server_id] scope
	// handles a reviewed provider update. Staging re-verifies installed bytes
	// and the provider release; the live mods directory is not written here.
	if len(msg.GetUpdateSelections()) > 0 {
		results, batch, _, err := s.stageReviewedModUpdates(ctx, msg.ServerId,
			msg.GetUpdateSelections(), msg.GetUpdateWindow(),
			msg.GetUpdateApplyAtUnix(), msg.GetUpdateIdleMinutes())
		if err != nil {
			s.auditMod(ctx, peerIP, activity.EventModUpdate, msg.ServerId, map[string]any{
				"operation": "stage_reviewed_updates",
				"error":     err.Error(),
			})
			code := connect.CodeInternal
			if connect.CodeOf(err) == connect.CodeAborted {
				return nil, err
			}
			if errors.Is(err, errInvalidModUpdateWindow) {
				code = connect.CodeInvalidArgument
			} else if errors.Is(err, storage.ErrUpdateBatchConflict) || errors.Is(err, storage.ErrDuplicateStagedTarget) || errors.Is(err, storage.ErrModpackBatchConflict) ||
				errors.Is(err, storage.ErrStagedModVersionChange) ||
				errors.Is(err, storage.ErrStagedModRuntimeTargetChanged) ||
				errors.Is(err, storage.ErrReviewedModRuntimeUnknown) || errors.Is(err, errReviewedModRuntimeChanged) {
				code = connect.CodeFailedPrecondition
			} else if strings.Contains(err.Error(), "server not found") || strings.Contains(err.Error(), "no mod updates") {
				code = connect.CodeInvalidArgument
			}
			return nil, connect.NewError(code, err)
		}
		response := &v1.UpdateModResponse{}
		if batch != nil {
			response.UpdateBatchId = batch.ID
		}
		for _, result := range results {
			response.StagedUpdates = append(response.StagedUpdates, &v1.StagedModUpdate{
				ModId:    result.ModID,
				FileName: result.FileName,
				StagedId: result.StagedID,
				Outcome:  result.Outcome,
				Reason:   result.Reason,
			})
		}
		s.auditMod(ctx, peerIP, activity.EventModUpdate, msg.ServerId, map[string]any{
			"operation": "stage_reviewed_updates",
			"batch_id":  response.UpdateBatchId,
			"results":   results,
		})
		return connect.NewResponse(response), nil
	}

	// Get server to find mod path
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	releaseWrite, err := beginDataWrite(server.ID)
	if err != nil {
		return nil, err
	}
	defer releaseWrite()

	root, err := dataRootFor(ctx, s.store, server, true, s.storageResolver)
	if err != nil {
		return nil, storageError(err)
	}

	modsDir := minecraft.GetModsPath(root, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	disabledDir := modsDir + "_disabled"

	// Find the mod file
	var modFileName string
	var currentlyEnabled bool
	var modInfo os.FileInfo

	// First, scan the mods directory
	if files, err := os.ReadDir(modsDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				// Generate the same ID as in ListMods to match
				fileID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+file.Name())).String()
				if fileID == msg.ModId {
					modFileName = file.Name()
					currentlyEnabled = true
					modInfo, _ = file.Info()
					break
				}
			}
		}
	}

	// If not found, check disabled directory
	if modFileName == "" {
		if files, err := os.ReadDir(disabledDir); err == nil {
			for _, file := range files {
				if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
					fileID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+file.Name())).String()
					if fileID == msg.ModId {
						modFileName = file.Name()
						currentlyEnabled = false
						modInfo, _ = file.Info()
						break
					}
				}
			}
		}
	}

	if modFileName == "" {
		s.auditMod(ctx, peerIP, activity.EventModUpdate, msg.ServerId, map[string]any{"mod_id": msg.ModId, "error": "mod not found"})
		return nil, connect.NewError(connect.CodeNotFound, errors.New("mod not found"))
	}

	// Handle enabling/disabling
	finalEnabled := currentlyEnabled
	if msg.Enabled != nil && *msg.Enabled != currentlyEnabled {
		if *msg.Enabled {
			// Move from disabled to mods directory
			oldPath := filepath.Join(disabledDir, modFileName)
			newPath := filepath.Join(modsDir, modFileName)
			if err := os.Rename(oldPath, newPath); err != nil {
				s.log.Error("Failed to enable mod: %v", err)
				s.auditMod(ctx, peerIP, activity.EventModUpdate, msg.ServerId, map[string]any{"file": modFileName, "error": "failed to enable mod"})
				return nil, connect.NewError(connect.CodeInternal, errors.New("failed to enable mod"))
			}
			finalEnabled = true
		} else {
			// Move from mods to disabled directory
			os.MkdirAll(disabledDir, 0755)
			oldPath := filepath.Join(modsDir, modFileName)
			newPath := filepath.Join(disabledDir, modFileName)
			if err := os.Rename(oldPath, newPath); err != nil {
				s.log.Error("Failed to disable mod: %v", err)
				s.auditMod(ctx, peerIP, activity.EventModUpdate, msg.ServerId, map[string]any{"file": modFileName, "error": "failed to disable mod"})
				return nil, connect.NewError(connect.CodeInternal, errors.New("failed to disable mod"))
			}
			finalEnabled = false
		}
	}

	// Build response
	displayName := modFileName
	if ext := filepath.Ext(displayName); ext != "" {
		displayName = displayName[:len(displayName)-len(ext)]
	}

	// Use provided display name if given
	if msg.DisplayName != nil && *msg.DisplayName != "" {
		displayName = *msg.DisplayName
	}

	description := ""
	if msg.Description != nil {
		description = *msg.Description
	}

	s.auditMod(ctx, peerIP, activity.EventModUpdate, msg.ServerId, map[string]any{"file": modFileName, "enabled": finalEnabled})
	return connect.NewResponse(&v1.UpdateModResponse{
		Mod: &v1.Mod{
			Id:          msg.ModId,
			ServerId:    msg.ServerId,
			FileName:    modFileName,
			DisplayName: displayName,
			Description: description,
			Enabled:     finalEnabled,
			FileSize:    modInfo.Size(),
			UploadedAt:  timestamppb.New(modInfo.ModTime()),
			UpdatedAt:   timestamppb.Now(),
		},
	}), nil
}

// DeleteMod deletes a mod
func (s *ModService) DeleteMod(ctx context.Context, req *connect.Request[v1.DeleteModRequest]) (*connect.Response[v1.DeleteModResponse], error) {
	msg := req.Msg
	peerIP := req.Peer().Addr

	// Get server to find file path
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	releaseWrite, err := beginDataWrite(server.ID)
	if err != nil {
		return nil, err
	}
	defer releaseWrite()

	root, err := dataRootFor(ctx, s.store, server, true, s.storageResolver)
	if err != nil {
		return nil, storageError(err)
	}

	modsDir := minecraft.GetModsPath(root, server.ModLoader)
	if modsDir == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("this server type does not support mods"))
	}

	// Try to find and delete the mod file
	deleted := false

	// Check active directory
	if files, err := os.ReadDir(modsDir); err == nil {
		for _, file := range files {
			if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
				fileID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+file.Name())).String()
				if fileID == msg.ModId {
					modPath := filepath.Join(modsDir, file.Name())
					if err := os.Remove(modPath); err != nil {
						s.log.Error("Failed to delete mod file: %v", err)
						s.auditMod(ctx, peerIP, activity.EventModRemove, msg.ServerId, map[string]any{"file": file.Name(), "error": "failed to delete mod file"})
						return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete mod file"))
					}
					deleted = true
					break
				}
			}
		}
	}

	// Check disabled directory
	if !deleted {
		disabledDir := modsDir + "_disabled"
		if files, err := os.ReadDir(disabledDir); err == nil {
			for _, file := range files {
				if !file.IsDir() && minecraft.IsValidModFile(file.Name(), server.ModLoader) {
					fileID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(msg.ServerId+file.Name())).String()
					if fileID == msg.ModId {
						modPath := filepath.Join(disabledDir, file.Name())
						if err := os.Remove(modPath); err != nil {
							s.log.Error("Failed to delete mod file: %v", err)
							s.auditMod(ctx, peerIP, activity.EventModRemove, msg.ServerId, map[string]any{"file": file.Name(), "error": "failed to delete mod file"})
							return nil, connect.NewError(connect.CodeInternal, errors.New("failed to delete mod file"))
						}
						deleted = true
						break
					}
				}
			}
		}
	}

	if !deleted {
		s.auditMod(ctx, peerIP, activity.EventModRemove, msg.ServerId, map[string]any{"mod_id": msg.ModId, "error": "mod not found"})
		return nil, connect.NewError(connect.CodeNotFound, errors.New("mod not found"))
	}

	s.auditMod(ctx, peerIP, activity.EventModRemove, msg.ServerId, map[string]any{"mod_id": msg.ModId})
	return connect.NewResponse(&v1.DeleteModResponse{
		Message: "Mod deleted successfully",
	}), nil
}

type modrinthVersionFile struct {
	URL      string `json:"url"`
	Filename string `json:"filename"`
	Primary  bool   `json:"primary"`
}

type modrinthVersionItem struct {
	ID            string                `json:"id"`
	VersionNumber string                `json:"version_number"`
	Files         []modrinthVersionFile `json:"files"`
}

var fabricOptStackDefs = []struct {
	ModID       string
	Name        string
	Description string
	PrefixMatch string
}{
	{ModID: "lithium", Name: "Lithium", Description: "General physics, mob AI, and world ticking optimization", PrefixMatch: "lithium"},
	{ModID: "ferrite-core", Name: "FerriteCore", Description: "Memory usage optimization (reduces RAM overhead)", PrefixMatch: "ferritecore"},
	{ModID: "modernfix", Name: "ModernFix", Description: "All-in-one memory leak, launch time, and allocation optimization", PrefixMatch: "modernfix"},
	{ModID: "c2me-fabric", Name: "C2ME", Description: "Concurrent chunk management and multithreaded world generation", PrefixMatch: "c2me"},
}

const (
	maxFabricOptimizationJarBytes     int64 = 256 << 20
	fabricOptimizationDownloadTimeout       = 2 * time.Minute
)

// guardedProviderHTTPClient is the connection-guarded, redirect-protecting
// client used for provider API and download egress. Static URL validation
// alone is not an SSRF guard; this mirrors the reviewed-mod fetcher and World
// Library import transport.
func guardedProviderHTTPClient(timeout time.Duration) *http.Client {
	return indexers.ProtectCredentialsOnRedirect(utils.NewSafeHTTPClient(timeout, false))
}

func fabricOptimizationHTTPClient() *http.Client {
	return guardedProviderHTTPClient(fabricOptimizationDownloadTimeout)
}

// fabricOptimizationInstallMu serializes standalone optimization installs that
// share one mods directory. InstallFabricOptimizationStack also holds a
// per-server lock across its complete sequence; this inner lock keeps a direct
// install from interleaving with it or with another direct install.
var fabricOptimizationInstallMu keyedMutex

// installFabricOptimizationJar downloads one optimization mod through a
// context-bound, bounded request and only replaces the live jar after the
// complete body has been written to a temporary file in the mods directory. A
// non-200, oversized, truncated or empty response leaves the existing jar in
// place and removes the partial temporary file. The guarded client refuses
// literal-IP, private and disallowed provider targets before any write.
func installFabricOptimizationJar(ctx context.Context, modsDir string, target modrinthVersionFile, prefix string) error {
	return installFabricOptimizationJarWithClient(ctx, modsDir, target, prefix, nil)
}

func installFabricOptimizationJarWithClient(ctx context.Context, modsDir string, target modrinthVersionFile, prefix string, client *http.Client) error {
	release := fabricOptimizationInstallMu.Lock(modsDir)
	defer release()
	targetPath, err := modInstallPath(modsDir, target.Filename)
	if err != nil {
		return err
	}
	if !providers.DownloadURLIsAllowed(target.URL) {
		return errors.New("download URL is not an allowed provider target")
	}
	requestCtx, cancel := context.WithTimeout(ctx, fabricOptimizationDownloadTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.URL, nil)
	if err != nil {
		return err
	}
	if client == nil {
		client = fabricOptimizationHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxFabricOptimizationJarBytes {
		return fmt.Errorf("download exceeds the %d-byte limit", maxFabricOptimizationJarBytes)
	}
	tmp, err := os.CreateTemp(modsDir, ".fabric-optimization-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	installed := false
	defer func() {
		if !installed {
			_ = os.Remove(tmpPath)
		}
	}()
	written, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxFabricOptimizationJarBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return fmt.Errorf("download body: %w", copyErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if written > maxFabricOptimizationJarBytes {
		return fmt.Errorf("download exceeds the %d-byte limit", maxFabricOptimizationJarBytes)
	}
	if written == 0 {
		return errors.New("download was empty")
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return err
	}
	installed = true
	// Old versions are removed only after the replacement is committed, so a
	// failed download cannot strand the server without an installed jar.
	if entries, err := os.ReadDir(modsDir); err == nil {
		for _, entry := range entries {
			if entry.IsDir() || entry.Name() == target.Filename {
				continue
			}
			if strings.Contains(strings.ToLower(entry.Name()), prefix) {
				_ = os.Remove(filepath.Join(modsDir, entry.Name()))
			}
		}
	}
	return nil
}

func fetchModrinthModVersion(ctx context.Context, slug string, mcVersion string) (*modrinthVersionItem, error) {
	loadersJSON := url.QueryEscape(`["fabric"]`)
	gameVersionsJSON := url.QueryEscape(fmt.Sprintf(`["%s"]`, mcVersion))
	reqURL := fmt.Sprintf("https://api.modrinth.com/v2/project/%s/version?loaders=%s&game_versions=%s", slug, loadersJSON, gameVersionsJSON)

	if !providers.DownloadURLIsAllowed(reqURL) {
		return nil, errors.New("modrinth API URL is not an allowed provider target")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "CarbonPanel/1.0 (contact@carbon-panel.local)")

	client := guardedProviderHTTPClient(15 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("modrinth returned status %d", resp.StatusCode)
	}

	var versions []modrinthVersionItem
	if err := json.NewDecoder(resp.Body).Decode(&versions); err != nil {
		return nil, err
	}

	if len(versions) == 0 {
		return nil, fmt.Errorf("no versions found for mc %s", mcVersion)
	}

	return &versions[0], nil
}

// fetchOptimizationVersion keeps the provider seam testable without changing
// the production Modrinth client.
func (s *ModService) fetchOptimizationVersion(ctx context.Context, slug, mcVersion string) (*modrinthVersionItem, error) {
	if s.optimizationFetch != nil {
		return s.optimizationFetch(ctx, slug, mcVersion)
	}
	return fetchModrinthModVersion(ctx, slug, mcVersion)
}

// GetFabricOptimizationStack checks the status and compatibility of Lithium, FerriteCore, ModernFix, and C2ME
func (s *ModService) GetFabricOptimizationStack(ctx context.Context, req *connect.Request[v1.GetFabricOptimizationStackRequest]) (*connect.Response[v1.GetFabricOptimizationStackResponse], error) {
	msg := req.Msg
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	isFabric := server.ModLoader == storage.ModLoaderFabric || server.ModLoader == storage.ModLoaderQuilt
	root, err := dataRootFor(ctx, s.store, server, false, s.storageResolver)
	if err != nil {
		return nil, storageError(err)
	}
	modsDir := minecraft.GetModsPath(root, server.ModLoader)

	// List files in mods directory to detect installed versions
	installedFiles := make(map[string]string)
	if entries, err := os.ReadDir(modsDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jar") {
				lower := strings.ToLower(entry.Name())
				for _, def := range fabricOptStackDefs {
					if strings.Contains(lower, def.PrefixMatch) {
						installedFiles[def.ModID] = entry.Name()
					}
				}
			}
		}
	}

	var resultMods []*v1.FabricOptimizationMod
	for _, def := range fabricOptStackDefs {
		modItem := &v1.FabricOptimizationMod{
			ModId:       def.ModID,
			Name:        def.Name,
			Description: def.Description,
		}

		if fileName, ok := installedFiles[def.ModID]; ok {
			modItem.Installed = true
			modItem.FileName = fileName
			modItem.InstalledVersion = fileName
		}

		if isFabric {
			v, err := fetchModrinthModVersion(ctx, def.ModID, server.MCVersion)
			if err == nil && v != nil && len(v.Files) > 0 {
				modItem.Compatible = true
				modItem.LatestCompatibleVersion = v.VersionNumber
				modItem.DownloadUrl = v.Files[0].URL
				if !modItem.Installed {
					modItem.FileName = v.Files[0].Filename
				}
			} else {
				modItem.Compatible = false
			}
		}

		resultMods = append(resultMods, modItem)
	}

	return connect.NewResponse(&v1.GetFabricOptimizationStackResponse{
		IsFabric:  isFabric,
		McVersion: server.MCVersion,
		Mods:      resultMods,
	}), nil
}

// InstallFabricOptimizationStack installs or updates Lithium, FerriteCore, ModernFix, and/or C2ME
func (s *ModService) InstallFabricOptimizationStack(ctx context.Context, req *connect.Request[v1.InstallFabricOptimizationStackRequest]) (*connect.Response[v1.InstallFabricOptimizationStackResponse], error) {
	msg := req.Msg
	peerIP := req.Peer().Addr
	server, err := s.store.GetServer(ctx, msg.ServerId)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}

	if server.ModLoader != storage.ModLoaderFabric && server.ModLoader != storage.ModLoaderQuilt {
		s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"stack": "fabric-optimization", "error": "server mod loader is not Fabric or Quilt"})
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("server mod loader is not Fabric or Quilt"))
	}

	// Hold the per-server install lock across provider lookup, all downloads
	// and old-jar cleanup so two stacks cannot delete each other's jars. It is
	// taken before the shared data-write lease; no lifecycle or publication
	// path takes this lock, so there is no ordering cycle.
	releaseInstall := s.optimizationMu.Lock(server.ID)
	defer releaseInstall()

	releaseWrite, err := beginDataWrite(server.ID)
	if err != nil {
		return nil, err
	}
	defer releaseWrite()

	root, err := dataRootFor(ctx, s.store, server, true, s.storageResolver)
	if err != nil {
		return nil, storageError(err)
	}

	modsDir := minecraft.GetModsPath(root, server.ModLoader)
	if err := os.MkdirAll(modsDir, 0755); err != nil {
		s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"stack": "fabric-optimization", "error": "failed to create mods directory"})
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("failed to create mods directory: %w", err))
	}

	filter := make(map[string]bool)
	for _, id := range msg.ModIds {
		filter[id] = true
	}

	var installedMods []*v1.FabricOptimizationMod
	var warnings []string

	for _, def := range fabricOptStackDefs {
		if len(filter) > 0 && !filter[def.ModID] {
			continue
		}

		v, err := s.fetchOptimizationVersion(ctx, def.ModID, server.MCVersion)
		if err != nil || v == nil || len(v.Files) == 0 {
			warnings = append(warnings, fmt.Sprintf("%s is not compatible with Minecraft %s: %v", def.Name, server.MCVersion, err))
			continue
		}

		targetFile := v.Files[0]
		var installErr error
		if s.optimizationClient != nil {
			installErr = installFabricOptimizationJarWithClient(ctx, modsDir, targetFile, def.PrefixMatch, s.optimizationClient)
		} else {
			installErr = installFabricOptimizationJar(ctx, modsDir, targetFile, def.PrefixMatch)
		}
		if installErr != nil {
			warnings = append(warnings, fmt.Sprintf("failed to install %s: %v", def.Name, installErr))
			continue
		}

		installedMods = append(installedMods, &v1.FabricOptimizationMod{
			ModId:                   def.ModID,
			Name:                    def.Name,
			Description:             def.Description,
			Installed:               true,
			Compatible:              true,
			InstalledVersion:        v.VersionNumber,
			LatestCompatibleVersion: v.VersionNumber,
			FileName:                targetFile.Filename,
			DownloadUrl:             targetFile.URL,
		})
	}

	installedFiles := make([]string, 0, len(installedMods))
	for _, m := range installedMods {
		installedFiles = append(installedFiles, m.FileName)
	}
	s.auditMod(ctx, peerIP, activity.EventModInstall, msg.ServerId, map[string]any{"stack": "fabric-optimization", "installed": installedFiles, "warnings": warnings})
	return connect.NewResponse(&v1.InstallFabricOptimizationStackResponse{
		InstalledMods: installedMods,
		Warnings:      warnings,
		Message:       fmt.Sprintf("Installed %d Fabric optimization mod(s)", len(installedMods)),
	}), nil
}
