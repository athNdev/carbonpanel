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
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func TestDeleteServerKeepDataDoesNotPurgeSidecarContainers(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	ctx := context.Background()

	var mu sync.Mutex
	var deleted []string
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/") {
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"refusing sidecar purge"}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(dockerAPI.Close)
	cli, err := docker.NewClient("tcp://"+strings.TrimPrefix(dockerAPI.URL, "http://"), logger.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if svc.docker != nil {
		_ = svc.docker.Close()
	}
	svc.docker = cli
	svc.moduleManager = module.NewManager(svc.store, cli, nil, nil, nil, logger.New())

	dataPath := filepath.Join(t.TempDir(), "world")
	sidecarMarker := writeServerMarker(t, filepath.Join(dataPath, "sidecar"))
	server := &db.Server{
		ID: "keep-sidecar", Name: "Keep Sidecar", NodeID: "test-node",
		MCVersion: "1.20.1", ModLoader: db.ModLoaderVanilla,
		Port: 25601, Memory: 2048, Status: db.StatusStopped, DataPath: dataPath,
	}
	if err := svc.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.DB().Create(&db.Module{
		ID: "mod-keep", Name: "map", ServerID: server.ID, TemplateID: "tpl",
		NodeID: server.NodeID, ContainerID: "sidecar-cid", Status: db.ModuleStatusRunning,
		DataPath: filepath.Join(dataPath, "sidecar"),
	}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := svc.DeleteServer(ctx, connect.NewRequest(&v1.DeleteServerRequest{
		Id: server.ID, KeepData: true,
	})); err != nil {
		t.Fatalf("keep_data delete: %v", err)
	}
	mu.Lock()
	got := append([]string(nil), deleted...)
	mu.Unlock()
	for _, path := range got {
		if strings.Contains(path, "sidecar-cid") {
			t.Fatalf("keep_data deleted sidecar container: %s", path)
		}
	}
	if _, err := os.Stat(sidecarMarker); err != nil {
		t.Fatalf("sidecar data stat = %v", err)
	}
	if _, err := svc.store.GetModule(ctx, "mod-keep"); err == nil {
		t.Fatal("module row still exists after keep_data")
	}
}

func TestDeleteServerPurgesSidecarContainers(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	ctx := context.Background()

	var mu sync.Mutex
	deletedSidecar := false
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/sidecar-cid") {
			mu.Lock()
			deletedSidecar = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/containers/json") || strings.Contains(r.URL.Path, "/stop") {
			w.WriteHeader(http.StatusNoContent)
			if strings.HasSuffix(r.URL.Path, "/containers/json") {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte("[]"))
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(dockerAPI.Close)
	cli, err := docker.NewClient("tcp://"+strings.TrimPrefix(dockerAPI.URL, "http://"), logger.New())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if svc.docker != nil {
		_ = svc.docker.Close()
	}
	svc.docker = cli
	svc.moduleManager = module.NewManager(svc.store, cli, nil, nil, nil, logger.New())

	dataPath := filepath.Join(t.TempDir(), "world")
	_ = writeServerMarker(t, dataPath)
	server := &db.Server{
		ID: "purge-sidecar", Name: "Purge Sidecar", NodeID: "test-node",
		MCVersion: "1.20.1", ModLoader: db.ModLoaderVanilla,
		Port: 25602, Memory: 2048, Status: db.StatusStopped, DataPath: dataPath,
	}
	if err := svc.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.DB().Create(&db.Module{
		ID: "mod-purge", Name: "map", ServerID: server.ID, TemplateID: "tpl",
		NodeID: server.NodeID, ContainerID: "sidecar-cid", Status: db.ModuleStatusStopped,
	}).Error; err != nil {
		t.Fatal(err)
	}

	if _, err := svc.DeleteServer(ctx, connect.NewRequest(&v1.DeleteServerRequest{Id: server.ID})); err != nil {
		t.Fatalf("delete: %v", err)
	}
	mu.Lock()
	ok := deletedSidecar
	mu.Unlock()
	if !ok {
		t.Fatal("full delete did not remove the sidecar container")
	}
	if _, err := svc.store.GetModule(ctx, "mod-purge"); err == nil {
		t.Fatal("module row still exists after full delete")
	}
}
