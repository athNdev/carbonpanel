// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"strings"

	"github.com/athNdev/carbon-panel/pkg/runtimeprofile"
	"github.com/moby/moby/api/types/system"
)

// DaemonEnforcement is the public daemon capability record. DescribeDaemonEnforcement
// maps a Docker /info payload onto it.
type DaemonEnforcement = runtimeprofile.DaemonEnforcement

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
