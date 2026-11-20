// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"crypto/sha512"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

type modUpdateFetchFixture struct {
	data    []byte
	outcome string
	err     error
	calls   int
}

func (f *modUpdateFetchFixture) Fetch(context.Context, *storage.ServerConfig, string, string, string, string, string, string, int64) ([]byte, int64, string, error) {
	f.calls++
	return f.data, int64(len(f.data)), f.outcome, f.err
}

func installedModDigest(data string) string {
	return fmt.Sprintf("%x", sha512.Sum512([]byte(data)))
}

func modUpdateRequest(serverID, installedHash string) *v1.UpdateModRequest {
	return &v1.UpdateModRequest{
		ServerId: serverID,
		UpdateSelections: []*v1.ModUpdateSelection{{
			ModId:              "reviewed-mod",
			FileName:           "mod.jar",
			InstalledSha512:    installedHash,
			CandidateVersionId: "update",
			CandidateSha512:    installedModDigest("next"),
			CandidateFilename:  "mod-2.jar",
		}},
		UpdateWindow: v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART,
	}
}

// twoModPreviewFixture serves two installed files so service tests can exercise
// racing or cumulative requests through the real preview and staging paths.
type twoModPreviewFixture struct {
	onLookup func(string)
}

func versionFor(project, hash, filename string) modrinth.Version {
	return modrinth.Version{
		ID: "v-" + project, ProjectID: project, VersionType: "release", Status: "listed",
		Loaders: []string{"fabric"}, GameVersions: []string{"1.21.1"},
		Files: []modrinth.File{{Filename: filename, Hashes: modrinth.Hashes{SHA512: hash}}},
	}
}

func (f twoModPreviewFixture) VersionsByFileHash(_ context.Context, hashes []string) (map[string]modrinth.Version, error) {
	if f.onLookup != nil {
		for _, hash := range hashes {
			f.onLookup(hash)
		}
	}
	out := make(map[string]modrinth.Version, len(hashes))
	for _, hash := range hashes {
		switch hash {
		case installedModDigest("installed-a"):
			v := versionFor("project-a", hash, "a.jar")
			v.DatePublished = "2025-01-01T00:00:00Z"
			out[hash] = v
		case installedModDigest("installed-b"):
			v := versionFor("project-b", hash, "b.jar")
			v.DatePublished = "2025-01-01T00:00:00Z"
			out[hash] = v
		}
	}
	return out, nil
}

func (twoModPreviewFixture) CompatibleUpdatesByFileHash(_ context.Context, hashes []string, _, _ string) (map[string]modrinth.Version, error) {
	out := make(map[string]modrinth.Version, len(hashes))
	for _, hash := range hashes {
		switch hash {
		case installedModDigest("installed-a"):
			v := versionFor("project-a", installedModDigest("next-a"), "a.jar")
			v.ID = "v2-project-a"
			v.DatePublished = "2025-02-01T00:00:00Z"
			out[hash] = v
		case installedModDigest("installed-b"):
			v := versionFor("project-b", installedModDigest("next-b"), "b.jar")
			v.ID = "v2-project-b"
			v.DatePublished = "2025-02-01T00:00:00Z"
			out[hash] = v
		}
	}
	return out, nil
}

func (twoModPreviewFixture) GetProjects(_ context.Context, _ []string) ([]modrinth.ProjectDetails, error) {
	return []modrinth.ProjectDetails{
		{ID: "project-a", ServerSide: "required"},
		{ID: "project-b", ServerSide: "required"},
	}, nil
}

type modUpdateFetchByFileFixture struct {
	mu      sync.Mutex
	data    map[string][]byte
	bad     map[string]bool
	delay   time.Duration
	onFetch func(string)
	calls   int
}

