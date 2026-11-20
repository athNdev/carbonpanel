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

func TestBuiltinResourcePackHostServesOnlyItsReadOnlyBind(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if err := InitBuiltinTemplates(store); err != nil {
		t.Fatal(err)
	}
	template, err := store.GetModuleTemplate(context.Background(), "builtin-resource-pack-host")
	if err != nil {
		t.Fatal(err)
	}
	const expectedImage = "ghcr.io/static-web-server/static-web-server:2.44.0@sha256:2c1a7c3e0feaea5859307403b74e1c575f3ec1499094fc077344173d11abaae2"
	if template.DockerImage != expectedImage || template.DefaultUID == "" || template.DefaultUID == "0" {
		t.Fatalf("expect an upstream pinned image running as non-root: %s, uid %q", template.DockerImage, template.DefaultUID)
	}
	if docker.ModuleSecurityProfile(template) != docker.ModuleProfileHardened {
		t.Fatal("resource pack host must use the hardening baseline")
	}
	var volumes []docker.ModuleVolumeMount
	if err := json.Unmarshal([]byte(template.DefaultVolumes), &volumes); err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 || !volumes[0].ReadOnly || !volumes[0].CreateDir || volumes[0].Source != "{{server.data_path}}/modules/resource-pack-host/packs" || volumes[0].Target != "/packs" {
		t.Fatalf("unexpected asset mount: %+v", volumes)
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(template.DefaultEnv), &env); err != nil {
		t.Fatal(err)
	}
	if env["SERVER_ROOT"] != "/packs" || env["SERVER_DISABLE_SYMLINKS"] != "true" || env["SERVER_DIRECTORY_LISTING"] != "false" || env["SERVER_IGNORE_HIDDEN_FILES"] != "true" {
		t.Fatalf("unsafe resource pack host config: %v", env)
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

// OSS-388: the built-in BlueMap template must pin its renderer version, keep
// the world read-only and derived from the actual level folder, expose no
// browser-facing access URL, and never silently accept Mojang downloads.
func TestBuiltinBlueMapTemplateSafetyContract(t *testing.T) {
	store := setupTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	if err := InitBuiltinTemplates(store); err != nil {
		t.Fatal(err)
	}
	templates, err := store.ListModuleTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var tpl *storage.ModuleTemplate
	for _, candidate := range templates {
		if candidate.ID == "builtin-bluemap" {
			tpl = candidate
			break
		}
	}
	if tpl == nil {
		t.Fatal("builtin-bluemap template missing")
	}
	if strings.HasSuffix(tpl.DockerImage, ":latest") {
		t.Fatalf("image %q is not pinned; upstream requires a specific release", tpl.DockerImage)
	}
	if !strings.Contains(tpl.DockerImage, ":v5.29") {
		t.Fatalf("image %q is not the ADR 0015 verified v5.29 adapter", tpl.DockerImage)
	}
	if len(tpl.DefaultAccessUrls) != 0 {
		t.Fatalf("DefaultAccessUrls = %v, want none: browser must never receive a renderer origin/port", tpl.DefaultAccessUrls)
	}
	if tpl.DefaultInitCommand != "" || tpl.DefaultRestartAfterInit || tpl.DefaultInitCommandDelay != 0 {
		t.Fatalf("template ships silent init/restart consent: cmd=%q delay=%d restart=%v",
			tpl.DefaultInitCommand, tpl.DefaultInitCommandDelay, tpl.DefaultRestartAfterInit)
	}
	if !strings.Contains(tpl.DefaultVolumes, "{{server.config.level}}") {
		t.Fatalf("world mount is not derived from the actual level folder: %s", tpl.DefaultVolumes)
	}
	var vols []docker.ModuleVolumeMount
	if err := json.Unmarshal([]byte(tpl.DefaultVolumes), &vols); err != nil {
		t.Fatalf("DefaultVolumes: %v", err)
	}
	foundWorld := false
	for _, v := range vols {
		if v.Target == "/app/world" {
			foundWorld = true
			if !v.ReadOnly {
				t.Fatal("world mount must stay read-only")
			}
		}
	}
	if !foundWorld {
		t.Fatal("missing /app/world read-only mount")
	}
	if len(tpl.Ports) != 1 || tpl.Ports[0].ContainerPort != 8100 || tpl.Ports[0].ExposePublic {
		t.Fatalf("renderer port must stay private container-only 8100: %+v", tpl.Ports)
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
