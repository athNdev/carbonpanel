// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"encoding/json"
	"sync"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// worldKey is a canonical, injection-free identity for a WorldRef. A folder
// name is never an identity; all tagged fields participate in the key.
func worldKey(ref *v1.WorldRef) string {
	if ref == nil {
		return ""
	}
	raw, err := json.Marshal(struct {
		Kind       int32  `json:"k"`
		ServerID   string `json:"s"`
		LevelName  string `json:"l"`
		BindingID  string `json:"b"`
		SnapshotID string `json:"n"`
	}{
		Kind: int32(ref.Kind), ServerID: ref.ServerId, LevelName: ref.LevelName,
		BindingID: ref.BindingId, SnapshotID: ref.SnapshotId,
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

// overlayKey is server + world + dimension. The server id is part of the key
// so an overlay can never leak across server scopes.
func overlayKey(serverID, refKey, dimension string) string {
	raw, _ := json.Marshal([]string{serverID, refKey, dimension})
	return string(raw)
}

// OverlayStore is the panel-owned, in-memory overlay store. Overlays are UI
// annotations, not Minecraft world data; they are never persisted to a world.
type OverlayStore struct {
	mu   sync.Mutex
	byID map[string]*v1.MapOverlay
	keys map[string][]string // overlayKey -> ordered ids
}

func NewOverlayStore() *OverlayStore {
	return &OverlayStore{byID: make(map[string]*v1.MapOverlay), keys: make(map[string][]string)}
}

// Set stores a copy of the overlay and returns another copy. An empty id is
// generated here so callers cannot choose a colliding identifier.
func (s *OverlayStore) Set(overlay *v1.MapOverlay) (*v1.MapOverlay, error) {
	if overlay == nil {
		return nil, ErrInvalidOverlay
	}
	stored := proto.Clone(overlay).(*v1.MapOverlay)
	key := overlayKey(stored.ServerId, worldKey(stored.WorldRef), stored.Dimension)
	s.mu.Lock()
	defer s.mu.Unlock()
	// A caller-supplied id may only update an overlay that already lives in
	// the same server/world/dimension bucket. An unknown or foreign id is
	// replaced with a freshly generated one so it cannot hijack another
	// server's overlay.
	if existing, ok := s.byID[stored.Id]; ok {
		if !s.sameBucket(existing, key) {
			stored.Id = uuid.NewString()
		}
	} else if stored.Id == "" {
		stored.Id = uuid.NewString()
	}
	if _, exists := s.byID[stored.Id]; !exists {
		s.keys[key] = append(s.keys[key], stored.Id)
	}
	s.byID[stored.Id] = stored
	return proto.Clone(stored).(*v1.MapOverlay), nil
}

// sameBucket reports whether an existing overlay belongs to the target bucket.
// Callers hold s.mu.
func (s *OverlayStore) sameBucket(existing *v1.MapOverlay, key string) bool {
	return overlayKey(existing.ServerId, worldKey(existing.WorldRef), existing.Dimension) == key
}

// List returns overlays for the exact server/world/dimension tuple.
func (s *OverlayStore) List(serverID, refKey, dimension string) []*v1.MapOverlay {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := s.keys[overlayKey(serverID, refKey, dimension)]
	out := make([]*v1.MapOverlay, 0, len(ids))
	for _, id := range ids {
		if o, ok := s.byID[id]; ok {
			out = append(out, proto.Clone(o).(*v1.MapOverlay))
		}
	}
	return out
}
