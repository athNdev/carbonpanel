// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

const (
	// serverWorkTempContainerPath is where modpack downloads, extracts, and
	// native libraries are written. It is the itzg image's own scratch
	// directory on the /data bind: the world disk, executable, and not the
	// 64 MiB noexec /tmp tmpfs.
	serverWorkTempContainerPath = "/data/.tmp"
	serverWorkTempDirName       = ".tmp"

	// javaIOTmpdirFlag is passed to every JVM. Files.createTempFile and
	// Netty/c2me native extraction use java.io.tmpdir. The HotSpot default
	// is /tmp, and a shell TMPDIR does not move it.
	javaIOTmpdirFlag = "-Djava.io.tmpdir=" + serverWorkTempContainerPath

	// serverWorkTempMode is a sticky world-writable directory. Go's
	// FileMode keeps the sticky bit in ModeSticky, not in the low 12 bits,
	// so 01777 alone would not set it.
	serverWorkTempMode os.FileMode = 0o777 | os.ModeSticky
)

// applyServerWorkTemp points TMPDIR and java.io.tmpdir at the data-volume
// work directory and bind-mounts that directory from the world path. An
// operator value of /tmp is replaced: that path is the noexec tmpfs this
// contract exists to avoid. Other JAVA_TOOL_OPTIONS are kept.
func applyServerWorkTemp(config *container.Config, hostConfig *container.HostConfig) {
	if config != nil {
		config.Env = upsertEnv(config.Env, "TMPDIR", serverWorkTempContainerPath)
		config.Env = upsertEnv(config.Env, "JAVA_TOOL_OPTIONS", mergeJavaIOTmpdir(envValue(config.Env, "JAVA_TOOL_OPTIONS"), javaIOTmpdirFlag))
	}
	ensureWorkTempMount(hostConfig)
}

func envValue(env []string, key string) string {
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return v
		}
	}
	return ""
}

func upsertEnv(env []string, key, value string) []string {
	kept := make([]string, 0, len(env)+1)
	for _, entry := range env {
		k, _, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			continue
		}
		kept = append(kept, entry)
	}
	return append(kept, key+"="+value)
}

// mergeJavaIOTmpdir drops any existing java.io.tmpdir property and appends
// flag. Other options stay in their original order.
func mergeJavaIOTmpdir(existing, flag string) string {
	fields := strings.Fields(existing)
	kept := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		if strings.HasPrefix(field, "-Djava.io.tmpdir=") {
			continue
		}
		kept = append(kept, field)
	}
	kept = append(kept, flag)
	return strings.Join(kept, " ")
}

func serverWorkTempHostPath(dataPath string) string {
	return filepath.Join(dataPath, serverWorkTempDirName)
}

// ensureWorkTempMount bind-mounts {data}/.tmp at /data/.tmp. Docker creates
// the host directory on the daemon that owns the world, including a remote
// node, so the panel does not mkdir a path it cannot see.
func ensureWorkTempMount(hostConfig *container.HostConfig) {
	if hostConfig == nil {
		return
	}
	for _, m := range hostConfig.Mounts {
		if m.Target == serverWorkTempContainerPath {
			return
		}
	}
	var dataSource string
	for _, m := range hostConfig.Mounts {
		if m.Target == "/data" && strings.TrimSpace(m.Source) != "" {
			dataSource = m.Source
			break
		}
	}
	if dataSource == "" {
		return
	}
	hostConfig.Mounts = append(hostConfig.Mounts, mount.Mount{
		Type:   mount.TypeBind,
		Source: serverWorkTempHostPath(dataSource),
		Target: serverWorkTempContainerPath,
		BindOptions: &mount.BindOptions{
			CreateMountpoint: true,
		},
	})
}

// ensureServerWorkTemp creates the work directory on a data path this
// process can see. The mode is sticky and world-writable so the
// unprivileged game user can write and execute there without a hard-coded
// uid. Adoption still copies the data directory's owner when that owner is
// already known.
func ensureServerWorkTemp(dataPath string) error {
	dir := serverWorkTempHostPath(dataPath)
	if err := os.MkdirAll(dir, serverWorkTempMode); err != nil {
		return err
	}
	if err := os.Chmod(dir, serverWorkTempMode); err != nil {
		return err
	}
	// The mode is what lets the game user write. Matching the data
	// directory's owner is best-effort and must not refuse creation.
	_ = files.AdoptOwnership(dataPath, dir, false)
	return nil
}
