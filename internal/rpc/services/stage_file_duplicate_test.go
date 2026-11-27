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

// TestStageFileRefusesDuplicateTarget proves the file staging entry point
// returns an actionable refusal instead of admitting two rows for one live
// target. The second row is what made an interrupted publication's single
// post-image unverifiable.
func TestStageFileRefusesDuplicateTarget(t *testing.T) {
	ctx := context.Background()
	store, server, _ := newContentStageFixture(t, "stage-duplicate-file", storage.StatusRunning)
	files := NewFileService(store, nil, nil, nil, logger.New())
	t.Cleanup(files.Stop)

	request := func(content string) *connect.Request[v1.UpdateFileRequest] {
		return connect.NewRequest(&v1.UpdateFileRequest{
			ServerId: server.ID,
			Path:     "server.properties",
			Content:  []byte(content),
			Stage:    true,
		})
	}
	_, err := files.UpdateFile(ctx, request("motd=first\n"))
	require.NoError(t, err)

	_, err = files.UpdateFile(ctx, request("motd=second\n"))
	require.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "%v", err)
	require.ErrorIs(t, err, storage.ErrDuplicateStagedTarget)
	require.Contains(t, err.Error(), "already staged")

	rows, err := store.ListStagedContent(ctx, server.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1, "the duplicate must not be committed")
	require.Equal(t, "server.properties", rows[0].TargetPath)
}
