// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp upload/commit family hand-off (OSS-370/371/372/373/375).
//
// This file is deliberately self-contained. It adds NO entry to the shared
// catalog and does not edit the shared Clients, catalog_data.go,
// integration_catalog.go or integration_registration.go. The exported seams
// below are the machine-readable hand-off the integration owner joins into the
// combined catalog and invocation harness after BOTH lanes (download and
// upload) have produced executable tools/call evidence. Until then the STRICT
// publication gate stays RED and the nine operations in
// integration_pending.go remain PENDING.
//
// Every tool in this family reaches the controller only through typed generated
// Connect clients, behind the production auth interceptor, using the controller-
// owned bounded streaming transport (OpenStream/ReadStreamFrame/CloseStream)
// for the composed rest_upload_stream path. The gateway holds only an opaque
// handle, staged bytes under the existing bounded caps and a cumulative digest;
// it never owns panel/node storage and never writes a destination directly.
package mcp

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Upload tool names. They match the suggestedTool values in the OSS-375
// coverage matrix so the integration owner can map evidence without renaming.
const (
	UploadToolSaveUploadedFile = "file_service_save_uploaded_file"
	UploadToolInitUpload       = "upload_service_init_upload"
	UploadToolUploadChunk      = "upload_service_upload_chunk"
	UploadToolStreamCommit     = "rest_upload_stream_commit"
)

// Upload procedure constants. A non-RPC surface is proven through its composed
// target procedure, never by naming a raw session id, byte stream or REST path.
const (
	uploadProcSave  = "/carbonpanel.v1.FileService/SaveUploadedFile"
	uploadProcInit  = "/carbonpanel.v1.UploadService/InitUpload"
	uploadProcChunk = "/carbonpanel.v1.UploadService/UploadChunk"
)

// UploadToolDescriptor is the machine-readable exposure decision for one
// upload-family operation. Factual tuple fields are joined from the generated
// rawCatalogEntries table (service, method, client, scope, build); only the
// local exposure decision (tool name, class, confirm, bounded args, composed
// target) is declared here. This is the artifact the integration owner
// consumes to generate catalog entries and exposed tool specs.
type UploadToolDescriptor struct {
	Tool           string     `json:"tool"`
	Procedure      string     `json:"procedure"`
	Service        string     `json:"service"`
	Method         string     `json:"method"`
	Client         string     `json:"client"`
	Scope          string     `json:"scope"`
	Class          Class      `json:"class"`
	Access         string     `json:"access"`
	Confirm        bool       `json:"confirm"`
	Build          PanelBuild `json:"build"`
	Transport      string     `json:"transport"`
	ComposedBy     string     `json:"composed_by,omitempty"`
	BoundedArgs    string     `json:"bounded_args"`
	SourceRef      string     `json:"source_ref"`
	EquivalentFlow string     `json:"equivalent_flow,omitempty"`
	Description    string     `json:"description"`
}

type uploadDecision struct {
	id             string
	tool           string
	procedure      string
	access         string
	confirm        bool
	transport      string
	composedBy     string
	boundedArgs    string
	sourceRef      string
	equivalentFlow string
	description    string
}

