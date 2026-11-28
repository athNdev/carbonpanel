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
	"github.com/athNdev/carbon-panel/internal/activity"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/internal/scheduler"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type modUpdateFetchFixture struct {
	data    []byte
	outcome string
	err     error
	calls   int
	loader  string
	game    string
}

func (f *modUpdateFetchFixture) Fetch(_ context.Context, _ *storage.ServerConfig, _, _, _, _, loader, game string, _ int64) ([]byte, int64, string, error) {
	f.calls++
	f.loader, f.game = loader, game
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
					strings.Contains(err.Error(), "active operation") ||
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, server, um := newContentStageFixture(t, "mod-discard-race", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(twoModPreviewFixture{})
	svc.SetModCandidateFetcher(&modUpdateFetchByFileFixture{
		data: map[string][]byte{"a.jar": []byte("next-a")},
	})
	configSvc := NewConfigService(store, nil, nil, nil, logger.New())
	discard := func() error {
		_, err := configSvc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{ServerId: server.ID}))
		return err
	}
	var raced bool
	require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register("fixture:discard_during_mod_insert", func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*storage.StagedContentChange)
		if !ok {
			return
		}
		// The real producer wrote payload bytes and still owns the journal.
		// Discard must not close its batch before the staged row commits.
		require.FileExists(t, filepath.Join(server.DataPath, filepath.FromSlash(row.StagePath)))
		require.ErrorContains(t, discard(), "already has an active operation")
		raced = true
	}))

	request := twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)
	response, err := svc.UpdateMod(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.True(t, raced)
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, response.Msg.GetUpdateBatchId(), batch.ID)
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Len(t, stagedFiles(t, server.DataPath), 1)

	require.NoError(t, discard(), "discard can retry after staging releases the journal")
	closed, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, closed)
	require.Empty(t, stagedFiles(t, server.DataPath))
	row, err := store.GetStagedContent(ctx, rows[0].ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusDiscarded, row.Status)
}

func TestReviewedUpdateRejectsInvalidWindowsBeforeProviderWork(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "invalid-update-window", storage.StatusStopped)
	svc := NewModService(store, nil, um, logger.New())
	for _, window := range []v1.ConfigApplyWindow{
		v1.ConfigApplyWindow(999),
		v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME,
		v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE,
	} {
		req := modUpdateRequest(server.ID, installedModDigest("installed"))
		req.UpdateWindow = window
		_, err := svc.UpdateMod(ctx, connect.NewRequest(req))
		require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "%s: %v", window, err)
	}
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch)
	var failures []storage.ActivityLog
	require.NoError(t, store.DB().Where("event = ? AND subject_id = ?", activity.EventModUpdate, server.ID).Find(&failures).Error)
	require.Len(t, failures, 3)
	for _, failure := range failures {
		require.Contains(t, failure.Properties, "invalid mod update window")
	}
}

