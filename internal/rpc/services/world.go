// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	"github.com/athNdev/carbon-panel/internal/worldlib/providers"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/athNdev/carbon-panel/pkg/upload"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var _ carbonpanelv1connect.WorldServiceHandler = (*WorldService)(nil)

// WorldService is the generated-RPC surface of the World Library. Every
// procedure is fail-closed through ProcedurePermissions; this service never
// widens an authorization decision and never falls back to controller-local
// storage for a remote node.
type WorldService struct {
	store       *storage.Store
	config      *config.Config
	log         *logger.Logger
	resolver    *nodestorage.Resolver
	uploads     *upload.Manager
	libraryRoot string

	importer  *worldlib.LocalImporter
	compat    *worldlib.Engine
	binder    *worldlib.Binder
	modrinth  providers.Searcher
	curseforge providers.Searcher
}

// NewWorldService constructs the service. Dependencies that are optional in a
// given deployment are installed with the Set* methods below.
func NewWorldService(store *storage.Store, cfg *config.Config, log *logger.Logger, resolver *nodestorage.Resolver, uploads *upload.Manager, libraryRoot string) *WorldService {
	if libraryRoot == "" {
		libraryRoot = filepath.Join(os.TempDir(), "carbon-world-library")
	}
	_ = os.MkdirAll(libraryRoot, 0o755)
	s := &WorldService{
		store: store, config: cfg, log: log, resolver: resolver,
		uploads: uploads, libraryRoot: libraryRoot,
	}
	s.importer = worldlib.NewLocalImporter(store, libraryRoot)
	s.compat = worldlib.NewEngine()
	return s
}

// SetBinder installs the journalled binding owner. It is injected from the
// composition root so lifecycle and quiesce dependencies stay explicit.
func (s *WorldService) SetBinder(b *worldlib.Binder) { s.binder = b }

// SetProviders installs the API-backed artifact searchers. Nil providers are
// reported as unavailable rather than replaced with a live HTTP call.
func (s *WorldService) SetProviders(modrinth, curseforge providers.Searcher) {
	s.modrinth = modrinth
	s.curseforge = curseforge
}

// ConfigureProviders builds the documented axis (a) clients from configuration.
// CurseForge is only enabled when an operator API key exists; its key never
// leaves the API origin. Manual platforms are deliberately not constructed.
func (s *WorldService) ConfigureProviders() {
	userAgent := ""
	if s.config != nil {
		userAgent = s.config.Server.UserAgent
	}
	if modrinth, err := providers.NewModrinth(userAgent, ""); err == nil {
		s.modrinth = modrinth
	} else if s.log != nil {
		s.log.Warn("World Library Modrinth client unavailable: %v", err)
	}
	var apiKey string
	if s.store != nil {
		if settings, _, err := s.store.GetGlobalSettings(context.Background()); err == nil && settings != nil && settings.CFAPIKey != nil {
			apiKey = *settings.CFAPIKey
		}
	}
	if apiKey == "" {
		return
	}
	if curseforge, err := providers.NewCurseForge(apiKey, userAgent, ""); err == nil {
		s.curseforge = curseforge
	} else if s.log != nil {
		s.log.Warn("World Library CurseForge client unavailable: %v", err)
	}
}

// ListWorldEntries returns library identities.
func (s *WorldService) ListWorldEntries(ctx context.Context, req *connect.Request[v1.ListWorldEntriesRequest]) (*connect.Response[v1.ListWorldEntriesResponse], error) {
	msg := req.Msg
	limit := int(msg.GetLimit())
	if limit <= 0 || limit > maxListLimit {
		limit = maxListLimit
	}
	entries, err := s.store.ListWorldEntries(ctx, strings.TrimSpace(msg.GetQuery()), strings.TrimSpace(msg.GetEdition()), limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list library worlds"))
	}
	out := &v1.ListWorldEntriesResponse{}
	for _, e := range entries {
		out.Entries = append(out.Entries, worldEntryProto(e))
	}
	return connect.NewResponse(out), nil
}

