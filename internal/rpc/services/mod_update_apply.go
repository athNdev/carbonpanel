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
	"github.com/athNdev/carbon-panel/internal/operation"
	"github.com/athNdev/carbon-panel/internal/worldlib/providers"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/utils"
)

const (
	maxModUpdateBytes      = 256 << 20
	maxModUpdateBatchBytes = 512 << 20
)

// modCandidateFetcher re-verifies a reviewed provider release and returns its
// exact bytes. It is injected so the RPC and the scheduled-policy executor can
// share one operation without either owning a global provider client.
type modCandidateFetcher interface {
	Fetch(ctx context.Context, settings *storage.ServerConfig, versionID, projectID, filename, sha512Hex, loader, game string, limit int64) ([]byte, string, error)
}

// modUpdateVersionLookup is the narrow provider capability the fetcher needs.
// The real Modrinth client satisfies it; tests inject a fixture.
type modUpdateVersionLookup interface {
	GetVersion(context.Context, string) (*modrinth.Version, error)
}

// providerModCandidateFetcher uses the configured Modrinth adapter. A candidate
// must still be listed, released, compatible, belong to the reviewed project
// and carry the reviewed SHA-512. Optional fields let the same code be
// exercised against an HTTP fixture.
type providerModCandidateFetcher struct {
	versions modUpdateVersionLookup
	client   *http.Client
}

