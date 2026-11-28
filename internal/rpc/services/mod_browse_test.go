// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/datawrite"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func seedModBrowseServer(t *testing.T, store *storage.Store) *storage.Server {
	t.Helper()
	server := &storage.Server{
		ID: "srv-browse", Name: "srv-browse", NodeID: "local",
		DataPath:  filepath.Join(t.TempDir(), "data"),
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.20.1",
	}
	if err := store.CreateServer(context.Background(), server); err != nil {
		t.Fatal(err)
	}
	return server
}

// TestModBrowseServiceSearchUsesSharedOperation verifies that the generated
// service maps the shared browse result (including int64 and side fields) and
// that Studio's "none" browse mode resolves without a server row.
func TestModBrowseServiceSearchUsesSharedOperation(t *testing.T) {
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("unexpected provider path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[{"project_id":"AANobbMI","slug":"sodium","title":"Sodium","description":"fast","icon_url":"https://icon/sodium.png","author":"jelly","downloads":123456,"categories":["optimization"],"client_side":"required","server_side":"optional"}],"total_hits":1,"limit":25,"offset":0}`))
	}))
	defer provider.Close()

	store := setupTestStore(t)
	manager := handlers.NewModOnlineManager(store, logger.New(), nil, nil, handlers.ModOnlineOptions{ModrinthBaseURL: provider.URL})
	svc := NewModBrowseService(store, manager)

	resp, err := svc.SearchMods(context.Background(), connect.NewRequest(&v1.SearchModsRequest{
		ServerId: "none", Query: "sodium", Platform: "modrinth",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Count != 1 || len(resp.Msg.Results) != 1 {
		t.Fatalf("unexpected response %v", resp.Msg)
	}
	got := resp.Msg.Results[0]
	if got.Id != "AANobbMI" || got.Slug != "sodium" || got.IconUrl != "https://icon/sodium.png" ||
		got.Downloads != 123456 || got.ClientSide != "required" || got.ServerSide != "optional" ||
		got.Platform != "modrinth" || len(got.Categories) != 1 || got.Categories[0] != "optimization" {
		t.Fatalf("shared result was not mapped to proto: %v", got)
	}
}

// TestModBrowseServiceInstallMatchesSharedOperation installs the same batch
// through the generated service and the shared operation, then compares the
// reported outcome and the bytes written to the mods directory.
func TestModBrowseServiceInstallMatchesSharedOperation(t *testing.T) {
	store := setupTestStore(t)
	server := seedModBrowseServer(t, store)
	download := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("jar-bytes"))
	}))
	defer download.Close()

	manager := handlers.NewModOnlineManager(store, logger.New(), nil, nil)
	svc := NewModBrowseService(store, manager)
	items := []handlers.InstallModItem{{Name: "Example", FileName: "example.jar", DownloadURL: download.URL}}

	owned, err := datawrite.AcquireExclusive(context.Background(), server.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owned.Release)
	_, err = svc.InstallMods(context.Background(), connect.NewRequest(&v1.InstallModsRequest{
		ServerId: server.ID,
		Items:    []*v1.InstallModEntry{{Name: "Example", FileName: "example.jar", DownloadUrl: download.URL}},
	}))
	if connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("owned server code = %v, want Aborted (%v)", connect.CodeOf(err), err)
	}
	if _, err := os.Stat(filepath.Join(server.DataPath, "mods")); !os.IsNotExist(err) {
		t.Fatalf("refused install touched mods directory: %v", err)
	}
	owned.Release()

	protoResp, err := svc.InstallMods(context.Background(), connect.NewRequest(&v1.InstallModsRequest{
		ServerId: server.ID,
		Items:    []*v1.InstallModEntry{{Name: "Example", FileName: "example.jar", DownloadUrl: download.URL}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	sharedResp, browseErr := manager.Install(context.Background(), server, items, handlers.ModInstallActor{})
	if browseErr != nil {
		t.Fatal(browseErr)
	}
	if !protoResp.Msg.Success || protoResp.Msg.Success != sharedResp.Success ||
		len(protoResp.Msg.Installed) != 1 || protoResp.Msg.Installed[0] != "example.jar" ||
		protoResp.Msg.Installed[0] != sharedResp.Installed[0] || protoResp.Msg.Message != sharedResp.Message {
		t.Fatalf("generated response %v differs from shared response %v", protoResp.Msg, sharedResp)
	}
	got, err := os.ReadFile(filepath.Join(server.DataPath, "mods", "example.jar"))
	if err != nil || string(got) != "jar-bytes" {
		t.Fatalf("installed jar = %q, %v", got, err)
	}
}

// TestModBrowseServiceErrorMapping proves the generated service preserves the
// shared operation's failure semantics: unknown resources, invalid input and
// per-item download failures are not collapsed into a generic success.
func TestModBrowseServiceErrorMapping(t *testing.T) {
	store := setupTestStore(t)
	seedModBrowseServer(t, store)

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer failing.Close()

	manager := handlers.NewModOnlineManager(store, logger.New(), nil, nil)
	svc := NewModBrowseService(store, manager)

	if _, err := svc.InstallMods(context.Background(), connect.NewRequest(&v1.InstallModsRequest{
		ServerId: "missing", Items: []*v1.InstallModEntry{{FileName: "a.jar", DownloadUrl: failing.URL}},
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown server code = %v, want NotFound (%v)", connect.CodeOf(err), err)
	}

	if _, err := svc.InstallMods(context.Background(), connect.NewRequest(&v1.InstallModsRequest{
		ServerId: "srv-browse",
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("empty items code = %v, want InvalidArgument (%v)", connect.CodeOf(err), err)
	}

	resp, err := svc.InstallMods(context.Background(), connect.NewRequest(&v1.InstallModsRequest{
		ServerId: "srv-browse",
		Items:    []*v1.InstallModEntry{{Name: "Broken", FileName: "broken.jar", DownloadUrl: failing.URL}},
	}))
	if err != nil {
		t.Fatalf("per-item failure should stay in the response: %v", err)
	}
	if resp.Msg.Success || len(resp.Msg.Errors) != 1 || len(resp.Msg.Installed) != 0 {
		t.Fatalf("per-item failure was not preserved: %v", resp.Msg)
	}

	if _, err := svc.GetModVersions(context.Background(), connect.NewRequest(&v1.GetModVersionsRequest{
		ServerId: "missing", Project: "sodium",
	})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unknown server versions code = %v, want NotFound (%v)", connect.CodeOf(err), err)
	}
}
