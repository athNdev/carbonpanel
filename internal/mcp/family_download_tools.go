// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370/371-375): download-family tools.
//
// This file is deliberately self-contained. It adds NO entry to the shared
// catalog and edits NO integration/shared file: DownloadToolSpecs and
// DownloadToolDescriptors are the exported seams the integration owner consumes
// when the remaining pending operations are reconciled.
//
// Every byte path below is a typed, auth-intercepted controller RPC. The
// InitFileDownload / DownloadArchive / ExportWorld RPCs stage a download on the
// controller and return an opaque session id plus its authoritative size.
// OpenStream with download_session_id then opens a bounded controller-owned
// frame handle over that staged file, ReadStreamFrame carries bounded frames
// with a cumulative SHA-256, and CloseStream releases the handle. The gateway
// writes nothing to disk and never sees a storage path: it holds only the small
// bounded frame working set and a digest, exactly like the existing
// scoped_stream_* seam (internal/mcp/integration_stream_transfer.go).
//
// The REST surface `GET /api/v1/download/{sessionId}` remains the controller's
// browser/CLI byte transport. In the MCP gateway it is intentionally NOT
// hand-rolled HTTP (knowledge/architecture/mcp-server.md forbids that); the
// same bytes travel through the typed bounded-frame RPCs above.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"hash"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// downloadMaxBytes is the gateway working-set cap for one composed download.
// It mirrors the established StreamTransfer per-session bound; larger staged
// downloads are refused here rather than buffered without limit. The browser
// and CLI REST byte path remains available for those transfers.
const downloadMaxBytes = 8 << 20

const downloadMaxPaths = 20

// DownloadToolSpecs is the exported catalog seam for the download lane. The
// download_stream tool deliberately shares the GetFile procedure with
// download_get_file: it is the composed byte path for the audited non-RPC
// surface `surface:rest_download_stream`, whose target in the pending inventory
// is GetFile. This mirrors the existing scoped_stream_chunk/cancel precedent.
func DownloadToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Name: "download_get_file", Procedure: "/carbonpanel.v1.FileService/GetFile", Class: ClassSafe,
			Description: "Read one bounded server-relative file through the typed GetFile RPC. Returns base64 bytes, exact byte count, SHA-256 and MIME type. Read-only."},
		{Name: "download_init_file", Procedure: "/carbonpanel.v1.FileService/InitFileDownload", Class: ClassSafe,
			Description: "Stage one server-relative file via typed InitFileDownload, then transfer its bytes as bounded controller frames. Returns session id, filename, size, SHA-256 and bytes. Read-only."},
		{Name: "download_archive_bundle", Procedure: "/carbonpanel.v1.FileService/DownloadArchive", Class: ClassSafe,
			Description: "Create a bounded archive via typed DownloadArchive, then transfer its bytes as bounded controller frames. At most 20 server-relative paths. Read-only."},
		{Name: "download_export_world_bundle", Procedure: "/carbonpanel.v1.FileService/ExportWorld", Class: ClassSafe,
			Description: "Prepare a world export via typed ExportWorld, then transfer its bytes as bounded controller frames. The world directory stays in place. Read-only."},
		{Name: "download_stream", Procedure: "/carbonpanel.v1.FileService/GetFile", Class: ClassSafe,
			Description: "Composed byte path for an existing download session (rest_download_stream). Reads bounded controller frames via OpenStream/ReadStreamFrame/CloseStream and returns bytes, exact size and cumulative SHA-256. No gateway file storage. Read-only."},
	}
}

var downloadSpecs = func() map[string]ToolSpec {
	m := make(map[string]ToolSpec, len(DownloadToolSpecs()))
	for _, spec := range DownloadToolSpecs() {
		m[spec.Name] = spec
	}
	return m
}()

