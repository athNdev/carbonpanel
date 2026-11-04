// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostmount

import (
	"context"
	"errors"
	"testing"
)

// hostNetworked is the case this fallback exists for: the container shares the
// host's UTS namespace, so its hostname is the machine name and the daemon has
// no container to inspect. SelfMounts then correctly reports "not a container".
type hostNetworked struct{}

func (hostNetworked) SelfMounts(context.Context) ([]Mount, error) { return nil, nil }

func TestResolveUsesTheKernelMountTableWhenTheDaemonCannotSeeUs(t *testing.T) {
	withMountinfo(t, sampleMountinfo)

	mapping, err := Resolve(context.Background(), hostNetworked{}, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !mapping.Translated {
		t.Fatalf("mapping = %+v, want a translated mapping", mapping)
	}
	if mapping.HostDir != "/srv/carbon-panel/data" {
		t.Errorf("host dir = %q, want the bind source from mountinfo", mapping.HostDir)
	}
	if mapping.Origin != "container mount table" {
		t.Errorf("origin = %q, want it to name the mount table", mapping.Origin)
	}
	if got, want := mapping.ToHost("/app/data/servers/a"), "/srv/carbon-panel/data/servers/a"; got != want {
		t.Errorf("ToHost = %q, want %q", got, want)
	}
}

func TestResolvePrefersTheDaemonOverTheMountTable(t *testing.T) {
	withMountinfo(t, sampleMountinfo)
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "/daemon/says/here", Destination: "/app/data", RW: true},
	}}

	mapping, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mapping.HostDir != "/daemon/says/here" {
		t.Fatalf("host dir = %q, want the daemon's answer", mapping.HostDir)
	}
}

func TestResolveStillReportsAnUnmountedDirectory(t *testing.T) {
	// Only /app/backups is mounted; the data directory is not.
	const backupsOnly = `1052 1049 8:1 / / rw,relatime - ext4 /dev/sda1 rw
1061 1052 8:1 /srv/carbon-panel/backups /app/backups rw,relatime - ext4 /dev/sda1 rw
`
	withMountinfo(t, backupsOnly)

	_, err := Resolve(context.Background(), hostNetworked{}, dirsFor("/app/data"))
	if !errors.Is(err, ErrNotMounted) {
		t.Fatalf("error = %v, want ErrNotMounted", err)
	}
}

func TestPersistentHostPathIgnoresAMountOfTheFilesystemRoot(t *testing.T) {
	// root "/" means the mount shares the filesystem root and says nothing
	// about where the directory lives on the host.
	const rootMount = `1060 1052 8:1 / /app/data rw,relatime - ext4 /dev/sda1 rw
`
	withMountinfo(t, rootMount)

	if hostDir, ok := persistentHostPath(Dirs{DataDir: "/app/data"}); ok {
		t.Fatalf("persistentHostPath = %q, want no answer for a root-mounted path", hostDir)
	}
}

func TestPersistentHostPathPicksTheDeepestMount(t *testing.T) {
	const nested = `1060 1052 8:1 /srv/outer /app rw,relatime - ext4 /dev/sda1 rw
1061 1052 8:1 /srv/data /app/data rw,relatime - ext4 /dev/sda1 rw
`
	withMountinfo(t, nested)

	hostDir, ok := persistentHostPath(Dirs{DataDir: "/app/data/servers/x"})
	if !ok {
		t.Fatal("persistentHostPath found nothing")
	}
	if hostDir != "/srv/data" {
		t.Fatalf("host dir = %q, want the deepest mount's source", hostDir)
	}
}
