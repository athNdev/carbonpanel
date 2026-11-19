// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"net/http"
	"sort"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// recordingMux captures every pattern registerServices mounts while still
// forwarding to a real ServeMux, so the drift guard sees the actual handler
// registrations rather than a second hand-written list.
type recordingMux struct {
	inner    *http.ServeMux
	patterns []string
}

func (m *recordingMux) Handle(pattern string, handler http.Handler) {
	m.patterns = append(m.patterns, pattern)
	m.inner.Handle(pattern, handler)
}

func sortedUnique(names []string) []string {
	set := make(map[string]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// TestRegisteredConnectServicesMatchesMountedHandlers is the drift guard for
// the gRPC reflection service. It runs the real service-registration path with
// a minimal server and compares the Connect services actually mounted with the
// authoritative RegisteredConnectServices list used by the reflector. Adding a
// handler without updating the list (or vice versa) fails here in CI. RBAC
// permission coverage is checked separately by the descriptor-driven gate in
// internal/rbac/mapping_test.go.
func TestRegisteredConnectServicesMatchesMountedHandlers(t *testing.T) {
	store := newTestStore(t)
	s := &Server{
		store:  store,
		config: &config.Config{},
		log:    logger.New(),
	}
	mux := &recordingMux{inner: http.NewServeMux()}
	s.registerServices(mux, nil)

	var mounted []string
	for _, pattern := range mux.patterns {
		if name, ok := connectServiceNameFromPath(pattern); ok {
			mounted = append(mounted, name)
		}
	}

	want := sortedUnique(RegisteredConnectServices)
	got := sortedUnique(mounted)
	if len(want) != len(got) {
		t.Fatalf("registered service list and mounted handlers differ:\n list: %v\n mux:  %v", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("registered service list and mounted handlers differ:\n list: %v\n mux:  %v", want, got)
		}
	}

	// A duplicate entry in the authoritative list would be silently
	// deduplicated in the comparison above, so reject it explicitly.
	seen := make(map[string]struct{}, len(RegisteredConnectServices))
	for _, name := range RegisteredConnectServices {
		if _, dup := seen[name]; dup {
			t.Errorf("service %q listed more than once in RegisteredConnectServices", name)
		}
		seen[name] = struct{}{}
	}
}
