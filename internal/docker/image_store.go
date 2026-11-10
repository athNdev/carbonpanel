// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"errors"
	"fmt"

	"github.com/moby/moby/client"
)

// ImageStoreTotalBytes reports deduplicated Docker image-store bytes on the node.
func (c *Client) ImageStoreTotalBytes(ctx context.Context) (int64, error) {
	if c == nil || c.docker == nil {
		return 0, errors.New("docker client unavailable")
	}
	du, err := c.docker.DiskUsage(ctx, client.DiskUsageOptions{})
	if err != nil {
		return 0, fmt.Errorf("docker disk usage: %w", err)
	}
	return du.Images.TotalSize, nil
}
