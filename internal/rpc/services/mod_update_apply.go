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
	Fetch(ctx context.Context, settings *storage.ServerConfig, versionID, projectID, filename, sha512Hex, loader, game string, limit int64) ([]byte, int64, string, error)
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

// modReleaseLookupIsTransient reports whether a failed provider release lookup
// is worth retrying. Structured indexer failures distinguish a network,
// rate-limit, 5xx or decode problem from a confirmed 404/410 or other invalid
// request; a canceled or timed-out context is always temporary.
func modReleaseLookupIsTransient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var indexerErr *indexers.IndexerError
	if !errors.As(err, &indexerErr) {
		return false
	}
	switch indexerErr.Kind {
	case indexers.ErrNetwork, indexers.ErrRateLimit, indexers.ErrDecode:
		return true
	case indexers.ErrNotFound, indexers.ErrAuth:
		return false
	case indexers.ErrAPI:
		if indexerErr.StatusCode >= 500 {
			return true
		}
		switch indexerErr.StatusCode {
		case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests:
			return true
		}
		return false
	default:
		return false
	}
}

func (f providerModCandidateFetcher) Fetch(ctx context.Context, settings *storage.ServerConfig, versionID, projectID, filename, sha512Hex, loader, game string, limit int64) ([]byte, int64, string, error) {
	lookup := f.versions
	if lookup == nil {
		lookup = modUpdateProvider(settings)
	}
	version, err := lookup.GetVersion(ctx, versionID)
	if err != nil {
		if modReleaseLookupIsTransient(err) || ctx.Err() != nil {
			return nil, 0, "transient_download", fmt.Errorf("provider release lookup failed: %w", err)
		}
		return nil, 0, "refused_provider", fmt.Errorf("provider did not confirm release %s: %w", versionID, err)
	}
	if version == nil || version.ID == "" {
		return nil, 0, "refused_provider", fmt.Errorf("provider did not confirm release %s", versionID)
	}
	if version.Status != "listed" || version.VersionType != "release" ||
		version.ProjectID != projectID ||
		!slices.Contains(version.Loaders, loader) || !slices.Contains(version.GameVersions, game) {
		return nil, 0, "refused_provider", errors.New("release is no longer listed, from the reviewed project and compatible")
	}
	// The preview marks a release with a required or incompatible dependency as
	// review_required. Recheck the fetched definition so a dependency added
	// after the review cannot bypass that operator gate.
	for _, dependency := range version.Dependencies {
		if dependency.DependencyType == "required" || dependency.DependencyType == "incompatible" {
			return nil, 0, "refused_provider", errors.New("release dependencies changed since review; review the release again")
		}
	}
	want, err := hex.DecodeString(sha512Hex)
	if err != nil || len(want) != sha512.Size {
		return nil, 0, "refused_provider", errors.New("reviewed checksum is not a SHA-512 digest")
	}
	var target *modrinth.File
	for i := range version.Files {
		file := &version.Files[i]
		if filepath.Base(file.Filename) == filename && strings.EqualFold(file.Hashes.SHA512, sha512Hex) {
			if target != nil {
				return nil, 0, "refused_provider", errors.New("release has ambiguous reviewed files")
			}
			target = file
		}
	}
	if target == nil || !providers.DownloadURLIsAllowed(target.URL) {
		return nil, 0, "refused_download", errors.New("no allowed download URL for the reviewed file")
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
		return nil, 0, "refused_download", err
	}
	if agent := providerUserAgent(settings); agent != "" {
		req.Header.Set("User-Agent", agent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, "transient_download", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 {
			return nil, 0, "transient_download", fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
		}
		return nil, 0, "refused_download", fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
	}
	readLimit := int64(maxModUpdateBytes)
	if limit > 0 && limit < readLimit {
		readLimit = limit
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, readLimit+1))
	consumed := int64(len(data))
	if err != nil {
		return nil, consumed, "transient_download", err
	}
	if consumed > readLimit || consumed == 0 {
		return nil, consumed, "refused_download", fmt.Errorf("candidate file exceeds the %d-byte update limit or is empty", readLimit)
	}
	sum := sha512.Sum512(data)
	if !bytes.Equal(sum[:], want) {
		return nil, consumed, "refused_checksum", errors.New("candidate bytes do not match the reviewed SHA-512")
	}
	return data, consumed, "", nil
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

// modUpdateOutcomeRetryable marks a pre-fetch already-staged decision that the
// current open batch no longer backs. This request must not download or stage
// that file; a resubmission can stage it.
const modUpdateOutcomeRetryable = "retryable"

func markModUpdateRetryable(result *stagedModUpdate) {
	result.Outcome = modUpdateOutcomeRetryable
	result.Reason = "the open update batch changed; resubmit to stage this file"
}

// currentBatchHoldsAlreadyStaged verifies the pre-fetch already-staged decisions
// against the batch that is open now. It returns that batch when every reported
// file is still pending, or nil after marking the stale results retryable.
func (s *ModService) currentBatchHoldsAlreadyStaged(ctx context.Context, serverID string, results []stagedModUpdate) (*storage.ServerUpdateBatch, bool, error) {
	current, err := s.store.OpenUpdateBatch(ctx, serverID)
	if err != nil {
		return nil, false, err
	}
	if current == nil {
		stale := false
		for i := range results {
			if results[i].Outcome == "already_staged" {
				markModUpdateRetryable(&results[i])
				stale = true
			}
		}
		return nil, stale, nil
	}
	rows, err := s.store.ListBatchContent(ctx, current.ID)
	if err != nil {
		return nil, false, err
	}
	present := make(map[string]bool, len(rows))
	for _, row := range rows {
		if row.Kind == storage.StagedKindMod {
			present[row.FileName] = true
		}
	}
	stale := false
	for i := range results {
		if results[i].Outcome != "already_staged" || present[results[i].FileName] {
			continue
		}
		markModUpdateRetryable(&results[i])
		stale = true
	}
	if stale {
		return nil, true, nil
	}
	return current, false, nil
}

type readyModUpdate struct {
	selection   *v1.ModUpdateSelection
	targetPath  string
	liveHash    string
	projectID   string
	bytes       []byte
	resultIndex int
}

// allAlreadyStaged reports whether every requested selection was already in the
// open batch. Duplicates inside the request count as copies of that same
// already-staged decision.
func allAlreadyStaged(results []stagedModUpdate) bool {
	if len(results) == 0 {
		return false
	}
	for _, result := range results {
		switch result.Outcome {
		case "already_staged", "already_selected":
		default:
			return false
		}
	}
	return true
}

// errModUpdateRunningSkipped reports a scheduled policy that decided at journal
// admission not to stage because the server is running. It is a policy outcome,
// not a failure, so the scheduler records a successful skip instead of retrying.
var errModUpdateRunningSkipped = errors.New("server is running; mod update policy skipped by running-server policy")

// modUpdateAdmission re-reads live server state under the operation journal
// immediately before a reviewed batch is created or any staged row is written.
// The scheduled running-server policy uses it so a stopped-to-running change
// during provider retry or download cannot slip past the earlier snapshot.
type modUpdateAdmission func(context.Context, *storage.Server) error

// modStagingErrorIsDefinitive separates validation and batch state refusals
// from storage/IO failures. Only the latter are worth retrying.
func modStagingErrorIsDefinitive(err error) bool {
	return errors.Is(err, storage.ErrUpdateBatchConflict) ||
		errors.Is(err, storage.ErrStagedModVersionChange) ||
		errors.Is(err, storage.ErrStagedModRuntimeTargetChanged) ||
		errors.Is(err, errStagedPath) ||
		errors.Is(err, errStagedModValidation)
}

// stageReviewedModUpdates re-verifies the installed fingerprint, recomputes
// the server-owned preview and download, then stages exact bytes into the open
// batch. The live mods directory is untouched. The apply window, staged rows
// and target uniqueness are committed together under the per-server operation
// journal so a restart or a racing manual/scheduled run cannot publish a
// partially configured or duplicated batch.
func (s *ModService) stageReviewedModUpdates(ctx context.Context, serverID string, selections []*v1.ModUpdateSelection, window v1.ConfigApplyWindow, applyAtUnix int64, idleMinutes int32, admission ...modUpdateAdmission) ([]stagedModUpdate, *storage.ServerUpdateBatch, error, error) {
	if len(selections) == 0 {
		return nil, nil, nil, errors.New("no mod updates selected")
	}
	windowName, applyAt, idle, err := windowFromProto(window, applyAtUnix, idleMinutes)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := storage.ValidateUpdateWindow(windowName, applyAt, idle); err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", errInvalidModUpdateWindow, err)
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
	runtimeServer, err := s.reviewedModRuntimeServer(ctx, server)
	if err != nil {
		return nil, nil, nil, err
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
	modsDir := minecraft.GetModsPath(dataRoot, runtimeServer.ModLoader)
	if modsDir == "" {
		return nil, nil, nil, errors.New("this server type does not support mods")
	}
	base, err := filepath.Rel(dataRoot, modsDir)
	if err != nil {
		return nil, nil, nil, err
	}
	loader := string(runtimeServer.ModLoader)
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

	// Compare the complete window before any persistent write or provider
	// work. A different at_time or idle interval must not retarget an existing
	// reviewed batch, even when every selection turns out to be a duplicate.
	if err := s.store.CheckReviewedContentWindow(ctx, server.ID, windowName, applyAt, idle); err != nil {
		return nil, nil, nil, err
	}

	// A resubmission must not download a file that is already staged or spend
	// the cumulative budget on it. The journal rechecks this below so a row
	// staged after this snapshot still cannot be duplicated.
	var openBatch *storage.ServerUpdateBatch
	alreadyStagedBeforeFetch := make(map[string]bool)
	if batch, err := s.store.OpenUpdateBatch(ctx, server.ID); err != nil {
		return nil, nil, nil, err
	} else if batch != nil {
		openBatch = batch
		rows, err := s.store.ListBatchContent(ctx, openBatch.ID)
		if err != nil {
			return nil, nil, nil, err
		}
		for _, row := range rows {
			if row.Kind == storage.StagedKindMod {
				alreadyStagedBeforeFetch[row.FileName] = true
			}
		}
	}

	// Rebuild the server-owned preview for exactly the reviewed files. The
	// preview owns project, dependency and version-selection policy, so apply
	// cannot reuse a decision that became stale while the operator reviewed.
	previewMods := make([]*v1.Mod, 0, len(selections))
	locations := make(map[string]string, len(selections))
	resolutionFailures := make(map[string]string)
	for _, sel := range selections {
		installed, enabled, reason, err := reviewedInstalledModLocation(ctx, root, base, sel.GetFileName(), sel.GetInstalledSha512())
		if err != nil {
			return nil, nil, nil, err
		}
		if reason != "" {
			resolutionFailures[sel.GetFileName()] = reason
			continue
		}
		locations[sel.GetFileName()] = installed
		previewMods = append(previewMods, &v1.Mod{
			Id: sel.GetModId(), ServerId: server.ID, FileName: sel.GetFileName(), Enabled: enabled,
		})
	}
	if len(previewMods) > 0 {
		if err := previewServerModUpdates(ctx, dataRoot, modsDir, runtimeServer, previewMods, lookup); err != nil {
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
		if alreadyStagedBeforeFetch[sel.GetFileName()] {
			result.Outcome, result.Reason = "already_staged", "this file already has an update in the open batch"
			results = append(results, result)
			continue
		}
		if reason, failed := resolutionFailures[sel.GetFileName()]; failed {
			result.Outcome, result.Reason = "refused_drift", reason
			results = append(results, result)
			continue
		}
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
		data, consumed, outcome, fetchErr := fetcher.Fetch(ctx, settings, sel.GetCandidateVersionId(),
			preview.GetProjectId(), sel.GetCandidateFilename(), sel.GetCandidateSha512(), loader, runtimeServer.MCVersion, remaining)
		// Count every transferred byte, including a rejected candidate: a
		// provider that keeps sending bad bytes must not burn the budget for
		// free.
		totalBytes += consumed
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
		ready = append(ready, readyModUpdate{
			selection: sel, targetPath: filepath.ToSlash(installed),
			liveHash: preview.GetInstalledSha512(), projectID: preview.GetProjectId(),
			bytes: data, resultIndex: len(results),
		})
		results = append(results, result)
	}
	if len(ready) == 0 {
		// An all-already-staged resubmission names the batch it was checked
		// against, so the caller binds to the same open batch instead of
		// treating the response as batchless. Recheck the current batch content
		// first: a batch discarded between the snapshot and here must not be
		// reported as holding files it no longer contains. Those results are
		// retryable so a resubmission can stage them. Other all-refused outcomes
		// are not evidence that the open batch is the right target.
		if openBatch != nil && allAlreadyStaged(results) {
			current, stale, err := s.currentBatchHoldsAlreadyStaged(ctx, server.ID, results)
			if err != nil {
				return nil, nil, nil, err
			}
			if stale {
				transient = errors.Join(transient, errors.New("the open update batch changed; resubmit to stage the reported files"))
				return results, nil, transient, nil
			}
			return results, current, transient, nil
		}
		return results, nil, transient, nil
	}

	var batch *storage.ServerUpdateBatch
	if err := operation.RunJournaled(ctx, s.store, s.log, server, "stage-mod-updates", "", func() error {
		// Re-read the server here, while this call owns the journal and before
		// the batch or any staged byte is written, so a start during provider
		// retry/download cannot be missed by the earlier snapshot. The same
		// reload also decides whether the runtime target the operator reviewed
		// is still the target the download was verified against.
		current, err := s.store.GetServer(ctx, server.ID)
		if err != nil {
			return fmt.Errorf("reload server before staging: %w", err)
		}
		// The scheduler passes an admission check for its running-server
		// policy. Run it against the freshly loaded row.
		if len(admission) > 0 && admission[0] != nil {
			if err := admission[0](ctx, current); err != nil {
				return err
			}
		}
		loaderNow, gameNow, err := s.store.ReviewedModRuntimeTarget(ctx, current)
		if err != nil {
			return err
		}
		if loaderNow != runtimeServer.ModLoader || gameNow != runtimeServer.MCVersion {
			return fmt.Errorf("%w: Docker runtime target changed from %s/%s to %s/%s while the update was downloaded; review the update again",
				errReviewedModRuntimeChanged, runtimeServer.ModLoader, runtimeServer.MCVersion, loaderNow, gameNow)
		}
		server = current
		// Keep publication from taking an open-batch snapshot between preparing
		// the window, writing the payloads, and recording their rows.
		releaseWrite, err := beginDataWrite(server.ID)
		if err != nil {
			return err
		}
		defer releaseWrite()

		// Commit the window before the first staged byte becomes visible.
		batch, err = s.store.PrepareReviewedContentBatch(ctx, server.ID, windowName, applyAt, idle)
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
		// The pre-fetch snapshot may have been taken against a batch that was
		// discarded while the candidate was downloaded. Do not claim
		// already_staged for a file the current batch no longer holds: mark it
		// retryable and leave it for a resubmission instead of staging it here.
		for i := range results {
			result := &results[i]
			if result.Outcome != "already_staged" || alreadyStaged[result.FileName] {
				continue
			}
			markModUpdateRetryable(result)
			transient = errors.Join(transient, fmt.Errorf("%s: %s", result.FileName, result.Reason))
		}
		for i := range ready {
			item := ready[i]
			sel := item.selection
			outcome := &results[item.resultIndex]
			if alreadyStaged[sel.GetFileName()] {
				outcome.Outcome, outcome.Reason = "already_staged", "this file already has an update in the open batch"
				continue
			}
			row, err := stageModContentToBatch(ctx, s.store, server, s.storageResolver,
				batch.ID, windowName, applyAt, idle,
				item.targetPath, sel.GetFileName(), item.bytes, item.liveHash, approvedModRelease{
					SHA512:    sel.GetCandidateSha512(),
					Provider:  "modrinth",
					ProjectID: item.projectID,
					VersionID: sel.GetCandidateVersionId(),
					Filename:  sel.GetCandidateFilename(),
				}, runtimeServer.ModLoader, runtimeServer.MCVersion)
			if errors.Is(err, storage.ErrUpdateBatchConflict) || errors.Is(err, storage.ErrDuplicateStagedTarget) || errors.Is(err, storage.ErrModpackBatchConflict) {
				return err
			}
			if err != nil {
				outcome.Outcome, outcome.Reason = "error", err.Error()
				// A per-item storage/IO failure leaves the scheduler retryable
				// while the rows staged before it stay committed. Definitive
				// validation refusals remain ordinary per-item outcomes.
				if !modStagingErrorIsDefinitive(err) {
					transient = errors.Join(transient, fmt.Errorf("stage %s: %w", sel.GetFileName(), err))
				}
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

func (s *ModService) reviewedModRuntimeServer(ctx context.Context, server *storage.Server) (*storage.Server, error) {
	loader, game, err := s.store.ReviewedModRuntimeTarget(ctx, server)
	if err != nil {
		return nil, err
	}
	runtime := *server
	runtime.ModLoader, runtime.MCVersion = loader, game
	return &runtime, nil
}

// reviewedInstalledModLocation returns the live relative path and enabled state
// of the copy whose bytes match the reviewed installed SHA-512. The enabled and
// disabled directories can both contain the same filename, so existence alone
// cannot say which copy the operator reviewed. A missing match is drift; two
// matching copies are ambiguous and must not be chosen arbitrarily.
func reviewedInstalledModLocation(ctx context.Context, root *os.Root, base, fileName, reviewedSHA string) (string, bool, string, error) {
	if strings.EqualFold(strings.TrimSpace(reviewedSHA), "") {
		return "", false, "the reviewed file is not a valid installed mod", nil
	}
	type candidate struct {
		path    string
		enabled bool
	}
	candidates := make([]candidate, 0, 2)
	for _, dir := range []struct {
		base    string
		enabled bool
	}{{base, true}, {base + "_disabled", false}} {
		path, err := modInstallPath(dir.base, fileName)
		if err != nil {
			return "", false, "the reviewed file is not a valid installed mod", nil
		}
		candidates = append(candidates, candidate{path: path, enabled: dir.enabled})
	}
	var matches []candidate
	existing := 0
	for _, candidate := range candidates {
		info, err := root.Lstat(candidate.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", false, "", err
		}
		if !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxModUpdateBytes {
			continue
		}
		existing++
		hash, _, err := installedModHash(ctx, root, candidate.path, maxModUpdateBytes)
		if err != nil {
			return "", false, "", err
		}
		if strings.EqualFold(hash, reviewedSHA) {
			matches = append(matches, candidate)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0].path, matches[0].enabled, "", nil
	case 0:
		if existing == 0 {
			return "", false, "the reviewed file is not a valid installed mod", nil
		}
		return "", false, "installed bytes changed since the review; check again", nil
	default:
		return "", false, "both enabled and disabled copies match the reviewed digest; refusing to choose one", nil
	}
}

var errInvalidModUpdateWindow = errors.New("invalid mod update window")

// errReviewedModRuntimeChanged means the committed Docker runtime target moved
// between the preview/download snapshot and journal admission. The verified
// candidate belongs to the old target, so it must be reviewed again.
var errReviewedModRuntimeChanged = errors.New("reviewed Docker runtime target changed before staging")

func windowFromProto(window v1.ConfigApplyWindow, applyAtUnix int64, idleMinutes int32) (storage.UpdateWindow, *time.Time, int, error) {
	var applyAt *time.Time
	if applyAtUnix > 0 {
		value := time.Unix(applyAtUnix, 0).UTC()
		applyAt = &value
	}
	switch window {
	case v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME:
		return storage.UpdateWindowAtTime, applyAt, 0, nil
	case v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE:
		return storage.UpdateWindowIdle, nil, int(idleMinutes), nil
	case v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_UNSPECIFIED, v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART:
		return storage.UpdateWindowNextRestart, nil, 0, nil
	default:
		return "", nil, 0, fmt.Errorf("%w: unknown apply window %d", errInvalidModUpdateWindow, window)
	}
}
