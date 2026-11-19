// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): exact PENDING inventory.
//
// The nine operations that used to be held here are now mounted in production
// by registerCombinedTools and carry real SDK stdio tools/call evidence. The
// hold is removed deliberately, not waived: adding a newly declared but
// unmounted operation back to PendingInventory immediately turns the STRICT
// gate red again (pendingReason is derived from this list).
//
// Evidence for the retired holds, by target procedure and tool:
//
//   - /carbonpanel.v1.FileService/GetFile -> download_get_file and
//     download_stream (composed rest_download_stream byte path).
//   - /carbonpanel.v1.FileService/InitFileDownload -> download_init_file;
//     /carbonpanel.v1.FileService/DownloadArchive -> download_archive_bundle;
//     /carbonpanel.v1.FileService/ExportWorld -> download_export_world_bundle.
//   - /carbonpanel.v1.FileService/SaveUploadedFile ->
//     file_service_save_uploaded_file and rest_upload_stream_commit (composed
//     rest_upload_stream byte path).
//   - /carbonpanel.v1.UploadService/InitUpload -> upload_service_init_upload;
//     /carbonpanel.v1.UploadService/UploadChunk -> upload_service_upload_chunk.
//
// The shared invocation harness proves the typed RPC, argument mapping,
// outcome and scope signals through the canonical catalog tool for each
// procedure (get_file, export_world, save_uploaded_file, ...). The newly
// mounted byte-transport tools are additionally driven end to end by their
// own production-NewServer stdio fixtures (family_download_tools_test.go,
// family_upload_tools_test.go) and by integration_byte_path_mount_test.go;
// both layers are required for the retired holds to be honest.
//
// Every tool reaches the controller only through a generated Connect client
// behind the production auth interceptor. Bytes move through the controller-
// owned bounded frame RPCs; the gateway holds no storage handle and never
// hand-rolls the REST download/upload surfaces. See
// knowledge/decisions/mcp-download-byte-transport.md and
// knowledge/decisions/mcp-upload-commit-family.md.
package mcp

// PendingOperationCount is the authoritative, reviewable size of the pending
// set. It is zero: the byte-transport lanes are merged and verified.
const PendingOperationCount = 0

// PendingOperation is one operation with no production authorization/
// transport path yet. No operation currently qualifies.
type PendingOperation struct {
	ID       string
	Target   string
	Reason   string
	FollowUp string
}

// PendingInventory is the exact, reviewable list of operations that are not
// VERIFIED. It is empty; see the evidence inventory in the package comment.
var PendingInventory = []PendingOperation{}