func (f providerModCandidateFetcher) Fetch(ctx context.Context, settings *storage.ServerConfig, versionID, projectID, filename, sha512Hex, loader, game string, limit int64) ([]byte, string, error) {
	lookup := f.versions
	if lookup == nil {
		lookup = modUpdateProvider(settings)
	}
	version, err := lookup.GetVersion(ctx, versionID)
	if err != nil || version == nil || version.ID == "" {
		return nil, "refused_provider", fmt.Errorf("provider did not confirm release %s", versionID)
	}
	if version.Status != "listed" || version.VersionType != "release" ||
		version.ProjectID != projectID ||
		!slices.Contains(version.Loaders, loader) || !slices.Contains(version.GameVersions, game) {
		return nil, "refused_provider", errors.New("release is no longer listed, from the reviewed project and compatible")
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
	// Static URL validation alone is not a connection-time SSRF guard. Reuse
	// the guarded client that World Library imports use so DNS/private targets
	// are refused while credentials are stripped on cross-origin redirects.
	client := f.client
	if client == nil {
		client = indexers.ProtectCredentialsOnRedirect(utils.NewSafeHTTPClient(2*time.Minute, false))
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
		return nil, "transient_download", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 {
			return nil, "transient_download", fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
		}
		return nil, "refused_download", fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	readLimit := int64(maxModUpdateBytes)
	if limit > 0 && limit < readLimit {
		readLimit = limit
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, readLimit+1))
	if err != nil {
		return nil, "transient_download", err
	}
	if int64(len(data)) > readLimit || len(data) == 0 {
		return nil, "refused_download", fmt.Errorf("candidate file exceeds the %d-byte update limit or is empty", readLimit)
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

// SetModCandidateFetcher replaces the provider download path. Tests inject a
// fixture; production uses the guarded provider adapter.
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

type readyModUpdate struct {
	selection   *v1.ModUpdateSelection
	targetPath  string
	liveHash    string
	bytes       []byte
	resultIndex int
}

// stageReviewedModUpdates re-verifies the installed fingerprint, recomputes
// the server-owned preview and download, then stages exact bytes into the open
// batch. The live mods directory is untouched. The apply window, staged rows
// and target uniqueness are committed together under the per-server operation
// journal so a restart or a racing manual/scheduled run cannot publish a
// partially configured or duplicated batch.
func (s *ModService) stageReviewedModUpdates(ctx context.Context, serverID string, selections []*v1.ModUpdateSelection, window v1.ConfigApplyWindow, applyAtUnix int64, idleMinutes int32) ([]stagedModUpdate, *storage.ServerUpdateBatch, error, error) {
	if len(selections) == 0 {
		return nil, nil, nil, errors.New("no mod updates selected")
	}
	// One live target appears once. A duplicated filename is skipped before
	// any provider work so a repeated submission cannot repeat a download or
	// create two staged rows for the same file.
	results := make([]stagedModUpdate, 0, len(selections))
	seenTargets := make(map[string]bool, len(selections))
	unique := make([]*v1.ModUpdateSelection, 0, len(selections))
	for _, sel := range selections {
		if seenTargets[sel.GetFileName()] {
			results = append(results, stagedModUpdate{
				ModID: sel.GetModId(), FileName: sel.GetFileName(),
				Outcome: "already_selected", Reason: "this file was already selected in this request",
			})
			continue
		}
		seenTargets[sel.GetFileName()] = true
		unique = append(unique, sel)
	}
	selections = unique
	server, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("server not found: %w", err)
	}
	dataRoot, err := dataRootFor(ctx, s.store, server, false, s.storageResolver)
	if err != nil {
		return nil, nil, nil, err
	}
	root, err := os.OpenRoot(dataRoot)
	if err != nil {
		return nil, nil, nil, err
	}
	defer func() { _ = root.Close() }()
	modsDir := minecraft.GetModsPath(dataRoot, server.ModLoader)
	if modsDir == "" {
		return nil, nil, nil, errors.New("this server type does not support mods")
	}
	base, err := filepath.Rel(dataRoot, modsDir)
	if err != nil {
		return nil, nil, nil, err
	}
	loader := string(server.ModLoader)
	if isModpackPlatformLoader(server.ModLoader) {
		loader = string(server.ModpackLoader)
	}
	if loader == "" || server.MCVersion == "" || strings.EqualFold(server.MCVersion, "latest") {
		return nil, nil, nil, errors.New("a known game version and loader are required")
	}
	settings, err := s.store.GetServerConfig(ctx, storage.GlobalSettingsID)
	if err != nil && !errors.Is(err, storage.ErrServerConfigNotFound) {
		return nil, nil, nil, fmt.Errorf("failed to load provider settings: %w", err)
	}
	lookup := s.updateLookup
	if lookup == nil {
		lookup = modUpdateProvider(settings)
	}
	fetcher := s.candidateFetcher
	if fetcher == nil {
		fetcher = providerModCandidateFetcher{}
	}
	windowName, applyAt, idle := windowFromProto(window, applyAtUnix, idleMinutes)
	if err := storage.ValidateUpdateWindow(windowName, applyAt, idle); err != nil {
		return nil, nil, nil, err
	}

	// Compare the complete window before any persistent write or provider
	// work. A different at_time or idle interval must not retarget an existing
	// reviewed batch, even when every selection turns out to be a duplicate.
	existing, err := s.store.OpenUpdateBatch(ctx, server.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	if existing != nil && !batchWindowEquals(existing, windowName, applyAt, idle) {
		configs, err := s.store.ListBatchConfig(ctx, existing.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		rows, err := s.store.ListBatchContent(ctx, existing.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(configs) > 0 || len(rows) > 0 {
			return nil, nil, nil, fmt.Errorf(
				"open batch %s has window %q (at %v, idle %d) with reviewed changes; refusing conflicting window %q (at %v, idle %d)",
				existing.ID, existing.Window, existing.ApplyAt, existing.IdleMinutes,
				windowName, applyAt, idle)
		}
	}

	// Rebuild the server-owned preview for exactly the reviewed files. The
	// preview owns project, dependency and version-selection policy, so apply
	// cannot reuse a decision that became stale while the operator reviewed.
	previewMods := make([]*v1.Mod, 0, len(selections))
	locations := make(map[string]string, len(selections))
	for _, sel := range selections {
		installed, enabled, err := installedModLocation(root, base, sel.GetFileName())
		if err != nil {
			continue
		}
		locations[sel.GetFileName()] = installed
		previewMods = append(previewMods, &v1.Mod{
			Id: sel.GetModId(), ServerId: server.ID, FileName: sel.GetFileName(), Enabled: enabled,
		})
	}
	if len(previewMods) > 0 {
		if err := previewServerModUpdates(ctx, dataRoot, modsDir, server, previewMods, lookup); err != nil {
			return nil, nil, nil, err
		}
	}
	previewByFile := make(map[string]*v1.InstalledModUpdate, len(previewMods))
	for _, mod := range previewMods {
		previewByFile[mod.FileName] = mod.UpdatePreview
	}

	ready := make([]readyModUpdate, 0, len(selections))
	var transient error
	var totalBytes int64
	for _, sel := range selections {
		result := stagedModUpdate{ModID: sel.GetModId(), FileName: sel.GetFileName()}
		installed := locations[sel.GetFileName()]
		preview := previewByFile[sel.GetFileName()]
		if installed == "" || preview == nil || strings.EqualFold(sel.GetInstalledSha512(), "") {
			result.Outcome, result.Reason = "refused_drift", "the reviewed file is not a valid installed mod"
			results = append(results, result)
			continue
		}
		// The preview re-hashes installed bytes and re-checks the provider.
		// Its identity and candidate must still match the reviewed selection,
		// including project and dependency policy.
		if preview == nil || preview.GetInstalledSha512() == "" ||
			!strings.EqualFold(preview.GetInstalledSha512(), sel.GetInstalledSha512()) {
			result.Outcome, result.Reason = "refused_drift", "installed bytes changed since the review; check again"
			results = append(results, result)
			continue
		}
		if preview.GetState() != "available" || preview.GetProjectId() == "" ||
			preview.GetCandidateVersionId() != sel.GetCandidateVersionId() ||
			!strings.EqualFold(preview.GetCandidateSha512(), sel.GetCandidateSha512()) {
			if strings.HasPrefix(preview.GetReason(), providerLookupPrefix) {
				result.Outcome = "transient_download"
				transient = errors.Join(transient, errors.New(preview.GetReason()))
			} else {
				result.Outcome = "refused_stale_preview"
			}
			result.Reason = "the server-owned preview no longer matches the reviewed release; check again"
			results = append(results, result)
			continue
		}
		if _, pathErr := modInstallPath(base, sel.GetCandidateFilename()); pathErr != nil {
			result.Outcome, result.Reason = "refused_provider", "the reviewed candidate filename is not a bare JAR name"
			results = append(results, result)
			continue
		}
		budget := s.updateBudget
		if budget <= 0 {
			budget = maxModUpdateBatchBytes
		}
		remaining := budget - totalBytes
		if remaining <= 0 {
			result.Outcome, result.Reason = "refused_limit", "the cumulative staged update budget is exhausted"
			results = append(results, result)
			continue
		}
		data, outcome, fetchErr := fetcher.Fetch(ctx, settings, sel.GetCandidateVersionId(),
			preview.GetProjectId(), sel.GetCandidateFilename(), sel.GetCandidateSha512(), loader, server.MCVersion, remaining)
		if fetchErr != nil {
			result.Outcome, result.Reason = outcome, fetchErr.Error()
			if outcome == "transient_download" {
				transient = errors.Join(transient, fetchErr)
			}
			results = append(results, result)
			continue
		}
		if int64(len(data)) > remaining {
			result.Outcome, result.Reason = "refused_limit", "candidate exceeds the cumulative staged update budget"
			results = append(results, result)
			continue
		}
		totalBytes += int64(len(data))
		ready = append(ready, readyModUpdate{
			selection: sel, targetPath: filepath.ToSlash(installed),
			liveHash: preview.GetInstalledSha512(), bytes: data,
			resultIndex: len(results),
		})
		results = append(results, result)
	}
	if len(ready) == 0 {
		return results, nil, transient, nil
	}

	var batch *storage.ServerUpdateBatch
	if err := operation.RunJournaled(ctx, s.store, s.log, server, "stage-mod-updates", "", func() error {
		existing, err := s.store.OpenUpdateBatch(ctx, server.ID)
		if err != nil {
			return err
		}
		if existing != nil && existing.Window != string(windowName) {
			content, err := s.store.ListBatchContent(ctx, existing.ID)
			if err != nil {
				return err
			}
			configs, err := s.store.ListBatchConfig(ctx, existing.ID)
			if err != nil {
				return err
			}
			if len(content) > 0 || len(configs) > 0 {
				return fmt.Errorf("open batch %s has window %q and already-reviewed changes; refusing to retarget to %q",
					existing.ID, existing.Window, windowName)
			}
		}
		// Commit the window before the first staged byte becomes visible.
		batch, err = s.store.SetOpenBatchWindow(ctx, server.ID, windowName, applyAt, idle)
		if err != nil {
			return fmt.Errorf("failed to set the apply window: %w", err)
		}
		alreadyStaged := make(map[string]bool)
		rows, err := s.store.ListBatchContent(ctx, batch.ID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Kind == storage.StagedKindMod {
				alreadyStaged[row.FileName] = true
			}
		}
		for i := range ready {
			item := ready[i]
			sel := item.selection
			outcome := &results[item.resultIndex]
			if alreadyStaged[sel.GetFileName()] {
				outcome.Outcome, outcome.Reason = "already_staged", "this file already has an update in the open batch"
				continue
			}
			row, err := stageContentBytesWithPrecondition(ctx, s.store, server, s.storageResolver,
				storage.StagedKindMod, storage.StagedActionReplace,
				item.targetPath, sel.GetFileName(), item.bytes, item.liveHash)
			if err != nil {
				outcome.Outcome, outcome.Reason = "error", err.Error()
				continue
			}
			outcome.Outcome, outcome.StagedID = "staged", row.ID
			alreadyStaged[sel.GetFileName()] = true
		}
		return nil
	}); err != nil {
		return results, nil, nil, err
	}
	return results, batch, transient, nil
}

// installedModLocation returns the live relative path and whether the reviewed
// file is in the enabled mods directory.
func installedModLocation(root *os.Root, base, fileName string) (string, bool, error) {
	active, err := modInstallPath(base, fileName)
	if err != nil {
		return "", false, err
	}
	if _, err := root.Lstat(active); err == nil {
		return active, true, nil
	}
	disabled, err := modInstallPath(base+"_disabled", fileName)
	if err != nil {
		return "", false, err
	}
	if _, err := root.Lstat(disabled); err != nil {
		return "", false, err
	}
	return disabled, false, nil
}

// batchWindowEquals compares the complete window tuple, not just the enum, so
// a second update cannot silently move a reviewed batch's at_time or interval.
func batchWindowEquals(batch *storage.ServerUpdateBatch, window storage.UpdateWindow, applyAt *time.Time, idleMinutes int) bool {
	if batch.Window != string(window) || batch.IdleMinutes != idleMinutes {
		return false
	}
	if batch.ApplyAt == nil || applyAt == nil {
		return batch.ApplyAt == nil && applyAt == nil
	}
	return batch.ApplyAt.Equal(*applyAt)
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
