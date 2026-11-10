// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/moby/moby/api/types/system"
)

// qualifiedDaemon is a rootful cgroup v2 overlay2+xfs /info mapping: every
// cgroup and storage-opt cap the panel asks for is reported supported.
func qualifiedDaemon() DaemonEnforcement {
	return DescribeDaemonEnforcement(system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		Driver:        "overlay2",
		DriverStatus:  [][2]string{{"Backing Filesystem", "xfs"}},
		MemoryLimit:   true,
		SwapLimit:     true,
		PidsLimit:     true,
		SecurityOptions: []string{
			"name=seccomp,profile=default",
		},
	})
}

type effectiveLimitCase struct {
	name string
	// info, when set, is mapped through DescribeDaemonEnforcement the same way
	// CreateContainer maps Docker /info.
	info         *system.Info
	daemon       *DaemonEnforcement
	mutate       func(*RuntimeSpec)
	writableRoot bool

	wantRequestedMemory int64
	wantEffectiveMemory int64
	wantRequestedPids   int64
	wantEffectivePids   int64
	wantRequestedLayer  int64
	wantEffectiveLayer  int64
	wantWorldDisk       bool
	mustHave            []RuntimeIssueCode
	mustNot             []RuntimeIssueCode
}

func TestEffectiveLimitsMatchRequestVersusDaemonInfo(t *testing.T) {
	const memory = int64(2048 << 20)
	const pids = int64(512)

	rootlessV1 := system.Info{
		CgroupDriver:    "cgroupfs",
		CgroupVersion:   "1",
		Driver:          "overlay2",
		MemoryLimit:     true,
		SwapLimit:       true,
		PidsLimit:       true,
		SecurityOptions: []string{"name=rootless"},
	}
	noMemory := system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		Driver:        "overlay2",
		DriverStatus:  [][2]string{{"Backing Filesystem", "xfs"}},
		MemoryLimit:   false,
		SwapLimit:     true,
		PidsLimit:     true,
	}
	noSwap := system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		Driver:        "overlay2",
		DriverStatus:  [][2]string{{"Backing Filesystem", "xfs"}},
		MemoryLimit:   true,
		SwapLimit:     false,
		PidsLimit:     true,
	}
	noPids := system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		Driver:        "overlay2",
		DriverStatus:  [][2]string{{"Backing Filesystem", "xfs"}},
		MemoryLimit:   true,
		SwapLimit:     true,
		PidsLimit:     false,
	}
	vfs := system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		Driver:        "vfs",
		MemoryLimit:   true,
		SwapLimit:     true,
		PidsLimit:     true,
	}
	overlayXFS := system.Info{
		CgroupDriver:  "systemd",
		CgroupVersion: "2",
		Driver:        "overlay2",
		DriverStatus:  [][2]string{{"Backing Filesystem", "xfs"}},
		MemoryLimit:   true,
		SwapLimit:     true,
		PidsLimit:     true,
	}

	applyLayer := func(spec *RuntimeSpec) {
		ApplyDiskQuota(spec.HostConfig, 1024)
		spec.WritableLayerMB = 1024
	}

	cases := []effectiveLimitCase{
		{
			name:                "unqualified_daemon_reports_request_as_effective",
			wantRequestedMemory: memory,
			wantEffectiveMemory: memory,
			wantRequestedPids:   pids,
			wantEffectivePids:   pids,
			wantWorldDisk:       false,
			mustNot: []RuntimeIssueCode{
				RuntimeIssueMemoryLimitUnset,
				RuntimeIssueMemoryLimitUnsupported,
				RuntimeIssuePidsLimitUnset,
				RuntimeIssuePidsLimitUnsupported,
			},
		},
		{
			name:                "qualified_overlay_xfs_applies_cgroup_caps_not_world_disk",
			info:                &overlayXFS,
			wantRequestedMemory: memory,
			wantEffectiveMemory: memory,
			wantRequestedPids:   pids,
			wantEffectivePids:   pids,
			wantWorldDisk:       false,
			mustNot: []RuntimeIssueCode{
				RuntimeIssueMemoryLimitUnsupported,
				RuntimeIssueSwapLimitUnsupported,
				RuntimeIssuePidsLimitUnsupported,
			},
		},
		{
			name:                "memory_flag_false_zeros_effective_memory",
			info:                &noMemory,
			wantRequestedMemory: memory,
			wantEffectiveMemory: 0,
			wantRequestedPids:   pids,
			wantEffectivePids:   pids,
			wantWorldDisk:       false,
			mustHave:            []RuntimeIssueCode{RuntimeIssueMemoryLimitUnsupported},
			mustNot: []RuntimeIssueCode{
				RuntimeIssueMemoryLimitUnset,
				RuntimeIssuePidsLimitUnsupported,
			},
		},
		{
			name:                "swap_flag_false_keeps_memory_and_warns",
			info:                &noSwap,
			wantRequestedMemory: memory,
			wantEffectiveMemory: memory,
			wantRequestedPids:   pids,
			wantEffectivePids:   pids,
			wantWorldDisk:       false,
			mustHave:            []RuntimeIssueCode{RuntimeIssueSwapLimitUnsupported},
			mustNot:             []RuntimeIssueCode{RuntimeIssueMemoryLimitUnsupported},
		},
		{
			name:                "pids_flag_false_zeros_effective_pids",
			info:                &noPids,
			wantRequestedMemory: memory,
			wantEffectiveMemory: memory,
			wantRequestedPids:   pids,
			wantEffectivePids:   0,
			wantWorldDisk:       false,
			mustHave:            []RuntimeIssueCode{RuntimeIssuePidsLimitUnsupported},
			mustNot:             []RuntimeIssueCode{RuntimeIssuePidsLimitUnset, RuntimeIssueMemoryLimitUnsupported},
		},
		{
			name:                "rootless_cgroup_v1_zeros_cgroup_caps",
			info:                &rootlessV1,
			wantRequestedMemory: memory,
			wantEffectiveMemory: 0,
			wantRequestedPids:   pids,
			wantEffectivePids:   0,
			wantWorldDisk:       false,
			mustHave:            []RuntimeIssueCode{RuntimeIssueRootlessCgroupUnqualified},
			mustNot: []RuntimeIssueCode{
				RuntimeIssueMemoryLimitUnset,
				RuntimeIssuePidsLimitUnset,
				RuntimeIssueMemoryLimitUnsupported,
			},
		},
		{
			name:                "vfs_storage_zeros_writable_layer_not_world_disk",
			info:                &vfs,
			writableRoot:        true,
			mutate:              applyLayer,
			wantRequestedMemory: memory,
			wantEffectiveMemory: memory,
			wantRequestedPids:   pids,
			wantEffectivePids:   pids,
			wantRequestedLayer:  1024,
			wantEffectiveLayer:  0,
			wantWorldDisk:       false,
			mustHave: []RuntimeIssueCode{
				RuntimeIssueWritableLayerCapUnsupported,
				RuntimeIssueStorageQuotaIsNotAWorldQuota,
			},
			mustNot: []RuntimeIssueCode{RuntimeIssueWritableLayerUnbounded, RuntimeIssueWritableLayerCapIgnored},
		},
		{
			name:                "overlay_xfs_layer_cap_is_not_world_quota",
			info:                &overlayXFS,
			writableRoot:        true,
			mutate:              applyLayer,
			wantRequestedMemory: memory,
			wantEffectiveMemory: memory,
			wantRequestedPids:   pids,
			wantEffectivePids:   pids,
			wantRequestedLayer:  1024,
			wantEffectiveLayer:  1024,
			wantWorldDisk:       false,
			mustHave:            []RuntimeIssueCode{RuntimeIssueStorageQuotaIsNotAWorldQuota},
			mustNot: []RuntimeIssueCode{
				RuntimeIssueWritableLayerCapUnsupported,
				RuntimeIssueWorldQuotaExceeded,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var overrides *v1.DockerOverrides
			if tc.writableRoot {
				overrides = &v1.DockerOverrides{
					Environment: map[string]string{writableRootfsEnv: "1"},
				}
			}
			spec := resolvedMinecraftContainer(overrides, false)
			spec.RequestedMemoryMB = 2048
			switch {
			case tc.info != nil:
				enforcement := DescribeDaemonEnforcement(*tc.info)
				spec.Daemon = &enforcement
			case tc.daemon != nil:
				spec.Daemon = tc.daemon
			}
			if tc.mutate != nil {
				tc.mutate(&spec)
			}

			beforePriv := spec.HostConfig.Privileged
			beforeNNP := hasNoNewPrivileges(spec.HostConfig.SecurityOpt)
			beforeRO := spec.HostConfig.ReadonlyRootfs

			profile := DescribeRuntime(spec)
			assertQualificationDidNotBroadenPrivileges(t, spec, profile, beforePriv, beforeNNP, beforeRO)

			got := profile.Limits
			if got.RequestedMemoryBytes != tc.wantRequestedMemory {
				t.Errorf("RequestedMemoryBytes = %d, want %d", got.RequestedMemoryBytes, tc.wantRequestedMemory)
			}
			if got.MemoryBytes != tc.wantEffectiveMemory {
				t.Errorf("MemoryBytes (effective) = %d, want %d", got.MemoryBytes, tc.wantEffectiveMemory)
			}
			if got.RequestedPidsLimit != tc.wantRequestedPids {
				t.Errorf("RequestedPidsLimit = %d, want %d", got.RequestedPidsLimit, tc.wantRequestedPids)
			}
			if got.PidsLimit != tc.wantEffectivePids {
				t.Errorf("PidsLimit (effective) = %d, want %d", got.PidsLimit, tc.wantEffectivePids)
			}
			if got.RequestedWritableLayerMB != tc.wantRequestedLayer {
				t.Errorf("RequestedWritableLayerMB = %d, want %d", got.RequestedWritableLayerMB, tc.wantRequestedLayer)
			}
			if got.WritableLayerMB != tc.wantEffectiveLayer {
				t.Errorf("WritableLayerMB (effective) = %d, want %d", got.WritableLayerMB, tc.wantEffectiveLayer)
			}
			if got.WorldDiskEnforced != tc.wantWorldDisk {
				t.Errorf("WorldDiskEnforced = %t, want %t", got.WorldDiskEnforced, tc.wantWorldDisk)
			}
			for _, code := range tc.mustHave {
				if !hasIssue(profile, code) {
					t.Errorf("issues = %v, want %s", issueCodes(profile), code)
				}
			}
			for _, code := range tc.mustNot {
				if hasIssue(profile, code) {
					t.Errorf("issues = %v, must not include %s", issueCodes(profile), code)
				}
			}
		})
	}
}

func TestQualifiedDaemonHelperMatchesOverlayXfsInfo(t *testing.T) {
	got := qualifiedDaemon()
	if !got.MemoryLimitSupported || !got.WritableLayerQuotaSupported || !got.CgroupLimitsQualified() || got.Rootless {
		t.Fatalf("qualifiedDaemon() = %+v", got)
	}
}