func (f *modUpdateFetchByFileFixture) Fetch(ctx context.Context, _ *storage.ServerConfig, _, _, filename, _, _, _ string, _ int64) ([]byte, int64, string, error) {
	if f.onFetch != nil {
		f.onFetch(filename)
	}
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	data := f.data[filename]
	if f.bad[filename] {
		return data, int64(len(data)), "refused_checksum", errors.New("checksum mismatch")
	}
	return data, int64(len(data)), "", nil
}

func twoModRequest(serverID, fileName, installed string, window v1.ConfigApplyWindow, atUnix int64) *v1.UpdateModRequest {
	return &v1.UpdateModRequest{
		ServerId: serverID,
		UpdateSelections: []*v1.ModUpdateSelection{{
			ModId: "mod-" + fileName, FileName: fileName,
			InstalledSha512:    installedModDigest(installed),
			CandidateVersionId: "v2-project-" + fileName[:1],
			CandidateSha512:    installedModDigest("next-" + fileName[:1]),
			CandidateFilename:  fileName,
		}},
		UpdateWindow:      window,
		UpdateApplyAtUnix: atUnix,
	}
}

func TestTwoModPreviewFixtureProducesAvailableUpdates(t *testing.T) {
	root := t.TempDir()
	modsDir := filepath.Join(root, "mods")
	require.NoError(t, os.MkdirAll(modsDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(modsDir, "a.jar"), []byte("installed-a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(modsDir, "b.jar"), []byte("installed-b"), 0o600))
	mods := []*v1.Mod{{FileName: "a.jar", Enabled: true}, {FileName: "b.jar", Enabled: true}}
	server := &storage.Server{ModLoader: storage.ModLoaderFabric, MCVersion: "1.21.1"}
	require.NoError(t, previewServerModUpdates(context.Background(), root, modsDir, server, mods, twoModPreviewFixture{}))
	for _, mod := range mods {
		t.Logf("%s: %s %s", mod.FileName, mod.UpdatePreview.GetState(), mod.UpdatePreview.GetReason())
		require.Equal(t, "available", mod.UpdatePreview.GetState())
	}
}

func TestSameModeWindowRaceCannotRescheduleReviewedBatch(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-window-race", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "b.jar"), []byte("installed-b"), 0o600))

	firstLookup := make(chan struct{})
	secondLookup := make(chan struct{})
	releaseLookups := make(chan struct{})
	lookup := twoModPreviewFixture{onLookup: func(hash string) {
		if hash == installedModDigest("installed-a") {
			close(firstLookup)
			<-releaseLookups
		} else {
			close(secondLookup)
			<-releaseLookups
		}
	}}
	fetcher := &modUpdateFetchByFileFixture{
		data: map[string][]byte{"a.jar": []byte("next-a"), "b.jar": []byte("next-b")},
	}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(lookup)
	svc.SetModCandidateFetcher(fetcher)

	firstTime := time.Now().Add(time.Hour).Unix()
	secondTime := time.Now().Add(2 * time.Hour).Unix()
	requests := []*v1.UpdateModRequest{
		twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME, firstTime),
		twoModRequest(server.ID, "b.jar", "installed-b", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME, secondTime),
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range requests {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = svc.UpdateMod(ctx, connect.NewRequest(requests[i]))
		}(i)
	}
	// Both requests pass preflight and reach provider lookup before either can
	// commit a window; only the in-operation full-window recheck can reject the
	// second one.
	select {
	case <-firstLookup:
	case <-time.After(10 * time.Second):
		t.Fatalf("first request never reached lookup: errs=%v %v", errs[0], errs[1])
	}
	select {
	case <-secondLookup:
	case <-time.After(10 * time.Second):
		t.Fatal("second request never reached lookup")
	}
	close(releaseLookups)
	wg.Wait()

	succeeded, refused := 0, 0
	for _, err := range errs {
		if err == nil {
			succeeded++
		} else {
			refused++
			require.Condition(t, func() bool {
				return strings.Contains(err.Error(), "conflicting window") ||
					strings.Contains(err.Error(), "in progress") ||
					strings.Contains(err.Error(), "database is locked")
			}, "race must be refused by the operation or window check: %v", err)
		}
	}
	require.Equal(t, 1, succeeded, "exactly one same-mode window may win")
	require.Equal(t, 1, refused)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, string(storage.UpdateWindowAtTime), batch.Window)
	require.True(t, batch.ApplyAt.Equal(time.Unix(firstTime, 0).UTC()) ||
		batch.ApplyAt.Equal(time.Unix(secondTime, 0).UTC()))
}

func TestCumulativeBudgetCountsRejectedDownloads(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-budget-failures", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "b.jar"), []byte("installed-b"), 0o600))

	fetcher := &modUpdateFetchByFileFixture{
		data: map[string][]byte{"a.jar": []byte("bad"), "b.jar": []byte("next-b")},
		bad:  map[string]bool{"a.jar": true},
	}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(twoModPreviewFixture{})
	svc.SetModCandidateFetcher(fetcher)
	svc.SetModUpdateBatchBudget(int64(len("bad") + len("next-b") - 1))

	request := twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)
	request.UpdateSelections = append(request.UpdateSelections, &v1.ModUpdateSelection{
		ModId:              "mod-b.jar",
		FileName:           "b.jar",
		InstalledSha512:    installedModDigest("installed-b"),
		CandidateVersionId: "v2-project-b",
		CandidateSha512:    installedModDigest("next-b"),
		CandidateFilename:  "b.jar",
	})
	res, err := svc.UpdateMod(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	outcomes := map[string]int{}
	for _, row := range res.Msg.GetStagedUpdates() {
		outcomes[row.GetOutcome()]++
	}
	require.Equal(t, 1, outcomes["refused_checksum"])
	require.Equal(t, 1, outcomes["refused_limit"], "bytes from the rejected download must consume budget")
	require.Empty(t, res.Msg.GetUpdateBatchId())
	require.Empty(t, stagedFiles(t, server.DataPath))
}

func TestRacedDiscardCannotOrphanStagedModPayload(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-discard-race", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "b.jar"), []byte("installed-b"), 0o600))

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(twoModPreviewFixture{})
	svc.SetModCandidateFetcher(&modUpdateFetchByFileFixture{
		data:  map[string][]byte{"a.jar": []byte("next-a"), "b.jar": []byte("next-b")},
		delay: 100 * time.Millisecond,
	})
	configSvc := NewConfigService(store, nil, nil, nil, logger.New())
	request := twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)

	type stageResult struct {
		batchID string
		err     error
	}
	result := make(chan stageResult, 1)
	go func() {
		res, err := svc.UpdateMod(ctx, connect.NewRequest(request))
		if err != nil {
			result <- stageResult{err: err}
			return
		}
		result <- stageResult{batchID: res.Msg.GetUpdateBatchId()}
	}()
	_, discardErr := configSvc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{ServerId: server.ID}))
	staged := <-result

	if staged.err != nil {
		// A discard that won the ownership race must leave no orphan payload.
		require.Empty(t, stagedFiles(t, server.DataPath))
		return
	}
	require.NoError(t, discardErr)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, staged.batchID, batch.ID, "the reply must name the batch that actually holds the bytes")
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		require.Equal(t, staged.batchID, row.BatchID)
	}
	phase, err := store.GetStagedContent(ctx, rows[0].ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, phase.Status)
}

