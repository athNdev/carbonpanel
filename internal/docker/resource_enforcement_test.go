// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"slices"
	"testing"

	"github.com/moby/moby/api/types/system"
)

func TestViewResourceEnforcementDoesNotClaimMemoryWhenUnsupported(t *testing.T) {
	daemon := DescribeDaemonEnforcement(system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		MemoryLimit:   false,
		SwapLimit:     true,
		PidsLimit:     true,
		Driver:        "overlay2",
	})
	profile := DescribeConfiguredResourceLimits(2048, 0, 0, 0, &daemon)
	view := ViewResourceEnforcement(profile, true, 0)
	if view.RequestedMemoryBytes == 0 {
		t.Fatal("RequestedMemoryBytes = 0, want the configured request")
	}
	if view.EffectiveMemoryBytes != 0 {
		t.Fatalf("EffectiveMemoryBytes = %d, want 0 when /info says memory limits are unsupported", view.EffectiveMemoryBytes)
	}
	if view.MemoryEnforced {
		t.Fatal("MemoryEnforced = true, want false when memory_limit_unsupported")
	}
	if !slices.Contains(view.Warnings, string(RuntimeIssueMemoryLimitUnsupported)) {
		t.Fatalf("warnings = %v, want memory_limit_unsupported", view.Warnings)
	}
}

func TestViewResourceEnforcementDoesNotClaimWorldQuotaFromStorageOpt(t *testing.T) {
	daemon := qualifiedDaemon()
	profile := DescribeConfiguredResourceLimits(2048, 256, 0, 0, &daemon)
	view := ViewResourceEnforcement(profile, true, 0)
	if view.WorldDiskEnforced {
		t.Fatal("WorldDiskEnforced = true, want false: storage-opt is not a world quota")
	}
	if !slices.Contains(view.Warnings, string(RuntimeIssueStorageQuotaIsNotAWorldQuota)) {
		t.Fatalf("warnings = %v, want storage_quota_is_not_a_world_quota", view.Warnings)
	}
	if view.MemoryEnforced && view.WorldDiskEnforced {
		t.Fatal("filling the memory budget must not imply world disk is bounded")
	}
}

func TestViewResourceEnforcementLeavesMemoryUnenforcedWithoutDaemonInfo(t *testing.T) {
	profile := DescribeConfiguredResourceLimits(2048, 0, 128, 0, nil)
	view := ViewResourceEnforcement(profile, false, 128)
	if view.MemoryEnforced {
		t.Fatal("MemoryEnforced = true, want false until /info is qualified")
	}
	if view.WorldDiskEnforced {
		t.Fatal("WorldDiskEnforced = true, want false")
	}
	if !view.WorldQuotaAdmission {
		t.Fatal("WorldQuotaAdmission = false, want true when world_quota_mb is set")
	}
}

func TestViewResourceEnforcementWritableLayerUnsupportedIsNotWorldQuota(t *testing.T) {
	daemon := DescribeDaemonEnforcement(system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		MemoryLimit:   true,
		SwapLimit:     true,
		PidsLimit:     true,
		Driver:        "vfs",
	})
	profile := DescribeConfiguredResourceLimits(2048, 256, 0, 0, &daemon)
	view := ViewResourceEnforcement(profile, true, 0)
	if view.WorldDiskEnforced {
		t.Fatal("WorldDiskEnforced = true, want false")
	}
	if !slices.Contains(view.Warnings, string(RuntimeIssueWritableLayerCapUnsupported)) &&
		!slices.Contains(view.Warnings, string(RuntimeIssueStorageQuotaIsNotAWorldQuota)) {
		t.Fatalf("warnings = %v, want writable-layer advisory", view.Warnings)
	}
}
