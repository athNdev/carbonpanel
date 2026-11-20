// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestServerRejectsModrinthProjectWithUnsupportedServerSide(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	ctx := ctxWithRole("operator")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"hits":[{"project_id":"client","slug":"client","title":"Rendering Pack","server_side":"unsupported","versions":["1.21.1"]}],"total_hits":1}`)
	}))
	defer provider.Close()
	originalURL := modrinth.BaseURL
	modrinth.BaseURL = provider.URL
	defer func() { modrinth.BaseURL = originalURL }()
	library := NewModpackService(svc.store, svc.config, nil, svc.log)
	synced, err := library.SyncModpacks(ctx, connect.NewRequest(&v1.SyncModpacksRequest{Indexer: "modrinth", Query: "client"}))
	if err != nil || synced.Msg.SyncedCount != 1 {
		t.Fatalf("sync = %v, %v", synced, err)
	}
	pack, err := svc.store.GetIndexedModpack(ctx, "modrinth-client")
	if err != nil {
		t.Fatal(err)
	}

	_, err = svc.CreateServer(ctx, connect.NewRequest(&v1.CreateServerRequest{Name: "rendering", ModpackId: pack.ID, NodeId: "test-node", Port: 25584, DockerImage: "java21", McVersion: "1.21.1"}))
	assertUnsupported := func(err error) {
		t.Helper()
		if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), pack.Name) || !strings.Contains(err.Error(), "unsupported on dedicated servers") {
			t.Fatalf("expected actionable dedicated-server rejection, got %v", err)
		}
	}
	assertUnsupported(err)
	var servers int64
	if err := svc.store.DB().Model(&db.Server{}).Count(&servers).Error; err != nil || servers != 0 {
		t.Fatalf("server count after rejection = %d, err = %v", servers, err)
	}

	server := seedModBrowseServer(t, svc.store)
	initial := "1G"
	config := svc.store.CreateDefaultServerConfig(server.ID)
	config.MaxMemory = &initial
	if err := svc.store.SaveServerConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	_, err = svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{Id: server.ID, Name: "changed", ModpackId: pack.ID, Memory: 6144}))
	assertUnsupported(err)
	saved, err := svc.store.GetServer(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	config, err = svc.store.GetServerConfig(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxMemory == nil || *config.MaxMemory != initial {
		t.Fatalf("rejected update changed the existing server memory configuration")
	}
	if saved.Name != server.Name || saved.Memory != server.Memory || saved.ModLoader != server.ModLoader {
		t.Fatalf("rejected update changed existing server: %+v", saved)
	}
}

func TestCreateServerAllowsModrinthProjectWithServerSupport(t *testing.T) {
	for _, side := range []string{"", "required", "optional", "unknown"} {
		t.Run("side_"+side, func(t *testing.T) {
			svc, _ := newDockerPrivilegeTestService(t)
			seedMemoryModpack(t, svc, 6144)
			if err := svc.store.DB().Model(&db.IndexedModpack{}).Where("id = ?", "modrinth-memory").Update("server_side", side).Error; err != nil {
				t.Fatal(err)
			}
			createModpackServer(t, svc, 25585, 2048)
		})
	}
}
