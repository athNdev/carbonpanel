// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

var (
	errNativeWorldMismatch = errors.New("the requested world is no longer active on this server")
	errNativeSourceAbsent  = errors.New("the active world storage is unavailable for a native map")
	errNativeWorldBusy     = errors.New("the world is changing; retry when the server operation finishes")
)

type nativeScopeStore interface {
	GetServerConfig(context.Context, string) (*storage.ServerConfig, error)
	GetNode(context.Context, string) (*storage.Node, error)
	GetWorldBinding(context.Context, string, string) (*storage.WorldBinding, error)
	WorldViewOperationState(context.Context, string) (restoreID string, busy bool, err error)
}

// nativeScope identifies read-only terrain before any renderer work. Its
// storage fields come from the persisted server and node, never the map URL.
// The effective owner, level, generation, binding and restore ID together
// invalidate work when an operator moves or replaces the saved world.
type nativeScope struct {
	serverID       string
	dataRoot       string
	level          string
	loader         storage.ModLoader
	node           nodestorage.NodeInfo
	nodeGeneration int64
	bindingID      string
	revisionID     string
	bindingStamp   int64
	restoreID      string
}

func resolveNativeScope(ctx context.Context, store nativeScopeStore, server *storage.Server, requested *v1.WorldRef) (nativeScope, error) {
	if server == nil || server.ID == "" || strings.TrimSpace(server.DataPath) == "" {
		return nativeScope{}, errNativeSourceAbsent
	}
	if requested != nil && requested.GetServerId() != "" && requested.GetServerId() != server.ID {
		return nativeScope{}, errNativeWorldMismatch
	}
	restoreID, busy, err := store.WorldViewOperationState(ctx, server.ID)
	if err != nil {
		return nativeScope{}, errNativeSourceAbsent
	}
	if busy {
		return nativeScope{}, errNativeWorldBusy
	}
	cfg, err := store.GetServerConfig(ctx, server.ID)
	if err != nil {
		return nativeScope{}, errNativeSourceAbsent
	}
	level := cfg.ActualLevelName()
	if level == "" {
		return nativeScope{}, errNativeSourceAbsent
	}
	if requested.GetLevelName() != "" && requested.GetLevelName() != level {
		return nativeScope{}, errNativeWorldMismatch
	}

	scope := nativeScope{
		serverID: server.ID, dataRoot: server.DataPath, level: level, loader: server.ModLoader,
		node: nodestorage.LocalNode(), nodeGeneration: 1, restoreID: restoreID,
	}
	if !nodestorage.IsLegacyLocalNodeID(server.NodeID) {
		node, err := store.GetNode(ctx, server.NodeID)
		if err != nil || node == nil || !node.Enabled || !node.IsLocal || node.EndpointGeneration < 1 {
			return nativeScope{}, errNativeSourceAbsent
		}
		scope.node = node.StorageInfo()
		scope.nodeGeneration = node.EndpointGeneration
	}
	binding, err := store.GetWorldBinding(ctx, server.ID, level)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nativeScope{}, errNativeSourceAbsent
	}
	if binding != nil {
		if !binding.MatchesOwner(canonicalNodeID(server.NodeID), scope.nodeGeneration) {
			return nativeScope{}, errNativeWorldMismatch
		}
		scope.bindingID, scope.revisionID = binding.ID, binding.RevisionID
		if !binding.BoundAt.IsZero() {
			scope.bindingStamp = binding.BoundAt.UnixNano()
		}
	}

	switch requested.GetKind() {
	case v1.WorldRef_KIND_UNSPECIFIED, v1.WorldRef_KIND_SERVER_LOCAL:
		if requested.GetBindingId() != "" || requested.GetSnapshotId() != "" {
			return nativeScope{}, errNativeWorldMismatch
		}
	case v1.WorldRef_KIND_LIBRARY_BINDING:
		if scope.bindingID == "" || requested.GetBindingId() != scope.bindingID || requested.GetSnapshotId() != "" {
			return nativeScope{}, errNativeWorldMismatch
		}
	default:
		return nativeScope{}, errNativeWorldMismatch
	}
	return scope, nil
}