// uploadDecisions is the local, reviewable exposure table. No factual scope or
// client field is declared here: those are joined from the generated catalog so
// they cannot drift from the live RBAC map and proto descriptors.
var uploadDecisions = []uploadDecision{
	{
		id:          "rpc:/carbonpanel.v1.FileService/SaveUploadedFile",
		tool:        UploadToolSaveUploadedFile,
		procedure:   uploadProcSave,
		access:      "mutation",
		confirm:     true,
		transport:   "typed_rpc",
		boundedArgs: "server_id + single upload_session_id + destination_path/filename (each <= 128 chars); confirm=true",
		sourceRef:   "proto/carbonpanel/v1/file.proto:SaveUploadedFile; internal/rbac/mapping.go:files/create[server_id]",
		description: "Commit one owned, completed upload session into one server-relative destination. Requires confirm=true; authorization is re-evaluated by the controller at commit (fresh files:create[server_id]). No byte data is carried by this tool.",
	},
	{
		id:             "rpc:/carbonpanel.v1.UploadService/InitUpload",
		tool:           UploadToolInitUpload,
		procedure:      uploadProcInit,
		access:         "mutation",
		confirm:        true,
		transport:      "typed_rpc",
		boundedArgs:    "filename <= 128 chars; total_size 1..4 GiB; chunk_size 1..256 KiB; confirm=true",
		sourceRef:      "proto/carbonpanel/v1/upload.proto:InitUpload; internal/rbac/mapping.go:uploads/create",
		equivalentFlow: uploadProcSave,
		description:    "Create a bounded upload session owned by the caller's dp_ token. Requires confirm=true; follow with upload_chunk and finish at save_uploaded_file. No raw byte stream is accepted.",
	},
	{
		id:             "rpc:/carbonpanel.v1.UploadService/UploadChunk",
		tool:           UploadToolUploadChunk,
		procedure:      uploadProcChunk,
		access:         "mutation",
		confirm:        true,
		transport:      "typed_rpc",
		boundedArgs:    "single session_id + chunk_index 0..100000 + base64 data <= 256 KiB; confirm=true",
		sourceRef:      "proto/carbonpanel/v1/upload.proto:UploadChunk; internal/rbac/mapping.go:uploads/create",
		equivalentFlow: uploadProcSave,
		description:    "Write one bounded base64 chunk into an owned upload session. Requires confirm=true; 256 KiB matches the controller streaming frame cap. Session ownership is enforced by the controller, not by a tool argument.",
	},
	{
		id:             "surface:rest_upload_stream",
		tool:           UploadToolStreamCommit,
		procedure:      uploadProcSave,
		access:         "mutation",
		confirm:        true,
		transport:      "bounded_stream_compose",
		composedBy:     uploadProcSave,
		boundedArgs:    "single session_handle + destination/filename (each <= 128 chars); confirm=true; no raw bytes",
		sourceRef:      "internal/rpc/server.go:PUT /api/v1/upload/{sessionId}; internal/rpc/handlers/upload.go",
		equivalentFlow: "scoped_stream_open + scoped_stream_chunk* -> rest_upload_stream_commit (InitUpload + UploadChunk -> SaveUploadedFile)",
		description:    "Compose one controller-owned bounded byte stream into a server-relative destination through typed, auth-intercepted RPCs. Requires confirm=true; one-at-a-time commit lease, ambiguous post-dispatch verification and zero writes on refusal.",
	},
}

// UploadFamilyDescriptors is the exported machine-readable hand-off. Factual
// fields are joined from the generated catalog; a missing catalog tuple is a
// hard failure rather than a silently empty descriptor.
var UploadFamilyDescriptors = buildUploadDescriptors()

func buildUploadDescriptors() []UploadToolDescriptor {
	out := make([]UploadToolDescriptor, 0, len(uploadDecisions))
	for _, d := range uploadDecisions {
		// Join factual fields from the generated raw catalog table rather than
		// DeclaredProcedure: the latter is populated in catalog.go's init(),
		// which runs AFTER package-level variable initializers. The raw table is
		// a literal and is safe to read here. Class is a LOCAL exposure decision:
		// the raw catalog excludes these operations until both lanes land.
		var entry CatalogEntry
		found := false
		for _, candidate := range rawCatalogEntries {
			if candidate.Procedure == d.procedure {
				entry, found = candidate, true
				break
			}
		}
		if !found {
			panic("mcp: upload family procedure missing from generated catalog: " + d.procedure)
		}
		out = append(out, UploadToolDescriptor{
			Tool:           d.tool,
			Procedure:      d.procedure,
			Service:        entry.Service,
			Method:         entry.Method,
			Client:         entry.Client,
			Scope:          entry.Scope,
			Class:          ClassReview, // local exposure decision for this lane
			Access:         d.access,
			Confirm:        d.confirm,
			Build:          entry.Build,
			Transport:      d.transport,
			ComposedBy:     d.composedBy,
			BoundedArgs:    d.boundedArgs,
			SourceRef:      d.sourceRef,
			EquivalentFlow: d.equivalentFlow,
			Description:    d.description,
		})
	}
	return out
}

// UploadFamilyToolSpecs returns the exposed tool specs generated from the
// descriptors. ClassExclude descriptors (none in this hand-off) would be omitted.
func UploadFamilyToolSpecs() []ToolSpec {
	out := make([]ToolSpec, 0, len(UploadFamilyDescriptors))
	for _, d := range UploadFamilyDescriptors {
		if d.Class == ClassExclude {
			continue
		}
		out = append(out, ToolSpec{
			Name: d.Tool, Procedure: d.Procedure, Class: d.Class,
			Confirm: d.Confirm, Description: d.Description,
		})
	}
	return out
}