func TestStageReviewedModUpdateKeepsLiveBytesAndStagesExactCandidate(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-apply", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	live := filepath.Join(mods, "mod.jar")
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))

	fetcher := &modUpdateFetchFixture{data: []byte("next")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(fetcher)

	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 1)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome(), res.Msg.GetStagedUpdates()[0].GetReason())
	require.NotEmpty(t, res.Msg.GetStagedUpdates()[0].GetStagedId())
	require.NotEmpty(t, res.Msg.GetUpdateBatchId())
	require.Equal(t, 1, fetcher.calls)

	after, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(after), "manual apply must not write the live mods directory")

	staged, err := os.ReadFile(filepath.Join(server.DataPath, ".carbon-panel", "staged",
		res.Msg.GetStagedUpdates()[0].GetStagedId(), "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "next", string(staged), "staged bytes must be the checked candidate")

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, string(storage.UpdateWindowNextRestart), batch.Window)

	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, storage.StagedKindMod, rows[0].Kind)
	require.Equal(t, storage.StagedActionReplace, rows[0].Action)
	require.Equal(t, filepath.ToSlash(filepath.Join("mods", "mod.jar")), rows[0].TargetPath)
}

func TestStageReviewedModUpdateRefusesInstalledDrift(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-drift", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("different"), 0o600))

	fetcher := &modUpdateFetchFixture{data: []byte("next")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(fetcher)

	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 1)
	require.Equal(t, "refused_drift", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Empty(t, res.Msg.GetUpdateBatchId())
	require.Zero(t, fetcher.calls, "drifted bytes must be refused before any provider download")
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch)
}

