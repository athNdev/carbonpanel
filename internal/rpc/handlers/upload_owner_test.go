// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/athNdev/carbon-panel/pkg/upload"
)

// The raw streaming endpoint must enforce the upload session's owner binding
// in addition to its existing RBAC check. A session ID alone is not a
// capability that another authenticated principal can use.
func TestUploadStreamEnforcesSessionOwner(t *testing.T) {
	authManager, enforcer := testNoAuthPolicy(t)
	mgr := upload.NewManager(t.TempDir(), time.Hour, 0, logger.New())
	t.Cleanup(mgr.Stop)
	h := NewUploadStreamHandler(mgr, authManager, enforcer, logger.New())

	foreign, err := mgr.InitSessionFor("foreign.bin", 4, 4, "another-user")
	if err != nil {
		t.Fatalf("InitSessionFor: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/upload/"+foreign.ID, bytes.NewReader([]byte("abcd"))))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-user stream status = %d, want 403; body %s", rec.Code, rec.Body.String())
	}
	got, err := mgr.GetSessionFor(foreign.ID, "another-user")
	if err != nil {
		t.Fatalf("owner lookup: %v", err)
	}
	if got.BytesReceived != 0 {
		t.Fatalf("cross-user stream wrote %d bytes", got.BytesReceived)
	}
}

// A legitimate stream into an unowned (legacy) session still works.
func TestUploadStreamAllowsLegacySession(t *testing.T) {
	authManager, enforcer := testNoAuthPolicy(t)
	mgr := upload.NewManager(t.TempDir(), time.Hour, 0, logger.New())
	t.Cleanup(mgr.Stop)
	h := NewUploadStreamHandler(mgr, authManager, enforcer, logger.New())

	legacy, err := mgr.InitSession("legacy.bin", 4, 4)
	if err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/v1/upload/"+legacy.ID, bytes.NewReader([]byte("abcd"))))
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy stream status = %d, want 200; body %s", rec.Code, rec.Body.String())
	}
	got, err := mgr.GetSession(legacy.ID)
	if err != nil {
		t.Fatalf("legacy session lookup: %v", err)
	}
	if got.BytesReceived != 4 {
		t.Fatalf("legacy BytesReceived = %d, want 4", got.BytesReceived)
	}
}
