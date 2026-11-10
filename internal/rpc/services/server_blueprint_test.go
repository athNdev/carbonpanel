// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/blueprint"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func seedByID(t *testing.T, id string) blueprint.Spec {
	t.Helper()
	for _, seed := range blueprint.Seed() {
		if seed.ID == id {
			spec, err := blueprint.Resolve(seed)
			if err != nil {
				t.Fatalf("resolve %s: %v", id, err)
			}
			return spec
		}
	}
	t.Fatalf("missing seed %s", id)
	return blueprint.Spec{}
}

func TestApplyResolvedBlueprintRefusesUnqualifiedBedrockSeed(t *testing.T) {
	spec := seedByID(t, "builtin-bedrock")
	err := applyResolvedBlueprint(&v1.CreateServerRequest{Name: "bedrock-from-seed"}, spec)
	if err == nil {
		t.Fatal("bedrock seed must not apply as a create path")
	}
	if !strings.Contains(err.Error(), "Native Bedrock") {
		t.Fatalf("error = %v, want native Bedrock limitation", err)
	}
}

func TestCreateServerFromQualifiedBlueprintResolvesImageEnvProtocol(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	spec := seedByID(t, "builtin-paper")
	msg := &v1.CreateServerRequest{
		Name: "paper-from-seed", McVersion: "1.21.1", NodeId: "test-node",
	}
	if err := applyResolvedBlueprint(msg, spec); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if msg.DockerImage != spec.StoredImage {
		t.Fatalf("docker image = %q, want stored %q", msg.DockerImage, spec.StoredImage)
	}
	if msg.Port != 25565 {
		t.Fatalf("port = %d, want Java 25565", msg.Port)
	}

	created, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(msg))
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	got := created.Msg.GetServer()
	if got.GetDockerImage() != spec.StoredImage {
		t.Fatalf("stored docker image = %q, want %q", got.GetDockerImage(), spec.StoredImage)
	}
	if got.GetModLoader() != v1.ModLoader_MOD_LOADER_PAPER {
		t.Fatalf("mod loader = %v, want PAPER", got.GetModLoader())
	}

	cfg, err := svc.store.GetServerConfig(ctxWithRole("operator"), got.GetId())
	if err != nil {
		t.Fatalf("GetServerConfig: %v", err)
	}
	if cfg.Type == nil || *cfg.Type != spec.Env["TYPE"] {
		t.Fatalf("TYPE env = %v, want %q", cfg.Type, spec.Env["TYPE"])
	}
	if cfg.EULA == nil || *cfg.EULA != "TRUE" {
		t.Fatalf("EULA = %v, want TRUE", cfg.EULA)
	}
}
