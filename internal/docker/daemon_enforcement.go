// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"strings"

	"github.com/moby/moby/api/types/system"
)

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

// DescribeDaemonEnforcement maps a Docker /info payload into the panel's
// qualification view. It uses the daemon's own capability flags instead of
// guessing from the OS string alone.
func DescribeDaemonEnforcement(info system.Info) DaemonEnforcement {
	return DaemonEnforcement{
		Rootless:                    securityOptionRootless(info.SecurityOptions),
		CgroupDriver:                strings.TrimSpace(info.CgroupDriver),
		CgroupVersion:               strings.TrimSpace(info.CgroupVersion),
		StorageDriver:               strings.TrimSpace(info.Driver),
		MemoryLimitSupported:        info.MemoryLimit,
		SwapLimitSupported:          info.SwapLimit,
		PidsLimitSupported:          info.PidsLimit,
		WritableLayerQuotaSupported: overlayWritableQuotaSupported(info.Driver, info.DriverStatus),
	}
}

func securityOptionRootless(opts []string) bool {
	for _, opt := range opts {
		lower := strings.ToLower(strings.TrimSpace(opt))
		if strings.Contains(lower, "rootless") {
			return true
		}
	}
	return false
}

// overlayWritableQuotaSupported reports whether storage-opt size on overlay2 is
// expected to work. Docker documents xfs+pquota on the backing filesystem; other
// drivers and overlay on non-xfs do not provide a portable layer cap.
func overlayWritableQuotaSupported(driver string, status [][2]string) bool {
	if !strings.EqualFold(strings.TrimSpace(driver), "overlay2") {
		return false
	}
	for _, pair := range status {
		if len(pair) != 2 {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(pair[0]), "Backing Filesystem") {
			return strings.Contains(strings.ToLower(pair[1]), "xfs")
		}
	}
	return false
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