// GetWorldEntry returns one library identity.
func (s *WorldService) GetWorldEntry(ctx context.Context, req *connect.Request[v1.GetWorldEntryRequest]) (*connect.Response[v1.GetWorldEntryResponse], error) {
	entry, err := s.store.GetWorldEntry(ctx, req.Msg.GetWorldEntryId())
	if err != nil {
		return nil, notFoundOrInternal(err, "world entry not found")
	}
	return connect.NewResponse(&v1.GetWorldEntryResponse{Entry: worldEntryProto(*entry)}), nil
}

// ListRevisions returns immutable revisions of an entry.
func (s *WorldService) ListRevisions(ctx context.Context, req *connect.Request[v1.ListRevisionsRequest]) (*connect.Response[v1.ListRevisionsResponse], error) {
	revisions, err := s.store.ListWorldRevisions(ctx, req.Msg.GetWorldEntryId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list revisions"))
	}
	out := &v1.ListRevisionsResponse{}
	for _, r := range revisions {
		out.Revisions = append(out.Revisions, s.revisionProto(ctx, r))
	}
	return connect.NewResponse(out), nil
}

// GetRevision returns one immutable revision with its provenance.
func (s *WorldService) GetRevision(ctx context.Context, req *connect.Request[v1.GetRevisionRequest]) (*connect.Response[v1.GetRevisionResponse], error) {
	revision, err := s.store.GetWorldRevision(ctx, req.Msg.GetRevisionId())
	if err != nil {
		return nil, notFoundOrInternal(err, "revision not found")
	}
	return connect.NewResponse(&v1.GetRevisionResponse{Revision: s.revisionProto(ctx, *revision)}), nil
}

// ListBindings returns active bindings, optionally scoped by server or revision.
func (s *WorldService) ListBindings(ctx context.Context, req *connect.Request[v1.ListBindingsRequest]) (*connect.Response[v1.ListBindingsResponse], error) {
	bindings, err := s.store.ListWorldBindings(ctx, req.Msg.GetServerId(), req.Msg.GetRevisionId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list bindings"))
	}
	out := &v1.ListBindingsResponse{}
	for _, b := range bindings {
		out.Bindings = append(out.Bindings, bindingProto(b))
	}
	return connect.NewResponse(out), nil
}

// GetBinding returns the active binding for one server level.
func (s *WorldService) GetBinding(ctx context.Context, req *connect.Request[v1.GetBindingRequest]) (*connect.Response[v1.GetBindingResponse], error) {
	binding, err := s.store.GetWorldBinding(ctx, req.Msg.GetServerId(), req.Msg.GetLevelName())
	if err != nil {
		return nil, notFoundOrInternal(err, "no active world binding")
	}
	return connect.NewResponse(&v1.GetBindingResponse{Binding: bindingProto(*binding)}), nil
}

