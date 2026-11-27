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
	"hash"
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
	// RowID binds each recovery entry to the staged content row that owns the
	// target. Recovery refuses an entry whose row is not in this batch.
	RowID  string `json:"row_id,omitempty"`
	Target string `json:"target"`
	// Source is the controller-owned staged file this attempt copies to Target
	// (empty for a delete). Recovery uses it, together with Action, to prove the
	// current target is this attempt's completed write before restoring over it.
	// Snapshots written before this field existed leave it empty and are
	// verified conservatively.
	Source  string `json:"source,omitempty"`
	Action  string `json:"action,omitempty"`
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

// fileDigest reads one file without loading it into memory and verifies that it
// did not change while the digest was being calculated.
func fileDigest(ctx context.Context, acc nodestorage.Accessor, target string, limit int64, digest hash.Hash) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	before, err := acc.Stat(target)
	if err != nil {
		return "", 0, err
	}
	if !before.Mode().IsRegular() {
		return "", 0, fmt.Errorf("file %s is not a regular file", target)
	}
	r, err := acc.OpenRead(ctx, target)
	if err != nil {
		return "", 0, err
	}
	n, copyErr := io.CopyBuffer(digest, io.LimitReader(r, limit+1), make([]byte, storageworker.ChunkBytes))
	closeErr := r.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		return "", 0, err
	}
	if n > limit {
		return "", 0, fmt.Errorf("file %s exceeds the %d-byte digest limit", target, limit)
	}
	after, err := acc.Stat(target)
	if err != nil {
		return "", 0, err
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || n != before.Size() {
		return "", 0, fmt.Errorf("file %s changed while its digest was checked", target)
	}
	return hex.EncodeToString(digest.Sum(nil)), n, nil
}

func fileSHA512(ctx context.Context, acc nodestorage.Accessor, target string, limit int64) (string, error) {
	sum, _, err := fileDigest(ctx, acc, target, limit, sha512.New())
	return sum, err
}

// fileSHA256 returns the size and SHA-256 of one regular file. Recovery compares
// a live target against both its recorded pre-image and this attempt's staged
// post-image with it.
func fileSHA256(ctx context.Context, acc nodestorage.Accessor, target string, limit int64) (string, int64, error) {
	return fileDigest(ctx, acc, target, limit, sha256.New())
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

type pendingFile struct {
	rowID  string
	kind   string
	source string
	target string
	action string
	guard  liveDriftGuard
	// expectedStageSha512 is the provider-approved digest a reviewed mod
	// publication requires at the staged writer.
	expectedStageSha512 string
}

func buildStreamingFiles(acc nodestorage.Accessor, rows []storage.StagedContentChange) ([]pendingFile, error) {
	var files []pendingFile
	for _, row := range rows {
		if row.Kind == storage.StagedKindModule {
			continue
		}
		target := normalizeStagedTarget(row.TargetPath)
		if target == undoRoot || strings.HasPrefix(target, undoRoot+"/") {
			return nil, fmt.Errorf("staged target %q is reserved for recovery", row.TargetPath)
		}
		if row.Kind == storage.StagedKindModpack {
			entries, err := modpackFiles(acc, row.StagePath)
			if err != nil {
				return nil, err
			}
			for _, e := range entries {
				files = append(files, pendingFile{rowID: row.ID, kind: row.Kind, source: e.stage, target: e.live, action: storage.StagedActionReplace})
			}
			continue
		}
		item := pendingFile{
			rowID: row.ID, kind: row.Kind, source: row.StagePath, target: row.TargetPath, action: row.Action,
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
	return files, nil
}

func commitStreamingFiles(ctx context.Context, acc nodestorage.Accessor, rows []storage.StagedContentChange, publication *publicationAttempt) ([]fileSnap, error) {
	files, err := buildStreamingFiles(acc, rows)
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		if publication != nil && len(rows) > 0 {
			// A module-only batch has no file entries, but its module row and
			// container can still change. Establish the prepared/applying
			// phases and an empty snapshot before that mutation.
			if publication.phase() == storage.PublicationPreparing {
				if err := publication.recordSnapshot(ctx, []undoEntry{}); err != nil {
					return nil, err
				}
			}
			if publication.phase() == storage.PublicationPrepared {
				if err := publication.markApplying(ctx); err != nil {
					return nil, err
				}
			}
		}
		return nil, nil
	}
	if len(files) > 500000 {
		return nil, errors.New("staged batch has too many files")
	}
	batchID := rows[0].BatchID
	if publication != nil && publication.record != nil {
		batchID = publication.record.BatchID
	}
	if _, err := uuid.Parse(batchID); err != nil {
		return nil, fmt.Errorf("invalid staged batch recovery identity: %w", err)
	}
	recovery := path.Join(undoRoot, batchID)
	if publication != nil {
		restored, err := preparePublicationRecovery(ctx, acc, publication, batchID, recovery)
		if err != nil {
			return nil, err
		}
		if restored {
			// Recovery restaged the snapshot's applied rows. Republish from
			// those rows now that the live tree is back at the recorded state.
			rows, err = publication.store.ListBatchContent(ctx, batchID)
			if err != nil {
				return nil, err
			}
			files, err = buildStreamingFiles(acc, rows)
			if err != nil {
				return nil, err
			}
			if len(files) == 0 {
				return nil, fmt.Errorf("staged recovery for batch %s restored its snapshot but has no rows left to publish; retry the lifecycle after inspecting the batch", batchID)
			}
		}
	} else if _, err := acc.Stat(recovery); err == nil {
		// Without controller-owned metadata the recovery tree's manifest is
		// mutable game data. Never delete or restore from it to make room.
		return nil, fmt.Errorf("staged recovery %q exists from an earlier attempt; no durable publication snapshot proves its targets; discard the newly staged rows and retry the lifecycle to resolve the retained publication", recovery)
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
		record := undoEntry{RowID: file.rowID, Target: file.target, Source: file.source, Action: file.action}
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
	if publication != nil {
		if err := publication.recordSnapshot(ctx, manifest); err != nil {
			return abortPreparation(err)
		}
	} else {
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
	}
	if publication != nil {
		// Only now may a live write start. A crash before this marker leaves a
		// prepared record whose snapshot can be discarded without restoring.
		if err := publication.markApplying(ctx); err != nil {
			return abortPreparation(err)
		}
	}
	// All old contents, preconditions, and the controller-owned recovery
	// snapshot exist before the first mutation.
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

func normalizeStagedTarget(value string) string {
	return strings.TrimSuffix(strings.ToLower(path.Clean(strings.ReplaceAll(value, "\\", "/"))), "/")
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