// UploadFamilyInvocationArgs are the valid bounded argument tables for the
// executable invocation harness. The composed stream tool is deliberately
// absent: its evidence is produced by the bounded-stream workflow test, not by
// the single-call typed-field harness (its arguments name a handle, not RPC
// fields). The integration owner merges these tables into allInvocationArgs.
func UploadFamilyInvocationArgs() map[string]map[string]any {
	return map[string]map[string]any{
		UploadToolSaveUploadedFile: {
			"server_id":         "srv-1",
			"upload_session_id": "up-1",
			"destination_path":  "mods",
			"filename":          "uploaded.jar",
			"confirm":           true,
		},
		UploadToolInitUpload: {
			"filename":   "uploaded.jar",
			"total_size": 16,
			"chunk_size": 262144,
			"confirm":    true,
		},
		UploadToolUploadChunk: {
			"session_id":  "up-1",
			"chunk_index": 0,
			"data":        base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")),
			"confirm":     true,
		},
	}
}

// --- Bounded argument envelopes ---------------------------------------------

type uploadSaveArgs struct {
	ServerID        string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	UploadSessionID string `json:"upload_session_id" jsonschema:"single completed upload session id, 1 to 128 characters"`
	DestinationPath string `json:"destination_path" jsonschema:"controlled server-relative destination, at most 512 characters"`
	Filename        string `json:"filename" jsonschema:"target filename without separators, at most 128 characters"`
	Confirm         bool   `json:"confirm,omitempty" jsonschema:"must be true to commit bytes"`
}

func (a uploadSaveArgs) Confirmation() bool { return a.Confirm }

type uploadInitArgs struct {
	Filename  string `json:"filename" jsonschema:"upload filename, 1 to 128 characters"`
	TotalSize int64  `json:"total_size" jsonschema:"declared byte size, 1 to 4 GiB"`
	ChunkSize int32  `json:"chunk_size" jsonschema:"chunk size, 1 to 256 KiB"`
	Confirm   bool   `json:"confirm,omitempty" jsonschema:"must be true to create a session"`
}

func (a uploadInitArgs) Confirmation() bool { return a.Confirm }

type uploadChunkArgs struct {
	SessionID  string `json:"session_id" jsonschema:"single owned upload session id, 1 to 128 characters"`
	ChunkIndex int32  `json:"chunk_index" jsonschema:"chunk index, 0 to 100000"`
	Data       string `json:"data" jsonschema:"base64 chunk data, at most 256 KiB decoded"`
	Confirm    bool   `json:"confirm,omitempty" jsonschema:"must be true to write a chunk"`
}

func (a uploadChunkArgs) Confirmation() bool { return a.Confirm }

type uploadStreamCommitArgs struct {
	SessionHandle string `json:"session_handle" jsonschema:"opaque handle returned by scoped_stream_open"`
	Destination   string `json:"destination" jsonschema:"controlled server-relative destination path"`
	Filename      string `json:"filename" jsonschema:"target filename without separators, at most 128 characters"`
	Confirm       bool   `json:"confirm,omitempty" jsonschema:"must be true to commit bytes"`
}

func (a uploadStreamCommitArgs) Confirmation() bool { return a.Confirm }

// --- Typed RPC cores ---------------------------------------------------------
//
// These helpers are the ONLY way this family reaches the controller. Each call
// uses a typed generated client with the caller's dp_ Bearer token, so the
// production auth interceptor enforces uploads/create for the session calls and
// a fresh files:create[server_id] for the final save. No direct helper writes a
// destination file.

func uploadSaveCore(ctx context.Context, cl *Clients, in uploadSaveArgs) (any, error) {
	if err := requireID(UploadToolSaveUploadedFile, "server_id", in.ServerID); err != nil {
		return nil, err
	}
	if err := requireID(UploadToolSaveUploadedFile, "upload_session_id", in.UploadSessionID); err != nil {
		return nil, err
	}
	if err := validatePath(UploadToolSaveUploadedFile, in.DestinationPath, true); err != nil {
		return nil, err
	}
	if err := uploadFilename(UploadToolSaveUploadedFile, in.Filename); err != nil {
		return nil, err
	}
	return invoke(ctx, cl.File.SaveUploadedFile, v1.SaveUploadedFileRequest{
		ServerId: in.ServerID, UploadSessionId: in.UploadSessionID,
		DestinationPath: in.DestinationPath, Filename: in.Filename,
	})
}

func uploadInitCore(ctx context.Context, cl *Clients, in uploadInitArgs) (any, error) {
	if err := uploadFilename(UploadToolInitUpload, in.Filename); err != nil {
		return nil, err
	}
	if in.TotalSize <= 0 || in.TotalSize > storageworker.MaxStreamBytes {
		return nil, invalidArgument(UploadToolInitUpload, "total_size", "must be between 1 byte and the 64 GiB stream cap")
	}
	if in.ChunkSize <= 0 || in.ChunkSize > storageworker.ChunkBytes {
		return nil, invalidArgument(UploadToolInitUpload, "chunk_size", "must be between 1 and 256 KiB")
	}
	return invoke(ctx, cl.Upload.InitUpload, v1.InitUploadRequest{
		Filename: in.Filename, TotalSize: in.TotalSize, ChunkSize: in.ChunkSize,
	})
}

