// SPDX-License-Identifier: MPL-2.0
package staging

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

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

type streamCopy struct {
	size            int64
	sha256          string
	sha512          string
	commitAttempted bool
}

// copyStream copies one bounded file. A non-empty reviewedSHA512 must match
// the bytes read from src on this stream before the writer commits, so a
// payload that changed after review is never adopted by the live tree.
func copyStream(ctx context.Context, acc nodestorage.Accessor, src, dst string, want *undoEntry, reviewedSHA512 string, stageStream bool, limit int64) (streamCopy, error) {
	var result streamCopy
	r, err := acc.OpenRead(ctx, src)
	if err != nil {
		return result, err
	}
	defer func() { _ = r.Close() }()
	w, err := acc.BeginWrite(ctx, dst, max(1, limit))
	if err != nil {
		return result, err
	}
	defer func() { _ = w.Close() }()
	h := sha256.New()
	h512 := sha512.New()
	result.size, err = io.CopyBuffer(io.MultiWriter(w, h, h512), io.LimitReader(r, limit+1), make([]byte, storageworker.ChunkBytes))
	err = errors.Join(err, r.Close())
	if err != nil {
		return result, err
	}
	if result.size > limit {
		return result, fmt.Errorf("staged transfer exceeds %d bytes", limit)
	}
	result.sha256 = hex.EncodeToString(h.Sum(nil))
	result.sha512 = hex.EncodeToString(h512.Sum(nil))
	if want != nil && (result.size != want.Size || result.sha256 != want.SHA256) {
		return result, fmt.Errorf("staged recovery checksum mismatch for %s", want.Target)
	}
	if reviewedSHA512 != "" && !strings.EqualFold(result.sha512, reviewedSHA512) {
		if stageStream {
			return result, fmt.Errorf("staged bytes no longer match the durable approved SHA-512; re-review and re-stage the update")
		}
		return result, fmt.Errorf("live target %q changed since review; staged bytes preserved", src)
	}
	// A remote commit may succeed even if its acknowledgement is lost.
	result.commitAttempted = true
	return result, w.Commit()
}

// fileSHA512 reads one file without loading it into memory and verifies that
// it did not change while the digest was being calculated.
func fileSHA512(ctx context.Context, acc nodestorage.Accessor, target string, limit int64) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	before, err := acc.Stat(target)
	if err != nil {
		return "", err
	}
	if !before.Mode().IsRegular() {
		return "", fmt.Errorf("file %s is not a regular file", target)
	}
	r, err := acc.OpenRead(ctx, target)
	if err != nil {
		return "", err
	}
	h := sha512.New()
	n, copyErr := io.CopyBuffer(h, io.LimitReader(r, limit+1), make([]byte, storageworker.ChunkBytes))
	closeErr := r.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", err
	}
	if n > limit {
		return "", fmt.Errorf("file %s exceeds the %d-byte digest limit", target, limit)
	}
	after, err := acc.Stat(target)
	if err != nil {
		return "", err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("file %s changed while its digest was checked", target)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// validSHA512Hex reports whether value is a complete SHA-512 in hex.
func validSHA512Hex(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == sha512.Size
}

// errStagedLiveDrift marks a reviewed target whose live bytes no longer match
// the digest observed when the batch was reviewed. The guard is checked again
// next to the live writer commit, not only while taking the undo snapshot.
var errStagedLiveDrift = errors.New("reviewed live digest changed after staging")

// liveDriftGuard binds a staged target to the reviewed live digest so the
// target and its precondition cannot be compared against the wrong row. The
// zero value means the row has no reviewed precondition (an ordinary upload).
type liveDriftGuard struct {
	target string
	sha512 string
}

func (g liveDriftGuard) empty() bool {
	return g.sha512 == ""
}

// matchesSnapshot verifies the digest already read from the live target while
// the undo snapshot was taken. It does not replace the pre-commit recheck.
func (g liveDriftGuard) matchesSnapshot(sum512 string) error {
	if g.empty() || strings.EqualFold(sum512, g.sha512) {
		return nil
	}
	return fmt.Errorf("%w: staged target %s changed after staging: expected SHA-512 %s but found %s", errStagedLiveDrift, g.target, g.sha512, sum512)
}

// verifyLive re-reads the live target immediately before its writer commits.
// The staged publisher holds the exclusive data-write gate, and ordinary panel
// writes take the shared side, so a panel write cannot change the target in
// this window. A process outside the panel is still not an atomic
// compare-and-swap and can race the final writer.
func (g liveDriftGuard) verifyLive(ctx context.Context, acc nodestorage.Accessor) error {
	if g.empty() {
		return nil
	}
	got, err := fileSHA512(ctx, acc, g.target, storageworker.MaxStreamBytes)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: staged target %s changed after staging: expected SHA-512 %s but the live file is missing", errStagedLiveDrift, g.target, g.sha512)
		}
		return fmt.Errorf("recheck staged target %s before commit: %w", g.target, err)
	}
	if !strings.EqualFold(got, g.sha512) {
		return fmt.Errorf("%w: staged target %s changed after staging: expected SHA-512 %s but found %s", errStagedLiveDrift, g.target, g.sha512, got)
	}
	return nil
}