// ListImportJobs returns durable import attempts.
func (s *WorldService) ListImportJobs(ctx context.Context, req *connect.Request[v1.ListImportJobsRequest]) (*connect.Response[v1.ListImportJobsResponse], error) {
	jobs, err := s.store.ListWorldImportJobs(ctx, req.Msg.GetWorldEntryId(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to list import jobs"))
	}
	out := &v1.ListImportJobsResponse{}
	for _, j := range jobs {
		out.Jobs = append(out.Jobs, importJobProto(j))
	}
	return connect.NewResponse(out), nil
}

// GetImportJob returns one import job.
func (s *WorldService) GetImportJob(ctx context.Context, req *connect.Request[v1.GetImportJobRequest]) (*connect.Response[v1.GetImportJobResponse], error) {
	job, err := s.store.GetWorldImportJob(ctx, req.Msg.GetJobId())
	if err != nil {
		return nil, notFoundOrInternal(err, "import job not found")
	}
	return connect.NewResponse(&v1.GetImportJobResponse{Job: importJobProto(*job)}), nil
}

// ImportWorld imports an operator-supplied URL or an already completed upload.
func (s *WorldService) ImportWorld(ctx context.Context, req *connect.Request[v1.ImportWorldRequest]) (*connect.Response[v1.ImportWorldResponse], error) {
	msg := req.Msg
	if strings.TrimSpace(msg.GetLicenseAttestation()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("license attestation is required for imported worlds"))
	}
	importReq := worldlib.ImportRequest{
		URL: msg.GetUrl(), UploadID: msg.GetUploadId(), DisplayName: msg.GetDisplayName(),
		SourcePlatform: msg.GetSourcePlatform(), LicenseAttestation: msg.GetLicenseAttestation(),
		SHA256Checksum: msg.GetSha256Checksum(), RequestID: msg.GetRequestId(), Operator: operatorName(ctx),
	}
	var result *worldlib.ImportResult
	var err error
	switch strings.ToLower(strings.TrimSpace(msg.GetSource())) {
	case "upload":
		if msg.GetUploadId() == "" || s.uploads == nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("upload session is unavailable"))
		}
		tempPath, _, pathErr := s.uploads.GetTempPath(msg.GetUploadId())
		if pathErr != nil {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("upload session not found"))
		}
		result, err = s.importer.ImportLocal(ctx, importReq, tempPath)
	case "url":
		result, err = s.importer.Import(ctx, importReq)
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("source must be url or upload"))
	}
	if err != nil {
		return nil, importError(err)
	}
	out := &v1.ImportWorldResponse{Job: importJobProto(*result.Job)}
	if result.Revision != nil {
		out.Revision = s.revisionProto(ctx, *result.Revision)
	}
	return connect.NewResponse(out), nil
}

// CheckCompatibility produces a read-only detect/infer/unknown report.
func (s *WorldService) CheckCompatibility(ctx context.Context, req *connect.Request[v1.CheckCompatibilityRequest]) (*connect.Response[v1.CheckCompatibilityResponse], error) {
	msg := req.Msg
	revision, err := s.store.GetWorldRevision(ctx, msg.GetRevisionId())
	if err != nil {
		return nil, notFoundOrInternal(err, "revision not found")
	}
	targetMC := msg.GetTargetMcVersion()
	edition := "java"
	loader := ""
	if msg.GetServerId() != "" {
		server, err := s.store.GetServer(ctx, msg.GetServerId())
		if err != nil {
			return nil, notFoundOrInternal(err, "server not found")
		}
		if targetMC == "" {
			targetMC = server.MCVersion
		}
		loader = string(server.ModLoader)
	}
	if targetMC == "" {
		targetMC = revision.MCVersion
	}
	report, err := s.compat.Check(ctx, revision.StoragePath, worldlib.Target{
		ServerID: msg.GetServerId(), MCVersion: targetMC, Edition: edition,
		ModLoader: loader, DataVersion: revision.DataVersion,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	out := &v1.CompatibilityReport{
		RevisionId: revision.ID, ServerId: msg.GetServerId(), TargetMcVersion: targetMC,
		Overall: report.Overall, Unsupported: report.Unsupported,
	}
	for _, f := range report.Findings {
		out.Findings = append(out.Findings, &v1.CompatibilityFinding{
			Subject: f.Subject, Confidence: f.Confidence, Value: f.Value, Reason: f.Reason,
			Observation: observationProto(f.Observation),
		})
	}
	transforms, err := s.compat.DryRun(ctx, report)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("dry run failed"))
	}
	for _, t := range transforms {
		out.Transforms = append(out.Transforms, &v1.TransformPlan{
			Name: t.Name, Status: t.Status, Reversibility: t.Reversibility, Downtime: t.Downtime, Reason: t.Reason,
		})
	}
	return connect.NewResponse(&v1.CheckCompatibilityResponse{Report: out}), nil
}

