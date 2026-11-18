// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package mcp integration lane (OSS-370): exact PENDING inventory.
//
// Path B (no enabled-but-unusable tool advertisement): the standalone MCP
// gateway has no authenticated controller-side streaming transport. Panel/node
// storage is deliberately NOT shared with the gateway process. The previously
// prototyped scoped_stream_* tools are therefore absent from the generated
// catalog and from NewServer registration. Their operations remain LISTED and
// REACHABLE but are PENDING, not VERIFIED.
package mcp

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
