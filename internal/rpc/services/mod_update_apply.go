// SPDX-License-Identifier: MPL-2.0
package services

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/internal/minecraft"
	"github.com/athNdev/carbon-panel/internal/worldlib/providers"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const maxModUpdateBytes = 256 << 20

// modCandidateFetcher re-verifies a reviewed provider release and returns its
// exact bytes. It is injected so the RPC and the scheduled-policy executor can
// share one operation without either owning a global provider client.
type modCandidateFetcher interface {
	Fetch(ctx context.Context, settings *storage.ServerConfig, versionID, filename, sha512Hex, loader, game string) ([]byte, string, error)
}

// modUpdateVersionLookup is the narrow provider capability the fetcher needs.
// The real Modrinth client satisfies it; tests inject a fixture.
type modUpdateVersionLookup interface {
	GetVersion(context.Context, string) (*modrinth.Version, error)
}

// providerModCandidateFetcher uses the configured Modrinth adapter. A candidate
// must still be listed, released, compatible and carry the reviewed SHA-512.
// Optional fields let the same code be exercised against an HTTP fixture.
type providerModCandidateFetcher struct {
	versions modUpdateVersionLookup
	client   *http.Client
}

func (f providerModCandidateFetcher) Fetch(ctx context.Context, settings *storage.ServerConfig, versionID, filename, sha512Hex, loader, game string) ([]byte, string, error) {
	lookup := f.versions
	if lookup == nil {
		lookup = modUpdateProvider(settings)
	}
	version, err := lookup.GetVersion(ctx, versionID)
	if err != nil || version == nil || version.ID == "" {
		return nil, "refused_provider", fmt.Errorf("provider did not confirm release %s", versionID)
	}
	if version.Status != "listed" || version.VersionType != "release" ||
		!slices.Contains(version.Loaders, loader) || !slices.Contains(version.GameVersions, game) {
		return nil, "refused_provider", errors.New("release is no longer listed and compatible")
	}
	want, err := hex.DecodeString(sha512Hex)
	if err != nil || len(want) != sha512.Size {
		return nil, "refused_provider", errors.New("reviewed checksum is not a SHA-512 digest")
	}
	var target *modrinth.File
	for i := range version.Files {
		file := &version.Files[i]
		if filepath.Base(file.Filename) == filename && strings.EqualFold(file.Hashes.SHA512, sha512Hex) {
			if target != nil {
				return nil, "refused_provider", errors.New("release has ambiguous reviewed files")
			}
			target = file
		}
	}
	if target == nil || !providers.DownloadURLIsAllowed(target.URL) {
		return nil, "refused_download", errors.New("no allowed download URL for the reviewed file")
	}
	client := f.client
	if client == nil {
		client = indexers.ProtectCredentialsOnRedirect(&http.Client{Timeout: 2 * time.Minute})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL, nil)
	if err != nil {
		return nil, "refused_download", err
	}
	if agent := providerUserAgent(settings); agent != "" {
		req.Header.Set("User-Agent", agent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "refused_download", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, "refused_download", fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxModUpdateBytes+1))
	if err != nil {
		return nil, "refused_download", err
	}
	if len(data) > maxModUpdateBytes || len(data) == 0 {
		return nil, "refused_download", errors.New("candidate file exceeds the update limit or is empty")
	}
	sum := sha512.Sum512(data)
	if !bytes.Equal(sum[:], want) {
		return nil, "refused_checksum", errors.New("candidate bytes do not match the reviewed SHA-512")
	}
	return data, "", nil
}

func providerUserAgent(settings *storage.ServerConfig) string {
	if settings != nil && settings.ModrinthUserAgent != nil && *settings.ModrinthUserAgent != "" {
		return *settings.ModrinthUserAgent
	}
	return "CarbonPanel/1.0 (CarbonPanel-admin)"
}

// SetModCandidateFetcher replaces the provider download path. The composition
// root uses this for policy wiring and tests inject a fixture.
func (s *ModService) SetModCandidateFetcher(fetcher modCandidateFetcher) {
	s.candidateFetcher = fetcher
}

// stagedModUpdate holds the neutral outcome used by both the RPC and the
// scheduled executor. It is converted to proto only at the transport boundary.
type stagedModUpdate struct {
	ModID    string
	FileName string
	StagedID string
	Outcome  string
	Reason   string
}

