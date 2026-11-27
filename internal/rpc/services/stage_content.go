// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/google/uuid"
)

// stagedContentRoot is outside mods, datapacks, and the world. Apply (a later
// change) is what copies these bytes onto the live tree.
const stagedContentRoot = ".carbon-panel/staged"

var errStagedPath = errors.New("invalid staged path")

// errStagedModValidation marks a definitive refusal of the reviewed mod bytes
// or identity. It is not a storage failure and must not be retried.
var errStagedModValidation = errors.New("invalid staged mod")

func safeStageTarget(rel string) (string, error) {
	rel = strings.TrimSpace(strings.ReplaceAll(rel, "\\", "/"))
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, ":") {
		return "", fmt.Errorf("%w: %q", errStagedPath, rel)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%w: %q", errStagedPath, rel)
	}
	for _, reserved := range []string{stagedContentRoot, ".carbon-panel/undo"} {
		folded := strings.ToLower(clean)
		if folded == reserved || strings.HasPrefix(folded, reserved+"/") {
			return "", fmt.Errorf("%w: %q", errStagedPath, rel)
		}
	}
	return clean, nil
}

func stagedFileBase(name string) (string, error) {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	if base == "" || base == "." || base == ".." {
		return "", fmt.Errorf("%w: file name %q", errStagedPath, name)
	}
	return base, nil
}

func serverAccessor(ctx context.Context, store *storage.Store, server *storage.Server, resolver *nodestorage.Resolver) (nodestorage.Accessor, error) {
	if _, err := dataRootFor(ctx, store, server, true, resolver); err != nil {
		return nil, err
	}
	info, err := storageNodeInfo(ctx, store, server.NodeID)
	if err != nil {
		return nil, err
	}
	return resolver.ForNode(ctx, info, server.DataPath)
}

// stageContentBytes records an install, replace, or delete on the open batch.
// Install and replace bytes are written through nodestorage under
// .carbon-panel/staged. A delete records the live path and writes nothing.
// The server's mods directory, datapacks directory, and world are not modified.
func stageContentBytes(ctx context.Context, store *storage.Store, server *storage.Server, resolver *nodestorage.Resolver, kind, action, targetRel, fileName string, data []byte) (*storage.StagedContentChange, error) {
	return stageContentBytesWithPrecondition(ctx, store, server, resolver, kind, action, targetRel, fileName, data, "")
}

// stageContentBytesWithPrecondition records the live digest observed at stage
// time. The journaled publish rechecks it before copying replacement bytes.
func stageContentBytesWithPrecondition(ctx context.Context, store *storage.Store, server *storage.Server, resolver *nodestorage.Resolver, kind, action, targetRel, fileName string, data []byte, expectedLiveSha512 string) (*storage.StagedContentChange, error) {
	releaseWrite, err := beginDataWrite(server.ID)
	if err != nil {
		return nil, err
	}
	defer releaseWrite()
	return stageContentBytesHeld(ctx, store, server, resolver, kind, action, targetRel, fileName, data, expectedLiveSha512)
}

// stageContentBytesHeld records one staged row while the caller already owns
// the shared data-write lease. Bulk operations use it so publication cannot
// take exclusive ownership between items and split one request.
func stageContentBytesHeld(ctx context.Context, store *storage.Store, server *storage.Server, resolver *nodestorage.Resolver, kind, action, targetRel, fileName string, data []byte, expectedLiveSha512 string) (*storage.StagedContentChange, error) {
	target, err := safeStageTarget(targetRel)
	if err != nil {
		return nil, err
	}
	base, err := stagedFileBase(fileName)
	if err != nil {
		return nil, err
	}

	acc, err := serverAccessor(ctx, store, server, resolver)
	if err != nil {
		return nil, err
	}
	defer func() { _ = acc.Close() }()

	id := uuid.New().String()
	stagePath := ""
	if action != storage.StagedActionDelete {
		stagePath = stagedContentRoot + "/" + id + "/" + base
		if _, err := nodestorage.WriteFrom(ctx, acc, stagePath, bytes.NewReader(data), 64<<30); err != nil {
			return nil, err
		}
	}
	row, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ID:                 id,
		ServerID:           server.ID,
		Kind:               kind,
		Action:             action,
		TargetPath:         target,
		StagePath:          stagePath,
		FileName:           base,
		ExpectedLiveSha512: expectedLiveSha512,
	})
	if err != nil {
		if stagePath != "" {
			_ = acc.RemoveAll(stagedContentRoot + "/" + id)
		}
		return nil, err
	}
	return row, nil
}

// approvedModRelease is the provider release the operator reviewed. Admission
// records it only after the staged bytes match SHA512, so publication can rely
// on the durable row instead of a mutable sidecar.
type approvedModRelease struct {
	SHA512    string
	Provider  string
	ProjectID string
	VersionID string
	Filename  string
}

