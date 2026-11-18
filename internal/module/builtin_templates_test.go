// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package module

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
)

func TestGeyserBedrockStaysPublic(t *testing.T) {
	port := geyserBedrockPort()
	if !port.ExposePublic {
		t.Fatal("Geyser Bedrock UDP must be marked public")
	}
	if port.Protocol != "udp" || port.ContainerPort != 19132 {
		t.Fatalf("port = %+v", port)
	}
}

// The standalone Geyser entrypoint now defaults to a loopback bind. The
// shipped module keeps its explicit wide bind because the panel's UDP proxy
// dials the container IP, so legitimate Bedrock clients must keep working.
func TestBuiltinGeyserOptsInToContainerWideBind(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if err := InitBuiltinTemplates(store); err != nil {
		t.Fatal(err)
	}
	templates, err := store.ListModuleTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var geyser *storage.ModuleTemplate
	for _, tpl := range templates {
		if tpl.ID == "builtin-geyser" {
			geyser = tpl
			break
		}
	}
	if geyser == nil {
		t.Fatal("builtin geyser template missing")
	}

	var env map[string]string
	if err := json.Unmarshal([]byte(geyser.DefaultEnv), &env); err != nil {
		t.Fatalf("geyser template DefaultEnv: %v", err)
	}
	if env["BEDROCK_ADDRESS"] != "0.0.0.0" {
		t.Errorf("geyser BEDROCK_ADDRESS = %q, want explicit 0.0.0.0 for UDP proxy reachability", env["BEDROCK_ADDRESS"])
	}
	if !strings.Contains(strings.ToLower(geyser.Documentation), "all interfaces") {
		t.Error("geyser template documentation must state why the container-wide bind is explicit")
	}
}

// The status sidecar's web endpoint has no request authentication. Its
// published port stays reachable only when the shipped container binds all
// interfaces; the panel's port proxy dials the container IP, not localhost
// inside that container. This fixture pins that deployment contract.
func TestBuiltinStatusPanelPublishesReachablePort(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if err := InitBuiltinTemplates(store); err != nil {
		t.Fatal(err)
	}
	templates, err := store.ListModuleTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var status *storage.ModuleTemplate
	for _, tpl := range templates {
		if tpl.ID == "builtin-status-panel" {
			status = tpl
			break
		}
	}
	if status == nil {
		t.Fatal("builtin status panel template missing")
	}

	var env map[string]string
	if err := json.Unmarshal([]byte(status.DefaultEnv), &env); err != nil {
		t.Fatalf("status template DefaultEnv: %v", err)
	}
	if env["HOST"] != "0.0.0.0" {
		t.Errorf("status container HOST = %q, want 0.0.0.0 for published port reachability", env["HOST"])
	}
	if env["PORT"] != "{{module.ports.Web.container_port}}" {
		t.Errorf("status template PORT = %q, want the Web container port placeholder", env["PORT"])
	}
	if len(status.Ports) == 0 || status.Ports[0].ContainerPort != 8181 {
		t.Errorf("status Web port = %+v, want container port 8181", status.Ports)
	}
	if status.HealthCheckPort != 8181 {
		t.Errorf("status health port = %d, want 8181", status.HealthCheckPort)
	}
	if !strings.Contains(strings.ToLower(status.Documentation), "no request authentication") {
		t.Error("status template documentation must state the unauthenticated endpoint contract")
	}
}

func TestBuiltinTemplatesAreNotPrivilegedHostExtensions(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if err := InitBuiltinTemplates(store); err != nil {
		t.Fatal(err)
	}
	templates, err := store.ListModuleTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) == 0 {
		t.Fatal("no builtin templates")
	}
	for _, tpl := range templates {
		blob := strings.ToLower(tpl.DefaultVolumes + tpl.DefaultEnv + tpl.Documentation)
		if strings.Contains(blob, "docker.sock") || strings.Contains(blob, "privileged") {
			t.Errorf("template %s ships a privileged host-extension (%q); that is an operator feature, not a builtin default", tpl.ID, tpl.Name)
		}
		var vols []docker.ModuleVolumeMount
		if tpl.DefaultVolumes == "" {
			continue
		}
		if err := json.Unmarshal([]byte(tpl.DefaultVolumes), &vols); err != nil {
			t.Errorf("template %s DefaultVolumes: %v", tpl.ID, err)
			continue
		}
		if err := docker.ValidateModuleVolumeMounts(vols, "/data/server", "/backups"); err != nil {
			t.Errorf("template %s default volumes fail confinement: %v", tpl.ID, err)
		}
	}
}
