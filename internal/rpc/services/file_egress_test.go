// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func newRemoteArchiveTestServer(t *testing.T, store *storage.Store) string {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "server")
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatal(err)
	}
	id := "srv-egress-test"
	server := &storage.Server{ID: id, Name: "Egress Test", DataPath: dataDir, NodeID: "local"}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestDownloadRemoteArchivePrivateEgressFailClosed proves the archive RPC uses
// its own policy: without the explicit operator flag a private URL is refused
// before any download operation is created.
func TestDownloadRemoteArchivePrivateEgressFailClosed(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	server := newRemoteArchiveTestServer(t, store)
	svc := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(svc.Stop)

	req := connect.NewRequest(&v1.DownloadRemoteArchiveRequest{
		ServerId:        server,
		Url:             "http://10.0.0.5/releases/pack.zip",
		DestinationPath: "imports",
	})
	_, err := svc.DownloadRemoteArchive(context.Background(), req)
	if err == nil {
		t.Fatal("private archive URL must be refused by default")
	}
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("code = %v, want invalid_argument: %v", connect.CodeOf(err), err)
	}
}

// TestDownloadRemoteArchiveDoesNotReflectCredentials verifies a URL carrying a
// secret cannot leak it through the RPC error, even before egress is decided.
func TestDownloadRemoteArchiveDoesNotReflectCredentials(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	server := newRemoteArchiveTestServer(t, store)
	svc := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(svc.Stop)

	const secret = "topsecret-token"
	req := connect.NewRequest(&v1.DownloadRemoteArchiveRequest{
		ServerId:        server,
		Url:             "https://alice:" + secret + "@example.test/pack.zip",
		DestinationPath: "imports",
	})
	_, err := svc.DownloadRemoteArchive(context.Background(), req)
	if err == nil {
		t.Fatal("URL with credentials must be refused")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "alice") {
		t.Fatalf("RPC error reflected a credential: %v", err)
	}
}
