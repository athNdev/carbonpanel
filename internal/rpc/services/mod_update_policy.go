// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/minecraft"
	"github.com/athNdev/carbon-panel/internal/scheduler"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/google/uuid"
)

// StageAvailableUpdates implements the scheduler-facing half of OSS-425. It
// reuses the exact preview and staging operations as the manual review flow;
// nothing here publishes bytes or restarts a server.
func (s *ModService) StageAvailableUpdates(ctx context.Context, serverID string, cfg storage.ModUpdateTaskConfig) (int, int, []string, error) {
	if !cfg.Enabled {
		return 0, 0, nil, nil
	}
	if err := scheduler.ValidateModUpdateConfig(cfg); err != nil {
		return 0, 0, nil, err
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return 0, 0, nil, errors.New("server not found")
	}
	// This early check avoids provider work when the server is already running.
	// It is not the authoritative decision: a server can start during retry or
	// download, so the same policy is re-checked at journal admission below.
	if cfg.SkipWhenRunning && server.Status == storage.StatusRunning {
		return 0, 0, []string{errModUpdateRunningSkipped.Error()}, nil
	}
	runtimeServer, err := s.reviewedModRuntimeServer(ctx, server)
	if err != nil {
		return 0, 0, nil, err
	}
	dataRoot, err := dataRootFor(ctx, s.store, server, false, s.storageResolver)
	if err != nil {
		return 0, 0, nil, err
	}
	modsDir := minecraft.GetModsPath(dataRoot, runtimeServer.ModLoader)
	if modsDir == "" {
		return 0, 0, nil, errors.New("this server type does not support mods")
	}
	mods, err := installedModRows(ctx, serverID, runtimeServer, modsDir)
	if err != nil {
		return 0, 0, nil, err
	}
	if len(mods) == 0 {
		return 0, 0, nil, nil
	}
	batch, err := s.store.OpenUpdateBatch(ctx, serverID)
	if err != nil {
		return 0, 0, nil, err
	}
	if batch != nil {
		rows, err := s.store.ListBatchContent(ctx, batch.ID)
		if err != nil {
			return 0, 0, nil, err
		}
		alreadyStaged := make(map[string]bool, len(rows))
		for _, row := range rows {
			if row.Kind == storage.StagedKindMod {
				alreadyStaged[row.FileName] = true
			}
		}
		mods = slices.DeleteFunc(mods, func(mod *v1.Mod) bool { return alreadyStaged[mod.GetFileName()] })
		if len(mods) == 0 {
			return 0, 0, nil, nil
		}
	}
	settings, err := s.store.GetServerConfig(ctx, storage.GlobalSettingsID)
	if err != nil && !errors.Is(err, storage.ErrServerConfigNotFound) {
		return 0, 0, nil, errors.New("failed to load provider settings")
	}
	lookup := s.updateLookup
	if lookup == nil {
		lookup = modUpdateProvider(settings)
	}
	if err := previewServerModUpdates(ctx, dataRoot, modsDir, runtimeServer, mods, lookup); err != nil {
		return 0, 0, nil, err
	}

	excluded := make(map[string]bool, len(cfg.ExcludedProjects))
	for _, id := range cfg.ExcludedProjects {
		excluded[id] = true
	}
	limit := cfg.MaxUpdatesPerRun
	if limit <= 0 {
		limit = scheduler.DefaultModUpdatesPerRun
	}

	selections := make([]*v1.ModUpdateSelection, 0, limit)
	refused := 0
	var reasons []string
	var providerFailure error
	for _, mod := range mods {
		preview := mod.GetUpdatePreview()
		if preview == nil || preview.GetState() != "available" || preview.GetCandidateSha512() == "" {
			refused++
			if preview != nil {
				reason := preview.GetReason()
				if strings.HasPrefix(reason, providerLookupPrefix) {
					providerFailure = errors.New(reason)
				}
				if reason != "" && len(reasons) < 5 {
					reasons = append(reasons, mod.GetFileName()+": "+reason)
				}
			}
			continue
		}
		if excluded[preview.GetProjectId()] {
			refused++
			reasons = append(reasons, mod.GetFileName()+": excluded")
			continue
		}
		if len(selections) >= limit {
			refused++
			reasons = append(reasons, mod.GetFileName()+": per-run limit reached")
			continue
		}
		selections = append(selections, &v1.ModUpdateSelection{
			ModId:              mod.GetId(),
			FileName:           mod.GetFileName(),
			InstalledSha512:    preview.GetInstalledSha512(),
			CandidateVersionId: preview.GetCandidateVersionId(),
			CandidateSha512:    preview.GetCandidateSha512(),
			CandidateFilename:  preview.GetCandidateFilename(),
		})
	}
	if len(selections) == 0 {
		return 0, refused, reasons, providerFailure
	}

	window := v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART
	switch strings.ToLower(strings.TrimSpace(cfg.Window)) {
	case "idle":
		window = v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE
	}
	results, _, transient, err := s.stageReviewedModUpdates(ctx, serverID, selections, window, 0, int32(cfg.IdleMinutes),
		func(_ context.Context, current *storage.Server) error {
			if cfg.SkipWhenRunning && current.Status == storage.StatusRunning {
				return errModUpdateRunningSkipped
			}
			return nil
		})
	if errors.Is(err, errModUpdateRunningSkipped) {
		return 0, 0, []string{errModUpdateRunningSkipped.Error()}, nil
	}
	staged := 0
	for _, result := range results {
		switch result.Outcome {
		case "staged":
			staged++
		case "already_staged", "already_selected":
			// The same file is already represented by another staged row or selection.
		default:
			refused++
			reasons = append(reasons, result.FileName+": "+result.Outcome+" — "+result.Reason)
		}
	}
	return staged, refused, reasons, errors.Join(providerFailure, transient, err)
}

// installedModRows mirrors the deterministic IDs used by ListMods so a policy
// can select the same installed files an operator sees in the Mods tab.
func installedModRows(ctx context.Context, serverID string, server *storage.Server, modsDir string) ([]*v1.Mod, error) {
	mods := make([]*v1.Mod, 0)
	for _, dir := range []struct {
		path    string
		enabled bool
	}{{modsDir, true}, {modsDir + "_disabled", false}} {
		info, err := os.Stat(dir.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read installed mods directory: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("read installed mods directory: %s is not a directory", dir.path)
		}
		entries, err := os.ReadDir(dir.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read installed mods directory: %w", err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if entry.IsDir() || !minecraft.IsValidModFile(entry.Name(), server.ModLoader) {
				continue
			}
			if _, err := entry.Info(); errors.Is(err, os.ErrNotExist) {
				continue
			} else if err != nil {
				return nil, fmt.Errorf("read installed mod %q: %w", entry.Name(), err)
			}
			mods = append(mods, &v1.Mod{
				Id:       uuid.NewSHA1(uuid.NameSpaceURL, []byte(serverID+entry.Name())).String(),
				ServerId: serverID,
				FileName: entry.Name(),
				Enabled:  dir.enabled,
			})
		}
	}
	return mods, nil
}

// compile-time confirmation that the service satisfies the scheduler seam.
var _ interface {
	StageAvailableUpdates(context.Context, string, storage.ModUpdateTaskConfig) (int, int, []string, error)
} = (*ModService)(nil)