// stageModContentToBatch writes candidate bytes and inserts the staged row
// only while the reviewed batch is still open with the reviewed window. This
// closes the window between choosing the batch and writing its payload.
func stageModContentToBatch(ctx context.Context, store *storage.Store, server *storage.Server, resolver *nodestorage.Resolver,
	batchID string, window storage.UpdateWindow, applyAt *time.Time, idleMinutes int,
	targetRel, fileName string, data []byte, expectedLiveSha512 string, approved approvedModRelease,
	reviewedLoader storage.ModLoader, reviewedVersion string) (*storage.StagedContentChange, error) {
	target, err := safeStageTarget(targetRel)
	if err != nil {
		return nil, err
	}
	base, err := stagedFileBase(fileName)
	if err != nil {
		return nil, err
	}
	// The row records the release identity and digest the operator approved
	// only after the exact bytes written below have been verified against it.
	// This is the durable approval publication later requires; it is not
	// re-derived from a mutable sidecar.
	if approved.SHA512 != "" {
		sum := sha512.Sum512(data)
		if !strings.EqualFold(hex.EncodeToString(sum[:]), approved.SHA512) {
			return nil, fmt.Errorf("%w: candidate bytes do not match the approved SHA-512", errStagedModValidation)
		}
		approved.SHA512 = strings.ToLower(approved.SHA512)
		approved.Provider = strings.ToLower(strings.TrimSpace(approved.Provider))
		approved.ProjectID = strings.TrimSpace(approved.ProjectID)
		approved.VersionID = strings.TrimSpace(approved.VersionID)
		approved.Filename = strings.TrimSpace(approved.Filename)
		if approved.Filename != "" && (strings.ContainsAny(approved.Filename, "/\\") || !strings.HasSuffix(strings.ToLower(approved.Filename), ".jar")) {
			return nil, fmt.Errorf("%w: approved candidate filename is not a bare JAR name", errStagedModValidation)
		}
	}
	acc, err := serverAccessor(ctx, store, server, resolver)
	if err != nil {
		return nil, err
	}
	defer func() { _ = acc.Close() }()
	id := uuid.New().String()
	stagePath := stagedContentRoot + "/" + id + "/" + base
	if _, err := nodestorage.WriteFrom(ctx, acc, stagePath, bytes.NewReader(data), 64<<30); err != nil {
		return nil, err
	}
	row := &storage.StagedContentChange{
		ID:                     id,
		ServerID:               server.ID,
		Kind:                   storage.StagedKindMod,
		Action:                 storage.StagedActionReplace,
		TargetPath:             target,
		StagePath:              stagePath,
		FileName:               base,
		ExpectedLiveSha512:     expectedLiveSha512,
		ReviewedRuntimeLoader:  string(reviewedLoader),
		ReviewedRuntimeVersion: reviewedVersion,
		ApprovedSha512:         approved.SHA512,
		ApprovedProvider:       approved.Provider,
		ApprovedProjectID:      approved.ProjectID,
		ApprovedVersionID:      approved.VersionID,
		ApprovedFilename:       approved.Filename,
	}
	if err := store.AddStagedContentToBatch(ctx, row, batchID, window, applyAt, idleMinutes); err != nil {
		_ = acc.RemoveAll(stagedContentRoot + "/" + id)
		return nil, err
	}
	return row, nil
}

// discardStagedContent deletes staged bytes, then marks the row discarded.
// The live tree is left as it is. A delete item has no bytes to remove.
func discardStagedContent(ctx context.Context, store *storage.Store, server *storage.Server, resolver *nodestorage.Resolver, id string) error {
	row, err := store.GetStagedContent(ctx, id)
	if err != nil {
		return err
	}
	if row.ServerID != server.ID {
		return fmt.Errorf("staged content %s is not on this server", id)
	}
	if row.StagePath != "" {
		acc, err := serverAccessor(ctx, store, server, resolver)
		if err != nil {
			return err
		}
		defer func() { _ = acc.Close() }()
		if err := acc.RemoveAll(path.Dir(row.StagePath)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	_, err = store.DiscardStagedContent(ctx, id)
	return err
}

func stageContentError(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeAborted, connect.CodeFailedPrecondition:
		// Admission refusals already carry an operator-facing retry reason
		// (publication in progress, archive export in progress). Keep it.
		return err
	}
	switch {
	case nodestorage.IsUnavailable(err):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, storage.ErrUpdateBatchConflict), errors.Is(err, storage.ErrDuplicateStagedTarget):
		// Batch state is an admission precondition, matching reviewed-mod and
		// config staging; keep the operator-facing conflict message intact.
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case nodestorage.IsEscape(err), errors.Is(err, errStagedPath):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}