func TestStagingRefusesBatchWithIndependentCronConfig(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-cron-cohab", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))
	cron, err := store.StageConfigChange(ctx, server.ID,
		map[string]any{"motd": "nightly"}, storage.StagedApplyScheduled, "0 3 * * *")
	require.NoError(t, err)

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})

	_, err = svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.Error(t, err)
	require.Contains(t, err.Error(), "independently scheduled config row")

	_, _, _, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.Error(t, err, "the scheduled entry point must refuse the same cohabitation")
	require.Contains(t, err.Error(), "independently scheduled config row")

	rows, err := store.ListBatchContent(ctx, cron.BatchID)
	require.NoError(t, err)
	require.Empty(t, rows, "no mod payload may be staged into the cron batch")
	staged, err := store.GetStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, staged.Status)
	require.Equal(t, storage.StagedApplyScheduled, staged.ApplyMode)
	live, err := os.ReadFile(filepath.Join(mods, "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "installed", string(live))
}

func TestConfigCommitsAfterModPreparationBeforeContentInsert(t *testing.T) {
	for _, change := range []string{"cron", "retarget"} {
		t.Run(change, func(t *testing.T) {
			testConfigCommitBeforeModInsert(t, change)
		})
	}
}

func testConfigCommitBeforeModInsert(t *testing.T, change string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, server, um := newContentStageFixture(t, "config-before-mod-insert", storage.StatusStopped)
	live := filepath.Join(server.DataPath, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))
	modSvc := NewModService(store, nil, um, logger.New())
	modSvc.SetModUpdateLookup(newPreviewFixture())
	modSvc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})
	cronSvc := NewStagedConfigService(store, handlers.NewStagedConfigOperations(store), nil)
	cfgSvc := NewConfigService(store, nil, nil, nil, logger.New())
	when := time.Now().Add(time.Hour).Unix()

	// Pause the real content producer immediately before its SQLite write.
	// The mod has already checked its batch twice and written staged bytes.
	var batchID string
	require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register("fixture:stage_config_before_mod_insert", func(tx *gorm.DB) {
		row, ok := tx.Statement.Dest.(*storage.StagedContentChange)
		if !ok {
			return
		}
		require.FileExists(t, filepath.Join(server.DataPath, filepath.FromSlash(row.StagePath)))
		batchID = row.BatchID
		if change == "retarget" {
			_, err := cfgSvc.UpdateServerConfig(ctx, connect.NewRequest(&v1.UpdateServerConfigRequest{
				ServerId: server.ID, Updates: map[string]string{"difficulty": "hard"}, Stage: true,
				Window: v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME, ApplyAtUnix: when,
			}))
			require.NoError(t, err)
			return
		}
		_, err := cronSvc.StageConfigChange(ctx, connect.NewRequest(&v1.StageConfigChangeRequest{
			ServerId: server.ID, Changes: `{"motd":"nightly"}`, ApplyMode: "scheduled", CronExpr: "0 3 * * *",
		}))
		require.NoError(t, err)
	}))
	req := modUpdateRequest(server.ID, installedModDigest("installed"))
	_, err := modSvc.UpdateMod(ctx, connect.NewRequest(req))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	configs, readErr := store.ListBatchConfig(ctx, batchID)
	require.NoError(t, readErr)
	require.Len(t, configs, 1)
	require.Equal(t, storage.StagedStatusStaged, configs[0].Status)
	if change == "cron" {
		require.ErrorContains(t, err, "independently scheduled config row")
		require.Equal(t, storage.StagedApplyScheduled, configs[0].ApplyMode)
		require.Equal(t, "0 3 * * *", configs[0].CronExpr)
	} else {
		require.ErrorContains(t, err, "changed window before staging")
		require.Equal(t, storage.StagedApplyAtTime, configs[0].ApplyMode)
		require.Equal(t, when, configs[0].ApplyAt.Unix())
	}
	rows, err := store.ListBatchContent(ctx, batchID)
	require.NoError(t, err)
	require.Empty(t, rows, "the refused insert must roll back")
	require.Empty(t, stagedFiles(t, server.DataPath), "the refused payload must be removed")
	data, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(data))
}

