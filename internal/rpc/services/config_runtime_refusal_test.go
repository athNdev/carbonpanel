// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/stretchr/testify/require"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// TestConfigUpdateRefusesRuntimeChangeWithReviewedCandidate covers the refusal
// code both at direct save and at staging: a runtime target change invalidates
// a staged reviewed mod review and must be a failed precondition, not internal.
func TestConfigUpdateRefusesRuntimeChangeWithReviewedCandidate(t *testing.T) {
	ctx := context.Background()
	svc, store := setupConfigTestService(t)
	server := &storage.Server{
		ID: "srv-runtime-refusal", Name: "runtime-refusal",
		ModLoader: storage.ModLoaderFabric, MCVersion: "1.20.1", Status: storage.StatusStopped,
	}
	require.NoError(t, store.CreateServer(ctx, server))
	_, err := store.AddStagedContent(ctx, &storage.StagedContentChange{
		ServerID:           server.ID,
		Kind:               storage.StagedKindMod,
		Action:             storage.StagedActionReplace,
		TargetPath:         "mods/mod.jar",
		StagePath:          ".carbon-panel/staged/reviewed/mod.jar",
		FileName:           "mod.jar",
		ExpectedLiveSha512: "deadbeef",
		ApprovedSha512:     strings.Repeat("ab", 64),
		ApprovedProvider:   "modrinth",
		ApprovedProjectID:  "project",
		ApprovedVersionID:  "release",
		ApprovedFilename:   "mod.jar",
	})
	require.NoError(t, err)

	for name, req := range map[string]*v1.UpdateServerConfigRequest{
		"direct": {ServerId: server.ID, Updates: map[string]string{"version": "1.21.1"}},
		"staged": {ServerId: server.ID, Updates: map[string]string{"version": "1.21.1"}, Stage: true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := svc.UpdateServerConfig(ctx, connect.NewRequest(req))
			require.Error(t, err)
			require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), err.Error())
			require.Contains(t, err.Error(), storage.ErrStagedModVersionChange.Error())
		})
	}

	// The refused runtime change must leave the live target untouched.
	cfg, err := store.GetServerConfig(ctx, server.ID)
	require.NoError(t, err)
	require.NotNil(t, cfg.Version)
	require.Equal(t, "1.20.1", *cfg.Version)
}
