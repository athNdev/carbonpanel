// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/google/uuid"
)

const undoRoot = ".carbon-panel/undo"

type undoEntry struct {
	Target  string `json:"target"`
	Backup  string `json:"backup,omitempty"`
	Existed bool   `json:"existed"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256,omitempty"`
}

func copyStream(ctx context.Context, acc nodestorage.Accessor, src, dst string, want *undoEntry, limit int64) (int64, string, error) {
	r, err := acc.OpenRead(ctx, src)
	if err != nil {
		return 0, "", err
	}
	defer r.Close()
	w, err := acc.BeginWrite(ctx, dst, max(1, limit))
	if err != nil {
		return 0, "", err
	}
	defer w.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(io.MultiWriter(w, h), io.LimitReader(r, limit+1), make([]byte, storageworker.ChunkBytes))
	err = errors.Join(err, r.Close())
	if err != nil {
		return n, "", err
	}
	if n > limit {
		return n, "", fmt.Errorf("staged transfer exceeds %d bytes", limit)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if want != nil && (n != want.Size || sum != want.SHA256) {
		return n, sum, fmt.Errorf("staged recovery checksum mismatch for %s", want.Target)
	}
	if err = w.Commit(); err != nil {
		return n, sum, err
	}
	return n, sum, nil
}

func commitStreamingFiles(ctx context.Context, acc nodestorage.Accessor, rows []storage.StagedContentChange) ([]fileSnap, error) {
	type pending struct{ source, target, action string }
	var files []pending
	for _, row := range rows {
		if row.Kind == storage.StagedKindModule {
			continue
		}
		if row.Kind == storage.StagedKindModpack {
			entries, err := modpackFiles(acc, row.StagePath)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				files = append(files, pending{source: e.stage, target: e.live, action: storage.StagedActionReplace})
			}
		} else {
			files = append(files, pending{source: row.StagePath, target: row.TargetPath, action: row.Action})
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	if len(files) > 500000 {
		return nil, errors.New("staged batch has too many files")
	}
	batchID := rows[0].BatchID
	if _, err := uuid.Parse(batchID); err != nil {
		return nil, fmt.Errorf("invalid staged batch recovery identity: %w", err)
	}
	recovery := path.Join(undoRoot, batchID)
	if _, err := acc.Stat(recovery); err == nil {
		return nil, fmt.Errorf("staged recovery %q exists; recover before retrying", recovery)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := acc.MkdirAllMode(recovery, 0700); err != nil {
		return nil, err
	}
	abortPreparation := func(err error) ([]fileSnap, error) {
		return nil, fmt.Errorf("staged undo preparation failed at %q: %w", recovery, errors.Join(err, acc.RemoveAll(recovery)))
	}
	snaps := make([]fileSnap, 0, len(files))
	manifest := make([]undoEntry, 0, len(files))
	var total int64
	for i, file := range files {
		info, err := acc.Stat(file.target)
		if err != nil && !os.IsNotExist(err) {
			return abortPreparation(err)
		}
		item := fileSnap{target: file.target, recoveryRoot: recovery}
		record := undoEntry{Target: file.target}
		if err == nil {
			if !info.Mode().IsRegular() {
				return abortPreparation(fmt.Errorf("staged target %s is not a regular file", file.target))
			}
			item.existed = true
			item.backup = path.Join(recovery, fmt.Sprintf("%06d.data", i))
			record.Existed = true
			record.Backup = item.backup
			n, sum, err := copyStream(ctx, acc, file.target, item.backup, nil, storageworker.MaxStreamBytes-total)
			if err != nil {
				return abortPreparation(err)
			}
			total += n
			if total > storageworker.MaxStreamBytes {
				return abortPreparation(errors.New("staged undo exceeds 64 GiB limit"))
			}
			record.Size = n
			record.SHA256 = sum
			item.size = n
			item.sha256 = sum
		}
		snaps = append(snaps, item)
		manifest = append(manifest, record)
	}
	body, err := json.Marshal(struct {
		Version int         `json:"version"`
		BatchID string      `json:"batch_id"`
		Entries []undoEntry `json:"entries"`
	}{1, batchID, manifest})
	if err != nil {
		return abortPreparation(err)
	}
	if err := acc.WriteFile(path.Join(recovery, "manifest.json"), body); err != nil {
		return abortPreparation(err)
	}
	// All old contents and the recovery manifest exist before the first mutation.
	var published int64
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return snaps, err
		}
		if file.action == storage.StagedActionDelete {
			if err := acc.Remove(file.target); err != nil && !os.IsNotExist(err) {
				return snaps, err
			}
		} else {
			n, _, err := copyStream(ctx, acc, file.source, file.target, nil, storageworker.MaxStreamBytes-published)
			if err != nil {
				return snaps, err
			}
			published += n
		}
	}
	return snaps, nil
}

func restoreFiles(ctx context.Context, acc nodestorage.Accessor, snaps []fileSnap) error {
	var failed error
	for i := len(snaps) - 1; i >= 0; i-- {
		item := snaps[i]
		if item.existed {
			_, _, err := copyStream(ctx, acc, item.backup, item.target, &undoEntry{Target: item.target, Size: item.size, SHA256: item.sha256}, item.size)
			if err != nil {
				failed = errors.Join(failed, fmt.Errorf("staged rollback incomplete; recovery %q: %w", item.recoveryRoot, err))
			}
		} else if err := acc.Remove(item.target); err != nil && !os.IsNotExist(err) {
			failed = errors.Join(failed, err)
		}
	}
	if failed != nil {
		return failed
	}
	seen := map[string]bool{}
	for _, item := range snaps {
		if !seen[item.recoveryRoot] {
			seen[item.recoveryRoot] = true
			failed = errors.Join(failed, acc.RemoveAll(item.recoveryRoot))
		}
	}
	return failed
}

func cleanupBatchRecovery(ctx context.Context, store *storage.Store, server *storage.Server, batchID string) error {
	if _, err := uuid.Parse(batchID); err != nil {
		return err
	}
	acc, err := openAccessor(ctx, store, server)
	if err != nil {
		return err
	}
	defer acc.Close()
	return acc.RemoveAll(path.Join(undoRoot, batchID))
}
