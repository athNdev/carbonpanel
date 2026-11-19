// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): exact PENDING inventory.
//
// OSS-387 implemented the authenticated controller-side bounded-frame
// transport: the four scoped_stream_* tools are advertised from the production
// NewServer wiring and move bytes only through typed, auth-intercepted
// controller RPCs. Panel/node storage is still NOT shared with the gateway.
//
// These nine operations stay PENDING in the STRICT publication gate until each
// has real production tools/call evidence in the shared harness. The transport
// is genuine (see integration_stream_entrypoint_test.go), but the strict gate
// is deliberately left RED rather than waived; no enabled-but-unusable tools
// are advertised.
//
// COUNT CLARITY (reviewed at exact head 0752403b318dcdbc4d8e62a3aef8ff7dd020ce23):
// PendingOperationCount is the PENDING-OPERATION count and is 9. The "four"
// elsewhere in this file and in architecture/mcp-server.md refers only to the
// number of MOUNTED scoped_stream_* tools, never to pending operations. The
// number 4 also appears as VerifiedSurfaces in the STRICT gate's matrix-baseline
// log; that is a verified-surface count. Neither is a pending count. The
// STRICT gate at this head reports exactly 9 gap lines, matching this list.
package mcp

// PendingOperationCount is the authoritative, reviewable size of the pending
// set. Keep it in sync with PendingInventory; integration_pending_test.go
// pins both the count and the exact operation IDs.
const PendingOperationCount = 9

// PendingOperation is one operation with no production authorization/
// transport path yet.
type PendingOperation struct {
	ID      string
	Target  string
	Reason  string
	FollowUp string
}

// PendingInventory is the exact, reviewable list of operations that are not
// VERIFIED. See docs/plans/mcp-controller-side-stream-transport.md.
var PendingInventory = []PendingOperation{
	{"/carbonpanel.v1.FileService/DownloadArchive", "/carbonpanel.v1.FileService/DownloadArchive",
		"no authenticated controller-side streaming transport", "OSS-370 follow-up: controller-side stream RPC"},
	{"/carbonpanel.v1.FileService/ExportWorld", "/carbonpanel.v1.FileService/ExportWorld",
		"no authenticated controller-side streaming transport", "OSS-370 follow-up: controller-side stream RPC"},
	{"/carbonpanel.v1.FileService/GetFile", "/carbonpanel.v1.FileService/GetFile",
		"bounded small-file read only; no session-consuming large transfer", "OSS-370 follow-up: bounded frame RPC"},
	{"/carbonpanel.v1.FileService/InitFileDownload", "/carbonpanel.v1.FileService/InitFileDownload",
		"no authenticated controller-side streaming transport", "OSS-370 follow-up: controller-owned session handle"},
	{"/carbonpanel.v1.FileService/SaveUploadedFile", "/carbonpanel.v1.FileService/SaveUploadedFile",
		"no gate-owned authorized upload/commit transport", "OSS-370 follow-up: fresh server-scoped write auth"},
	{"/carbonpanel.v1.UploadService/InitUpload", "/carbonpanel.v1.FileService/SaveUploadedFile",
		"no gate-owned authorized upload/commit transport", "OSS-370 follow-up: controller-side upload session"},
	{"/carbonpanel.v1.UploadService/UploadChunk", "/carbonpanel.v1.FileService/SaveUploadedFile",
		"no gate-owned authorized upload/commit transport", "OSS-370 follow-up: bounded frame RPC"},
	{"surface:rest_upload_stream", "/carbonpanel.v1.FileService/SaveUploadedFile",
		"no gate-owned authorized upload/commit transport", "OSS-370 follow-up: composed byte path"},
	{"surface:rest_download_stream", "/carbonpanel.v1.FileService/GetFile",
		"no authenticated controller-side streaming transport", "OSS-370 follow-up: composed byte path"},
}