func commitStreamingFiles(ctx context.Context, acc nodestorage.Accessor, rows []storage.StagedContentChange, recoverUnapplied bool) ([]fileSnap, error) {
	type pending struct {
		source, target, action string
		guard                  liveDriftGuard
		expectedStageSha512    string
	}
	var files []pending
	for _, row := range rows {
		if row.Kind == storage.StagedKindModule {
			continue
		}
		target := strings.ToLower(path.Clean(strings.ReplaceAll(row.TargetPath, "\\", "/")))
		if target == undoRoot || strings.HasPrefix(target, undoRoot+"/") {
			return nil, fmt.Errorf("staged target %q is reserved for recovery", row.TargetPath)
		}
		if row.Kind == storage.StagedKindModpack {
			entries, err := modpackFiles(acc, row.StagePath)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				files = append(files, pending{source: e.stage, target: e.live, action: storage.StagedActionReplace})
			}
			continue
		}
		item := pending{
			source: row.StagePath, target: row.TargetPath, action: row.Action,
			guard: liveDriftGuard{target: row.TargetPath, sha512: row.ExpectedLiveSha512},
		}
		// A provider-reviewed mod publication precondition was verified against
		// the provider release and the staged bytes at admission. Require the
		// durable approved identity and digest; a row without them predates
		// durable approval and must be re-reviewed rather than deriving
		// approval from a mutable sidecar.
		if row.Kind == storage.StagedKindMod && row.ExpectedLiveSha512 != "" {
			if row.ApprovedSha512 == "" {
				return nil, fmt.Errorf("staged mod candidate %s was recorded before durable approved digests; re-review and re-stage the update: %w", row.FileName, storage.ErrStagedModApprovalMissing)
			}
			if !validSHA512Hex(row.ApprovedSha512) {
				return nil, fmt.Errorf("staged mod candidate %s has an invalid durable approved SHA-512; re-review and re-stage the update: %w", row.FileName, storage.ErrStagedModApprovalInvalid)
			}
			if row.ApprovedProvider == "" || row.ApprovedProjectID == "" || row.ApprovedVersionID == "" || row.ApprovedFilename == "" {
				return nil, fmt.Errorf("staged mod candidate %s has no durable approved release identity; re-review and re-stage the update: %w", row.FileName, storage.ErrStagedModApprovalIdentityMissing)
			}
			item.expectedStageSha512 = row.ApprovedSha512
		}
		files = append(files, item)
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
		if !recoverUnapplied {
			// A retained attempt owns this recovery tree. Its manifest belongs
			// to rows that may already be applied, so a later attempt must
			// neither overwrite it nor delete it to make room. Refuse the new
			// rows with an explicit resolution: discard them if they are still
			// staged, then retry the lifecycle to finalize the retained
			// snapshot.
			return nil, fmt.Errorf("staged recovery %q exists from an earlier attempt; discard the newly staged rows and retry the lifecycle to resolve the retained publication", recovery)
		}
		// No APPLIED row exists for this batch, and the next-restart ordering
		// mutates the runtime only after APPLIED. The tree therefore belongs to
		// an interrupted copy that never reached the runtime: restore the old
		// contents from its manifest and reuse the tree for this attempt.
		if err := restoreInterruptedRecovery(ctx, acc, recovery); err != nil {
			return nil, err
		}
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
			snapshot, err := copyStream(ctx, acc, file.target, item.backup, nil, "", false, storageworker.MaxStreamBytes-total)
			if err != nil {
				return abortPreparation(err)
			}
			if err := file.guard.matchesSnapshot(snapshot.sha512); err != nil {
				return abortPreparation(err)
			}
			total += snapshot.size
			if total > storageworker.MaxStreamBytes {
				return abortPreparation(errors.New("staged undo exceeds 64 GiB limit"))
			}
			record.Size = snapshot.size
			record.SHA256 = snapshot.sha256
			item.size = snapshot.size
			item.sha256 = snapshot.sha256
		} else if !file.guard.empty() {
			return abortPreparation(fmt.Errorf("%w: staged target %s changed after staging: expected SHA-512 %s but the live file is missing", errStagedLiveDrift, file.guard.target, file.guard.sha512))
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
	// All old contents, preconditions, and the recovery manifest exist before the first mutation.
	var published int64
	for i, file := range files {
		if err := ctx.Err(); err != nil {
			return snaps, err
		}
		// Recheck at the mutation point, not only while taking the snapshot.
		// The exclusive data-write gate covers normal panel writes; see
		// liveDriftGuard.verifyLive for the remaining out-of-band boundary.
		if err := file.guard.verifyLive(ctx, acc); err != nil {
			return snaps, err
		}
		if file.action == storage.StagedActionDelete {
			err := acc.Remove(file.target)
			if os.IsNotExist(err) {
				continue
			}
			// An error other than absence can leave the removal outcome unknown.
			snaps[i].mutated = true
			if err != nil {
				return snaps, err
			}
			continue
		}
		copied, err := copyStream(ctx, acc, file.source, file.target, nil, file.expectedStageSha512, true, storageworker.MaxStreamBytes-published)
		snaps[i].mutated = copied.commitAttempted
		if err != nil {
			return snaps, err
		}
		published += copied.size
	}
	return snaps, nil
}