// BindWorld journals a stop-based bind or swap.
func (s *WorldService) BindWorld(ctx context.Context, req *connect.Request[v1.BindWorldRequest]) (*connect.Response[v1.BindWorldResponse], error) {
	if s.binder == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("world binding is not configured"))
	}
	msg := req.Msg
	server, err := s.store.GetServer(ctx, msg.GetServerId())
	if err != nil {
		return nil, notFoundOrInternal(err, "server not found")
	}
	generation := nodeGeneration(ctx, server.NodeID)
	result, err := s.binder.Bind(ctx, worldlib.BindRequest{
		ServerID: msg.GetServerId(), LevelName: msg.GetLevelName(), RevisionID: msg.GetRevisionId(),
		Window: msg.GetWindow(), RequestID: msg.GetRequestId(), NodeID: server.NodeID,
		Generation: generation, Operator: operatorName(ctx),
	})
	if err != nil {
		return nil, worldError(err)
	}
	return connect.NewResponse(&v1.BindWorldResponse{
		Binding: bindingProto(*result.Binding), OperationId: result.OperationID,
		Outcome: result.Outcome, Consistency: result.Consistency,
	}), nil
}

// UnbindWorld removes a binding row; it never deletes world bytes.
func (s *WorldService) UnbindWorld(ctx context.Context, req *connect.Request[v1.UnbindWorldRequest]) (*connect.Response[v1.UnbindWorldResponse], error) {
	if s.binder == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("world binding is not configured"))
	}
	removed, err := s.binder.Unbind(ctx, req.Msg.GetServerId(), req.Msg.GetLevelName(), req.Msg.GetRequestId())
	if err != nil {
		return nil, worldError(err)
	}
	return connect.NewResponse(&v1.UnbindWorldResponse{Removed: removed}), nil
}

// ResolveWorldOperation records an operator decision; it never auto-retries.
func (s *WorldService) ResolveWorldOperation(ctx context.Context, req *connect.Request[v1.ResolveWorldOperationRequest]) (*connect.Response[v1.ResolveWorldOperationResponse], error) {
	if s.binder == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("world binding is not configured"))
	}
	if err := s.binder.ResolveOperation(ctx, req.Msg.GetServerId(), req.Msg.GetOperationId(), req.Msg.GetNote()); err != nil {
		return nil, worldError(err)
	}
	return connect.NewResponse(&v1.ResolveWorldOperationResponse{Outcome: "resolved"}), nil
}

// GetWorldConfigSchema returns the version-driven grouped wizard schema.
func (s *WorldService) GetWorldConfigSchema(ctx context.Context, req *connect.Request[v1.GetWorldConfigSchemaRequest]) (*connect.Response[v1.GetWorldConfigSchemaResponse], error) {
	msg := req.Msg
	schema := worldlib.BuildSchema(msg.GetEdition(), msg.GetMcVersion(), msg.GetModLoader().String(), msg.GetDataVersion())
	return connect.NewResponse(&v1.GetWorldConfigSchemaResponse{Schema: schemaProto(schema)}), nil
}

// PreviewWorldConfig returns a read-only classified diff.
func (s *WorldService) PreviewWorldConfig(ctx context.Context, req *connect.Request[v1.PreviewWorldConfigRequest]) (*connect.Response[v1.PreviewWorldConfigResponse], error) {
	msg := req.Msg
	schema := worldlib.BuildSchema(msg.GetEdition(), msg.GetMcVersion(), msg.GetModLoader().String(), msg.GetDataVersion())
	preview, err := worldlib.Preview(schema, msg.GetValues(), msg.GetPreset())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	out := &v1.ConfigDraftPreview{Summary: preview.Summary}
	for _, c := range preview.Changes {
		out.Changes = append(out.Changes, &v1.ConfigChange{
			Key: c.Key, Classification: c.Classification, Before: c.Before, After: c.After,
			AppliesWhen: c.AppliesWhen, Reason: c.Reason,
		})
	}
	out.ValidationErrors = preview.ValidationErrors
	out.Conflicts = preview.Conflicts
	out.RestartRequired = preview.RestartRequired
	return connect.NewResponse(&v1.PreviewWorldConfigResponse{Preview: out}), nil
}