func TestCronCannotJoinAnExistingTimedModBatch(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "cron-inverse", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	res, err := func() (*connect.Response[v1.UpdateModResponse], error) {
		svc := NewModService(store, nil, um, logger.New())
		svc.SetModUpdateLookup(newPreviewFixture())
		svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})
		return svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	}()
	require.NoError(t, err)
	require.NotEmpty(t, res.Msg.GetUpdateBatchId())

	cfgSvc := NewStagedConfigService(store, handlers.NewStagedConfigOperations(store), nil)
	_, err = cfgSvc.StageConfigChange(ctx, connect.NewRequest(&v1.StageConfigChangeRequest{
		ServerId: server.ID, Changes: `{"motd":"nightly"}`, ApplyMode: "scheduled", CronExpr: "0 3 * * *",
	}))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "has staged content")

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, res.Msg.GetUpdateBatchId(), batch.ID)
	configs, err := store.ListBatchConfig(ctx, batch.ID)
	require.NoError(t, err)
	require.Empty(t, configs, "cron row must not join the timed mod batch")
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestConfigServiceRequiresReviewedBatchWindow(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "config-service-order", storage.StatusStopped)
	live := filepath.Join(server.DataPath, "mods", "mod.jar")
	require.NoError(t, os.MkdirAll(filepath.Dir(live), 0o700))
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))
	cfgSvc := NewConfigService(store, nil, nil, nil, logger.New())
	modSvc := NewModService(store, nil, um, logger.New())
	modSvc.SetModUpdateLookup(newPreviewFixture())
	modSvc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})

	when := time.Now().Add(2 * time.Hour).Unix()
	req := modUpdateRequest(server.ID, installedModDigest("installed"))
	req.UpdateWindow = v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME
	req.UpdateApplyAtUnix = when
	res, err := modSvc.UpdateMod(ctx, connect.NewRequest(req))
	require.NoError(t, err)
	require.NotEmpty(t, res.Msg.GetUpdateBatchId())
	before, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	content, err := store.ListBatchContent(ctx, before.ID)
	require.NoError(t, err)
	require.Len(t, content, 1)
	config, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)

	stage := func(serverID string, window v1.ConfigApplyWindow, at int64) error {
		_, err := cfgSvc.UpdateServerConfig(ctx, connect.NewRequest(&v1.UpdateServerConfigRequest{
			ServerId: serverID, Updates: map[string]string{"difficulty": "hard"},
			Stage: true, Window: window, ApplyAtUnix: at,
		}))
		return err
	}
	atTime := v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME
	nextRestart := v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART
	for _, err := range []error{stage(server.ID, atTime, when+3600), stage(server.ID, nextRestart, 0)} {
		require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	}
	after, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, before, after)
	configs, err := store.ListBatchConfig(ctx, before.ID)
	require.NoError(t, err)
	require.Empty(t, configs)
	current, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.Equal(t, config, current)
	afterContent, err := store.ListBatchContent(ctx, before.ID)
	require.NoError(t, err)
	require.Equal(t, content, afterContent)
	data, err := os.ReadFile(filepath.Join(server.DataPath, filepath.FromSlash(content[0].StagePath)))
	require.NoError(t, err)
	require.Equal(t, "next", string(data))
	data, err = os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(data))

	require.NoError(t, stage(server.ID, atTime, when))
	configs, err = store.ListBatchConfig(ctx, before.ID)
	require.NoError(t, err)
	require.Len(t, configs, 1)
	require.Equal(t, storage.StagedApplyAtTime, configs[0].ApplyMode)
	require.True(t, configs[0].ApplyAt.Equal(*before.ApplyAt))

	// A legacy batch shaped by an independent cron row also refuses a
	// timed window. It still accepts the historical next-restart config
	// while keeping the independent cron row byte-for-byte.
	cronServer := &storage.Server{ID: "config-service-cron", Name: "config-service-cron", DataPath: t.TempDir(), Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, cronServer))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(cronServer.ID)))
	cron, err := store.StageConfigChange(ctx, cronServer.ID,
		map[string]any{"motd": "nightly"}, storage.StagedApplyScheduled, "0 3 * * *")
	require.NoError(t, err)
	cron, err = store.GetStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	cronBatch, err := store.OpenUpdateBatch(ctx, cronServer.ID)
	require.NoError(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(stage(cronServer.ID, atTime, when)))
	after, err = store.OpenUpdateBatch(ctx, cronServer.ID)
	require.NoError(t, err)
	require.Equal(t, cronBatch, after)
	configs, err = store.ListBatchConfig(ctx, cron.BatchID)
	require.NoError(t, err)
	require.Equal(t, []*storage.StagedConfigChange{cron}, configs)
	require.NoError(t, stage(cronServer.ID, nextRestart, 0))
	still, err := store.GetStagedConfigChange(ctx, cron.ID)
	require.NoError(t, err)
	require.Equal(t, cron, still)
}

func TestDiscardUpdateBatchPreservesTypedRefusals(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "discard-typed", storage.StatusStopped)
	configSvc := NewConfigService(store, nil, nil, nil, logger.New())
	_ = um

	_, err := configSvc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{
		ServerId: server.ID, ItemId: "missing-item",
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	other := &storage.Server{ID: "discard-other", Name: "discard-other", DataPath: t.TempDir(), Status: storage.StatusStopped}
	require.NoError(t, store.CreateServer(ctx, other))
	row, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ID: "srv-item", ServerID: server.ID, Kind: storage.StagedKindMod,
		Action: storage.StagedActionReplace, TargetPath: "mods/a.jar",
		StagePath: ".carbon-panel/staged/a/a.jar", FileName: "a.jar",
	})
	require.NoError(t, err)
	_, err = configSvc.DiscardUpdateBatch(ctx, connect.NewRequest(&v1.DiscardUpdateBatchRequest{
		ServerId: other.ID, ItemId: row.ID,
	}))
	require.Error(t, err)
	require.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
	still, err := store.GetStagedContent(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, storage.StagedStatusStaged, still.Status, "a cross-server discard must not touch the row")
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
	require.Equal(t, installedModDigest("next"), rows[0].ApprovedSha512,
		"the admitted row records the release digest verified against the staged bytes")
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

