// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

// TestUpdateServerAdmissionDuringCommitLeavesConsistentPair is the regression
// for checking the reviewed-mod candidate outside the server/config write
// transaction. A candidate admitted after the preflight used to let the server
// row commit while the config sync's guard refused, leaving an inconsistent
// pair that broke every later read and lifecycle call. The trigger injects the
// admission at the server save; the serialized commit must roll the whole pair
// back.
func TestUpdateServerAdmissionDuringCommitLeavesConsistentPair(t *testing.T) {
	ctx := ctxWithRole("operator")
	svc, _ := newDockerPrivilegeTestService(t)
	if svc.docker != nil {
		_ = svc.docker.Close()
	}
	ensureLocalTestNode(t, svc.store, "test-node")

	server := &storage.Server{
		ID: "race-admission", Name: "race-admission", NodeID: "test-node",
		Port: 25911, Memory: 1024, MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric,
		Status: storage.StatusStopped, DataPath: t.TempDir(),
	}
	require.NoError(t, svc.store.CreateServer(ctx, server))
	cfg := svc.store.CreateDefaultServerConfig(server.ID)
	loader, version := "fabric", "1.21.1"
	cfg.Type, cfg.Version = &loader, &version
	require.NoError(t, svc.store.SaveServerConfig(ctx, cfg))

	// Inject a reviewed candidate exactly while the server row is written, the
	// window the old preflight/write split left open.
	require.NoError(t, svc.store.DB().Exec(`
		CREATE TRIGGER admit_reviewed_candidate_during_save
		BEFORE UPDATE OF mc_version ON servers
		WHEN NEW.mc_version <> OLD.mc_version
		BEGIN
			INSERT INTO staged_content_changes
				(id, server_id, batch_id, kind, action, target_path, stage_path, file_name, status, expected_live_sha512, created_at)
			VALUES
				('race-candidate', NEW.id, 'race-batch', 'mod', 'replace', 'mods/mod.jar',
				 '.carbon-panel/staged/race/mod.jar', 'mod.jar', 'staged', 'deadbeef', CURRENT_TIMESTAMP);
		END`).Error)
	t.Cleanup(func() {
		_ = svc.store.DB().Exec(`DROP TRIGGER admit_reviewed_candidate_during_save`).Error
	})

	resp, err := svc.UpdateServer(ctx, connect.NewRequest(&v1.UpdateServerRequest{
		Id: server.ID, McVersion: "1.21.4",
	}))
	require.Nil(t, resp)
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "%v", err)
	require.Contains(t, err.Error(), "apply or discard the staged reviewed mod update")

	got, err := svc.store.GetServer(context.Background(), server.ID)
	require.NoError(t, err)
	require.Equal(t, "1.21.1", got.MCVersion,
		"a concurrent admission must not leave the server row on the new target")
	storedCfg, err := svc.store.GetServerConfig(context.Background(), server.ID)
	require.NoError(t, err)
	require.Equal(t, "1.21.1", *storedCfg.Version,
		"the config row must stay on the same target as the server row")
	require.True(t, *storedCfg.Type == "fabric")

	rows, err := svc.store.ListStagedContent(context.Background(), server.ID)
	require.NoError(t, err)
	require.Empty(t, rows, "the injected admission rolled back with the refused pair")

	// The pair is still readable through the normal config entry point.
	configSvc := NewConfigService(svc.store, nil, nil, nil, logger.New())
	_, err = configSvc.GetServerConfig(ctx, connect.NewRequest(&v1.GetServerConfigRequest{ServerId: server.ID}))
	require.NoError(t, err)
}
