// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): registered, authenticated entry
// points for the scoped streaming transfer.
//
// The owner of a session is derived from the authenticated dp_ Bearer token
// that NewClients stores, never from a tool argument. Scope is enforced by the
// typed FileService RPCs, which travel through the production auth
// interceptor. Raw byte channels remain out of the MCP tool catalog: these
// tools return bounded frames and a cumulative SHA-256, and commit only to a
// controlled server-relative destination.
package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"io"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type streamOpenArgs struct {
	ServerID  string `json:"server_id" jsonschema:"exact server id, 1 to 128 characters"`
	Path      string `json:"path" jsonschema:"server-relative source path, 1 to 512 characters"`
	TotalSize int64  `json:"total_size" jsonschema:"declared byte size, bounded by the transfer cap"`
}

type streamChunkArgs struct {
	SessionHandle string `json:"session_handle" jsonschema:"opaque session handle returned by scoped_stream_open"`
	Confirm       bool   `json:"confirm,omitempty" jsonschema:"must be true for a mutation"`
}

func (a streamChunkArgs) Confirmation() bool { return a.Confirm }

type streamCommitArgs struct {
	SessionHandle string `json:"session_handle" jsonschema:"opaque session handle returned by scoped_stream_open"`
	Destination   string `json:"destination" jsonschema:"controlled server-relative destination path"`
	Filename      string `json:"filename" jsonschema:"target filename, at most 128 characters"`
	Confirm       bool   `json:"confirm,omitempty" jsonschema:"must be true to commit bytes"`
}

func (a streamCommitArgs) Confirmation() bool { return a.Confirm }

// ErrStreamUnavailable is surfaced only if a client was built without the
// controller-owned stream registry (the production constructor always builds
// one; see NewClients). It is not a storage-capability error: the gateway
// never owns panel/node storage.
var ErrStreamUnavailable = errors.New("scoped streaming controller client is not configured")