func TestReviewedModUpdateStagesTheCopyMatchingTheReviewedDigest(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-disabled-copy", storage.StatusStopped)
	active := filepath.Join(server.DataPath, "mods")
	disabled := filepath.Join(server.DataPath, "mods_disabled")
	require.NoError(t, os.MkdirAll(active, 0o700))
	require.NoError(t, os.MkdirAll(disabled, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(active, "mod.jar"), []byte("active-bytes"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(disabled, "mod.jar"), []byte("disabled-bytes"), 0o600))

	lookup := newPreviewFixture()
	lookup.current.Files[0].Hashes.SHA512 = installedModDigest("disabled-bytes")
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(lookup)
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})

	request := modUpdateRequest(server.ID, installedModDigest("disabled-bytes"))
	res, err := svc.UpdateMod(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 1)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "mods_disabled/mod.jar", rows[0].TargetPath,
		"the reviewed disabled copy must not be replaced at the active path")
}

func TestReviewedModUpdateRefusesAmbiguousMatchingCopies(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-ambiguous-copy", storage.StatusStopped)
	for _, name := range []string{"mods", "mods_disabled"} {
		dir := filepath.Join(server.DataPath, name)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "mod.jar"), []byte("installed"), 0o600))
	}

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})

	request := modUpdateRequest(server.ID, installedModDigest("installed"))
	res, err := svc.UpdateMod(ctx, connect.NewRequest(request))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 1)
	require.Equal(t, "refused_drift", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Contains(t, res.Msg.GetStagedUpdates()[0].GetReason(), "both enabled and disabled copies")
	require.Empty(t, res.Msg.GetUpdateBatchId())
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch)
}

func TestReviewedModUpdateRefusesDependencyAddedAfterPreview(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-update-new-dependency", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	// The reviewed preview reported no dependencies, but the exact version,
	// filename and SHA-512 now carry a required dependency.
	version := candidateVersion("https://cdn.modrinth.test/files/mod-2.jar", installedModDigest("next"))
	version.Dependencies = []modrinth.Dependency{{DependencyType: "required"}}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(providerModCandidateFetcher{versions: versionLookupFixture{version: version}})

	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 1)
	require.Equal(t, "refused_provider", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Contains(t, res.Msg.GetStagedUpdates()[0].GetReason(), "dependencies changed since review")
	require.Empty(t, res.Msg.GetUpdateBatchId())
	require.Empty(t, stagedFiles(t, server.DataPath))
}

// A scheduled policy on a server whose data root has not been allocated yet
// must report an empty inventory instead of failing on the missing directory.
// The policy planner is a read path; it does not create the data root.
func TestScheduledModUpdatePolicyWithoutDataRootReportsEmptyInventory(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy-no-root", storage.StatusStopped)
	server.DataPath = filepath.Join(t.TempDir(), "not-created")
	require.NoError(t, store.UpdateServer(ctx, server))

	svc := NewModService(store, nil, um, logger.New())
	staged, refused, reasons, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.NoError(t, err)
	require.Zero(t, staged)
	require.Zero(t, refused)
	require.Nil(t, reasons)
	require.NoDirExists(t, server.DataPath)
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
	require.Zero(t, refused, "an already staged file is not a refusal")
	require.Empty(t, reasons)
	require.Equal(t, 1, fetcher.calls, "deduplication must happen before any provider download")

	live, err := os.ReadFile(filepath.Join(mods, "mod.jar"))
	require.NoError(t, err)
	require.Equal(t, "installed", string(live), "an automatic policy must not write the live tree")
}

