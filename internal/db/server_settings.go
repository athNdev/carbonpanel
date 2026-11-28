// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package db

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ServerSettingsCommit is the server row and optional config row one
// UpdateServer request commits. The reviewed-mod admission check and both
// writes share one writer transaction, so a candidate admitted concurrently
// cannot land between the preflight and the committed pair.
type ServerSettingsCommit struct {
	ServerID string
	Proposed *Server
	// ProposedConfig is written when SaveConfig is set.
	ProposedConfig *ServerConfig
	SaveConfig     bool
}

// CommitServerSettings atomically re-checks the reviewed-mod runtime-target
// rule against the committed rows and writes the proposed server and config
// pair. The writer lock is taken before the guard reads, so config admission
// (which also starts with a write) serializes with this commit. A refusal rolls
// back both rows and returns ErrStagedModVersionChange.
func (s *Store) CommitServerSettings(ctx context.Context, commit ServerSettingsCommit) error {
	if commit.Proposed == nil || commit.Proposed.ID == "" || commit.Proposed.ID != commit.ServerID {
		return fmt.Errorf("server settings commit requires the proposed server")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := takeServerConfigWriterLock(tx, commit.ServerID); err != nil {
			return err
		}
		var current Server
		if err := tx.Where("id = ?", commit.ServerID).First(&current).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrServerNotFound
			}
			return err
		}
		currentConfig, err := serverConfigTx(tx, commit.ServerID)
		if err != nil {
			return err
		}
		// A config row that appeared after the caller's read must still be
		// compared, or a runtime move could bypass the review check.
		proposedConfig := commit.ProposedConfig
		if proposedConfig == nil && currentConfig != nil {
			cloned := *currentConfig
			proposedConfig = &cloned
		}
		if err := refuseReviewedModRuntimeChangeTx(tx, &current, currentConfig, commit.Proposed, proposedConfig); err != nil {
			return err
		}
		if commit.SaveConfig {
			if commit.ProposedConfig == nil {
				return fmt.Errorf("server settings commit requires the proposed config")
			}
			if err := saveServerConfigTx(tx, commit.ProposedConfig); err != nil {
				return err
			}
		}
		if err := tx.Save(commit.Proposed).Error; err != nil {
			return err
		}
		return s.syncServerConfigWithServerTx(tx, commit.Proposed)
	})
}

func serverConfigTx(tx *gorm.DB, serverID string) (*ServerConfig, error) {
	var config ServerConfig
	err := tx.Where("server_id = ?", serverID).First(&config).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// syncServerConfigWithServerTx mirrors SyncServerConfigWithServer inside the
// settings commit so the projected server fields and the guarded save share the
// same writer transaction.
func (s *Store) syncServerConfigWithServerTx(tx *gorm.DB, server *Server) error {
	config, err := serverConfigTx(tx, server.ID)
	if err != nil {
		return err
	}
	if config == nil {
		config = s.CreateDefaultServerConfig(server.ID)
	}
	applyServerFieldsToConfig(config, server)
	return saveServerConfigTx(tx, config)
}