// CreateWorldFromConfig creates a fresh immutable revision from a draft. It
// stores only generated metadata and empty folders; the server creates chunks
// on first start, and an unknown data version is never guessed.
func (s *WorldService) CreateWorldFromConfig(ctx context.Context, req *connect.Request[v1.CreateWorldFromConfigRequest]) (*connect.Response[v1.CreateWorldFromConfigResponse], error) {
	msg := req.Msg
	if strings.TrimSpace(msg.GetName()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("world name is required"))
	}
	schema := worldlib.BuildSchema(msg.GetEdition(), msg.GetMcVersion(), msg.GetModLoader().String(), msg.GetDataVersion())
	preview, err := worldlib.Preview(schema, msg.GetValues(), msg.GetPreset())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if len(preview.ValidationErrors) > 0 || len(preview.Conflicts) > 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			fmt.Errorf("draft is not valid: %s", strings.Join(append(preview.ValidationErrors, preview.Conflicts...), "; ")))
	}
	values := map[string]string{"level-name": "world"}
	for _, c := range preview.Changes {
		values[c.Key] = c.After
	}
	archive, err := worldlib.WriteFreshWorldArchive(values, msg.GetDataVersion())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	sha := sha256Hex(archive)
	revisionID := worldlib.RevisionID(sha)
	entryID := worldlib.NewEntryID(uuid.NewString())
	requestID := msg.GetRequestId()
	if requestID == "" {
		requestID = "world-create-" + entryID
	}
	if existing, err := s.store.GetWorldRevision(ctx, revisionID); err == nil {
		return connect.NewResponse(&v1.CreateWorldFromConfigResponse{Revision: s.revisionProto(ctx, *existing)}), nil
	} else if !isRecordNotFound(err) {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to check revision"))
	}
	archivePath := filepath.Join(s.libraryRoot, revisionID+".zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to store revision"))
	}
	entry := &storage.WorldEntry{ID: entryID, Name: msg.GetName(), Edition: msg.GetEdition(), Family: "overworld", CurrentRevisionID: revisionID}
	if err := s.store.CreateWorldEntry(ctx, entry); err != nil {
		_ = os.Remove(archivePath)
		return nil, connect.NewError(connect.CodeInternal, errors.New("failed to create library entry"))
	}
	revision := &storage.WorldRevision{
		ID: revisionID, WorldEntryID: entryID, SHA256: sha, SizeBytes: int64(len(archive)),
		EntryCount: 3, MCVersion: msg.GetMcVersion(), DataVersion: msg.GetDataVersion(),
		ModLoader: msg.GetModLoader().String(), Provenance: `{"platform":"panel","operator":"` + operatorName(ctx) + `"}`,
		LicenseAttestation: msg.GetLicenseAttestation(), CreatedBy: operatorName(ctx),
		NodeID: "", StoragePath: archivePath,
	}
	if err := s.store.PutWorldRevision(ctx, revision); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.CreateWorldFromConfigResponse{Revision: s.revisionProto(ctx, *revision), Entry: worldEntryProto(*entry)}), nil
}

