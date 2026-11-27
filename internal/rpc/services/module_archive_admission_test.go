// SPDX-License-Identifier: MPL-2.0

package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// A module create is acknowledged only after its lifecycle admission has been
// reserved. While an export owns the gate the create is refused before it can
// leave a module row whose asynchronous worker would race the archive read.
func TestCreateModuleDuringArchiveExportIsRefusedBeforePublication(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	server := &storage.Server{ID: "module-export-server", Name: "Export", NodeID: "node", Status: storage.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	template := &storage.ModuleTemplate{ID: "module-export-template", Name: "Template", DockerImage: "alpine:latest"}
	if err := store.CreateModuleTemplate(ctx, template); err != nil {
		t.Fatalf("CreateModuleTemplate: %v", err)
	}
	manager := module.NewManager(store, nil, nil, &config.Config{}, nil, logger.New())
	svc := &ModuleService{store: store, moduleManager: manager, log: logger.New()}

	fence, err := module.TryBeginServerModuleArchive(server.ID)
	if err != nil {
		t.Fatalf("archive fence: %v", err)
	}
	defer fence.Release()

	_, err = svc.CreateModule(ctx, connect.NewRequest(&v1.CreateModuleRequest{
		Name: "web", ServerId: server.ID, TemplateId: template.ID,
	}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("CreateModule code = %v, want FailedPrecondition (%v)", got, err)
	}
	modules, listErr := store.ListServerModules(ctx, server.ID)
	if listErr != nil {
		t.Fatalf("ListServerModules: %v", listErr)
	}
	if len(modules) != 0 {
		t.Fatalf("CreateModule published %d rows while an export held the gate", len(modules))
	}
}

// An update that needs a container recreate reserves the gate before it is
// acknowledged and leaves the stored configuration untouched when an export is
// already reading the server.
func TestUpdateModuleDuringArchiveExportIsRefusedBeforeWrite(t *testing.T) {
	ctx := context.Background()
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()

	server := &storage.Server{ID: "module-update-server", Name: "Export", NodeID: "node", Status: storage.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	mod := &storage.Module{
		ID: "module-update", Name: "web", ServerID: server.ID, TemplateID: "custom",
		NodeID: server.NodeID, Status: storage.ModuleStatusStopped, ContainerID: "container",
		Memory: 512,
	}
	if err := store.CreateModule(ctx, mod); err != nil {
		t.Fatalf("CreateModule: %v", err)
	}
	manager := module.NewManager(store, nil, nil, &config.Config{}, nil, logger.New())
	svc := &ModuleService{store: store, moduleManager: manager, log: logger.New()}

	fence, err := module.TryBeginServerModuleArchive(server.ID)
	if err != nil {
		t.Fatalf("archive fence: %v", err)
	}
	defer fence.Release()

	memory := int32(2048)
	_, err = svc.UpdateModule(ctx, connect.NewRequest(&v1.UpdateModuleRequest{Id: mod.ID, Memory: &memory}))
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("UpdateModule code = %v, want FailedPrecondition (%v)", got, err)
	}
	persisted, getErr := store.GetModule(ctx, mod.ID)
	if getErr != nil {
		t.Fatalf("GetModule: %v", getErr)
	}
	if persisted.Memory != 512 {
		t.Fatalf("UpdateModule wrote memory=%d while an export held the gate", persisted.Memory)
	}
}
