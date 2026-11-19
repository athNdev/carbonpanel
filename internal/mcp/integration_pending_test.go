// SPDX-License-Identifier: MPL-2.0
package mcp

import (
	"reflect"
	"testing"
)

// retiredPendingIDs are the nine operations that used to be held by the STRICT
// gate. They are listed here so the completion test below is exact: an ID must
// not silently disappear from the audit when the hold is lifted.
var retiredPendingIDs = []string{
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

// TestPendingInventoryAuthoritativeCount pins the ONE agreed pending-set count
// to the committed source of truth. The count is now zero because every
// formerly pending operation has real production tools/call evidence. Keeping
// the exact retired list here prevents the historical "four vs nine" confusion
// from hiding a silently dropped audit row.
func TestPendingInventoryAuthoritativeCount(t *testing.T) {
	if PendingOperationCount != 0 || PendingOperationCount != len(PendingInventory) {
		t.Fatalf("authoritative pending count = %d (%d entries), want 0/0",
			PendingOperationCount, len(PendingInventory))
	}
	got := make([]string, 0, len(PendingInventory))
	for _, op := range PendingInventory {
		got = append(got, op.ID)
	}
	if !reflect.DeepEqual(got, []string{}) {
		t.Fatalf("pending operation list is not empty: %v", got)
	}

	// Every retired ID still exists in the audit matrix and is no longer held.
	byID := map[string]FamilyAuditOperation{}
	for _, op := range FamilyAudit().Operations {
		byID[op.ID] = op
		// The retired RPC IDs were procedure strings, not "rpc:"-prefixed IDs.
		byID[op.Procedure] = op
	}
	for _, id := range retiredPendingIDs {
		op, ok := byID[id]
		if !ok {
			t.Errorf("%s: retired operation is missing from the audit matrix", id)
			continue
		}
		if !op.Supported() {
			t.Errorf("%s: retired operation is no longer SUPPORTED", id)
		}
		if reason := pendingReason(op); reason != "" {
			t.Errorf("%s: retired operation is still held: %s", id, reason)
		}
	}
}
