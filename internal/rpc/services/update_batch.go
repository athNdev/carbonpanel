// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/operation"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"gorm.io/gorm"
)

// GetUpdateBatch returns the server's open staged batch and the items in it.
func (s *ConfigService) GetUpdateBatch(ctx context.Context, req *connect.Request[v1.GetUpdateBatchRequest]) (*connect.Response[v1.GetUpdateBatchResponse], error) {
	if _, err := s.store.GetServer(ctx, req.Msg.GetServerId()); err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}
	batch, err := s.store.OpenUpdateBatch(ctx, req.Msg.GetServerId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if batch == nil {
		return connect.NewResponse(&v1.GetUpdateBatchResponse{}), nil
	}
	items, err := s.batchItems(ctx, batch.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &v1.GetUpdateBatchResponse{
		Open:        true,
		Window:      protoApplyWindow(batch.Window),
		IdleMinutes: int32(batch.IdleMinutes),
		Items:       items,
	}
	if batch.ApplyAt != nil {
		resp.ApplyAtUnix = batch.ApplyAt.Unix()
	}
	return connect.NewResponse(resp), nil
}

// DiscardUpdateBatch drops one staged item, or the whole batch when no item is named.
func (s *ConfigService) DiscardUpdateBatch(ctx context.Context, req *connect.Request[v1.DiscardUpdateBatchRequest]) (*connect.Response[v1.DiscardUpdateBatchResponse], error) {
	server, err := s.store.GetServer(ctx, req.Msg.GetServerId())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("server not found"))
	}
	release, err := beginDataWrite(server.ID)
	if err != nil {
		return nil, err
	}
	defer release()
	// Mod-update staging holds the same per-server operation while it commits
	// a window and staged rows. Sharing the ownership prevents a discard from
	// closing the chosen batch halfway through a staging write.
	err = operation.RunJournaled(ctx, s.store, s.log, server, "discard-update-batch", "", func() error {
		if req.Msg.GetItemId() == "" {
			return s.discardWholeBatch(ctx, server)
		}
		if err := s.discardBatchItem(ctx, server, req.Msg.GetItemId()); err != nil {
			return err
		}
		if err := s.closeBatchIfEmpty(ctx, server.ID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		// Keep the typed refusal from the inner operation (for example a
		// missing item or wrong server) instead of flattening it to Internal.
		if _, ok := err.(*connect.Error); ok {
			return nil, err
		}
		// A concurrent staging or publication owns the server journal. The
		// discard did not run, so retrying after it finishes is valid.
		if errors.Is(err, storage.ErrActiveOperation) || errors.Is(err, operation.ErrInProgress) {
			return nil, connect.NewError(connect.CodeAborted,
				fmt.Errorf("discard refused while another operation owns the server: %w", err))
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.DiscardUpdateBatchResponse{}), nil
}

func (s *ConfigService) batchItems(ctx context.Context, batchID string) ([]*v1.StagedUpdateItem, error) {
	configs, err := s.store.ListBatchConfig(ctx, batchID)
	if err != nil {
		return nil, err
	}
	content, err := s.store.ListBatchContent(ctx, batchID)
	if err != nil {
		return nil, err
	}
	items := make([]*v1.StagedUpdateItem, 0, len(configs)+len(content))
	for _, cfg := range configs {
		items = append(items, &v1.StagedUpdateItem{
			Id:      cfg.ID,
			Kind:    "config",
			Action:  "update",
			Summary: configDiffSummary(cfg.Payload),
		})
	}
	for _, row := range content {
		summary := row.Action + " " + row.FileName
		if row.Kind == storage.StagedKindModpack || row.Kind == storage.StagedKindModule {
			summary = row.FileName
		}
		items = append(items, &v1.StagedUpdateItem{
			Id:      row.ID,
			Kind:    row.Kind,
			Action:  row.Action,
			Summary: summary,
		})
	}
	return items, nil
}

func (s *ConfigService) discardWholeBatch(ctx context.Context, server *storage.Server) error {
	batch, err := s.store.OpenUpdateBatch(ctx, server.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if batch == nil {
		return nil
	}
	// Closing a batch that retains an acknowledged or unresolved publication
	// would orphan its recovery tree and the applied rows that prove cleanup is
	// owed. A lifecycle retry must resolve it first.
	if batch.SealedAt != nil || batch.PublishedAt != nil {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this update batch has a retained publication; retry the server start or restart before discarding it"))
	}
	applied, err := s.store.CountBatchContentByStatus(ctx, batch.ID, storage.StagedStatusApplied)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	if applied > 0 {
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("this update batch has published content awaiting finalization; retry the server start or restart before discarding it"))
	}
	content, err := s.store.ListBatchContent(ctx, batch.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for i := range content {
		if err := discardStagedContent(ctx, s.store, server, s.storageResolver, content[i].ID); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	configs, err := s.store.ListBatchConfig(ctx, batch.ID)
	if err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	for _, cfg := range configs {
		if err := s.store.DiscardStagedConfigChange(ctx, cfg.ID); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	}
	if err := s.store.CloseUpdateBatch(ctx, batch.ID, storage.UpdateBatchDiscarded); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

func (s *ConfigService) discardBatchItem(ctx context.Context, server *storage.Server, id string) error {
	row, err := s.store.GetStagedContent(ctx, id)
	if err == nil {
		if row.ServerID != server.ID || row.Status != storage.StagedStatusStaged {
			return connect.NewError(connect.CodeNotFound, errors.New("staged item not found"))
		}
		if err := discardStagedContent(ctx, s.store, server, s.storageResolver, id); err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
		return nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return connect.NewError(connect.CodeInternal, err)
	}
	cfg, err := s.store.GetStagedConfigChange(ctx, id)
	if err != nil || cfg.ServerID != server.ID || cfg.Status != storage.StagedStatusStaged {
		return connect.NewError(connect.CodeNotFound, errors.New("staged item not found"))
	}
	if err := s.store.DiscardStagedConfigChange(ctx, id); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	return nil
}

func (s *ConfigService) closeBatchIfEmpty(ctx context.Context, serverID string) error {
	batch, err := s.store.OpenUpdateBatch(ctx, serverID)
	if err != nil || batch == nil {
		return err
	}
	content, err := s.store.ListBatchContent(ctx, batch.ID)
	if err != nil {
		return err
	}
	configs, err := s.store.ListBatchConfig(ctx, batch.ID)
	if err != nil {
		return err
	}
	if len(content) > 0 || len(configs) > 0 {
		return nil
	}
	// Discarding the last staged row must not close a batch that still owns a
	// retained publication or applied content recovery. Keep it open so the
	// next lifecycle retry can finalize it without losing the undo tree.
	if batch.SealedAt != nil || batch.PublishedAt != nil {
		return nil
	}
	applied, err := s.store.CountBatchContentByStatus(ctx, batch.ID, storage.StagedStatusApplied)
	if err != nil {
		return err
	}
	if applied > 0 {
		return nil
	}
	return s.store.CloseUpdateBatch(ctx, batch.ID, storage.UpdateBatchDiscarded)
}

func configDiffSummary(payload string) string {
	var diff map[string]any
	if err := json.Unmarshal([]byte(payload), &diff); err != nil || len(diff) == 0 {
		return "config update"
	}
	keys := make([]string, 0, len(diff))
	for key := range diff {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return "config: " + strings.Join(keys, ", ")
}

func protoApplyWindow(window string) v1.ConfigApplyWindow {
	switch storage.UpdateWindow(window) {
	case storage.UpdateWindowAtTime:
		return v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_AT_TIME
	case storage.UpdateWindowIdle:
		return v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_IDLE
	default:
		return v1.ConfigApplyWindow_CONFIG_APPLY_WINDOW_NEXT_RESTART
	}
}