func restoreFiles(ctx context.Context, acc nodestorage.Accessor, snaps []fileSnap) error {
	var failed error
	for i := len(snaps) - 1; i >= 0; i-- {
		item := snaps[i]
		if !item.mutated {
			continue
		}
		if item.existed {
			_, err := copyStream(ctx, acc, item.backup, item.target, &undoEntry{Target: item.target, Size: item.size, SHA256: item.sha256}, "", false, item.size)
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

// restoreInterruptedRecovery restores the old contents recorded by a recovery
// tree whose attempt never recorded an APPLIED row, then removes the tree. The
// manifest is written before the first live mutation, so every entry it names
// can be restored. A missing or unreadable manifest fails closed: the tree is
// never overwritten or deleted without that evidence.
func restoreInterruptedRecovery(ctx context.Context, acc nodestorage.Accessor, recovery string) error {
	body, err := acc.ReadFile(path.Join(recovery, "manifest.json"))
	if err != nil {
		return fmt.Errorf("interrupted staged recovery %q has no readable manifest: %w", recovery, err)
	}
	var manifest struct {
		Version int         `json:"version"`
		BatchID string      `json:"batch_id"`
		Entries []undoEntry `json:"entries"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("interrupted staged recovery %q has a corrupt manifest: %w", recovery, err)
	}
	if manifest.Version != 1 {
		return fmt.Errorf("interrupted staged recovery %q has unsupported manifest version %d", recovery, manifest.Version)
	}
	for i := len(manifest.Entries) - 1; i >= 0; i-- {
		entry := manifest.Entries[i]
		if !entry.Existed {
			if err := acc.Remove(entry.Target); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("interrupted staged recovery %q could not remove %q: %w", recovery, entry.Target, err)
			}
			continue
		}
		want := &undoEntry{Target: entry.Target, Size: entry.Size, SHA256: entry.SHA256}
		if _, err := copyStream(ctx, acc, entry.Backup, entry.Target, want, "", false, entry.Size); err != nil {
			return fmt.Errorf("interrupted staged recovery %q could not restore %q: %w", recovery, entry.Target, err)
		}
	}
	return acc.RemoveAll(recovery)
}

func cleanupBatchRecovery(ctx context.Context, store *storage.Store, server *storage.Server, batchID string, resolver *nodestorage.Resolver) error {
	if _, err := uuid.Parse(batchID); err != nil {
		return err
	}
	acc, err := openAccessor(ctx, store, server, resolver)
	if err != nil {
		return err
	}
	defer func() { _ = acc.Close() }()
	return acc.RemoveAll(path.Join(undoRoot, batchID))
}
