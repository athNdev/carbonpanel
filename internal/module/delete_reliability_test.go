// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.


package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

func TestDeleteModule_DockerRemovalFailurePreservesDatabaseRow(t *testing.T) {
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/container-module") {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"daemon unavailable"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer dockerAPI.Close()

	store := setupTestStore(t)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	dockerClient, err := docker.NewClient(strings.Replace(dockerAPI.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := dockerClient.Close(); err != nil {
			t.Errorf("close Docker client: %v", err)
		}
	})
	manager := NewManager(store, dockerClient, nil, nil, nil, logger.New())
	module := &storage.Module{
		ID: "module-delete-failure", Name: "delete-failure", ServerID: "server-delete-failure",
		NodeID: "local", ContainerID: "container-module", Status: storage.ModuleStatusStopped,
	}
	if err := store.DB().Create(module).Error; err != nil {
		t.Fatalf("create module fixture: %v", err)
	}

	if err := manager.DeleteModule(context.Background(), module.ID); err == nil {
		t.Fatal("DeleteModule error = nil when Docker removal failed")
	}
	var persisted storage.Module
	if err := store.DB().First(&persisted, "id = ?", module.ID).Error; err != nil {
		t.Fatalf("module row was deleted despite Docker removal failure: %v", err)
	}
}

func TestForgetModuleDoesNotRemoveTheContainer(t *testing.T) {
	removed := false
	dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/") {
			removed = true
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"would purge volumes"}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer dockerAPI.Close()

	store := setupTestStore(t)
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	dockerClient, err := docker.NewClient(strings.Replace(dockerAPI.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := dockerClient.Close(); err != nil {
			t.Errorf("close Docker client: %v", err)
		}
	})
	manager := NewManager(store, dockerClient, nil, nil, nil, logger.New())
	module := &storage.Module{
		ID: "module-forget", Name: "forget", ServerID: "server-forget",
		NodeID: "local", ContainerID: "container-sidecar", Status: storage.ModuleStatusRunning,
	}
	if err := store.DB().Create(module).Error; err != nil {
		t.Fatalf("create module fixture: %v", err)
	}

	if err := manager.ForgetModule(context.Background(), module.ID); err != nil {
		t.Fatalf("ForgetModule: %v", err)
	}
	if removed {
		t.Fatal("ForgetModule must not DELETE the sidecar container")
	}
	var persisted storage.Module
	if err := store.DB().First(&persisted, "id = ?", module.ID).Error; err == nil {
		t.Fatal("module row still exists after ForgetModule")
	}
}