// RegisterStreamingTools mounts the bounded scoped-transfer workflow. Every
// call is authenticated by the same dp_ token all other tools present.
func RegisterStreamingTools(s *sdkmcp.Server, c *Clients) {
	addTool(s, "scoped_stream_open", c, func(ctx context.Context, cl *Clients, in streamOpenArgs) (any, error) {
		transfer, err := cl.streamTransfer()
		if err != nil {
			return nil, err
		}
		if err := requireID("scoped_stream_open", "server_id", in.ServerID); err != nil {
			return nil, err
		}
		if err := validatePath("scoped_stream_open", in.Path, true); err != nil {
			return nil, err
		}
		// The typed InitFileDownload RPC is the scoped, auth-intercepted
		// authority to open a download of this server-relative path.
		if _, err := cl.File.InitFileDownload(ctx, connect.NewRequest(&v1.InitFileDownloadRequest{
			ServerId: in.ServerID, Path: in.Path,
		})); err != nil {
			return nil, err
		}
		handle, err := transfer.OpenWithSize(ctx, cl.owner, in.ServerID, in.Path, in.TotalSize)
		if err != nil {
			return nil, err
		}
		// total_size is an admission UPPER BOUND; the authoritative final byte
		// count is the offset on the final frame (see ReadStreamFrameResponse).
		return map[string]any{"session_handle": handle, "server_id": in.ServerID,
			"size_upper_bound": in.TotalSize}, nil
	})

	addTool(s, "scoped_stream_chunk", c, func(ctx context.Context, cl *Clients, in streamChunkArgs) (any, error) {
		transfer, err := cl.streamTransfer()
		if err != nil {
			return nil, err
		}
		if err := requireID("scoped_stream_chunk", "session_handle", in.SessionHandle); err != nil {
			return nil, err
		}
		if _, err := transfer.Lookup(cl.owner, in.SessionHandle); err != nil {
			return nil, err
		}
		// Authorization is re-evaluated by the controller auth interceptor on
		// EVERY frame (files/read[server_id]); a revoked scope is refused with
		// zero bytes. No whole-file reread is used: the controller owns the
		// read handle and advances its offset. The tools/call request deadline
		// and cancellation are passed through to the typed frame RPC.
		chunk, digest, offset, readErr := transfer.ReadChunk(ctx, cl.owner, in.SessionHandle)
		done := errors.Is(readErr, io.EOF)
		if readErr != nil && !done {
			return nil, readErr
		}
		return map[string]any{
			"data": base64.StdEncoding.EncodeToString(chunk),
			"sha256": digest, "done": done, "offset": offset,
		}, nil
	})

	addTool(s, "scoped_stream_cancel", c, func(ctx context.Context, cl *Clients, in streamChunkArgs) (any, error) {
		transfer, err := cl.streamTransfer()
		if err != nil {
			return nil, err
		}
		if err := requireID("scoped_stream_cancel", "session_handle", in.SessionHandle); err != nil {
			return nil, err
		}
		if _, err := transfer.Lookup(cl.owner, in.SessionHandle); err != nil {
			return nil, err
		}
		if err := transfer.Cancel(ctx, cl.owner, in.SessionHandle); err != nil {
			return nil, err
		}
		return map[string]any{"canceled": true}, nil
	})

	addTool(s, "scoped_stream_commit", c, func(ctx context.Context, cl *Clients, in streamCommitArgs) (any, error) {
		transfer, err := cl.streamTransfer()
		if err != nil {
			return nil, err
		}
		if err := requireID("scoped_stream_commit", "session_handle", in.SessionHandle); err != nil {
			return nil, err
		}
		if err := validatePath("scoped_stream_commit", in.Destination, true); err != nil {
			return nil, err
		}
		session, err := transfer.Lookup(cl.owner, in.SessionHandle)
		if err != nil {
			return nil, err
		}
		if err := requireID("scoped_stream_commit", "filename", in.Filename); err != nil {
			return nil, err
		}
		// ClaimCommit is the one-at-a-time lease: it pins gateway memory for the
		// whole upload and refuses a parallel commit of the same handle before
		// any mutation. FinishCommit always runs exactly once via defer.
		data, digest, err := transfer.ClaimCommit(cl.owner, in.SessionHandle)
		if err != nil {
			return nil, err
		}
		committed := false
		defer func() { transfer.FinishCommit(cl.owner, in.SessionHandle, committed) }()
		// The declared size is only a bound. A zero-byte stream has nothing to
		// commit; refuse explicitly instead of presenting a false success.
		if len(data) == 0 {
			return nil, invalidArgument("scoped_stream_commit", "data",
				"stream produced zero bytes; nothing to commit")
		}
		// Commit through the AUTHORIZED PRODUCTION UPLOAD PATH, never through a
		// direct helper write. Every RPC below carries the same dp_ Bearer
		// token through the controller's auth interceptor: InitUpload requires
		// uploads/create, UploadChunk requires ownership of that session, and
		// SaveUploadedFile requires fresh files:create[server_id]. A read-only
		// or revoked principal is refused here with zero controller writes.
		uploadCtx := ctx
		init, err := cl.Upload.InitUpload(uploadCtx, connect.NewRequest(&v1.InitUploadRequest{
			Filename: in.Filename, TotalSize: int64(len(data)), ChunkSize: storageworker.ChunkBytes,
		}))
		if err != nil {
			return nil, err
		}
		sessionID := init.Msg.GetSessionId()
		for i := 0; i < len(data); i += storageworker.ChunkBytes {
			end := i + storageworker.ChunkBytes
			if end > len(data) {
				end = len(data)
			}
			if _, err := cl.Upload.UploadChunk(uploadCtx, connect.NewRequest(&v1.UploadChunkRequest{
				SessionId: sessionID,
				ChunkIndex: int32(i / storageworker.ChunkBytes),
				Data: data[i:end],
			})); err != nil {
				return nil, err
			}
		}
		if _, err := cl.File.SaveUploadedFile(uploadCtx, connect.NewRequest(&v1.SaveUploadedFileRequest{
			ServerId: session.ServerID(),
			UploadSessionId: sessionID,
			DestinationPath: in.Destination,
			Filename: in.Filename,
		})); err != nil {
			return nil, err
		}
		// A successful SaveUploadedFile marks the lease successful; the deferred
		// FinishCommit then frees the pinned budget exactly once.
		committed = true
		// len(data) is the ACTUAL transferred byte count (the final frame's
		// offset), never the declared upper bound returned by scoped_stream_open.
		return map[string]any{"bytes": len(data), "sha256": digest, "destination": in.Destination,
			"upload_session": sessionID, "actual_size": len(data),
			"authorization": "production typed upload + server-scoped save"}, nil
	})
}

func (c *Clients) streamTransfer() (*StreamTransfer, error) {
	if c.transfer == nil {
		return nil, ErrStreamUnavailable
	}
	return c.transfer, nil
}

func StreamToolSpecs() []ToolSpec {
	return []ToolSpec{
		{Name: "scoped_stream_open", Procedure: "/carbonpanel.v1.FileService/InitFileDownload", Class: ClassSafe,
			Description: "Open a bounded, authenticated scoped download session. total_size is an admission upper bound; the final frame reports the actual byte count. The owner is derived from the presented dp_ token."},
		{Name: "scoped_stream_chunk", Procedure: "/carbonpanel.v1.FileService/GetFile", Class: ClassSafe,
			Description: "Read one bounded base64 frame and cumulative SHA-256 from an owned session. On done=true, offset is the authoritative actual byte count (not the declared bound)."},
		{Name: "scoped_stream_cancel", Procedure: "/carbonpanel.v1.FileService/GetFile", Class: ClassReview, Confirm: true,
			Description: "Cancel an owned streaming session mid-transfer. Requires confirm=true."},
		{Name: "scoped_stream_commit", Procedure: "/carbonpanel.v1.FileService/SaveUploadedFile", Class: ClassReview, Confirm: true,
			Description: "Commit transferred bytes to one controlled server-relative destination. Requires confirm=true."},
	}
}