// TestScheduledRunningPolicyRechecksAtAdmission covers a server that starts
// while the provider is downloading. The earlier stopped snapshot must not
// authorize a scheduled policy that opted out of running servers, but the same
// transition must not change manual staging behavior.
func TestScheduledRunningPolicyRechecksAtAdmission(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy-running-admission", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	fetcher := &modUpdateFetchByFileFixture{
		data: map[string][]byte{"mod-2.jar": []byte("next")},
		onFetch: func(string) {
			require.NoError(t, store.UpdateServerStatus(ctx, server.ID, storage.StatusRunning))
		},
	}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(fetcher)

	staged, refused, reasons, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{
		Enabled: true, SkipWhenRunning: true,
	})
	require.NoError(t, err)
	require.Zero(t, staged, "a server that started during the download must not be staged by a running-server policy")
	require.Zero(t, refused)
	require.Len(t, reasons, 1)
	require.Contains(t, reasons[0], "running-server policy")
	require.Equal(t, 1, fetcher.calls, "the transition is detected after the provider work at journal admission")
	require.Empty(t, stagedFiles(t, server.DataPath))
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch, "a policy skip must not create an empty batch")

	// An already-running server skips before provider work.
	fetcher.calls = 0
	staged, refused, reasons, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{
		Enabled: true, SkipWhenRunning: true,
	})
	require.NoError(t, err)
	require.Zero(t, staged)
	require.Zero(t, refused)
	require.GreaterOrEqual(t, len(reasons), 1)
	require.Contains(t, reasons[0], "running-server policy")
	require.Zero(t, fetcher.calls, "an already-running server must skip before the provider download")

	// Manual review remains authoritative: it never consults the scheduler's
	// running-server policy and still stages the reviewed bytes.
	require.NoError(t, store.UpdateServerStatus(ctx, server.ID, storage.StatusStopped))
	fetcher.onFetch = func(string) {
		require.NoError(t, store.UpdateServerStatus(ctx, server.ID, storage.StatusRunning))
	}
	res, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Len(t, res.Msg.GetStagedUpdates(), 1)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.NotEmpty(t, res.Msg.GetUpdateBatchId())
	require.Len(t, stagedFiles(t, server.DataPath), 1)
}

type failSecondCandidateOnce struct {
	calls  []string
	failed bool
}

func (f *failSecondCandidateOnce) Fetch(_ context.Context, _ *storage.ServerConfig, _, _, name, _, _, _ string, _ int64) ([]byte, int64, string, error) {
	f.calls = append(f.calls, name)
	if name == "b.jar" && !f.failed {
		f.failed = true
		return nil, 0, "transient_download", errors.New("candidate download unavailable")
	}
	data := []byte("next-" + name[:1])
	return data, int64(len(data)), "", nil
}

func TestScheduledRetryDoesNotCountAlreadyStagedModAsRefused(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy-retry-report", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "b.jar"), []byte("installed-b"), 0o600))

	fetcher := &failSecondCandidateOnce{}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(twoModPreviewFixture{})
	svc.SetModCandidateFetcher(fetcher)
	sched := scheduler.NewScheduler(store, nil, nil, nil, nil, logger.New())
	sched.SetModUpdatePlanner(svc)
	task := &storage.ScheduledTask{
		ID: "retry-report", ServerID: server.ID, Name: "test staged count",
		TaskType: storage.TaskTypeModUpdate, Status: storage.TaskStatusEnabled,
		Schedule: storage.ScheduleTypeCron, CronExpr: "0 3 * * *", RetryCount: 1, RetryDelay: 1,
		Config: `{"enabled":true,"max_updates_per_run":2}`,
	}
	require.NoError(t, store.CreateScheduledTask(ctx, task))
	execution, err := sched.TriggerTask(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, storage.ExecutionStatusCompleted, execution.Status)
	require.Equal(t, 1, execution.RetryNum)
	require.Equal(t, "staged 2 compatible update(s); 0 refused and left untouched", execution.Output)
	require.Equal(t, []string{"a.jar", "b.jar", "b.jar"}, fetcher.calls)
	require.Len(t, stagedFiles(t, server.DataPath), 2)
	stored, err := store.GetScheduledTask(ctx, task.ID)
	require.NoError(t, err)
	require.NotNil(t, stored.NextRun, "manual execution can advance an observed NULL deadline")
	require.NotNil(t, stored.LastRun)
}

func TestScheduledStagingStorageFailureIsRetryableAndPreservesStagedItems(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy-stage-storage", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "b.jar"), []byte("installed-b"), 0o600))

	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(twoModPreviewFixture{})
	svc.SetModCandidateFetcher(&modUpdateFetchByFileFixture{data: map[string][]byte{
		"a.jar": []byte("next-a"), "b.jar": []byte("next-b"),
	}})

	const callbackName = "fixture:fail-second-staged-content"
	require.NoError(t, store.DB().Callback().Create().Before("gorm:create").Register(
		callbackName,
		func(tx *gorm.DB) {
			row, ok := tx.Statement.Dest.(*storage.StagedContentChange)
			if !ok || row.FileName != "b.jar" {
				return
			}
			_ = tx.AddError(errors.New("storage write failed"))
		},
	))

	staged, refused, _, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{
		Enabled: true, MaxUpdatesPerRun: 2,
	})
	require.ErrorContains(t, err, "storage write failed", "a per-item storage failure must reach the scheduler retry policy")
	require.Equal(t, 1, staged, "items staged before the failure stay committed")
	require.Equal(t, 1, refused)

	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	rows, err := store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "a.jar", rows[0].FileName)

	require.NoError(t, store.DB().Callback().Create().Remove(callbackName))
	staged, refused, _, err = svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{
		Enabled: true, MaxUpdatesPerRun: 2,
	})
	require.NoError(t, err)
	require.Equal(t, 1, staged, "the retry stages only the file that failed")
	require.Zero(t, refused)
	rows, err = store.ListBatchContent(ctx, batch.ID)
	require.NoError(t, err)
	require.Len(t, rows, 2, "the committed item must not be staged twice")
}

