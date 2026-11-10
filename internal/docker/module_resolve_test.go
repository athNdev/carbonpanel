// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/moby/moby/api/types/container"
)

func TestPickModuleContainerPrefersTheLiveSidecar(t *testing.T) {
	stopped := container.Summary{ID: "old-stopped", Names: []string{"/carbon-panel-module-mod-1"}, State: container.StateExited}
	running := container.Summary{ID: "new-running", Names: []string{"/carbon-panel-module-mod-1-live"}, State: container.StateRunning}
	got := pickModuleContainer([]container.Summary{stopped, running}, "old-stopped")
	if got == nil || got.ID != "new-running" {
		t.Fatalf("picked %+v, want the running replacement, not the exited original", got)
	}
}

func TestPickModuleContainerSkipsARetainedName(t *testing.T) {
	retained := container.Summary{
		ID:    "replaced",
		Names: []string{"/" + retainedContainerNamePrefix + "mod-1-1"},
		State: container.StateCreated,
	}
	live := container.Summary{ID: "live", Names: []string{"/carbon-panel-module-mod-1"}, State: container.StateRunning}
	got := pickModuleContainer([]container.Summary{retained, live}, "")
	if got == nil || got.ID != "live" {
		t.Fatalf("picked %+v, want the canonical sidecar not the retained replacement artifact", got)
	}
}

func TestPickModuleContainerKeepsARecordedLiveID(t *testing.T) {
	a := container.Summary{ID: "recorded", Names: []string{"/a"}, State: container.StateRunning}
	b := container.Summary{ID: "other", Names: []string{"/b"}, State: container.StateRunning}
	got := pickModuleContainer([]container.Summary{b, a}, "recorded")
	if got == nil || got.ID != "recorded" {
		t.Fatalf("picked %+v, want the recorded live container so ports are not flipped", got)
	}
}

func TestResolveModuleContainerAdoptsTheRunningReplacement(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[
				{"Id":"old-stopped","Names":["/carbon-panel-module-mod-replace"],"State":"exited","Ports":[{"PublicPort":19132,"PrivatePort":19132,"Type":"udp"}]},
				{"Id":"new-running","Names":["/carbon-panel-module-mod-replace-live"],"State":"running","Ports":[{"PublicPort":19132,"PrivatePort":19132,"Type":"udp"}]}
			]`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(api.Close)
	cli, err := NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })

	got, err := cli.ResolveModuleContainer(context.Background(), "mod-replace")
	if err != nil {
		t.Fatalf("ResolveModuleContainer: %v", err)
	}
	if got == nil || got.ID != "new-running" {
		t.Fatalf("resolved %+v, want new-running after an external replace", got)
	}
	if len(got.Ports) == 0 || got.Ports[0].PublicPort != 19132 {
		t.Fatalf("ports = %+v, want the replacement's published 19132", got.Ports)
	}
}
