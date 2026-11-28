// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"crypto/sha512"
	"fmt"
	"os"
	"path"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRestoreFilesKeepsUntouchedTargetsAfterLateRefusal(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		stageBytes            string
		changeBeforeLiveCheck bool
		wantError             string
	}{
		{"live drift", "approved", true, errStagedLiveDrift.Error()},
		{"staged digest mismatch", "changed stage", false, "durable approved SHA-512"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			acc, err := nodestorage.NewLocal(t.TempDir())
			require.NoError(t, err)
			t.Cleanup(func() { _ = acc.Close() })
			require.NoError(t, acc.WriteFile("first", []byte("old first")))
			require.NoError(t, acc.WriteFile("first.stage", []byte("new first")))
			require.NoError(t, acc.WriteFile("target", []byte("installed")))
			require.NoError(t, acc.WriteFile("stage", []byte(tc.stageBytes)))
			batch := uuid.NewString()
			approved := fmt.Sprintf("%x", sha512.Sum512([]byte("approved")))
			rows := []storage.StagedContentChange{
				{BatchID: batch, Kind: storage.StagedKindFile, Action: storage.StagedActionReplace, TargetPath: "first", StagePath: "first.stage"},
				{BatchID: batch, Kind: storage.StagedKindMod, Action: storage.StagedActionReplace, TargetPath: "target", StagePath: "stage", FileName: "target",
					ExpectedLiveSha512: fmt.Sprintf("%x", sha512.Sum512([]byte("installed"))), ApprovedSha512: approved,
					ApprovedProvider: "modrinth", ApprovedProjectID: "project", ApprovedVersionID: "release", ApprovedFilename: "target.jar"},
				{BatchID: batch, Kind: storage.StagedKindFile, Action: storage.StagedActionReplace, TargetPath: "later", StagePath: "first.stage"},
			}

			publication := &mutationTestAccessor{Accessor: acc}
			changeUnownedTargets := func() error {
				if err := acc.WriteFile("target", []byte("independent change")); err != nil {
					return err
				}
				return acc.WriteFile("later", []byte("independent creation"))
			}
			if tc.changeBeforeLiveCheck {
				publication.afterCommit = func(rel string) error {
					if rel == "first" {
						return changeUnownedTargets()
					}
					return nil
				}
			} else {
				publication.beforeRead = func(rel string) error {
					if rel == "stage" {
						return changeUnownedTargets()
					}
					return nil
				}
			}

			snaps, err := commitFiles(ctx, publication, rows)
			require.ErrorContains(t, err, tc.wantError)
			first, err := acc.ReadFile("first")
			require.NoError(t, err)
			require.Equal(t, "new first", string(first))

			require.NoError(t, restoreFiles(ctx, acc, snaps))
			for target, expected := range map[string]string{"first": "old first", "target": "independent change", "later": "independent creation"} {
				body, err := acc.ReadFile(target)
				require.NoError(t, err)
				require.Equal(t, expected, string(body), target)
			}
			_, err = acc.Stat(path.Join(undoRoot, batch))
			require.ErrorIs(t, err, os.ErrNotExist, "recovery directory should be removed")
		})
	}
}

func TestRestoreFilesKeepsUndoForAmbiguousCommit(t *testing.T) {
	ctx := context.Background()
	acc, err := nodestorage.NewLocal(t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { _ = acc.Close() })
	require.NoError(t, acc.WriteFile("target", []byte("old")))
	require.NoError(t, acc.WriteFile("stage", []byte("new")))
	publication := &mutationTestAccessor{Accessor: acc, afterCommit: func(rel string) error {
		if rel == "target" {
			return fmt.Errorf("commit acknowledgement lost")
		}
		return nil
	}}
	rows := []storage.StagedContentChange{{BatchID: uuid.NewString(), Kind: storage.StagedKindFile, Action: storage.StagedActionReplace, TargetPath: "target", StagePath: "stage"}}
	snaps, err := commitFiles(ctx, publication, rows)
	require.ErrorContains(t, err, "commit acknowledgement lost")
	body, err := acc.ReadFile("target")
	require.NoError(t, err)
	require.Equal(t, "new", string(body))
	require.NoError(t, restoreFiles(ctx, acc, snaps))
	body, err = acc.ReadFile("target")
	require.NoError(t, err)
	require.Equal(t, "old", string(body))
}

type mutationTestAccessor struct {
	nodestorage.Accessor
	beforeRead  func(string) error
	afterCommit func(string) error
}

func (a *mutationTestAccessor) OpenRead(ctx context.Context, rel string) (storageworker.ReadHandle, error) {
	if a.beforeRead != nil {
		if err := a.beforeRead(rel); err != nil {
			return nil, err
		}
	}
	return a.Accessor.OpenRead(ctx, rel)
}

func (a *mutationTestAccessor) BeginWrite(ctx context.Context, rel string, limit int64) (storageworker.AtomicWriter, error) {
	w, err := a.Accessor.BeginWrite(ctx, rel, limit)
	if err != nil || a.afterCommit == nil {
		return w, err
	}
	return &mutationTestWriter{AtomicWriter: w, afterCommit: func() error { return a.afterCommit(rel) }}, nil
}

type mutationTestWriter struct {
	storageworker.AtomicWriter
	afterCommit func() error
}

func (w *mutationTestWriter) Commit() error {
	if err := w.AtomicWriter.Commit(); err != nil {
		return err
	}
	return w.afterCommit()
}