func TestScheduledStagingValidationRefusalIsNotRetryable(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy-stage-validation", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	// The fetcher claims success but returns bytes that do not match the
	// reviewed digest. Staging refuses them definitively; a retry cannot help.
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("tampered")})

	staged, refused, reasons, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.NoError(t, err, "a definitive validation refusal is an ordinary policy outcome")
	require.Zero(t, staged)
	require.Equal(t, 1, refused)
	require.NotEmpty(t, reasons)
	require.Empty(t, stagedFiles(t, server.DataPath))
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	if batch != nil {
		rows, err := store.ListBatchContent(ctx, batch.ID)
		require.NoError(t, err)
		require.Empty(t, rows)
	}
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

type scheduledLookupFailure struct {
	*modPreviewFixture
	failProject bool
	failPage    int
	calls       int
}

func (f *scheduledLookupFailure) CompatibleUpdatesByFileHash(ctx context.Context, hashes []string, loader, game string) (map[string]modrinth.Version, error) {
	f.calls++
	if f.calls == f.failPage {
		return nil, errors.New("provider update lookup unavailable")
	}
	return f.modPreviewFixture.CompatibleUpdatesByFileHash(ctx, hashes, loader, game)
}

func (f *scheduledLookupFailure) GetProjects(ctx context.Context, ids []string) ([]modrinth.ProjectDetails, error) {
	if f.failProject {
		return nil, errors.New("provider project lookup unavailable")
	}
	return f.modPreviewFixture.GetProjects(ctx, ids)
}

func TestScheduledProviderFailuresRetryEvenWithSomeStagedUpdates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		projects   bool
		page, want int
	}{
		{name: "project lookup", projects: true},
		{name: "partial update lookup", page: 2, want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, server, um := newContentStageFixture(t, "mod-policy-provider", storage.StatusStopped)
			mods := filepath.Join(server.DataPath, "mods")
			require.NoError(t, os.MkdirAll(mods, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(mods, "000.jar"), []byte("installed"), 0o600))
			if tc.want > 0 {
				for i := 1; i <= 100; i++ {
					name := fmt.Sprintf("%03d.jar", i)
					require.NoError(t, os.WriteFile(filepath.Join(mods, name), []byte(name), 0o600))
				}
			}
			svc := NewModService(store, nil, um, logger.New())
			svc.SetModUpdateLookup(&scheduledLookupFailure{
				modPreviewFixture: newPreviewFixture(), failProject: tc.projects, failPage: tc.page,
			})
			svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})
			staged, _, _, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
			require.ErrorContains(t, err, "Provider lookup failed: Try again later.")
			require.NotContains(t, err.Error(), "provider project lookup unavailable",
				"provider diagnostics stay server-side and out of the refusal reason")
			require.Equal(t, tc.want, staged)
		})
	}
}