// SearchWorldArtifacts searches axis (a) content artifacts only. A modpack or
// datapack is NOT a prebuilt world, and manual platforms are never queried.
func (s *WorldService) SearchWorldArtifacts(ctx context.Context, req *connect.Request[v1.SearchWorldArtifactsRequest]) (*connect.Response[v1.SearchWorldArtifactsResponse], error) {
	msg := req.Msg
	limit := int(msg.GetLimit())
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	searchReq := providers.SearchRequest{
		Query: msg.GetQuery(), MCVersion: msg.GetMcVersion(), Loader: msg.GetModLoader().String(),
		ProjectType: msg.GetProjectType(), Cursor: msg.GetCursor(), Limit: limit,
	}
	var searcher providers.Searcher
	switch strings.ToLower(strings.TrimSpace(msg.GetProvider())) {
	case "modrinth":
		searcher = s.modrinth
	case "curseforge":
		searcher = s.curseforge
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("provider must be modrinth or curseforge; manual platforms use URL/file import"))
	}
	if searcher == nil {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("provider is not configured"))
	}
	artifacts, cursor, rateLimited, err := searcher.Search(ctx, searchReq)
	if err != nil {
		return nil, worldError(err)
	}
	out := &v1.SearchWorldArtifactsResponse{NextCursor: cursor, RateLimited: rateLimited}
	for _, a := range artifacts {
		out.Artifacts = append(out.Artifacts, &v1.WorldArtifact{
			Provider: a.Provider, ProjectId: a.ProjectID, FileId: a.FileID, Name: a.Name,
			ProjectType: a.ProjectType, McVersion: a.MCVersion, DownloadUrl: a.DownloadURL,
			Sha256: a.SHA256, License: a.License, SourceUrl: a.SourceURL,
		})
	}
	return connect.NewResponse(out), nil
}

// GetProviderCapabilities reports the two support axes honestly.
func (s *WorldService) GetProviderCapabilities(ctx context.Context, req *connect.Request[v1.GetProviderCapabilitiesRequest]) (*connect.Response[v1.GetProviderCapabilitiesResponse], error) {
	out := &v1.GetProviderCapabilitiesResponse{}
	for _, c := range providers.Capabilities() {
		out.Providers = append(out.Providers, &v1.ProviderCapability{
			Provider: c.Provider, ApiBackedArtifacts: c.APIBackedArtifacts,
			PrebuiltWorldCatalog: c.PrebuiltWorldCatalog, DiscoveryGate: c.DiscoveryGate, Note: c.Note,
		})
	}
	return connect.NewResponse(out), nil
}

func worldEntryProto(e storage.WorldEntry) *v1.WorldEntry {
	return &v1.WorldEntry{
		Id: e.ID, Name: e.Name, Edition: e.Edition, Family: e.Family,
		CurrentRevisionId: e.CurrentRevisionID,
		CreatedAt: timestamppb.New(e.CreatedAt), UpdatedAt: timestamppb.New(e.UpdatedAt),
	}
}

func (s *WorldService) revisionProto(ctx context.Context, r storage.WorldRevision) *v1.WorldRevision {
	loader := v1.ModLoader(v1.ModLoader_value[r.ModLoader])
	out := &v1.WorldRevision{
		Id: r.ID, WorldEntryId: r.WorldEntryID, Sha256: r.SHA256, SizeBytes: r.SizeBytes,
		EntryCount: r.EntryCount, McVersion: r.MCVersion, DataVersion: r.DataVersion,
		ModLoader: loader, Provenance: r.Provenance, LicenseAttestation: r.LicenseAttestation,
		CreatedBy: r.CreatedBy, CreatedAt: timestamppb.New(r.CreatedAt),
	}
	bindings, err := s.store.ListWorldBindings(ctx, "", r.ID)
	if err == nil {
		out.Bound = len(bindings) > 0
	}
	return out
}

func bindingProto(b storage.WorldBinding) *v1.WorldBinding {
	return &v1.WorldBinding{
		Id: b.ID, ServerId: b.ServerID, LevelName: b.LevelName, RevisionId: b.RevisionID,
		NodeId: b.NodeID, Generation: b.Generation, Consistency: b.Consistency,
		BoundAt: timestamppb.New(b.BoundAt),
	}
}