func uploadChunkCore(ctx context.Context, cl *Clients, in uploadChunkArgs) (any, error) {
	if err := requireID(UploadToolUploadChunk, "session_id", in.SessionID); err != nil {
		return nil, err
	}
	if in.ChunkIndex < 0 || in.ChunkIndex > 100000 {
		return nil, invalidArgument(UploadToolUploadChunk, "chunk_index", "must be between 0 and 100000")
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		return nil, invalidArgument(UploadToolUploadChunk, "data", "must be valid base64")
	}
	if len(data) == 0 || len(data) > storageworker.ChunkBytes {
		return nil, invalidArgument(UploadToolUploadChunk, "data", "decoded chunk must be 1..256 KiB")
	}
	return invoke(ctx, cl.Upload.UploadChunk, v1.UploadChunkRequest{
		SessionId: in.SessionID, ChunkIndex: in.ChunkIndex, Data: data,
	})
}

func uploadFilename(tool, name string) error {
	if err := requireText(tool, "filename", name, 128); err != nil {
		return err
	}
	if strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return invalidArgument(tool, "filename", "must not contain path separators")
	}
	return nil
}

// uploadTool registers one upload-family tool with an explicit spec. It respects
// the production registration allowlist; a directly constructed (unbound) test
// server allows the full hand-off set, while the combined suite uses NewServer.
func uploadTool[In any](s *sdkmcp.Server, spec ToolSpec, clients *Clients, run func(context.Context, *Clients, In) (any, error)) {
	if !toolAllowedOn(s, spec.Name) {
		return
	}
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: spec.Name, Description: spec.Description},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, ToolOutput, error) {
			if spec.Confirm {
				if c, ok := any(in).(interface{ Confirmation() bool }); !ok || !c.Confirmation() {
					return failure(spec, &ToolError{
						Kind:      ErrKindConfirmationRequired,
						Tool:      spec.Name,
						Procedure: spec.Procedure,
						Message:   spec.Name + " is a mutation: set confirm=true with the exact target to proceed",
					})
				}
			}
			result, err := run(ctx, clients, in)
			if err != nil {
				return failure(spec, err)
			}
			return nil, ToolOutput{Status: "ok", Result: result}, nil
		})
}

// RegisterUploadFamilyTools is the production seam. The integration owner calls
// it from registerCombinedTools after bindRegistrationPolicy, once every lane's
// descriptors are merged into the generated catalog allowlist.
func RegisterUploadFamilyTools(s *sdkmcp.Server, c *Clients) {
	if c == nil {
		return
	}
	specs := map[string]ToolSpec{}
	for _, spec := range UploadFamilyToolSpecs() {
		specs[spec.Name] = spec
	}
	save := specs[UploadToolSaveUploadedFile]
	uploadTool(s, save, c, func(ctx context.Context, cl *Clients, in uploadSaveArgs) (any, error) {
		return uploadSaveCore(ctx, cl, in)
	})
	initSpec := specs[UploadToolInitUpload]
	uploadTool(s, initSpec, c, func(ctx context.Context, cl *Clients, in uploadInitArgs) (any, error) {
		return uploadInitCore(ctx, cl, in)
	})
	chunkSpec := specs[UploadToolUploadChunk]
	uploadTool(s, chunkSpec, c, func(ctx context.Context, cl *Clients, in uploadChunkArgs) (any, error) {
		return uploadChunkCore(ctx, cl, in)
	})
	commitSpec := specs[UploadToolStreamCommit]
	uploadTool(s, commitSpec, c, func(ctx context.Context, cl *Clients, in uploadStreamCommitArgs) (any, error) {
		return uploadStreamCommitCore(ctx, cl, in)
	})
}

