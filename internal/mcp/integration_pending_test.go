// SPDX-License-Identifier: MPL-2.0
package mcp

import (
	"reflect"
	"testing"
)

// TestPendingInventoryAuthoritativeCount pins the ONE agreed pending-set count
// and the exact operation list to the committed source of truth. It prevents
// the historical "four vs nine" confusion: four is the number of mounted
// scoped_stream_* TOOLS, never the number of PENDING OPERATIONS.
func TestPendingInventoryAuthoritativeCount(t *testing.T) {
	want := []string{
		"/carbonpanel.v1.FileService/DownloadArchive",
		"/carbonpanel.v1.FileService/ExportWorld",
		"/carbonpanel.v1.FileService/GetFile",
		"/carbonpanel.v1.FileService/InitFileDownload",
		"/carbonpanel.v1.FileService/SaveUploadedFile",
		"/carbonpanel.v1.UploadService/InitUpload",
		"/carbonpanel.v1.UploadService/UploadChunk",
		"surface:rest_upload_stream",
		"surface:rest_download_stream",
	}
	if PendingOperationCount != 9 || PendingOperationCount != len(want) {
		t.Fatalf("authoritative pending count = %d, want 9", PendingOperationCount)
	}
	if len(PendingInventory) != PendingOperationCount {
		t.Fatalf("PendingInventory has %d entries, want %d", len(PendingInventory), PendingOperationCount)
	}
	got := make([]string, 0, len(PendingInventory))
	for _, op := range PendingInventory {
		got = append(got, op.ID)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pending operation list drifted:\n got %v\nwant %v", got, want)
	}
}