func TestScheduledInventoryReadFailureIsRetryable(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-policy-inventory", storage.StatusStopped)
	require.NoError(t, os.WriteFile(filepath.Join(server.DataPath, "mods"), []byte("unexpected file"), 0o600))
	svc := NewModService(store, nil, um, logger.New())
	_, _, _, err := svc.StageAvailableUpdates(ctx, server.ID, storage.ModUpdateTaskConfig{Enabled: true})
	require.ErrorContains(t, err, "read installed mods directory")
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
	svc.SetModCandidateFetcher(&modUpdateFetchFixture{data: []byte("next")})
	svc.SetModUpdateBatchBudget(3)

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

// A committed runtime target change during download invalidates the reviewed
// loader/game pair. The journaled admission must refuse before staging rather
// than write a candidate the new runtime never reviewed.
func TestReviewedModUpdateRefusesRuntimeChangeDuringDownload(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-runtime-during-download", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	live := filepath.Join(mods, "mod.jar")
	require.NoError(t, os.WriteFile(live, []byte("installed"), 0o600))

	fetcher := &modUpdateFetchByFileFixture{
		data: map[string][]byte{"mod-2.jar": []byte("next")},
		onFetch: func(string) {
			cfg, err := store.GetServerConfig(ctx, server.ID)
			require.NoError(t, err)
			next := "1.21.4"
			cfg.Version = &next
			require.NoError(t, store.SaveServerConfig(ctx, cfg))
		},
	}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(fetcher)

	_, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.Error(t, err)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	require.Contains(t, err.Error(), "review the update again")
	require.Equal(t, 1, fetcher.calls)
	body, err := os.ReadFile(live)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body), "the live jar must not be replaced")
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, batch, "a refused runtime change must not leave an open batch")
	require.Empty(t, stagedFiles(t, server.DataPath))
}

// Resubmitting A alongside B must report A as already staged without spending
// the cumulative budget, so B still gets its download.
func TestAlreadyStagedUpdateDoesNotConsumeAnotherCandidateBudget(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-already-staged-budget", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "b.jar"), []byte("installed-b"), 0o600))

	fetcher := &modUpdateFetchByFileFixture{
		data: map[string][]byte{"a.jar": []byte("next-a"), "b.jar": []byte("next-b")},
	}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(twoModPreviewFixture{})
	svc.SetModCandidateFetcher(fetcher)
	svc.SetModUpdateBatchBudget(int64(len("next-a")))

	first := twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)
	res, err := svc.UpdateMod(ctx, connect.NewRequest(first))
	require.NoError(t, err)
	require.Equal(t, "staged", res.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Equal(t, 1, fetcher.calls)

	fetcher.calls = 0
	second := twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)
	second.UpdateSelections = append(second.UpdateSelections, &v1.ModUpdateSelection{
		ModId: "mod-b.jar", FileName: "b.jar",
		InstalledSha512:    installedModDigest("installed-b"),
		CandidateVersionId: "v2-project-b",
		CandidateSha512:    installedModDigest("next-b"),
		CandidateFilename:  "b.jar",
	})
	res, err = svc.UpdateMod(ctx, connect.NewRequest(second))
	require.NoError(t, err)
	outcomes := map[string]string{}
	for _, row := range res.Msg.GetStagedUpdates() {
		outcomes[row.GetFileName()] = row.GetOutcome()
	}
	require.Equal(t, "already_staged", outcomes["a.jar"])
	require.Equal(t, "staged", outcomes["b.jar"])
	require.Equal(t, 1, fetcher.calls, "the already-staged file must not be downloaded again")
	rows, err := store.ListBatchContent(ctx, res.Msg.GetUpdateBatchId())
	require.NoError(t, err)
	require.Len(t, rows, 2)
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

// A resubmission whose every selection is already in the open batch must name
// the batch it was checked against, so the caller does not treat the response
// as batchless.
func TestAllAlreadyStagedResubmissionReturnsOpenBatchID(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-all-already-staged", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "mod.jar"), []byte("installed"), 0o600))

	fetcher := &modUpdateFetchFixture{data: []byte("next")}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(newPreviewFixture())
	svc.SetModCandidateFetcher(fetcher)

	first, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "staged", first.Msg.GetStagedUpdates()[0].GetOutcome())
	batch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, batch)
	require.Equal(t, batch.ID, first.Msg.GetUpdateBatchId())
	require.Equal(t, 1, fetcher.calls)

	fetcher.calls = 0
	second, err := svc.UpdateMod(ctx, connect.NewRequest(modUpdateRequest(server.ID, installedModDigest("installed"))))
	require.NoError(t, err)
	require.Equal(t, "already_staged", second.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Equal(t, batch.ID, second.Msg.GetUpdateBatchId(), "an all-already-staged resubmission must carry the checked batch id")
	require.Zero(t, fetcher.calls, "an already-staged file must not be downloaded again")
	rows, err := store.ListBatchContent(ctx, second.Msg.GetUpdateBatchId())
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

// A batch discarded after the pre-fetch snapshot must not be named as the batch
// that holds an all-already-staged resubmission. The result is retryable, and a
// resubmission can stage the file.
func TestAllAlreadyStagedResubmissionDroppedBatchIsRetryable(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-all-already-staged-dropped", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))

	fetcher := &modUpdateFetchByFileFixture{data: map[string][]byte{"a.jar": []byte("next-a")}}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(twoModPreviewFixture{})
	svc.SetModCandidateFetcher(fetcher)

	first, err := svc.UpdateMod(ctx, connect.NewRequest(
		twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)))
	require.NoError(t, err)
	require.Equal(t, "staged", first.Msg.GetStagedUpdates()[0].GetOutcome())
	firstBatch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, firstBatch)

	var discardOnce sync.Once
	svc.SetModUpdateLookup(twoModPreviewFixture{onLookup: func(string) {
		discardOnce.Do(func() {
			dropContentBatch(t, ctx, store, firstBatch.ID)
		})
	}})
	fetcher.calls = 0
	second, err := svc.UpdateMod(ctx, connect.NewRequest(
		twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)))
	require.NoError(t, err)
	require.Empty(t, second.Msg.GetUpdateBatchId(), "a discarded batch must not be returned as the target")
	require.Len(t, second.Msg.GetStagedUpdates(), 1)
	require.Equal(t, modUpdateOutcomeRetryable, second.Msg.GetStagedUpdates()[0].GetOutcome())
	require.Zero(t, fetcher.calls, "a retryable already-staged result must not download again")

	current, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, current)
}

