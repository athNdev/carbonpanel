// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package metrics

import (
	"context"
	"fmt"
)

// ImageDiskUsageReader measures Docker image-store bytes on a node.
type ImageDiskUsageReader interface {
	ImageStoreBytes(ctx context.Context, nodeID string) (int64, error)
}

type dockerImageDiskReader struct {
	collector *Collector
}

func newDockerImageDiskReader(c *Collector) ImageDiskUsageReader {
	return &dockerImageDiskReader{collector: c}
}

func (r *dockerImageDiskReader) ImageStoreBytes(ctx context.Context, nodeID string) (int64, error) {
	if r == nil || r.collector == nil {
		return 0, fmt.Errorf("metrics collector unavailable")
	}
	cli, err := r.collector.resolveDocker(nodeID)
	if err != nil {
		return 0, err
	}
	return cli.ImageStoreTotalBytes(ctx)
}