func TestStageReviewedModUpdateRefusesChecksumMismatch(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-checksum", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	fetcher := &modUpdateFetchFixture{err: errors.New("candidate bytes do not match the reviewed SHA-512"), outcome: "refused_checksum"}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(fetcher)

	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "refused_checksum", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Empty(t, res.Msg.GetUpdateBatchId())
	require.Empty(t, stagedFiles(t, server.DataPath), "a failed candidate must leave no staged bytes")
}

func TestScheduledModUpdatePolicyStagesOnceAndHonorsExclusions(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	lookup := newPreviewFixture()
	fetcher := &modUpdateFetchFixture{data: []byte("next")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(lookup)
	svc.SetModCandidateFetcher(fetcher)

	staged, refused, reasons, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{})
	require.NoError(t, err)
	require.Zero(t, staged, "a disabled policy must not stage")
	require.Zero(t, refused)
	require.Empty(t, reasons)

	staged, refused, reasons, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{
		Enabled:          true,
		ExcludedProjects: []string{"project"},
	})
	require.NoError(t, err)
	require.Zero(t, staged, "an excluded project must not be staged")
	require.Equal(t, 1, refused)
	require.NotEmpty(t, reasons, "the refusal cause must be reported to the operator")
	require.Zero(t, fetcher.calls)

	staged, refused, reasons, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, 1, staged)
	require.Zero(t, refused)
	require.Empty(t, reasons)
	require.Equal(t, 1, fetcher.calls)

	staged, refused, reasons, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.NoError(t, err)
	require.Zero(t, staged, "a second run must deduplicate the already staged file")
	require.Equal(t, 1, refused)
	require.NotEmpty(t, reasons)
	require.Equal(t, 1, fetcher.calls, "deduplication must happen before any provider download")

	live, err := os.ReadFile(filepath.Join(mods, "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "installed", string(live), "an automatic policy must not write the live tree")
}

func TestScheduledIdleWindowStagesThroughRealPlanner(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy-idle", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})

	staged, refused, reasons, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{
		Enabled: true, Window: "idle", IdleMinutes: 15,
	})
	require.NoError(t, err)
	require.Equal(t, 1, staged)
	require.Zero(t, refused)
	require.Empty(t, reasons)

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, string(storage.UpdateWindowIdle), batch.Window)
	require.Equal(t, 15, batch.IdleMinutes)
	require.Nil(t, batch.ApplyAt)
	live, err := os.ReadFile(filepath.Join(mods, "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "installed", string(live))
}

func TestTransientProviderFailureReturnsRetryableError(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy-transient", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	fetcher := &modUpdateFetchFixture{outcome: "transient_download", err: errors.New("provider temporarily unavailable")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(fetcher)

	_, _, reasons, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.Error(t, err, "a transient download must reach the scheduler retry policy")
	require.Contains(t, err.Error(), "provider temporarily unavailable")
	require.NotEmpty(t, reasons)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch, "no batch should be committed when nothing staged")

	fetcher.outcome, fetcher.err, fetcher.data = "", nil, []byte("next")
	staged, _, _, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, 1, staged, "the retry must stage through the real planner")
}

