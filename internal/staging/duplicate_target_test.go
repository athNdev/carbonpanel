// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/require"
)

// TestLegacyDuplicateTargetRefusedBeforeApplying is the recovery boundary for
// rows older data may already have admitted twice. Publication refuses the
// duplicate before any live write, so it cannot record an APPLYING snapshot
// whose two entries one live post-image can never satisfy.
func TestLegacyDuplicateTargetRefusedBeforeApplying(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "legacy-duplicate", Name: "legacy-duplicate", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	require.NoError(t, store.SaveServerConfig(ctx, store.CreateDefaultServerConfig(server.ID)))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)
	stageFile(t, store, data, batch, server.ID, "file.txt", "first")

	// Legacy data may already hold a second row with an alias target. Write its
	// staged bytes too, so an old build genuinely publishes both rows instead
	// of failing earlier on a missing stage file.
	legacyStage := filepath.Join(data, ".carbon-panel", "staged", "legacy-duplicate-row")
	require.NoError(t, os.MkdirAll(legacyStage, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(legacyStage, "file.txt"), []byte("second"), 0o644))
	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID:         "legacy-duplicate-row",
		ServerID:   server.ID,
		BatchID:    batch,
		Kind:       storage.StagedKindFile,
		Action:     storage.StagedActionInstall,
		TargetPath: "FILE.TXT",
		StagePath:  ".carbon-panel/staged/legacy-duplicate-row/file.txt",
		FileName:   "file.txt",
		Status:     storage.StagedStatusStaged,
	}).Error)

	_, err := CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorContains(t, err, "more than one row for target")
	require.NoFileExists(t, filepath.Join(data, "file.txt"), "the refusal must precede every live write")

	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.NotEqual(t, storage.PublicationApplying, record.Phase,
		"an unverifiable duplicate must not record APPLYING")
}

// TestModpackExpandedOverlapRefusedBeforeApplying adds the expanded-target case
// the literal TargetPath check cannot see: a modpack row stores "modpack" but
// publishes config/shared.txt, so a separate file row for that path would make
// two undo entries for one live target.
func TestModpackExpandedOverlapRefusedBeforeApplying(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "modpack-overlap", Name: "modpack-overlap", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	batch := openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)

	src := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(src, "config"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(src, "config", "shared.txt"), []byte("pack"), 0o644))
	_, err := StageModpackTree(ctx, store, server, src, "modpack overlap")
	require.NoError(t, err)

	stage := filepath.Join(data, ".carbon-panel", "staged", "overlap-file")
	require.NoError(t, os.MkdirAll(stage, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(stage, "shared.txt"), []byte("file"), 0o644))
	require.NoError(t, store.DB().Create(&storage.StagedContentChange{
		ID:         "overlap-file",
		ServerID:   server.ID,
		BatchID:    batch,
		Kind:       storage.StagedKindFile,
		Action:     storage.StagedActionInstall,
		TargetPath: "config/shared.txt",
		StagePath:  ".carbon-panel/staged/overlap-file/shared.txt",
		FileName:   "shared.txt",
		Status:     storage.StagedStatusStaged,
	}).Error)

	_, err = CommitNextRestartFiles(ctx, store, server, nil)
	require.ErrorContains(t, err, "more than one row for target")
	require.NoFileExists(t, filepath.Join(data, "config", "shared.txt"), "the refusal must precede every live write")

	record, err := store.GetUpdatePublication(ctx, batch)
	require.NoError(t, err)
	require.NotNil(t, record)
	require.NotEqual(t, storage.PublicationApplying, record.Phase,
		"an expanded duplicate must not record APPLYING")
}

// TestAdmissionRefusesModpackWithFileRow is the conservative closure for
// overlaps admission cannot see without expanding the staged pack: a modpack
// and a separately staged file row cannot share one batch.
func TestAdmissionRefusesModpackWithFileRow(t *testing.T) {
	ctx := context.Background()
	store, data := newStageStore(t)
	server := &storage.Server{ID: "modpack-admission", Name: "modpack-admission", DataPath: data}
	require.NoError(t, store.CreateServer(ctx, server))
	openBatch(t, store, server.ID, storage.UpdateWindowNextRestart, nil, 0)

	_, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ServerID:   server.ID,
		Kind:       storage.StagedKindFile,
		Action:     storage.StagedActionInstall,
		TargetPath: "config/shared.txt",
		StagePath:  ".carbon-panel/staged/admission/shared.txt",
		FileName:   "shared.txt",
	})
	require.NoError(t, err)

	src := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(src, "keep.txt"), []byte("pack"), 0o644))
	_, err = StageModpackTree(ctx, store, server, src, "modpack admission")
	require.ErrorIs(t, err, storage.ErrModpackBatchConflict)
}
