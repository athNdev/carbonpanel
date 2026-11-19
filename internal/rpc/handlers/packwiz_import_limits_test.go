// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package handlers

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/stretchr/testify/require"
)

func importMultipartRequest(t *testing.T, filename, contents string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write([]byte(contents))
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("format", "mrpack"))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/api/v1/packwiz/packs/import", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	return req
}

// TestImportPackMultipartCap proves the request body is bounded before it is
// buffered. ParseMultipartForm's in-memory argument never bounded the upload.
func TestImportPackMultipartCap(t *testing.T) {
	original := maxImportUploadBytes
	maxImportUploadBytes = 1024
	t.Cleanup(func() { maxImportUploadBytes = original })

	manager, enforcer := testNoAuthPolicy(t)
	mgr := packwiz.NewManager(filepath.Join(t.TempDir(), "packs"), logger.New())
	h := NewPackwizHandler(mgr, nil, nil, logger.New(), manager, enforcer, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, importMultipartRequest(t, "evil.mrpack", strings.Repeat("A", 4096)))
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

// TestImportPackRequiresEnforcer keeps the import route fail-closed: an
// unauthenticated request cannot reach the bounded extractor at all.
func TestImportPackRequiresEnforcer(t *testing.T) {
	manager, _ := testNoAuthPolicy(t)
	h := NewPackwizHandler(nil, nil, nil, logger.New(), manager, nil, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, importMultipartRequest(t, "p.mrpack", "not-a-zip"))
	require.Equal(t, http.StatusForbidden, rec.Code)
}