// A-already-staged plus B: if the snapshot's batch is discarded while B is
// downloaded, A must become retryable while B stages into the new batch.
func TestAlreadyStagedFileIsRetryableWhenBatchDiscardedBeforeAdmission(t *testing.T) {
	ctx := context.Background()
	store, server, um := newContentStageFixture(t, "mod-stale-already-staged", storage.StatusStopped)
	mods := filepath.Join(server.DataPath, "mods")
	require.NoError(t, os.MkdirAll(mods, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "a.jar"), []byte("installed-a"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(mods, "b.jar"), []byte("installed-b"), 0o600))

	fetcher := &modUpdateFetchByFileFixture{
		data: map[string][]byte{"a.jar": []byte("next-a"), "b.jar": []byte("next-b")},
	}
	svc := NewModService(store, nil, um, logger.New())
	svc.SetModUpdateLookup(twoModPreviewFixture{})
	svc.SetModCandidateFetcher(fetcher)

	first, err := svc.UpdateMod(ctx, connect.NewRequest(
		twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)))
	require.NoError(t, err)
	require.Equal(t, "staged", first.Msg.GetStagedUpdates()[0].GetOutcome())
	firstBatch, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, firstBatch)

	fetcher.calls = 0
	fetcher.onFetch = func(filename string) {
		if filename == "b.jar" {
			dropContentBatch(t, ctx, store, firstBatch.ID)
		}
	}
	second := twoModRequest(server.ID, "a.jar", "installed-a", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0)
	second.UpdateSelections = append(second.UpdateSelections,
		twoModRequest(server.ID, "b.jar", "installed-b", v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART, 0).UpdateSelections...)

	res, err := svc.UpdateMod(ctx, connect.NewRequest(second))
	require.NoError(t, err)
	require.Equal(t, 1, fetcher.calls, "only B is downloaded; A was reported already staged in the snapshot")
	outcomes := map[string]string{}
	for _, row := range res.Msg.GetStagedUpdates() {
		outcomes[row.GetFileName()] = row.GetOutcome()
	}
	require.Equal(t, modUpdateOutcomeRetryable, outcomes["a.jar"])
	require.Equal(t, "staged", outcomes["b.jar"])
	require.NotEmpty(t, res.Msg.GetUpdateBatchId())
	require.NotEqual(t, firstBatch.ID, res.Msg.GetUpdateBatchId())
	rows, err := store.ListBatchContent(ctx, res.Msg.GetUpdateBatchId())
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "b.jar", rows[0].FileName)
}

// dropContentBatch models an operator discarding the open batch: staged rows
// become discarded and the batch closes so it can no longer be targeted.
func dropContentBatch(t *testing.T, ctx context.Context, store *storage.Store, batchID string) {
	t.Helper()
	rows, err := store.ListBatchContent(ctx, batchID)
	require.NoError(t, err)
	for _, row := range rows {
		_, err := store.DiscardStagedContent(ctx, row.ID)
		require.NoError(t, err)
	}
	require.NoError(t, store.CloseUpdateBatch(ctx, batchID, storage.UpdateBatchDiscarded))
}