// RegisterDownloadTools mounts the download lane on an existing SDK server.
// The production composition root (integration owner) calls this from
// NewServer; the executable evidence fixture calls it explicitly so no shared
// catalog file has to change for these tools to be genuinely callable.
func RegisterDownloadTools(s *sdkmcp.Server, c *Clients) {
	addDownloadTool(s, "download_get_file", c, func(ctx context.Context, cl *Clients, in fileArgs) (any, error) {
		if err := requireID("download_get_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("download_get_file", in.Path, true); err != nil {
			return nil, err
		}
		res, err := cl.File.GetFile(ctx, connect.NewRequest(&v1.GetFileRequest{
			ServerId: in.ServerID, Path: in.Path,
		}))
		if err != nil {
			return nil, err
		}
		content := res.Msg.GetContent()
		sum := sha256.Sum256(content)
		return map[string]any{
			"server_id": in.ServerID,
			"path":      in.Path,
			"content":   base64.StdEncoding.EncodeToString(content),
			"size":      len(content),
			"sha256":    hex.EncodeToString(sum[:]),
			"mime_type": res.Msg.GetMimeType(),
		}, nil
	})

	addDownloadTool(s, "download_init_file", c, func(ctx context.Context, cl *Clients, in fileArgs) (any, error) {
		if err := requireID("download_init_file", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("download_init_file", in.Path, true); err != nil {
			return nil, err
		}
		res, err := cl.File.InitFileDownload(ctx, connect.NewRequest(&v1.InitFileDownloadRequest{
			ServerId: in.ServerID, Path: in.Path,
		}))
		if err != nil {
			return nil, err
		}
		dsid, size := res.Msg.GetSessionId(), res.Msg.GetTotalSize()
		content, digest, frames, err := readDownloadFrames(ctx, cl, in.ServerID, dsid, size, "")
		if err != nil {
			return nil, err
		}
		return downloadResult(map[string]any{
			"session_id": dsid, "filename": res.Msg.GetFilename(), "total_size": size,
		}, content, digest, frames), nil
	})

	addDownloadTool(s, "download_archive_bundle", c, func(ctx context.Context, cl *Clients, in downloadArchiveArgs) (any, error) {
		if err := requireID("download_archive_bundle", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if len(in.Paths) == 0 || len(in.Paths) > downloadMaxPaths {
			return nil, downloadArgError("download_archive_bundle", "paths",
				fmt.Sprintf("must contain 1 to %d server-relative paths", downloadMaxPaths))
		}
		for i, p := range in.Paths {
			if err := validatePath("download_archive_bundle", p, true); err != nil {
				return nil, downloadArgError("download_archive_bundle", fmt.Sprintf("paths[%d]", i), err.Error())
			}
		}
		res, err := cl.File.DownloadArchive(ctx, connect.NewRequest(&v1.DownloadArchiveRequest{
			ServerId: in.ServerID, Paths: in.Paths,
		}))
		if err != nil {
			return nil, err
		}
		dsid, size := res.Msg.GetSessionId(), res.Msg.GetTotalSize()
		content, digest, frames, err := readDownloadFrames(ctx, cl, in.ServerID, dsid, size, "")
		if err != nil {
			return nil, err
		}
		return downloadResult(map[string]any{
			"session_id": dsid, "filename": res.Msg.GetFilename(), "total_size": size,
			"paths": in.Paths,
		}, content, digest, frames), nil
	})

	addDownloadTool(s, "download_export_world_bundle", c, func(ctx context.Context, cl *Clients, in serverArgs) (any, error) {
		if err := requireID("download_export_world_bundle", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		res, err := cl.File.ExportWorld(ctx, connect.NewRequest(&v1.ExportWorldRequest{
			ServerId: in.ServerID,
		}))
		if err != nil {
			return nil, err
		}
		dsid, size := res.Msg.GetSessionId(), res.Msg.GetTotalSize()
		content, digest, frames, err := readDownloadFrames(ctx, cl, in.ServerID, dsid, size, "")
		if err != nil {
			return nil, err
		}
		return downloadResult(map[string]any{
			"session_id": dsid, "filename": res.Msg.GetFilename(), "total_size": size,
			"data_path": res.Msg.GetDataPath(),
		}, content, digest, frames), nil
	})

	addDownloadTool(s, "download_stream", c, func(ctx context.Context, cl *Clients, in downloadStreamArgs) (any, error) {
		if err := requireID("download_stream", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := requireID("download_stream", "session_id", in.SessionID); err != nil {
			return nil, err
		}
		if in.TotalSize < 0 || in.TotalSize > downloadMaxBytes {
			return nil, downloadArgError("download_stream", "total_size",
				fmt.Sprintf("must be an admission bound from 0 to %d bytes", downloadMaxBytes))
		}
		if in.ExpectedSHA256 != "" && !isSHA256Hex(in.ExpectedSHA256) {
			return nil, downloadArgError("download_stream", "expected_sha256", "must be 64 hexadecimal characters")
		}
		content, digest, frames, err := readDownloadFrames(ctx, cl, in.ServerID, in.SessionID, in.TotalSize, in.ExpectedSHA256)
		if err != nil {
			return nil, err
		}
		return downloadResult(map[string]any{
			"session_id": in.SessionID, "server_id": in.ServerID,
			"transport": "typed controller bounded frames",
		}, content, digest, frames), nil
	})
}

type downloadArchiveArgs struct {
	ServerID string   `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Paths    []string `json:"paths" jsonschema:"1 to 20 server-relative source paths, each at most 512 characters"`
}

type downloadStreamArgs struct {
	ServerID       string `json:"server_id" jsonschema:"exact server id the session belongs to, 1 to 128 characters"`
	SessionID      string `json:"session_id" jsonschema:"opaque download session id, 1 to 128 characters"`
	TotalSize      int64  `json:"total_size" jsonschema:"authoritative size from the session response; admission upper bound, 0 to 8 MiB"`
	ExpectedSHA256 string `json:"expected_sha256,omitempty" jsonschema:"optional 64-character hex digest checked against the transferred bytes"`
}

func downloadResult(meta map[string]any, content []byte, digest string, frames int) map[string]any {
	out := make(map[string]any, len(meta)+5)
	for k, v := range meta {
		out[k] = v
	}
	out["content"] = base64.StdEncoding.EncodeToString(content)
	out["size"] = len(content)
	out["sha256"] = digest
	out["frames"] = frames
	out["frame_bytes"] = storageworker.ChunkBytes
	return out
}

func downloadArgError(tool, field, rule string) error {
	spec := downloadSpecs[tool]
	return &ToolError{
		Kind: ErrKindInvalidArguments, Tool: tool, Procedure: spec.Procedure,
		Message: fmt.Sprintf("%s for tool %q: %s", field, tool, rule),
	}
}

func isSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func addDownloadTool[In any](s *sdkmcp.Server, name string, c *Clients,
	run func(context.Context, *Clients, In) (any, error)) {
	spec := downloadSpecs[name]
	// Production servers are policy-bound before registration: never mount a
	// tool the OSS-375 audit excluded, even momentarily. A directly constructed
	// (unbound) server keeps the complete hand-off set; the combined suite uses
	// NewServer so this production path is always exercised.
	if !toolAllowedOn(s, spec.Name) {
		return
	}
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: spec.Name, Description: spec.Description},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, ToolOutput, error) {
			result, err := run(ctx, c, in)
			if err != nil {
				return failure(spec, err)
			}
			return nil, ToolOutput{Status: "ok", Result: result}, nil
		})
}

// downloadFrameStream is the gateway-side view of one controller-owned staged
// download. It reuses the exact typed RPCs and W17 frame/SHA/offset semantics of
// the scoped_stream_* seam; it never opens a file or a gateway-local path.
type downloadFrameStream struct {
	client       carbonpanelv1connect.FileServiceClient
	serverID     string
	controllerID string
	declared     int64
	offset       int64
	nextFrame    int32
	hash         hash.Hash
	data         []byte
	done         bool
}

func (f *downloadFrameStream) open(ctx context.Context, downloadSessionID string) error {
	res, err := f.client.OpenStream(ctx, connect.NewRequest(&v1.OpenStreamRequest{
		ServerId: f.serverID, DownloadSessionId: downloadSessionID, TotalSize: f.declared,
	}))
	if err != nil {
		return err
	}
	f.controllerID = res.Msg.GetSessionId()
	return nil
}

func (f *downloadFrameStream) readFrame(ctx context.Context) (bool, error) {
	res, err := f.client.ReadStreamFrame(ctx, connect.NewRequest(&v1.ReadStreamFrameRequest{
		SessionId: f.controllerID, FrameIndex: f.nextFrame, ServerId: f.serverID,
	}))
	if err != nil {
		return false, err
	}
	frame := res.Msg.GetData()
	if int64(len(frame)) > storageworker.ChunkBytes || f.offset+int64(len(frame)) > f.declared ||
		len(f.data)+len(frame) > downloadMaxBytes {
		return false, fmt.Errorf("%w: declared=%d offset=%d frame=%d",
			ErrStreamTooLarge, f.declared, f.offset, len(frame))
	}
	f.data = append(f.data, frame...)
	_, _ = f.hash.Write(frame)
	f.offset += int64(len(frame))
	f.nextFrame++
	done := res.Msg.GetDone()
	if done {
		if res.Msg.GetOffset() != f.offset {
			return false, fmt.Errorf("%w: controller offset %d, local %d",
				ErrStreamDigestMismatch, res.Msg.GetOffset(), f.offset)
		}
		local := hex.EncodeToString(f.hash.Sum(nil))
		if res.Msg.GetSha256() != local {
			return false, fmt.Errorf("%w: controller %q, local %q", ErrStreamDigestMismatch, res.Msg.GetSha256(), local)
		}
		f.done = true
	}
	return done, nil
}

func (f *downloadFrameStream) close() {
	if f.controllerID == "" {
		return
	}
	// Cleanup is independent of a canceled tools/call context but strictly
	// bounded: a caller cancellation must not leak a controller-owned handle.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = f.client.CloseStream(ctx, connect.NewRequest(&v1.CloseStreamRequest{
		SessionId: f.controllerID, ServerId: f.serverID, Cancel: !f.done,
	}))
}

func readDownloadFrames(ctx context.Context, cl *Clients, serverID, sessionID string,
	declared int64, expectedSHA string) ([]byte, string, int, error) {
	if declared < 0 || declared > downloadMaxBytes || sessionID == "" {
		return nil, "", 0, fmt.Errorf("%w: declared=%d", ErrStreamTooLarge, declared)
	}
	f := &downloadFrameStream{
		client: cl.File, serverID: serverID, declared: declared, hash: sha256.New(),
	}
	if err := f.open(ctx, sessionID); err != nil {
		return nil, "", 0, err
	}
	defer f.close()
	frames := 0
	for {
		done, err := f.readFrame(ctx)
		if err != nil {
			return nil, "", 0, err
		}
		frames++
		if done {
			break
		}
	}
	digest := hex.EncodeToString(f.hash.Sum(nil))
	if expectedSHA != "" && !strings.EqualFold(expectedSHA, digest) {
		return nil, "", 0, fmt.Errorf("%w: expected %q, got %q", ErrStreamDigestMismatch, expectedSHA, digest)
	}
	return f.data, digest, frames, nil
}

// DownloadToolDescriptor is the machine-readable hand-off artifact for the
// integration owner. It mirrors ContentToolDescriptor's role without touching
// the shared catalog or descriptor files.
type DownloadToolDescriptor struct {
	PendingID    string   `json:"pending_id"`
	Target       string   `json:"target"`
	Operation    string   `json:"operation"`
	Tool         string   `json:"tool"`
	Procedure    string   `json:"procedure"`
	Service      string   `json:"service"`
	Method       string   `json:"method"`
	Client       string   `json:"client"`
	Scope        string   `json:"scope"`
	Class        Class    `json:"class"`
	Transport    string   `json:"transport"`
	Args         []string `json:"args"`
	ResultFields []string `json:"result_fields"`
	ComposedBy   string   `json:"composed_by,omitempty"`
	Notes        string   `json:"notes,omitempty"`
}

// DownloadToolDescriptors is the exported machine-readable descriptor set. It
// is also serialized to internal/mcp/family_download_descriptors.json for the
// integration owner; family_download_tools_test.go pins them together.
func DownloadToolDescriptors() []DownloadToolDescriptor {
	const fileService = "FileService"
	const fileClient = "FileServiceClient"
	const scope = "files/read[server_id]"
	return []DownloadToolDescriptor{
		{
			PendingID: "/carbonpanel.v1.FileService/DownloadArchive", Target: "/carbonpanel.v1.FileService/DownloadArchive",
			Operation: "download_archive", Tool: "download_archive_bundle", Procedure: "/carbonpanel.v1.FileService/DownloadArchive",
			Service: fileService, Method: "DownloadArchive", Client: fileClient, Scope: scope, Class: ClassSafe,
			Transport: "typed_rpc+typed_controller_frames",
			Args:      []string{"server_id", "paths[1..20]"},
			ResultFields: []string{"session_id", "filename", "total_size", "paths", "content", "size", "sha256", "frames", "frame_bytes"},
			Notes:         "Stages a bounded zip through the typed RPC, then streams it with OpenStream(download_session_id)/ReadStreamFrame/CloseStream. No gateway file storage.",
		},
		{
			PendingID: "/carbonpanel.v1.FileService/ExportWorld", Target: "/carbonpanel.v1.FileService/ExportWorld",
			Operation: "export_world", Tool: "download_export_world_bundle", Procedure: "/carbonpanel.v1.FileService/ExportWorld",
			Service: fileService, Method: "ExportWorld", Client: fileClient, Scope: scope, Class: ClassSafe,
			Transport: "typed_rpc+typed_controller_frames",
			Args:      []string{"server_id"},
			ResultFields: []string{"session_id", "filename", "total_size", "data_path", "content", "size", "sha256", "frames", "frame_bytes"},
			Notes:         "World directory and container stay in place. Export is staged by the controller and framed over typed RPCs; owned temp fixtures only.",
		},
		{
			PendingID: "/carbonpanel.v1.FileService/GetFile", Target: "/carbonpanel.v1.FileService/GetFile",
			Operation: "get_file", Tool: "download_get_file", Procedure: "/carbonpanel.v1.FileService/GetFile",
			Service: fileService, Method: "GetFile", Client: fileClient, Scope: scope, Class: ClassSafe,
			Transport: "typed_rpc",
			Args:      []string{"server_id", "path"},
			ResultFields: []string{"server_id", "path", "content", "size", "sha256", "mime_type"},
			Notes:         "Bounded small-file read (controller 8 MiB cap). Returns exact byte count and SHA-256 of the typed response content.",
		},
		{
			PendingID: "/carbonpanel.v1.FileService/InitFileDownload", Target: "/carbonpanel.v1.FileService/InitFileDownload",
			Operation: "init_file_download", Tool: "download_init_file", Procedure: "/carbonpanel.v1.FileService/InitFileDownload",
			Service: fileService, Method: "InitFileDownload", Client: fileClient, Scope: scope, Class: ClassSafe,
			Transport: "typed_rpc+typed_controller_frames",
			Args:      []string{"server_id", "path"},
			ResultFields: []string{"session_id", "filename", "total_size", "content", "size", "sha256", "frames", "frame_bytes"},
			Notes:         "Single-file session followed by the same bounded frame path as the archive/world bundles.",
		},
		{
			PendingID: "surface:rest_download_stream", Target: "/carbonpanel.v1.FileService/GetFile",
			Operation: "rest_download_stream", Tool: "download_stream", Procedure: "/carbonpanel.v1.FileService/GetFile",
			Service: fileService, Method: "OpenStream/ReadStreamFrame/CloseStream", Client: fileClient, Scope: scope, Class: ClassSafe,
			Transport: "typed_controller_frames",
			Args:      []string{"server_id", "session_id", "total_size", "expected_sha256?"},
			ResultFields: []string{"session_id", "server_id", "transport", "content", "size", "sha256", "frames", "frame_bytes"},
			ComposedBy:    "init_file_download / download_archive / export_world -> download_stream",
			Notes:         "The audited REST surface GET /api/v1/download/{sessionId} is represented in MCP by typed bounded-frame RPCs, not hand-rolled HTTP. Shares the GetFile procedure with download_get_file in the generated catalog.",
		},
	}
}
