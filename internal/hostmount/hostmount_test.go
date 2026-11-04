// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package hostmount

import (
	"context"
	"errors"
	"os"
	"runtime"
	"strings"
	"testing"
)

type fakeInspector struct {
	mounts []Mount
	err    error
}

func (f fakeInspector) SelfMounts(context.Context) ([]Mount, error) {
	return f.mounts, f.err
}

func dirsFor(dataDir string) Dirs {
	return Dirs{DataDir: dataDir, BackupDir: dataDir, TempDir: dataDir}
}

func TestValidateDirsAcceptsWritableAbsoluteDirs(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateDirs(dirsFor(dir)); err != nil {
		t.Fatalf("ValidateDirs: %v", err)
	}
	// The probe must not leave anything behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".carbon-panel-writecheck-") {
			t.Errorf("left a probe file behind: %s", entry.Name())
		}
	}
}

func TestValidateDirsRejectsARelativePath(t *testing.T) {
	err := ValidateDirs(Dirs{DataDir: "data", BackupDir: "data", TempDir: "data"})
	if err == nil {
		t.Fatal("error = nil for a relative data directory")
	}
	if !strings.Contains(err.Error(), "mount path should be absolute") {
		t.Fatalf("error = %q, want it to say the mount path should be absolute", err)
	}
}

func TestValidateDirsRejectsAnEmptyPath(t *testing.T) {
	err := ValidateDirs(Dirs{DataDir: "", BackupDir: t.TempDir(), TempDir: t.TempDir()})
	if err == nil {
		t.Fatal("error = nil for an empty data directory")
	}
}

func TestValidateDirsRejectsAnUnwritableDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits are not enforced the same way on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root can write to a read-only directory, so the probe cannot fail")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	err := ValidateDirs(dirsFor(dir))
	if err == nil {
		t.Fatal("error = nil for a read-only directory")
	}
	if !strings.Contains(err.Error(), "no write permission on the mount dir") {
		t.Fatalf("error = %q, want it to name the write permission problem", err)
	}
}

func TestResolveUsesTheContainerMount(t *testing.T) {
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "/srv/carbon-panel/data", Destination: "/app/data", RW: true},
	}}
	mapping, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !mapping.Translated {
		t.Fatalf("mapping = %+v, want a translated mapping", mapping)
	}
	if mapping.HostDir != "/srv/carbon-panel/data" {
		t.Errorf("host dir = %q, want /srv/carbon-panel/data", mapping.HostDir)
	}
	if got, want := mapping.ToHost("/app/data/servers/abc/world"), "/srv/carbon-panel/data/servers/abc/world"; got != want {
		t.Errorf("ToHost = %q, want %q", got, want)
	}
}

func TestResolvePicksTheDeepestCoveringMount(t *testing.T) {
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "/srv/outer", Destination: "/app", RW: true},
		{Kind: KindBind, Source: "/srv/data", Destination: "/app/data", RW: true},
	}}
	mapping, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mapping.HostDir != "/srv/data" {
		t.Fatalf("host dir = %q, want the deepest mount /srv/data", mapping.HostDir)
	}
}

func TestResolveRejectsAMissingMount(t *testing.T) {
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "/srv/backups", Destination: "/app/backups", RW: true},
	}}
	_, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if !errors.Is(err, ErrNotMounted) {
		t.Fatalf("error = %v, want ErrNotMounted", err)
	}
	if !strings.Contains(err.Error(), "/app/data") {
		t.Errorf("error = %q, want it to name the unmounted directory", err)
	}
}

func TestResolveRejectsAReadOnlyMount(t *testing.T) {
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "/srv/data", Destination: "/app/data", RW: false},
	}}
	_, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err == nil {
		t.Fatal("error = nil for a read-only data mount")
	}
	if !strings.Contains(err.Error(), "no write permission on the mount dir") {
		t.Fatalf("error = %q, want it to name the write permission problem", err)
	}
}

func TestResolveRejectsATmpfsDataDir(t *testing.T) {
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindTmpfs, Destination: "/app/data", RW: true},
	}}
	_, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err == nil {
		t.Fatal("error = nil for a tmpfs data directory")
	}
	if !strings.Contains(err.Error(), "tmpfs") {
		t.Fatalf("error = %q, want it to name tmpfs", err)
	}
}

