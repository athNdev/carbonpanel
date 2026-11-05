// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

func TestApplyServerWorkTempUsesDataVolumeNotTmpfs(t *testing.T) {
	host := &container.HostConfig{}
	config := &container.Config{Env: []string{
		"TMPDIR=/tmp",
		"JAVA_TOOL_OPTIONS=-Djava.io.tmpdir=/tmp -Djava.net.preferIPv4Stack=true",
		"EULA=TRUE",
	}}
	applyServerRootfs(host, config, nil)
	applyServerRootfs(host, config, nil)

	if got := host.Tmpfs["/tmp"]; got != "rw,noexec,nosuid,size=64m" {
		t.Fatalf("/tmp tmpfs = %q, want the 64 MiB noexec tmpfs", got)
	}
	if got, n := envValues(config.Env, "TMPDIR"); n != 1 || got != serverWorkTempContainerPath {
		t.Fatalf("TMPDIR = %q (%d entries), want one %s", got, n, serverWorkTempContainerPath)
	}
	got, n := envValues(config.Env, "JAVA_TOOL_OPTIONS")
	if n != 1 {
		t.Fatalf("JAVA_TOOL_OPTIONS entries = %d, want 1 (%q)", n, got)
	}
	if !strings.Contains(got, javaIOTmpdirFlag) || strings.Contains(got, "-Djava.io.tmpdir=/tmp") {
		t.Fatalf("JAVA_TOOL_OPTIONS = %q, want %s and not /tmp", got, javaIOTmpdirFlag)
	}
	if !strings.Contains(got, "-Djava.net.preferIPv4Stack=true") {
		t.Fatalf("JAVA_TOOL_OPTIONS = %q, dropped an existing option", got)
	}
	if _, n := envValues(config.Env, "EULA"); n != 1 {
		t.Fatalf("EULA entries = %d, want the original value kept once", n)
	}
	if strings.Contains(serverWorkTempContainerPath, `\`) || !strings.HasPrefix(serverWorkTempContainerPath, "/data/") {
		t.Fatalf("container work temp = %q, want a /data path", serverWorkTempContainerPath)
	}
}

func TestWorkTempMountFollowsTheDataBind(t *testing.T) {
	host := &container.HostConfig{Mounts: []mount.Mount{{
		Type:   mount.TypeBind,
		Source: "/srv/worlds/one",
		Target: "/data",
	}}}
	config := &container.Config{}
	applyServerRootfs(host, config, nil)
	applyServerRootfs(host, config, nil)

	var found int
	for _, m := range host.Mounts {
		if m.Target != serverWorkTempContainerPath {
			continue
		}
		found++
		if m.Source != "/srv/worlds/one/.tmp" && m.Source != serverWorkTempHostPath("/srv/worlds/one") {
			t.Fatalf("work temp source = %q", m.Source)
		}
		if m.BindOptions == nil || !m.BindOptions.CreateMountpoint {
			t.Fatal("work temp mount must be created on the daemon that owns the world")
		}
		if m.ReadOnly {
			t.Fatal("work temp mount is read-only")
		}
	}
	if found != 1 {
		t.Fatalf("work temp mounts = %d, want 1", found)
	}
}

func TestEnsureServerWorkTempIsStickyAndWritable(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(serverWorkTempHostPath(root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureServerWorkTemp(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(serverWorkTempHostPath(root))
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatal("work temp is not a directory")
	}
	if runtime.GOOS == "linux" {
		if info.Mode().Perm() != 0o777 || info.Mode()&os.ModeSticky == 0 {
			t.Fatalf("mode = %o, want 1777", info.Mode())
		}
	}
	if err := ensureServerWorkTemp(root); err != nil {
		t.Fatal(err)
	}
}

func envValues(env []string, key string) (string, int) {
	n := 0
	value := ""
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(k, key) {
			n++
			value = v
		}
	}
	return value, n
}
