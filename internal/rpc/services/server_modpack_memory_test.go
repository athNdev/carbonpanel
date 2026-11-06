// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

func seedMemoryModpack(t *testing.T, svc *ServerService, recommendedRAM int) {
	t.Helper()
	if err := svc.store.DB().Create(&db.IndexedModpack{
		ID: "modrinth-memory", Indexer: "modrinth", IndexerID: "memory-pack",
		Name: "Memory Pack", GameVersions: `["1.21.1"]`, RecommendedRAM: recommendedRAM,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func createModpackServer(t *testing.T, svc *ServerService, port, memory int32) *db.Server {
	t.Helper()
	resp, err := svc.CreateServer(ctxWithRole("operator"), connect.NewRequest(&v1.CreateServerRequest{
		Name: "memory-pack", McVersion: "1.21.1", DockerImage: "java21",
		NodeId: "test-node", Port: port, Memory: memory, ModpackId: "modrinth-memory",
	}))
	if err != nil {
		t.Fatalf("CreateServer: %v", err)
	}
	server, err := svc.store.GetServer(context.Background(), resp.Msg.Server.Id)
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestCreateServerKeepsTheModpackMemoryTheCallerEntered(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	seedMemoryModpack(t, svc, 6144)

	server := createModpackServer(t, svc, 25582, 2048)
	if server.Memory != 2048 {
		t.Fatalf("server memory = %d, want the caller's 2048 MB", server.Memory)
	}
}

func TestCreateServerUsesTheModpackRecommendationWhenMemoryIsOmitted(t *testing.T) {
	svc, _ := newDockerPrivilegeTestService(t)
	seedMemoryModpack(t, svc, 6144)

	server := createModpackServer(t, svc, 25583, 0)
	if server.Memory != 6144 {
		t.Fatalf("server memory = %d, want the pack recommendation 6144 MB", server.Memory)
	}
}