// uploadStreamCommitCore is the composed rest_upload_stream path. It consumes a
// completed controller-owned bounded stream, holds the one-at-a-time commit
// lease for the whole upload, and commits ONLY through the typed upload path.
// A known pre-save failure stays retryable; an ambiguous post-dispatch save is
// resolved by verifying the exact destination, never by a blind re-upload.
func uploadStreamCommitCore(ctx context.Context, cl *Clients, in uploadStreamCommitArgs) (any, error) {
	transfer, err := cl.streamTransfer()
	if err != nil {
		return nil, err
	}
	if err := requireID(UploadToolStreamCommit, "session_handle", in.SessionHandle); err != nil {
		return nil, err
	}
	if err := validatePath(UploadToolStreamCommit, in.Destination, true); err != nil {
		return nil, err
	}
	if err := uploadFilename(UploadToolStreamCommit, in.Filename); err != nil {
		return nil, err
	}
	session, err := transfer.Lookup(cl.owner, in.SessionHandle)
	if err != nil {
		return nil, err
	}
	data, digest, err := transfer.ClaimCommit(cl.owner, in.SessionHandle)
	if err != nil {
		return nil, err
	}
	outcome := CommitRetryable
	defer func() { transfer.FinishCommit(cl.owner, in.SessionHandle, outcome) }()
	if len(data) == 0 {
		return nil, invalidArgument(UploadToolStreamCommit, "data", "stream produced zero bytes; nothing to commit")
	}

	initRes, err := uploadInitCore(ctx, cl, uploadInitArgs{
		Filename: in.Filename, TotalSize: int64(len(data)), ChunkSize: storageworker.ChunkBytes,
	})
	if err != nil {
		return nil, err
	}
	sessionID := initRes.(*v1.InitUploadResponse).GetSessionId()
	for i := 0; i < len(data); i += storageworker.ChunkBytes {
		end := i + storageworker.ChunkBytes
		if end > len(data) {
			end = len(data)
		}
		if _, err := uploadChunkCore(ctx, cl, uploadChunkArgs{
			SessionID: sessionID, ChunkIndex: int32(i / storageworker.ChunkBytes),
			Data: base64.StdEncoding.EncodeToString(data[i:end]),
		}); err != nil {
			return nil, err
		}
	}
	_, saveErr := uploadSaveCore(ctx, cl, uploadSaveArgs{
		ServerID: session.ServerID(), UploadSessionID: sessionID,
		DestinationPath: in.Destination, Filename: in.Filename,
	})
	verified := false
	joined := path.Join(in.Destination, in.Filename)
	if saveErr != nil && ambiguousSaveOutcome(saveErr) {
		ok, verifyErr := verifyCommittedFile(ctx, cl, session.ServerID(), in.Destination,
			in.Filename, int64(len(data)), digest)
		if verifyErr == nil && ok {
			verified = true
		} else {
			outcome = CommitUnknown
			return nil, fmt.Errorf("%w (connect %v): verification=%v; inspect server %q destination %q before any retry; "+
				"do not blindly retry the same bytes; upload session %q may already be consumed",
				ErrCommitOutcomeUnknown, connect.CodeOf(saveErr), verifyErr,
				session.ServerID(), joined, sessionID)
		}
	} else if saveErr != nil {
		return nil, saveErr
	}
	outcome = CommitSucceeded
	result := map[string]any{
		"bytes": len(data), "sha256": digest, "destination": in.Destination,
		"upload_session": sessionID, "actual_size": len(data),
		"authorization": "production typed upload + server-scoped save",
	}
	if verified {
		result["verification"] = "destination verified after ambiguous final-save reply"
	}
	return result, nil
}

// UploadFamilyErrorSentinels exposes the terminal/refusal signals used by the
// composed path so the integration harness can assert zero-write semantics
// without importing internal transport types.
func UploadFamilyErrorSentinels() map[string]error {
	return map[string]error{
		"canceled":        ErrStreamCanceled,
		"expired":         ErrStreamExpired,
		"cross_scope":     ErrStreamCrossScope,
		"too_large":       ErrStreamTooLarge,
		"not_found":       ErrStreamNotFound,
		"not_complete":    ErrStreamNotComplete,
		"digest_mismatch": ErrStreamDigestMismatch,
		"commit_busy":     ErrStreamCommitBusy,
		"outcome_unknown": ErrCommitOutcomeUnknown,
	}
}

//go:embed family_upload_descriptors.json
var uploadFamilyDescriptorsJSON []byte

// UploadFamilyDescriptorsJSON returns the committed machine-readable artifact.
// A drift test compares it with the live descriptor table on every build.
func UploadFamilyDescriptorsJSON() []byte { return uploadFamilyDescriptorsJSON }

// MarshalUploadFamilyDescriptors returns the machine-readable descriptor output
// (pretty JSON). It is the authoritative artifact committed as
// family_upload_descriptors.json.
func MarshalUploadFamilyDescriptors() ([]byte, error) {
	return json.MarshalIndent(UploadFamilyDescriptors, "", "  ")
}
