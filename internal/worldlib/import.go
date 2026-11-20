// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers"
	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/athNdev/carbon-panel/pkg/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Supported import sources. Anything else is refused with ErrUnsupported.
const (
	SourceUpload = "upload"
	SourceURL    = "url"
)

// Import lifecycle phases, persisted on db.WorldImportJob.
const (
	jobPending    = "pending"
	jobDownloaded = "downloading"
	jobVerifying  = "verifying"
	jobComplete   = "complete"
	jobFailed     = "failed"
)

// maxImportArchiveBytes caps a downloaded archive at 512 MiB. Content-Length
// is never trusted; the body is streamed through LimitReader(cap+1).
const maxImportArchiveBytes int64 = 512 << 20

const importHTTPTimeout = 5 * time.Minute

// ImportRequest describes one operator-driven import. Workstation paths are
// never persisted: only the canonical source descriptor and provenance are.
type ImportRequest struct {
	Source             string
	URL                string
	UploadID           string
	DisplayName        string
	SourcePlatform     string
	LicenseAttestation string
	SHA256Checksum     string
	RequestID          string
	Operator           string
	NodeID             string
}

// ImportResult carries the durable rows produced by one successful import.
type ImportResult struct {
	Entry    *db.WorldEntry
	Revision *db.WorldRevision
	Job      *db.WorldImportJob
}

// Importer is the seam the WorldService RPC will depend on.
type Importer interface {
	Import(ctx context.Context, req ImportRequest) (*ImportResult, error)
}

// LocalImporter verifies and stores an archive in a node-local library root.
// It is independently constructible: a real *db.Store and a root path are all
// it needs, so tests exercise real SQLite and real filesystem seams.
type LocalImporter struct {
	store       *db.Store
	libraryRoot string
	// client is optional. When nil, an SSRF-guarded, credential-protecting
	// client is built per download. Tests in this package may inject one.
	client *http.Client
}

// NewLocalImporter constructs an importer over a store and library root.
func NewLocalImporter(store *db.Store, libraryRoot string) *LocalImporter {
	return &LocalImporter{store: store, libraryRoot: libraryRoot}
}

// ImportLocal imports an already-present local archive path. It is a thin
// convenience over Import with Source=upload.
func (i *LocalImporter) ImportLocal(ctx context.Context, req ImportRequest, archivePath string) (*ImportResult, error) {
	req.Source = SourceUpload
	req.URL = archivePath
	return i.Import(ctx, req)
}

