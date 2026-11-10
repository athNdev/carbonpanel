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
