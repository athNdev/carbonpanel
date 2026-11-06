// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"

	stdcopy "github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// maxTailContainerLogs bounds one-shot log reads so failure detection cannot
// turn into a memory hog on chatty servers.
const maxTailContainerLogs = 1000

// TailContainerLogs returns the last tailLines of a container's combined
// stdout/stderr log, each prefixed with its daemon timestamp
// ("2006-01-02T15:04:05.999Z message"), oldest first. It is a one-shot read
// for failure detection, not a stream: callers that need live output use the
// log streamer instead.
func (c *Client) TailContainerLogs(ctx context.Context, containerID string, tailLines int) ([]string, error) {
	ctx, cancel := boundedCall(ctx)
	defer cancel()

	if tailLines <= 0 || tailLines > maxTailContainerLogs {
		tailLines = maxTailContainerLogs
	}

	inspectResult, err := c.docker.ContainerInspect(ctx, containerID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	tty := inspectResult.Container.Config != nil && inspectResult.Container.Config.Tty

	reader, err := c.docker.ContainerLogs(ctx, containerID, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Timestamps: true,
		Tail:       strconv.Itoa(tailLines),
	})
	if err != nil {
		return nil, err
	}
	defer reader.Close()

	// Demultiplex into a single buffer so stdout/stderr stay interleaved in
	// emission order; failure evidence reads as one timeline.
	var buf bytes.Buffer
	if tty {
		if _, err := io.Copy(&buf, reader); err != nil {
			return nil, fmt.Errorf("read container logs: %w", err)
		}
	} else {
		if _, err := stdcopy.StdCopy(&buf, &buf, reader); err != nil && err != io.EOF {
			return nil, fmt.Errorf("read container logs: %w", err)
		}
	}

	var lines []string
	scanner := bufio.NewScanner(&buf)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read container logs: %w", err)
	}
	return lines, nil
}