// Import verifies the source, stores an immutable revision and journals the
// one-shot attempt. A replayed RequestID returns the existing result without
// creating a second revision.
func (i *LocalImporter) Import(ctx context.Context, req ImportRequest) (*ImportResult, error) {
	// Fail before any mutation when the caller is already gone.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if i == nil || i.store == nil {
		return nil, errors.New("worldlib: importer store is required")
	}
	if strings.TrimSpace(i.libraryRoot) == "" {
		return nil, errors.New("worldlib: library root is required")
	}

	req = normalizeRequest(req)
	if err := validateRequest(req); err != nil {
		return nil, err
	}
	if req.RequestID != "" {
		if result, replayed, err := i.replay(ctx, req.RequestID); err != nil || replayed {
			return result, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	job := &db.WorldImportJob{
		ID:                 "imp_" + uuid.NewString(),
		RequestID:          req.RequestID,
		SourcePlatform:     req.SourcePlatform,
		SourceURL:          sourceDescriptor(req),
		Operator:           req.Operator,
		LicenseAttestation: req.LicenseAttestation,
		Phase:              jobPending,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := i.store.CreateWorldImportJob(ctx, job); err != nil {
		return nil, fmt.Errorf("create import job: %w", err)
	}
	result := &ImportResult{Job: job}
	fail := func(err error) (*ImportResult, error) {
		i.updateJob(job, jobFailed, err.Error())
		return result, err
	}

	archivePath, cleanup, err := i.acquire(ctx, req, job)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		return fail(err)
	}

	i.updateJob(job, jobVerifying, "")
	checksum, err := files.SHA256File(archivePath)
	if err != nil {
		return fail(fmt.Errorf("hash archive: %w", err))
	}
	expected, err := normalizeChecksum(req.SHA256Checksum)
	if err != nil {
		return fail(err)
	}
	if !strings.EqualFold(checksum, expected) {
		return fail(fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, expected, checksum))
	}

	// One bounded, context-aware pass covers the archive hardening limits and
	// the world layout gate. Refuse layouts the binder cannot map instead of
	// storing an immutable revision that can never be bound.
	report, layout, err := InspectRevisionArchive(ctx, archivePath)
	if err != nil {
		return fail(err)
	}
	if err := layout.Validate(); err != nil {
		return fail(err)
	}
	archiveSize, err := ArchiveFileSize(archivePath)
	if err != nil {
		return fail(fmt.Errorf("stat archive: %w", err))
	}

	// Content dedup avoids orphaning a revision directory when the same bytes
	// are submitted under a different RequestID.
	var existing db.WorldRevision
	err = i.store.DB().WithContext(ctx).Where("sha256 = ?", checksum).First(&existing).Error
	if err == nil {
		entry, entryErr := i.store.GetWorldEntry(ctx, existing.WorldEntryID)
		if entryErr != nil {
			return fail(entryErr)
		}
		job.WorldEntryID = existing.WorldEntryID
		job.ResultRevisionID = existing.ID
		i.updateJob(job, jobComplete, "")
		result.Entry, result.Revision = entry, &existing
		return result, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fail(err)
	}

	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	revisionID := RevisionID(checksum)
	storedName := "world.zip"
	revisionDir := filepath.Join(i.libraryRoot, revisionID)
	storagePath, err := storeRevisionArchive(archivePath, revisionDir, storedName)
	if err != nil {
		return fail(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(revisionDir)
		}
	}()

	provenance, err := db.EncodeWorldProvenance(db.WorldProvenance{
		Platform:     req.SourcePlatform,
		URL:          canonicalSourceURL(req),
		Creator:      req.Operator,
		License:      req.LicenseAttestation,
		SHA256:       checksum,
		DownloadedAt: now.Format(time.RFC3339),
		Operator:     req.Operator,
	})
	if err != nil {
		return fail(fmt.Errorf("encode provenance: %w", err))
	}

	entry := &db.WorldEntry{
		ID:                NewEntryID(uuid.NewString()),
		Name:              req.DisplayName,
		Family:            "imported",
		CurrentRevisionID: revisionID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}
	revision := &db.WorldRevision{
		ID:                 revisionID,
		WorldEntryID:       entry.ID,
		SHA256:             checksum,
		SizeBytes:          archiveSize,
		EntryCount:         int32(report.EntryCount),
		Provenance:         provenance,
		LicenseAttestation: req.LicenseAttestation,
		CreatedBy:          req.Operator,
		NodeID:             req.NodeID,
		StoragePath:        storagePath,
		CreatedAt:          now,
	}

	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if err := i.store.CreateWorldEntry(ctx, entry); err != nil {
		return fail(fmt.Errorf("create world entry: %w", err))
	}
	if err := i.store.PutWorldRevision(ctx, revision); err != nil {
		// Roll back the companion entry row with a bounded detached context;
		// the request may already be cancelled at this point.
		detached, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = i.store.DB().WithContext(detached).Where("id = ?", entry.ID).Delete(&db.WorldEntry{}).Error
		cancel()
		return fail(fmt.Errorf("put world revision: %w", err))
	}
	committed = true

	job.WorldEntryID = entry.ID
	job.ResultRevisionID = revision.ID
	i.updateJob(job, jobComplete, "")
	result.Entry, result.Revision = entry, revision
	return result, nil
}