func TestStagingRefusesToRetargetAnAlreadyReviewedBatch(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-window-conflict", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))
	_, err := store.StageServerConfigUpdate(ctx, server.ID, map[string]any{"motd": "pending"}, storage.UpdateWindowNextRestart, nil, 0)
	require.NoError(t, err)

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})
	request := modUpdateRequest(server.ID, installedModDigest("installed"))
	request.UpdateWindow = v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE
	request.UpdateIdleMinutes = 15

	_, err = svc.UpdateMod(ctx, connect.NewRequest(request))
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing conflicting window")

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, string(storage.UpdateWindowNextRestart), batch.Window)
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Empty(t, rows, "no mod bytes should be staged into the conflicting batch")
	live, err := os.ReadFile(filepath.Join(mods, "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "installed", string(live))
}

func TestDuplicateSelectionDownloadsOnceAndRefusesConflictingWindow(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-duplicate", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	fetcher := &modUpdateFetchFixture{data: []byte("next")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(fetcher)
	request := modUpdateRequest(server.ID, installedModDigest("installed"))
	request.UpdateSelections = append(request.UpdateSelections, request.UpdateSelections[0])

	res, err := svc.UpdateMod(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 2)
	outcomes := map[string]int{}
	for _, row := range res.Msg.GetStagedUpdates() {
		outcomes[row.GetOutcome()]++
	}
	require.Equal(t, 1, outcomes["staged"])
	require.Equal(t, 1, outcomes["already_selected"])
	require.Equal(t, 1, fetcher.calls, "a duplicate target must not repeat the download")
	rows, err := store.ListBatchContent(ctx, res.Msg.GetUpdateBatchId())
	require.NoError(t, err)
	require.Len(t, rows, 1)

	// The same enum with a different idle interval must be refused before a
	// second download, leaving the reviewed batch untouched.
	second := modUpdateRequest(server.ID, installedModDigest("installed"))
	second.UpdateWindow = v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE
	second.UpdateIdleMinutes = 45
	_, err = svc.UpdateMod(ctx, connect.NewRequest(second))
	require.Error(t, err)
	require.Contains(t, err.Error(), "refusing conflicting window")
	require.Equal(t, 1, fetcher.calls)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, string(storage.UpdateWindowNextRestart), batch.Window)
	require.Equal(t, 0, batch.IdleMinutes)
}

func TestCumulativeCandidateBudgetIsEnforced(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-budget", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("candidate")})
	svc.SetModUpdateBatchBudget(4)

	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "refused_limit", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Empty(t, res.Msg.GetUpdateBatchId())
	require.Empty(t, stagedFiles(t, server.DataPath), "no bytes staged when the budget is exceeded")

	svc.SetModUpdateBatchBudget(1024)
	res, err = svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())
}

func TestStageReviewedModUpdateRequiresKnownServerAndValidWindow(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-window", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})

	_, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest("missing-server", installedModDigest("installed"))))
	require.Error(t, err)
	require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))

	atTime := modUpdateRequest(server.ID, installedModDigest("installed"))
	atTime.UpdateWindow = v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME
	_, err = svc.UpdateMod(ctx, connect.NewRequest(atTime))
	require.Error(t, err, "at_time requires an apply time")

	idle := modUpdateRequest(server.ID, installedModDigest("installed"))
	idle.UpdateWindow = v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE
	_, err = svc.UpdateMod(ctx, connect.NewRequest(idle))
	require.Error(t, err, "idle requires positive idle minutes")
}
