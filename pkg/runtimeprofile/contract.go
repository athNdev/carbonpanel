// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package runtimeprofile

import (
	"errors"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// RunContract executes the shared runtime and storage fixture suite through
// describe. Panel's container create path and Cloud's node agent both pass
// their own describe function so the same cases cover both callers.
func RunContract(t *testing.T, describe func(RuntimeSpec) RuntimeProfile) {
	t.Helper()
	for _, tc := range contractCases() {
		t.Run(tc.name, func(t *testing.T) {
			spec := defaultContractSpec()
			if tc.mutate != nil {
				tc.mutate(&spec)
			}
			profile := describe(spec)
			err := PreflightProfile(profile)
			if tc.fatal {
				if !profile.Fatal() {
					t.Fatalf("expected a fatal profile, issues %v", issueCodes(profile))
				}
				if !errors.Is(err, ErrRuntimeIncompatible) {
					t.Fatalf("PreflightProfile() = %v, want ErrRuntimeIncompatible", err)
				}
			} else if err != nil || profile.Fatal() {
				t.Fatalf("PreflightProfile() = %v, issues %v", err, issueCodes(profile))
			}
			for _, code := range tc.codes {
				if !hasIssueCode(profile, code) {
					t.Fatalf("missing %s in %v", code, issueCodes(profile))
				}
				if tc.fatal && (err == nil || !strings.Contains(err.Error(), string(code))) {
					t.Fatalf("error %v does not name %s", err, code)
				}
			}
			if tc.check != nil {
				tc.check(t, profile)
			}
		})
	}
}

type contractCase struct {
	name   string
	mutate func(*RuntimeSpec)
	fatal  bool
	codes  []RuntimeIssueCode
	check  func(*testing.T, RuntimeProfile)
}

func contractCases() []contractCase {
	return []contractCase{
		{
			name: "effective minecraft config",
			check: func(t *testing.T, profile RuntimeProfile) {
				t.Helper()
				if !profile.ReadOnlyRootfs || !profile.NoNewPrivileges || !profile.DataWritable {
					t.Fatalf("effective config = read-only %t nnp %t data writable %t", profile.ReadOnlyRootfs, profile.NoNewPrivileges, profile.DataWritable)
				}
				if profile.Limits.WorldDiskEnforced {
					t.Fatal("WorldDiskEnforced = true, want false")
				}
				if profile.Limits.MemoryBytes == 0 || profile.Limits.PidsLimit == 0 {
					t.Fatalf("limits = memory %d pids %d", profile.Limits.MemoryBytes, profile.Limits.PidsLimit)
				}
			},
			codes: []RuntimeIssueCode{RuntimeIssueImageUnpinned},
		},
		{
			name: "image bootstrap without nsswitch skip",
			mutate: func(spec *RuntimeSpec) {
				spec.Config.Env = nil
			},
			fatal: true,
			codes: []RuntimeIssueCode{RuntimeIssueRootfsReadOnlyNsswitch},
		},
		{
			name: "bootstrap capabilities removed",
			mutate: func(spec *RuntimeSpec) {
				spec.HostConfig.CapAdd = nil
			},
			fatal: true,
			codes: []RuntimeIssueCode{RuntimeIssueBootstrapCapabilitiesMissing},
		},
		{
			name: "missing world mount",
			mutate: func(spec *RuntimeSpec) {
				spec.HostConfig.Mounts = nil
			},
			fatal: true,
			codes: []RuntimeIssueCode{RuntimeIssueDataMountMissing},
		},
		{
			name: "read-only world mount",
			mutate: func(spec *RuntimeSpec) {
				spec.HostConfig.Mounts[0].ReadOnly = true
			},
			fatal: true,
			codes: []RuntimeIssueCode{RuntimeIssueDataMountReadOnly},
		},
		{
			name: "world owner mismatch",
			mutate: func(spec *RuntimeSpec) {
				spec.Config.User = "1000:1000"
				uid := 0
				spec.DataOwnerUID = &uid
			},
			fatal: true,
			codes: []RuntimeIssueCode{RuntimeIssueDataOwnerMismatch},
		},
		{
			name: "writable layer cap is not a world quota",
			mutate: func(spec *RuntimeSpec) {
				spec.WritableLayerMB = 1024
				spec.HostConfig.StorageOpt = map[string]string{"size": "1024M"}
			},
			codes: []RuntimeIssueCode{RuntimeIssueStorageQuotaIsNotAWorldQuota},
			check: func(t *testing.T, profile RuntimeProfile) {
				t.Helper()
				if profile.Limits.WorldDiskEnforced {
					t.Fatal("storage-opt size set WorldDiskEnforced")
				}
				if profile.Limits.RequestedWritableLayerMB != 1024 {
					t.Fatalf("RequestedWritableLayerMB = %d", profile.Limits.RequestedWritableLayerMB)
				}
			},
		},
		{
			name: "measured world meets admission quota",
			mutate: func(spec *RuntimeSpec) {
				spec.WorldQuotaMB = 1
				spec.WorldBytesKnown = true
				spec.WorldBytes = 2 << 20
			},
			fatal: true,
			codes: []RuntimeIssueCode{RuntimeIssueWorldQuotaExceeded},
			check: func(t *testing.T, profile RuntimeProfile) {
				t.Helper()
				if profile.Limits.WorldDiskEnforced {
					t.Fatal("world admission set WorldDiskEnforced")
				}
			},
		},
		{
			name: "world quota unmeasured",
			mutate: func(spec *RuntimeSpec) {
				spec.WorldQuotaMB = 1
			},
			fatal: true,
			codes: []RuntimeIssueCode{RuntimeIssueWorldQuotaUnmeasured},
		},
	}
}

func defaultContractSpec() RuntimeSpec {
	pids := int64(512)
	memory := int64(2048 << 20)
	return RuntimeSpec{
		Image:             "itzg/minecraft-server:java21",
		DataPath:          "/srv/worlds/world-a",
		MinecraftImage:    true,
		RequestedMemoryMB: 2048,
		Config: &container.Config{
			Image: "itzg/minecraft-server:java21",
			Env:   []string{SkipNsswitchConf + "=TRUE"},
		},
		HostConfig: &container.HostConfig{
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			CapAdd:         MinecraftBootstrapCapabilities(),
			SecurityOpt:    []string{"no-new-privileges:true"},
			Mounts: []mount.Mount{{
				Type:   mount.TypeBind,
				Source: "/srv/worlds/world-a",
				Target: "/data",
			}},
			Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,size=64m"},
			Resources: container.Resources{
				Memory:     memory,
				MemorySwap: memory,
				PidsLimit:  &pids,
			},
		},
	}
}

func issueCodes(profile RuntimeProfile) []string {
	codes := make([]string, 0, len(profile.Issues))
	for _, issue := range profile.Issues {
		codes = append(codes, string(issue.Code))
	}
	return codes
}

func hasIssueCode(profile RuntimeProfile, code RuntimeIssueCode) bool {
	for _, issue := range profile.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}