func TestResolveRejectsARelativeHostSource(t *testing.T) {
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "opt/carbon-panel/data", Destination: "/app/data", RW: true},
	}}
	_, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err == nil {
		t.Fatal("error = nil for a relative host source")
	}
	if !strings.Contains(err.Error(), "mount path should be absolute") {
		t.Fatalf("error = %q, want the absolute-path reason", err)
	}
}

func TestResolveUsesANamedVolumePath(t *testing.T) {
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindVolume, Source: "/var/lib/docker/volumes/panel-data/_data", Destination: "/app/data", RW: true},
	}}
	mapping, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mapping.HostDir != "/var/lib/docker/volumes/panel-data/_data" {
		t.Fatalf("host dir = %q, want the daemon's volume path", mapping.HostDir)
	}
	if mapping.Origin != "container volume" {
		t.Errorf("origin = %q, want container volume", mapping.Origin)
	}
}

func TestResolveWithoutAContainerIsIdentity(t *testing.T) {
	mapping, err := Resolve(context.Background(), fakeInspector{}, dirsFor("/opt/carbon-panel/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mapping.Translated {
		t.Fatalf("mapping = %+v, want no translation for a host run", mapping)
	}
	if got, want := mapping.ToHost("/opt/carbon-panel/data/servers/a"), "/opt/carbon-panel/data/servers/a"; got != want {
		t.Errorf("ToHost = %q, want %q", got, want)
	}
}

func TestResolveFallsBackToTheLegacyOverride(t *testing.T) {
	t.Setenv("CARBONPANEL_HOST_DATA_PATH", "/srv/legacy")
	insp := fakeInspector{err: errors.New("socket proxy forbids container reads")}
	mapping, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mapping.HostDir != "/srv/legacy" {
		t.Fatalf("host dir = %q, want the legacy override", mapping.HostDir)
	}
	if !strings.Contains(mapping.Origin, "deprecated") {
		t.Errorf("origin = %q, want it to record the override as deprecated", mapping.Origin)
	}
}

func TestResolveUsesTheOverrideWhenNothingCoversTheDataDir(t *testing.T) {
	t.Setenv("CARBONPANEL_HOST_DATA_PATH", "/srv/override")
	insp := fakeInspector{mounts: []Mount{
		{Kind: KindBind, Source: "/srv/other", Destination: "/app/other", RW: true},
	}}
	mapping, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if mapping.HostDir != "/srv/override" {
		t.Fatalf("host dir = %q, want the override", mapping.HostDir)
	}
}

func TestResolveRejectsARelativeLegacyOverride(t *testing.T) {
	t.Setenv("CARBONPANEL_HOST_DATA_PATH", "srv/legacy")
	insp := fakeInspector{err: errors.New("socket proxy forbids container reads")}
	_, err := Resolve(context.Background(), insp, dirsFor("/app/data"))
	if err == nil {
		t.Fatal("error = nil for a relative override")
	}
	if !strings.Contains(err.Error(), "mount path should be absolute") {
		t.Fatalf("error = %q, want the absolute-path reason", err)
	}
}

func TestToHostLeavesPathsOutsideTheDataDirAlone(t *testing.T) {
	mapping := Mapping{ContainerDir: "/app/data", HostDir: "/srv/data", Translated: true}
	for _, path := range []string{"/etc/passwd", "/app/data", "/app/data/"} {
		got := mapping.ToHost(path)
		switch path {
		case "/app/data", "/app/data/":
			if got != "/srv/data" {
				t.Errorf("ToHost(%q) = %q, want /srv/data", path, got)
			}
		default:
			if got != path {
				t.Errorf("ToHost(%q) = %q, want it unchanged", path, got)
			}
		}
	}
	// A sibling with the same prefix must not be translated.
	if got := mapping.ToHost("/app/database/x"); got != "/app/database/x" {
		t.Errorf("ToHost translated a sibling path: %q", got)
	}
}

func TestTranslateUsesTheConfiguredMapping(t *testing.T) {
	t.Cleanup(func() { Configure(Mapping{}) })
	Configure(Mapping{ContainerDir: "/app/data", HostDir: "/srv/data", Translated: true})

	if got, want := Translate("/app/data/servers/a"), "/srv/data/servers/a"; got != want {
		t.Fatalf("Translate = %q, want %q", got, want)
	}
	if got := Translate("/tmp/x"); got != "/tmp/x" {
		t.Errorf("Translate(%q) = %q, want it unchanged", "/tmp/x", got)
	}
}