func importJobProto(j storage.WorldImportJob) *v1.WorldImportJob {
	return &v1.WorldImportJob{
		Id: j.ID, WorldEntryId: j.WorldEntryID, SourcePlatform: j.SourcePlatform,
		SourceUrl: j.SourceURL, Operator: j.Operator, LicenseAttestation: j.LicenseAttestation,
		Phase: j.Phase, ResultRevisionId: j.ResultRevisionID, Error: j.Error,
		CreatedAt: timestamppb.New(j.CreatedAt), UpdatedAt: timestamppb.New(j.UpdatedAt),
	}
}

func observationProto(o worldlib.Observation) *v1.Observation {
	out := &v1.Observation{Source: o.Source, Applicability: o.Applicability, Confidence: o.Confidence, Note: o.Note}
	if !o.ObservedAt.IsZero() {
		out.ObservedAt = timestamppb.New(o.ObservedAt)
	}
	return out
}

func schemaProto(s *worldlib.ConfigSchema) *v1.WorldConfigSchema {
	out := &v1.WorldConfigSchema{
		Edition: s.Edition, McVersion: s.MCVersion, DataVersion: s.DataVersion,
		Partial: s.Partial, ConflictRules: s.ConflictRules,
	}
	out.ModLoader = v1.ModLoader(v1.ModLoader_value[s.ModLoader])
	for _, c := range s.Controls {
		out.Controls = append(out.Controls, &v1.ConfigControl{
			Key: c.Key, Label: c.Label, Group: c.Group, Description: c.Description,
			Classification: c.Classification, ValueType: c.ValueType, Options: c.Options,
			DefaultValue: c.DefaultValue, Advanced: c.Advanced, Expert: c.Expert, AppliesWhen: c.AppliesWhen,
		})
	}
	for _, p := range s.Presets {
		out.Presets = append(out.Presets, &v1.ConfigPreset{Id: p.ID, Label: p.Label, Description: p.Description, Values: p.Values})
	}
	for _, o := range s.Observations {
		out.Observations = append(out.Observations, observationProto(o))
	}
	return out
}

func operatorName(ctx context.Context) string {
	if user := auth.GetUserFromContext(ctx); user != nil {
		return user.Username
	}
	return ""
}

func notFoundOrInternal(err error, message string) error {
	if isRecordNotFound(err) {
		return connect.NewError(connect.CodeNotFound, errors.New(message))
	}
	return connect.NewError(connect.CodeInternal, errors.New(message))
}

func worldError(err error) error {
	switch {
	case errors.Is(err, worldlib.ErrRemoteUnavailable), errors.Is(err, nodestorage.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, worldlib.ErrTwoWriters), errors.Is(err, worldlib.ErrStaleGeneration), errors.Is(err, worldlib.ErrUncertainOutcome):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, worldlib.ErrUnknownVersion), errors.Is(err, worldlib.ErrUnsupported):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, providers.ErrRateLimit):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, providers.ErrCategoryUnavailable):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, worldlib.ErrArchiveRejected), errors.Is(err, worldlib.ErrChecksumMismatch),
		errors.Is(err, worldlib.ErrCredentialBoundary), errors.Is(err, providers.ErrCredentialBoundary):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func importError(err error) error {
	if errors.Is(err, worldlib.ErrChecksumMismatch) || errors.Is(err, worldlib.ErrArchiveRejected) || errors.Is(err, worldlib.ErrCredentialBoundary) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return worldError(err)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func isRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// nodeGeneration is supplied by the composition root through context for now;
// it defaults to zero so binding fails closed until a real generation is set.
func nodeGeneration(ctx context.Context, nodeID string) int64 {
	if generation, ok := ctx.Value(worldNodeGenerationKey{}).(int64); ok {
		return generation
	}
	return 0
}

type worldNodeGenerationKey struct{}
