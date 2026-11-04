// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/hostmount"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

// selfInspectServer answers the inspect call for this process's own hostname.
func selfInspectServer(t *testing.T, body string, status int) *Client {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		t.Skip("hostname is unavailable, so self-inspection cannot be addressed")
	}

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/containers/") || !strings.HasSuffix(r.URL.Path, "/json") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != "" {
			_, _ = w.Write([]byte(body))
		}
	}))
	t.Cleanup(api.Close)

	cli, err := NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if err := cli.Close(); err != nil {
			t.Errorf("close client: %v", err)
		}
	})
	return cli
}

func TestSelfMountsReportsTheContainerMountTable(t *testing.T) {
	body := `{
		"Id": "abc123",
		"Mounts": [
			{"Type": "bind", "Source": "/srv/carbon-panel/data", "Destination": "/app/data", "RW": true},
			{"Type": "volume", "Name": "panel-tmp", "Source": "/var/lib/docker/volumes/panel-tmp/_data", "Destination": "/app/tmp", "RW": true},
			{"Type": "bind", "Source": "/srv/carbon-panel/backups", "Destination": "/app/backups", "RW": false}
		]
	}`
	cli := selfInspectServer(t, body, http.StatusOK)

	mounts, err := cli.SelfMounts(context.Background())
	if err != nil {
		t.Fatalf("SelfMounts: %v", err)
	}
	if len(mounts) != 3 {
		t.Fatalf("mounts = %+v, want 3", mounts)
	}

	want := []hostmount.Mount{
		{Kind: hostmount.KindBind, Source: "/srv/carbon-panel/data", Destination: "/app/data", RW: true},
		{Kind: hostmount.KindVolume, Source: "/var/lib/docker/volumes/panel-tmp/_data", Destination: "/app/tmp", RW: true},
		{Kind: hostmount.KindBind, Source: "/srv/carbon-panel/backups", Destination: "/app/backups", RW: false},
	}
	for i, w := range want {
		if mounts[i] != w {
			t.Errorf("mount %d = %+v, want %+v", i, mounts[i], w)
		}
	}
}

func TestSelfMountsReturnsNothingWhenThisProcessIsNotAContainer(t *testing.T) {
	// A 404 from inspect means the daemon does not manage this hostname, which
	// is the normal case for a panel running directly on the host.
	cli := selfInspectServer(t, `{"message":"No such container"}`, http.StatusNotFound)

	mounts, err := cli.SelfMounts(context.Background())
	if err != nil {
		t.Fatalf("SelfMounts: %v", err)
	}
	if len(mounts) != 0 {
		t.Fatalf("mounts = %+v, want none", mounts)
	}
}

func TestSelfMountsSurfacesADaemonFailure(t *testing.T) {
	// A restricted socket proxy answers with a permission error. Startup must be
	// able to tell "not a container" apart from "cannot inspect", so it can fall
	// back instead of concluding the data directory is unmounted.
	cli := selfInspectServer(t, `{"message":"permission denied"}`, http.StatusForbidden)

	if _, err := cli.SelfMounts(context.Background()); err == nil {
		t.Fatal("error = nil when the daemon refused the inspect")
	}
}

// TestResolveFromSelfMountsUsesTheClient is the seam test: the client reads the
// daemon's inspect payload and the resolver turns it into the mapping the
// container creation paths use.
func TestResolveFromSelfMountsUsesTheClient(t *testing.T) {
	body := `{"Id":"abc123","Mounts":[{"Type":"bind","Source":"/srv/carbon-panel/data","Destination":"/app/data","RW":true}]}`
	cli := selfInspectServer(t, body, http.StatusOK)

	mapping, err := hostmount.Resolve(context.Background(), cli, hostmount.Dirs{
		DataDir:   "/app/data",
		BackupDir: "/app/backups",
		TempDir:   "/app/tmp",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got, want := mapping.ToHost("/app/data/servers/s1"), "/srv/carbon-panel/data/servers/s1"; got != want {
		t.Fatalf("ToHost = %q, want %q", got, want)
	}
}
