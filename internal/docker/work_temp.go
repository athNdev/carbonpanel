// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/pkg/files"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
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
)

// applyServerWorkTemp points TMPDIR and java.io.tmpdir at the data-volume
// work directory. An operator value of /tmp is replaced: that path is the
// noexec tmpfs this contract exists to avoid. Other JAVA_TOOL_OPTIONS are
// kept.
func applyServerWorkTemp(config *container.Config) {
	if config == nil {
		return
	}
	config.Env = upsertEnv(config.Env, "TMPDIR", serverWorkTempContainerPath)
	config.Env = upsertEnv(config.Env, "JAVA_TOOL_OPTIONS", mergeJavaIOTmpdir(envValue(config.Env, "JAVA_TOOL_OPTIONS"), javaIOTmpdirFlag))
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

// ensureServerWorkTemp creates the work directory on a data path this
// process can see. 01777 matches a sticky world-writable temp directory so
// the unprivileged game user can write and execute there without a
// hard-coded uid. Adoption still copies the data directory's owner when
// that owner is already known.
func ensureServerWorkTemp(dataPath string) error {
	dir := serverWorkTempHostPath(dataPath)
	if err := os.MkdirAll(dir, 0o1777); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o1777); err != nil {
		return err
	}
	// 01777 is what lets the game user write. Matching the data directory's
	// owner is best-effort and must not refuse creation.
	_ = files.AdoptOwnership(dataPath, dir, false)
	return nil
}

func workTempSetupName(serverID string) string {
	var b strings.Builder
	b.WriteString("carbon-panel-worktemp-")
	for _, r := range serverID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	if b.Len() == len("carbon-panel-worktemp-") {
		b.WriteString("server")
	}
	return b.String()
}

func serverWorkTempSetupCommand() string {
	return "mkdir -p " + serverWorkTempContainerPath + " && chmod 1777 " + serverWorkTempContainerPath
}

// ensureRemoteWorkTemp creates /data/.tmp on the daemon that owns dataPath.
// The panel process must not mkdir a remote node's path locally. The
// container is not the game server: it does not carry managed-server labels,
// and it is removed before CreateContainer returns.
func (c *Client) ensureRemoteWorkTemp(ctx context.Context, image, dataPath, serverID string) error {
	if c == nil || c.docker == nil {
		return fmt.Errorf("docker client is nil")
	}
	image = strings.TrimSpace(image)
	dataPath = strings.TrimSpace(dataPath)
	if image == "" {
		return fmt.Errorf("server image is required to create the work temp directory")
	}
	if dataPath == "" {
		return fmt.Errorf("server data path is required to create the work temp directory")
	}

	name := workTempSetupName(serverID)
	if _, err := c.docker.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		return fmt.Errorf("failed to remove previous work temp setup container: %w", err)
	}

	created, err := c.docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:      image,
			Entrypoint: []string{"/bin/sh", "-c", serverWorkTempSetupCommand()},
			User:       "0:0",
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode("none"),
			Mounts: []mount.Mount{{
				Type:   mount.TypeBind,
				Source: dataPath,
				Target: "/data",
				BindOptions: &mount.BindOptions{
					CreateMountpoint: true,
				},
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("failed to create work temp directory on the server data volume: %w", err)
	}
	id := created.ID
	defer func() {
		rmCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_, _ = c.docker.ContainerRemove(rmCtx, id, client.ContainerRemoveOptions{Force: true})
	}()

	wait := c.docker.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := c.docker.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("failed to start work temp setup: %w", err)
	}
	select {
	case <-ctx.Done():
		return fmt.Errorf("work temp setup cancelled: %w", ctx.Err())
	case err := <-wait.Error:
		if err != nil {
			return fmt.Errorf("work temp setup wait failed: %w", err)
		}
	case res := <-wait.Result:
		if res.StatusCode != 0 {
			detail := ""
			if res.Error != nil {
				detail = res.Error.Message
			}
			return fmt.Errorf("work temp setup exited %d: %s", res.StatusCode, detail)
		}
	}
	return nil
}
