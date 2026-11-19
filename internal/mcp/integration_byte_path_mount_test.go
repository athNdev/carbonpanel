// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370/371-375): production mounting evidence.
//
// The byte-transport lanes used to be registered only by isolated fixtures.
// This test goes through the real NewServer wiring and the SDK stdio wire so a
// future refactor cannot silently drop a tool while the family-slice fixtures
// keep passing against their own server.
package mcp

import (
	"context"
	"testing"
)

// bytePathToolNames are the nine tools that close the nine retired PENDING
// operations. Five are the download byte path (the last two share GetFile's
// procedure) and four are the upload/commit path (the last one composes the
// bounded stream into SaveUploadedFile).
var bytePathToolNames = []string{
	"download_get_file",
	"download_init_file",
	"download_archive_bundle",
	"download_export_world_bundle",
	"download_stream",
	UploadToolSaveUploadedFile,
	UploadToolInitUpload,
	UploadToolUploadChunk,
	UploadToolStreamCommit,
}

func TestBytePathToolsMountedInProduction(t *testing.T) {
	f := newFixture(t, "", true)
	listed, err := f.sessions["admin"].ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	mounted := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		if mounted[tool.Name] {
			t.Fatalf("production tools/list mounted %s twice", tool.Name)
		}
		mounted[tool.Name] = true
	}
	for _, name := range bytePathToolNames {
		if !mounted[name] {
			t.Errorf("byte-transport tool %s is not mounted by production NewServer", name)
		}
		spec, ok := declaredToolSpec(name)
		if !ok {
			t.Errorf("byte-transport tool %s has no generated catalog declaration", name)
			continue
		}
		if _, ok := DeclaredProcedure(spec.Procedure); !ok {
			t.Errorf("byte-transport tool %s declares unmounted procedure %s", name, spec.Procedure)
		}
	}

	// A real tools/call over the stdio wire proves the newly mounted tools are
	// callable through the production server, not merely advertised. The
	// dedicated family fixtures cover the staged byte path, caps, digests,
	// scope denials and cross-owner refusals with a live FileService.
	for name, args := range map[string]map[string]any{
		"download_get_file": {"server_id": "srv-1", "path": "config.yml"},
		UploadToolSaveUploadedFile: {
			"server_id": "srv-1", "upload_session_id": "up-1",
			"destination_path": "mods", "filename": "mount.jar", "confirm": true,
		},
		UploadToolInitUpload: {
			"filename": "mount.jar", "total_size": 16, "chunk_size": 262144, "confirm": true,
		},
	} {
		out := callTool(t, f.sessions["admin"], name, args)
		assertSuccess(t, out, name)
	}
}

func declaredToolSpec(name string) (ToolSpec, bool) {
	for _, spec := range exposedTools {
		if spec.Name == name {
			return spec, true
		}
	}
	return ToolSpec{}, false
}
