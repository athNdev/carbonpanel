// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"testing"

	"github.com/moby/moby/api/types/system"
)

func TestDescribeDaemonEnforcementRootfulCgroupV2(t *testing.T) {
	info := system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		Driver:        "overlay2",
		DriverStatus: [][2]string{
			{"Backing Filesystem", "xfs"},
		},
		MemoryLimit: true,
		SwapLimit:   true,
		PidsLimit:   true,
		SecurityOptions: []string{
			"name=seccomp,profile=default",
		},
	}
	got := DescribeDaemonEnforcement(info)
	if got.Rootless {
		t.Fatal("Rootless = true, want false")
	}
	if !got.MemoryLimitSupported || !got.SwapLimitSupported || !got.PidsLimitSupported {
		t.Fatalf("cgroup support = %+v, want all true", got)
	}
	if !got.WritableLayerQuotaSupported {
		t.Fatal("WritableLayerQuotaSupported = false, want true for overlay2+xfs")
	}
	if !got.CgroupLimitsQualified() {
		t.Fatal("CgroupLimitsQualified = false, want true")
	}
}

func TestDescribeDaemonEnforcementRootlessCgroupV1Unqualified(t *testing.T) {
	info := system.Info{
		CgroupDriver:  "cgroupfs",
		CgroupVersion: "1",
		Driver:        "overlay2",
		MemoryLimit:   true,
		SwapLimit:     true,
		PidsLimit:     true,
		SecurityOptions: []string{
			"name=rootless",
		},
	}
	got := DescribeDaemonEnforcement(info)
	if !got.Rootless {
		t.Fatal("Rootless = false, want true")
	}
	if got.CgroupLimitsQualified() {
		t.Fatal("CgroupLimitsQualified = true, want false on rootless cgroup v1")
	}
}

func TestDescribeDaemonEnforcementNonOverlayNoLayerQuota(t *testing.T) {
	info := system.Info{
		Driver: "vfs",
	}
	got := DescribeDaemonEnforcement(info)
	if got.WritableLayerQuotaSupported {
		t.Fatal("WritableLayerQuotaSupported = true, want false for vfs")
	}
}
