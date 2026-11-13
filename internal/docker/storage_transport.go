// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/containerd/errdefs"
	stdcopy "github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	storageWorkerMount         = "/carbon-storage-root"
	storageWorkerBin           = "/usr/local/bin/carbon-storage"
	storageWorkerNameLabel     = "com.carbonpanel.storage-worker"
	storageWorkerShutdownGrace = time.Second
)

// StorageTransport runs the carbon-storage accessor worker on remote daemons
// through a one-shot attached container (OSS-281).
type StorageTransport struct {
	pool        *ClientPool
	log         *logger.Logger
	workerImage string
}

// NewStorageTransport constructs a nodestorage.Transport backed by Docker.
func NewStorageTransport(pool *ClientPool, log *logger.Logger, workerImage string) *StorageTransport {
	return &StorageTransport{pool: pool, log: log, workerImage: workerImage}
}

// Open implements nodestorage.Transport.
func (t *StorageTransport) Open(ctx context.Context, node nodestorage.NodeInfo, dataRoot string) (nodestorage.Accessor, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t == nil || t.pool == nil || t.workerImage == "" {
		return nil, fmt.Errorf("%w: remote storage worker image is not configured", nodestorage.ErrUnavailable)
	}
	nodeID := node.ID
	if nodeID == "" {
		nodeID = node.Name
	}
	dockerCli, err := t.pool.GetClient(nodeID)
	if err != nil {
		return nil, fmt.Errorf("%w: docker client for node %q: %v", nodestorage.ErrUnavailable, node.Name, err)
	}
	hostRoot := nodestorage.NodePath(node, dataRoot, dataRoot)
	sess, err := dockerCli.startStorageWorker(ctx, t.workerImage, hostRoot)
	if err != nil {
		return nil, fmt.Errorf("%w: storage worker on node %q: %w", nodestorage.ErrUnavailable, node.Name, err)
	}
	return storageworker.NewRemoteAccessor(sess), nil
}

func (c *Client) startStorageWorker(ctx context.Context, image, hostDataRoot string) (*storageworker.Session, error) {
	if c == nil || c.docker == nil {
		return nil, fmt.Errorf("docker client is not available")
	}
	config := &container.Config{
		Image: image,
		Cmd: []string{
			storageWorkerBin,
			"--mode=accessor",
			"--root", storageWorkerMount,
		},
		OpenStdin:    true,
		StdinOnce:    true,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          false,
		Labels: map[string]string{
			storageWorkerNameLabel: "true",
		},
	}
	hostConfig := &container.HostConfig{
		Binds:      []string{fmt.Sprintf("%s:%s:rw", hostDataRoot, storageWorkerMount)},
		AutoRemove: true,
	}
	resp, err := c.docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     config,
		HostConfig: hostConfig,
	})
	if err != nil {
		return nil, fmt.Errorf("create storage worker: %w", err)
	}
	attach, err := c.docker.ContainerAttach(ctx, resp.ID, client.ContainerAttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
	if err != nil {
		return nil, errors.Join(fmt.Errorf("attach storage worker: %w", err), c.removeStorageWorker(resp.ID))
	}
	if _, err := c.docker.ContainerStart(ctx, resp.ID, client.ContainerStartOptions{}); err != nil {
		attach.Close()
		return nil, errors.Join(fmt.Errorf("start storage worker: %w", err), c.removeStorageWorker(resp.ID))
	}
	stdoutR, stdoutW := io.Pipe()
	go func() {
		_, _ = stdcopy.StdCopy(stdoutW, io.Discard, attach.Reader)
		_ = stdoutW.Close()
	}()
	var closeOnce sync.Once
	var closeErr error
	closeF := func() error {
		closeOnce.Do(func() {
			if attach.Conn != nil {
				_ = attach.Conn.Close()
			}
			attach.Close()
			_ = stdoutR.Close()
			// StdinOnce makes attach disconnect deliver EOF. Let the worker
			// abort its owned transactions before force-removal can kill it.
			closeErr = errors.Join(c.waitStorageWorkerExit(resp.ID), c.removeStorageWorker(resp.ID))
		})
		return closeErr
	}
	return storageworker.NewSessionContext(ctx, attach.Conn, stdoutR, closeF)
}

func (c *Client) waitStorageWorkerExit(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), storageWorkerShutdownGrace)
	defer cancel()
	wait := c.docker.ContainerWait(ctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case result := <-wait.Result:
		if result.Error != nil {
			return fmt.Errorf("storage worker %q exit wait failed: %s", id, result.Error.Message)
		}
		return nil
	case err := <-wait.Error:
		if errdefs.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("wait for storage worker %q shutdown; forced removal may leave recovery staging: %w", id, err)
	}
}

// Cleanup must outlive a canceled operation, but must not wait indefinitely.
func (c *Client) removeStorageWorker(id string) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultAPITimeout)
	defer cancel()
	_, err := c.docker.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
	if err == nil || errdefs.IsNotFound(err) {
		return nil
	}
	return fmt.Errorf("remove storage worker %q: %w", id, err)
}

var _ nodestorage.Transport = (*StorageTransport)(nil)