// stageReviewedModUpdates re-verifies the installed fingerprint and the
// provider release, then writes candidate bytes into the open batch through
// the existing node-owned staging path. The live mods directory is untouched.
func (s *ModService) stageReviewedModUpdates(ctx context.Context, serverID string, selections []*v1.ModUpdateSelection, window v1.ConfigApplyWindow, applyAtUnix int64, idleMinutes int32) ([]stagedModUpdate, *storage.ServerUpdateBatch, error) {
	if len(selections) == 0 {
		return nil, nil, errors.New("no mod updates selected")
	}
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, nil, fmt.Errorf("server not found: %w", err)
	}
	dataRoot, err := dataRootFor(ctx, s.store, server, false, s.storageResolver)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(dataRoot)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = root.Close() }()
	modsDir := minecraft.GetModsPath(dataRoot, server.ModLoader)
	if modsDir == "" {
		return nil, nil, errors.New("this server type does not support mods")
	}
	base, err := filepath.Rel(dataRoot, modsDir)
	if err != nil {
		return nil, nil, err
	}
	loader := string(server.ModLoader)
	if isModpackPlatformLoader(server.ModLoader) {
		loader = string(server.ModpackLoader)
	}
	if loader == "" || server.MCVersion == "" || strings.EqualFold(server.MCVersion, "latest") {
		return nil, nil, errors.New("a known game version and loader are required")
	}
	settings, err := s.store.GetServerConfig(ctx, storage.GlobalSettingsID)
	if err != nil && !errors.Is(err, storage.ErrServerConfigNotFound) {
		return nil, nil, fmt.Errorf("failed to load provider settings: %w", err)
	}
	fetcher := s.candidateFetcher
	if fetcher == nil {
		fetcher = providerModCandidateFetcher{}
	}

	// Validate the window before any provider work or byte writes. An invalid
	// at_time/idle request must not leave staged bytes or an open batch behind.
	windowName, applyAt, idle := windowFromProto(window, applyAtUnix, idleMinutes)
	if err := storage.ValidateUpdateWindow(windowName, applyAt, idle); err != nil {
		return nil, nil, err
	}

	// One live target can appear only once in the open batch, regardless of
	// whether it was selected manually or by a policy run.
	alreadyStaged := make(map[string]bool)
	if batch, err := s.store.OpenUpdateBatch(ctx, server.ID); err == nil && batch != nil {
		if rows, err := s.store.ListBatchContent(ctx, batch.ID); err == nil {
			for _, row := range rows {
				if row.Kind == storage.StagedKindMod {
					alreadyStaged[row.FileName] = true
				}
			}
		}
	}

	results := make([]stagedModUpdate, 0, len(selections))
	staged := 0
	for _, sel := range selections {
		result := stagedModUpdate{ModID: sel.GetModId(), FileName: sel.GetFileName()}
		if alreadyStaged[sel.GetFileName()] {
			result.Outcome, result.Reason = "already_staged", "this file already has an update in the open batch"
			results = append(results, result)
			continue
		}
		installed, err := modInstallPath(base, sel.GetFileName())
		if err != nil || sel.GetInstalledSha512() == "" {
			result.Outcome, result.Reason = "refused_drift", "the reviewed file is not a valid installed mod"
			results = append(results, result)
			continue
		}
		if _, err := root.Lstat(installed); err != nil {
			installed, err = modInstallPath(base+"_disabled", sel.GetFileName())
		}
		hash, _, err := installedModHash(ctx, root, installed, maxModUpdateBytes)
		if err != nil || !strings.EqualFold(hash, sel.GetInstalledSha512()) {
			result.Outcome, result.Reason = "refused_drift", "installed bytes changed since the review; check again"
			results = append(results, result)
			continue
		}
		if _, err := modInstallPath(base, sel.GetCandidateFilename()); err != nil {
			result.Outcome, result.Reason = "refused_provider", "the reviewed candidate filename is not a bare JAR name"
			results = append(results, result)
			continue
		}
		data, outcome, err := fetcher.Fetch(ctx, settings, sel.GetCandidateVersionId(), sel.GetCandidateFilename(), sel.GetCandidateSha512(), loader, server.MCVersion)
		if err != nil {
			result.Outcome, result.Reason = outcome, err.Error()
			results = append(results, result)
			continue
		}
		row, err := stageContentBytes(ctx, s.store, server, s.storageResolver,
			storage.StagedKindMod, storage.StagedActionReplace,
			filepath.ToSlash(installed), sel.GetFileName(), data)
		if err != nil {
			result.Outcome, result.Reason = "error", err.Error()
			results = append(results, result)
			continue
		}
		result.Outcome, result.StagedID = "staged", row.ID
		alreadyStaged[sel.GetFileName()] = true
		staged++
		results = append(results, result)
	}
	if staged == 0 {
		return results, nil, nil
	}

	batch, err := s.store.SetOpenBatchWindow(ctx, server.ID, windowName, applyAt, idle)
	if err != nil {
		return results, nil, fmt.Errorf("failed to set the apply window: %w", err)
	}
	return results, batch, nil
}

func windowFromProto(window v1.ConfigApplyWindow, applyAtUnix int64, idleMinutes int32) (storage.UpdateWindow, *time.Time, int) {
	var applyAt *time.Time
	if applyAtUnix > 0 {
		value := time.Unix(applyAtUnix, 0).UTC()
		applyAt = &value
	}
	switch window {
	case v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME:
		return storage.UpdateWindowAtTime, applyAt, 0
	case v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE:
		return storage.UpdateWindowIdle, nil, int(idleMinutes)
	default:
		return storage.UpdateWindowNextRestart, nil, 0
	}
}