// acquire returns a readable archive path and an optional cleanup function.
// For uploads the caller-owned path is used directly (nothing to clean). For
// URLs a bounded SSRF-guarded GET streams to a private temp directory.
func (i *LocalImporter) acquire(ctx context.Context, req ImportRequest, job *db.WorldImportJob) (string, func(), error) {
	if req.Source == SourceUpload {
		localPath := req.URL
		if localPath == "" {
			localPath = req.UploadID
		}
		info, err := os.Stat(localPath)
		if err != nil {
			return "", nil, fmt.Errorf("stat upload: %w", err)
		}
		if info.IsDir() {
			return "", nil, fmt.Errorf("upload path %q is a directory", localPath)
		}
		if info.Size() > maxImportArchiveBytes {
			return "", nil, fmt.Errorf("upload exceeds %d bytes", maxImportArchiveBytes)
		}
		return localPath, nil, nil
	}

	i.updateJob(job, jobDownloaded, "")
	client := i.client
	if client == nil {
		client = indexers.ProtectCredentialsOnRedirect(utils.NewSafeHTTPClient(importHTTPTimeout, false))
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, req.URL, nil)
	if err != nil {
		return "", nil, fmt.Errorf("create download request: %w", err)
	}
	httpReq.Header.Set("User-Agent", "CarbonPanel/2.x (World Library Import)")
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("download failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	tempDir, err := os.MkdirTemp("", "worldlib-import-")
	if err != nil {
		return "", nil, fmt.Errorf("create temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tempDir) }
	tmp, err := os.CreateTemp(tempDir, "archive-*.zip")
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("create temp archive: %w", err)
	}
	archivePath := tmp.Name()
	written, copyErr := io.Copy(tmp, io.LimitReader(resp.Body, maxImportArchiveBytes+1))
	closeErr := tmp.Close() // On Windows the file must be closed before remove.
	if copyErr != nil || closeErr != nil || written > maxImportArchiveBytes {
		cleanup()
		if copyErr != nil {
			return "", nil, fmt.Errorf("write temp archive: %w", copyErr)
		}
		if closeErr != nil {
			return "", nil, fmt.Errorf("close temp archive: %w", closeErr)
		}
		return "", nil, fmt.Errorf("download exceeds %d bytes", maxImportArchiveBytes)
	}
	return archivePath, cleanup, nil
}

// storeRevisionArchive copies an inspected archive into a fresh per-revision
// directory with ordinary 0644 file permissions. O_EXCL and Mkdir refuse to
// reuse an existing target; no privilege or capability changes are made.
func storeRevisionArchive(src, revisionDir, storedName string) (string, error) {
	if err := os.Mkdir(revisionDir, 0755); err != nil {
		return "", fmt.Errorf("create revision directory: %w", err)
	}
	dst := filepath.Join(revisionDir, storedName)
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		_ = os.RemoveAll(revisionDir)
		return "", fmt.Errorf("create revision archive: %w", err)
	}
	in, err := os.Open(src)
	if err != nil {
		_ = out.Close()
		_ = os.RemoveAll(revisionDir)
		return "", fmt.Errorf("open source archive: %w", err)
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeInErr := in.Close()
	closeOutErr := out.Close()
	if copyErr != nil || syncErr != nil || closeInErr != nil || closeOutErr != nil {
		_ = os.RemoveAll(revisionDir)
		if copyErr != nil {
			return "", fmt.Errorf("copy revision archive: %w", copyErr)
		}
		return "", errors.New("finalize revision archive failed")
	}
	return filepath.ToSlash(filepath.Join(filepath.Base(revisionDir), storedName)), nil
}

// replay returns the existing result for a RequestID. A completed job is
// idempotent; an in-flight or failed one is surfaced as an uncertain outcome
// so the caller never starts a second revision.
func (i *LocalImporter) replay(ctx context.Context, requestID string) (*ImportResult, bool, error) {
	var job db.WorldImportJob
	err := i.store.DB().WithContext(ctx).Where("request_id = ?", requestID).First(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("lookup import job: %w", err)
	}
	result := &ImportResult{Job: &job}
	if job.Phase == jobComplete && job.ResultRevisionID != "" {
		revision, err := i.store.GetWorldRevision(ctx, job.ResultRevisionID)
		if err != nil {
			return result, true, fmt.Errorf("load replayed revision: %w", err)
		}
		entry, err := i.store.GetWorldEntry(ctx, revision.WorldEntryID)
		if err != nil {
			return result, true, fmt.Errorf("load replayed entry: %w", err)
		}
		result.Revision, result.Entry = revision, entry
		return result, true, nil
	}
	return result, true, fmt.Errorf("%w: request %q already exists in phase %q", ErrUncertainOutcome, requestID, job.Phase)
}

// updateJob persists a phase transition with a bounded detached context: an
// HTTP request ending mid-import must not leave a job stuck in "verifying".
func (i *LocalImporter) updateJob(job *db.WorldImportJob, phase, errText string) {
	job.Phase = phase
	if errText != "" {
		job.Error = errText
	}
	job.UpdatedAt = time.Now().UTC()
	detached, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = i.store.UpdateWorldImportJob(detached, job)
}

func normalizeRequest(req ImportRequest) ImportRequest {
	req.Source = strings.ToLower(strings.TrimSpace(req.Source))
	req.DisplayName = strings.TrimSpace(req.DisplayName)
	if req.DisplayName == "" {
		req.DisplayName = "Imported World"
	}
	req.SourcePlatform = strings.TrimSpace(req.SourcePlatform)
	if req.SourcePlatform == "" {
		req.SourcePlatform = "manual"
	}
	return req
}

func validateRequest(req ImportRequest) error {
	switch req.Source {
	case SourceUpload, SourceURL:
	default:
		return fmt.Errorf("%w: import source %q", ErrUnsupported, req.Source)
	}
	if strings.TrimSpace(req.LicenseAttestation) == "" {
		return errors.New("worldlib: license attestation is required for an imported revision")
	}
	if strings.TrimSpace(req.SHA256Checksum) == "" {
		return errors.New("worldlib: expected whole-archive SHA-256 is required")
	}
	if _, err := normalizeChecksum(req.SHA256Checksum); err != nil {
		return err
	}
	if req.Source == SourceURL {
		if strings.TrimSpace(req.URL) == "" {
			return errors.New("worldlib: url is required for url source")
		}
		if _, err := utils.ValidateURLStatic(req.URL, false); err != nil {
			return fmt.Errorf("worldlib: invalid source url: %w", err)
		}
		return nil
	}
	if strings.TrimSpace(req.URL) == "" && strings.TrimSpace(req.UploadID) == "" {
		return errors.New("worldlib: local archive path is required for upload source")
	}
	return nil
}

func normalizeChecksum(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "sha256:")))
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256Size {
		return "", fmt.Errorf("worldlib: invalid SHA-256 checksum %q", raw)
	}
	return value, nil
}

const sha256Size = 32

// canonicalSourceURL strips credentials, query strings and fragments so a
// persisted source descriptor cannot leak tokens or workstation paths.
func canonicalSourceURL(req ImportRequest) string {
	if req.Source != SourceURL {
		base := filepath.Base(req.URL)
		if base == "" || base == "." {
			base = req.UploadID
		}
		return "upload://" + files.SanitizePathName(base)
	}
	parsed, err := url.Parse(req.URL)
	if err != nil {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String()
}

// sourceDescriptor is the value stored on the import job row.
func sourceDescriptor(req ImportRequest) string {
	if req.Source == SourceURL {
		return canonicalSourceURL(req)
	}
	return canonicalSourceURL(req)
}
