// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestImportRemoteModpackCallerFlagCannotUnlockPrivateEgress pins the B-07
// boundary at the public RPC seam. A caller-supplied allow_private_network
// must not be able to reach RFC1918 targets, even on an installation whose
// operator enabled the archive flag: a controller-scoped library import has no
// named destination node to bind that flag to (ADR 0043).
func TestImportRemoteModpackCallerFlagCannotUnlockPrivateEgress(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	cfg := &config.Config{
		Storage:  config.StorageConfig{DataDir: t.TempDir()},
		Security: config.SecurityConfig{AllowPrivateArchiveEgress: true},
	}
	svc := NewModpackService(store, cfg, nil, logger.New())

	req := &v1.ImportRemoteModpackRequest{
		Url:                 "http://10.0.0.5/pack.zip",
		Name:                "Private Pack",
		AllowPrivateNetwork: true,
	}
	_, err := svc.ImportRemoteModpack(context.Background(), connect.NewRequest(req))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "archive egress policy blocked")
	assert.Contains(t, err.Error(), "private network address")
}

// TestImportRemoteModpackRedactsURLCredentials verifies that neither the
// client-facing error nor the emitted log line reflects credentials embedded
// in a rejected remote modpack URL.
func TestImportRemoteModpackRedactsURLCredentials(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	var logOutput bytes.Buffer
	capturingLog := &logger.Logger{Logger: log.New(&logOutput, "", 0)}
	svc := NewModpackService(store, &config.Config{Storage: config.StorageConfig{DataDir: t.TempDir()}}, nil, capturingLog)

	const secret = "s3cr3t-user"
	req := &v1.ImportRemoteModpackRequest{
		Url:  "http://" + secret + "@127.0.0.1:1/pack.zip?token=s3cr3t-token",
		Name: "Credentialed Pack",
	}
	_, err := svc.ImportRemoteModpack(context.Background(), connect.NewRequest(req))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain credentials")
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, err.Error(), "s3cr3t-token")
	assert.NotContains(t, logOutput.String(), secret)
	assert.NotContains(t, logOutput.String(), "s3cr3t-token")
}

// TestImportRemoteModpackFailedDownloadDoesNotLogToken exercises the
// client.Do failure seam: net/http's *url.Error embeds the full URL, so
// logging it verbatim leaks a query signature even though the client-facing
// error is redacted. "sig" is chosen deliberately because the logger's general
// SanitizeLogMessage does not cover it while RedactURL does.
func TestImportRemoteModpackFailedDownloadDoesNotLogToken(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	var logOutput bytes.Buffer
	capturingLog := &logger.Logger{Logger: log.New(&logOutput, "", 0)}
	svc := NewModpackService(store, &config.Config{Storage: config.StorageConfig{DataDir: t.TempDir()}}, nil, capturingLog)

	const secret = "s3cr3t-download-token"
	req := &v1.ImportRemoteModpackRequest{
		Url:  "http://127.0.0.1:1/pack.zip?sig=" + secret,
		Name: "Failing Pack",
	}
	_, err := svc.ImportRemoteModpack(context.Background(), connect.NewRequest(req))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to download modpack")
	assert.NotContains(t, err.Error(), secret)
	assert.NotContains(t, logOutput.String(), secret)
}

// TestImportRemoteModpackLoopbackFixtureStillWorks keeps the documented
// httptest seam working without any caller override: the test-only loopback
// opt-in is the only latitude needed, so removing allow_private_network does
// not break the ordinary import flow.
func TestImportRemoteModpackLoopbackFixtureStillWorks(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })

	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	f, err := zw.Create("manifest.json")
	require.NoError(t, err)
	_, err = f.Write([]byte(`{"name":"Loopback Pack","version":"1.0.0","author":"tester","minecraft":{"version":"1.20.1","modLoaders":[{"id":"fabric-0.14","primary":true}]}}`))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	zipBytes := zipBuf.Bytes()
	expectedHash := sha256.Sum256(zipBytes)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(zipBytes)
	}))
	defer ts.Close()

	svc := NewModpackService(store, &config.Config{Storage: config.StorageConfig{DataDir: t.TempDir()}}, nil, logger.New())
	resp, err := svc.ImportRemoteModpack(context.Background(), connect.NewRequest(&v1.ImportRemoteModpackRequest{
		Url:  ts.URL + "/pack.zip",
		Name: "Loopback Pack",
	}))
	require.NoError(t, err)
	assert.Equal(t, hex.EncodeToString(expectedHash[:]), resp.Msg.Sha256)
	assert.False(t, strings.Contains(resp.Msg.Message, ts.URL), "response must not reflect the raw source URL")
}
