// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package runtimeprofile

// DaemonEnforcement records which cgroup and storage limits the Docker daemon
// reports as supported. Callers compare this against the resolved container
// request so an ignored limit is never presented as enforced.
type DaemonEnforcement struct {
	Rootless                    bool
	CgroupDriver                string
	CgroupVersion               string
	StorageDriver               string
	MemoryLimitSupported        bool
	SwapLimitSupported          bool
	PidsLimitSupported          bool
	WritableLayerQuotaSupported bool
}

// CgroupLimitsQualified reports whether memory/swap/pids caps are expected to
// apply on this daemon. Rootless mode needs cgroup v2 delegation; a v1 rootless
// daemon may ignore resource flags even when MemoryLimit is true.
func (d DaemonEnforcement) CgroupLimitsQualified() bool {
	if d.Rootless && d.CgroupVersion != "2" {
		return false
	}
	return d.MemoryLimitSupported && d.SwapLimitSupported && d.PidsLimitSupported
}
