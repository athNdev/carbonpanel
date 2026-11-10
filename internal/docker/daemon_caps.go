// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"strings"

	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

// DaemonRootlessFromInfo reports whether Docker Engine reports rootless mode.
func DaemonRootlessFromInfo(info system.Info) bool {
	for _, opt := range info.SecurityOptions {
		if strings.EqualFold(strings.TrimSpace(opt), "name=rootless") {
			return true
		}
	}
	for _, w := range info.Warnings {
		lower := strings.ToLower(w)
		if strings.Contains(lower, "rootless") {
			return true
		}
	}
	return false
}

// queryDaemonRootless asks the connected daemon whether it runs rootless. A
// lookup failure is treated as not rootless so a transient API error does not
// block creation on rootful daemons.
func (c *Client) queryDaemonRootless(ctx context.Context) bool {
	if c == nil || c.docker == nil {
		return false
	}
	bctx, cancel := boundedCall(ctx)
	defer cancel()
	res, err := c.docker.Info(bctx, client.InfoOptions{})
	if err != nil {
		return false
	}
	return DaemonRootlessFromInfo(res.Info)
}
