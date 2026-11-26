// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"crypto/sha512"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/stretchr/testify/require"
)

func approvedDigest(t *testing.T, body string) string {
	t.Helper()
	sum := sha512.Sum512([]byte(body))
	return fmt.Sprintf("%x", sum[:])
}

func TestReviewedModAdmissionRequiresDurableApprovedDigest(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	t.Cleanup(func() { _ = store.Close() })
	server := &Server{ID: "srv-approved-admission", Name: "approved-admission", ModLoader: ModLoaderFabric, MCVersion: "1.21.1"}
	require.NoError(t, store.CreateServer(ctx, server))

	reviewed := &StagedContentChange{
		ServerID:           server.ID,
		Kind:               StagedKindMod,
		Action:             StagedActionReplace,
		TargetPath:         "mods/mod.jar",
		StagePath:          ".carbon-panel/staged/reviewed/mod.jar",
		FileName:           "mod.jar",
		ExpectedLiveSha512: approvedDigest(t, "installed"),
	}
	_, err := store.AddStagedContent(ctx, reviewed)
	require.ErrorIs(t, err, ErrStagedModApprovalMissing)
	open, err := store.OpenUpdateBatch(ctx, server.ID)
	require.NoError(t, err)
	require.Nil(t, open, "a refused admission must not leave an open batch")

	reviewed.ApprovedSha512 = "not-a-sha-512"
	_, err = store.AddStagedContent(ctx, reviewed)
	require.ErrorIs(t, err, ErrStagedModApprovalInvalid)

	// A digest recorded before the durable release identity cannot prove which
	// provider release the operator approved.
	reviewed.ApprovedSha512 = strings.ToUpper(approvedDigest(t, "candidate"))
	_, err = store.AddStagedContent(ctx, reviewed)
	require.ErrorIs(t, err, ErrStagedModApprovalIdentityMissing)

	reviewed.ApprovedProvider = "modrinth"
	reviewed.ApprovedProjectID = "project"
	reviewed.ApprovedVersionID = "release"
	reviewed.ApprovedFilename = "mod.jar"
	row, err := store.AddStagedContent(ctx, reviewed)
	require.NoError(t, err)
	require.Equal(t, strings.ToLower(reviewed.ApprovedSha512), row.ApprovedSha512)

	stored, err := store.GetStagedContent(ctx, row.ID)
	require.NoError(t, err)
	require.Equal(t, strings.ToLower(reviewed.ApprovedSha512), stored.ApprovedSha512,
		"the approved digest must be durable row state, not an in-memory admission guard")
	require.Equal(t, "modrinth", stored.ApprovedProvider)
	require.Equal(t, "project", stored.ApprovedProjectID)
	require.Equal(t, "release", stored.ApprovedVersionID)
	require.Equal(t, "mod.jar", stored.ApprovedFilename,
		"the approved provider, project, version and file identity must be durable row state")

	// An ordinary upload carries no reviewed precondition and needs no approval.
	upload := &StagedContentChange{
		ServerID:   server.ID,
		Kind:       StagedKindMod,
		Action:     StagedActionInstall,
		TargetPath: "mods/upload.jar",
		StagePath:  ".carbon-panel/staged/upload/upload.jar",
		FileName:   "upload.jar",
	}
	uploaded, err := store.AddStagedContent(ctx, upload)
	require.NoError(t, err)
	require.Empty(t, uploaded.ApprovedSha512)
}

// TestApprovedDigestColumnIsAdditive proves an existing database keeps its
// staged reviewed row while the new durable digest column is added. The legacy
// row remains visibly unapproved so publication requires re-review.
func TestApprovedDigestColumnIsAdditive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "approved-digest-migration.db")
	store, err := NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{Path: path, AutoMigrate: false}})
	require.NoError(t, err)
	defer func() { _ = store.Close() }()

	require.NoError(t, store.DB().Exec(`CREATE TABLE staged_content_changes (
		id text PRIMARY KEY,
		server_id text NOT NULL,
		batch_id text NOT NULL,
		kind text NOT NULL,
		action text NOT NULL,
		target_path text NOT NULL,
		stage_path text,
		file_name text,
		expected_live_sha512 text,
		status text NOT NULL,
		created_at datetime,
		applied_at datetime
	)`).Error)
	require.NoError(t, store.DB().Exec(`INSERT INTO staged_content_changes (
		id, server_id, batch_id, kind, action, target_path, stage_path,
		file_name, expected_live_sha512, status
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-reviewed", "legacy-server", "legacy-batch", StagedKindMod,
		StagedActionReplace, "mods/mod.jar", ".carbon-panel/staged/legacy/mod.jar",
		"mod.jar", approvedDigest(t, "installed"), StagedStatusStaged).Error)

	require.NoError(t, store.Migrate())
	require.True(t, store.DB().Migrator().HasColumn(&StagedContentChange{}, "approved_sha512"))
	require.True(t, store.DB().Migrator().HasColumn(&StagedContentChange{}, "approved_provider"))
	require.True(t, store.DB().Migrator().HasColumn(&StagedContentChange{}, "approved_project_id"))
	require.True(t, store.DB().Migrator().HasColumn(&StagedContentChange{}, "approved_version_id"))
	require.True(t, store.DB().Migrator().HasColumn(&StagedContentChange{}, "approved_filename"))

	var approved sql.NullString
	require.NoError(t, store.DB().Raw(
		`SELECT approved_sha512 FROM staged_content_changes WHERE id = ?`, "legacy-reviewed",
	).Row().Scan(&approved))
	require.False(t, approved.Valid, "migration must not invent approval for a legacy row")
	require.Equal(t, "", approved.String)

	for _, column := range []string{"approved_provider", "approved_project_id", "approved_version_id", "approved_filename"} {
		var value sql.NullString
		require.NoError(t, store.DB().Raw(
			`SELECT `+column+` FROM staged_content_changes WHERE id = ?`, "legacy-reviewed",
		).Row().Scan(&value))
		require.False(t, value.Valid, "migration must not invent the approved release identity: %s", column)
	}

	var expectedLive string
	require.NoError(t, store.DB().Raw(
		`SELECT expected_live_sha512 FROM staged_content_changes WHERE id = ?`, "legacy-reviewed",
	).Row().Scan(&expectedLive))
	require.Equal(t, approvedDigest(t, "installed"), expectedLive, "the legacy row and its precondition survive")
}
