// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"context"
	"errors"
	"testing"

	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

const matrixPinnedDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// runtimeMatrixCase is one supported or unsupported effective profile. Each row
// builds the resolved container request the same way CreateContainer does and
// asserts the named qualification outcome (OSS-238 / D34–D36).
type runtimeMatrixCase struct {
	name      string
	overrides *v1.DockerOverrides
	dataRO    bool
	ownerUID  *int
	rootless  bool
	// mutate adjusts the resolved spec after CreateContainer helpers run so a
	// stored or hostile request can be qualified as the daemon would see it.
	mutate            func(*RuntimeSpec)
	wantFatal         bool
	mustHave          []RuntimeIssueCode
	mustNot           []RuntimeIssueCode
	wantRootStartDrop *bool
	wantPinned        *bool
}

func boolPtr(v bool) *bool { return &v }

func TestRuntimeQualificationMatrix(t *testing.T) {
	owner := func(uid int) *int { return &uid }

	cases := []runtimeMatrixCase{
		{
			name:              "root_then_drop_read_only_root_with_nsswitch_skip",
			wantFatal:         false,
			wantRootStartDrop: boolPtr(true),
			wantPinned:        boolPtr(false),
			mustHave:          []RuntimeIssueCode{RuntimeIssueImageUnpinned},
			mustNot: []RuntimeIssueCode{
				RuntimeIssueRootfsReadOnlyNsswitch,
				RuntimeIssueDataMountMissing,
				RuntimeIssueDataMountReadOnly,
				RuntimeIssueBootstrapCapabilitiesMissing,
				RuntimeIssueDataOwnerMismatch,
				RuntimeIssueRootlessHostRootData,
				RuntimeIssuePrivilegedMode,
			},
		},
		{
			name:              "non_root_uid_matching_owner",
			overrides:         &v1.DockerOverrides{User: "1000:1000"},
			ownerUID:          owner(1000),
			wantFatal:         false,
			wantRootStartDrop: boolPtr(false),
			mustNot:           []RuntimeIssueCode{RuntimeIssueDataOwnerMismatch, RuntimeIssueRootlessHostRootData},
		},
		{
			name:              "non_root_uid_owner_mismatch",
			overrides:         &v1.DockerOverrides{User: "1000:1000"},
			ownerUID:          owner(0),
			wantFatal:         true,
			wantRootStartDrop: boolPtr(false),
			mustHave:          []RuntimeIssueCode{RuntimeIssueDataOwnerMismatch},
		},
		{
			name: "read_only_root_without_nsswitch_skip",
			mutate: func(spec *RuntimeSpec) {
				spec.Config.Env = nil
			},
			wantFatal: true,
			mustHave:  []RuntimeIssueCode{RuntimeIssueRootfsReadOnlyNsswitch},
		},
		{
			name: "writable_root_without_nsswitch_skip",
			overrides: &v1.DockerOverrides{
				Environment: map[string]string{writableRootfsEnv: "1"},
			},
			mutate: func(spec *RuntimeSpec) {
				spec.Config.Env = nil
			},
			wantFatal: false,
			mustHave:  []RuntimeIssueCode{RuntimeIssueWritableRootfs},
			mustNot:   []RuntimeIssueCode{RuntimeIssueRootfsReadOnlyNsswitch},
		},
		{
			name: "cap_drop_all_removing_bootstrap_caps",
			mutate: func(spec *RuntimeSpec) {
				spec.HostConfig.CapAdd = nil
				spec.HostConfig.CapDrop = []string{"ALL"}
			},
			wantFatal: true,
			mustHave:  []RuntimeIssueCode{RuntimeIssueBootstrapCapabilitiesMissing},
		},
		{
			name:      "cap_drop_all_override_keeps_bootstrap_capadd",
			overrides: &v1.DockerOverrides{CapDrop: []string{"ALL"}},
			wantFatal: false,
			mustNot:   []RuntimeIssueCode{RuntimeIssueBootstrapCapabilitiesMissing},
		},
		{
			name: "missing_writable_data",
			mutate: func(spec *RuntimeSpec) {
				spec.HostConfig.Mounts = nil
			},
			wantFatal: true,
			mustHave:  []RuntimeIssueCode{RuntimeIssueDataMountMissing},
		},
		{
			name: "read_only_data_mount",
			mutate: func(spec *RuntimeSpec) {
				spec.HostConfig.Mounts[0].ReadOnly = true
			},
			wantFatal: true,
			mustHave:  []RuntimeIssueCode{RuntimeIssueDataMountReadOnly},
		},
		{
			name:      "rootless_host_root_data",
			rootless:  true,
			ownerUID:  owner(0),
			wantFatal: true,
			mustHave:  []RuntimeIssueCode{RuntimeIssueRootlessHostRootData},
		},
		{
			name:      "rootless_game_owned_data",
			rootless:  true,
			ownerUID:  owner(1000),
			wantFatal: false,
			mustNot:   []RuntimeIssueCode{RuntimeIssueRootlessHostRootData, RuntimeIssueDataOwnerMismatch},
		},
		{
			name:              "rootless_non_root_uid_matching",
			overrides:         &v1.DockerOverrides{User: "1000:1000"},
			rootless:          true,
			ownerUID:          owner(1000),
			wantFatal:         false,
			wantRootStartDrop: boolPtr(false),
			mustNot:           []RuntimeIssueCode{RuntimeIssueRootlessHostRootData, RuntimeIssueDataOwnerMismatch},
		},
		{
			name:       "unpinned_image_warning",
			wantFatal:  false,
			wantPinned: boolPtr(false),
			mustHave:   []RuntimeIssueCode{RuntimeIssueImageUnpinned},
		},
		{
			name: "pinned_digest_no_unpinned_warning",
			mutate: func(spec *RuntimeSpec) {
				spec.Image = "itzg/minecraft-server:java21@" + matrixPinnedDigest
				spec.MinecraftImage = IsMinecraftImage(spec.Image)
			},
			wantFatal:  false,
			wantPinned: boolPtr(true),
			mustNot:    []RuntimeIssueCode{RuntimeIssueImageUnpinned},
		},
		{
			name:      "operator_privileged_is_warning",
			overrides: &v1.DockerOverrides{Privileged: true},
			wantFatal: false,
			mustHave:  []RuntimeIssueCode{RuntimeIssuePrivilegedMode},
		},
		{
			name:      "operator_replaced_security_opt_drops_nnp",
			overrides: &v1.DockerOverrides{SecurityOpt: []string{"seccomp=unconfined"}},
			wantFatal: false,
			mustHave:  []RuntimeIssueCode{RuntimeIssueNoNewPrivilegesDisabled},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := resolvedMinecraftContainer(tc.overrides, tc.dataRO)
			spec.DataOwnerUID = tc.ownerUID
			if tc.rootless {
				spec.Daemon = &DaemonEnforcement{
					Rootless:             true,
					CgroupVersion:        "2",
					MemoryLimitSupported: true,
					SwapLimitSupported:   true,
					PidsLimitSupported:   true,
				}
			}
			if tc.mutate != nil {
				tc.mutate(&spec)
			}

			beforePriv := spec.HostConfig.Privileged
			beforeNNP := hasNoNewPrivileges(spec.HostConfig.SecurityOpt)
			beforeRO := spec.HostConfig.ReadonlyRootfs

			profile := DescribeRuntime(spec)
			err := PreflightRuntime(spec)
			profileErr := PreflightProfile(profile)
			if (err == nil) != (profileErr == nil) {
				t.Fatalf("PreflightRuntime() = %v, PreflightProfile() = %v", err, profileErr)
			}

			assertQualificationDidNotBroadenPrivileges(t, spec, profile, beforePriv, beforeNNP, beforeRO)

			if tc.wantFatal {
				if !errors.Is(err, ErrRuntimeIncompatible) {
					t.Fatalf("PreflightRuntime() = %v, want ErrRuntimeIncompatible (issues=%v)", err, issueCodes(profile))
				}
				if !profile.Fatal() {
					t.Fatalf("profile fatal = false, want true; issues=%v", issueCodes(profile))
				}
			} else if err != nil {
				t.Fatalf("supported profile refused: %v (issues=%v)", err, issueCodes(profile))
			}

			for _, code := range tc.mustHave {
				if !hasIssue(profile, code) {
					t.Fatalf("issues = %v, want %s", issueCodes(profile), code)
				}
			}
			for _, code := range tc.mustNot {
				if hasIssue(profile, code) {
					t.Fatalf("issues = %v, must not include %s", issueCodes(profile), code)
				}
			}
			if tc.wantRootStartDrop != nil && profile.RootStartDropUser != *tc.wantRootStartDrop {
				t.Fatalf("RootStartDropUser = %t, want %t", profile.RootStartDropUser, *tc.wantRootStartDrop)
			}
			if tc.wantPinned != nil && profile.ImagePinned != *tc.wantPinned {
				t.Fatalf("ImagePinned = %t, want %t", profile.ImagePinned, *tc.wantPinned)
			}
		})
	}
}

// TestRuntimeQualificationMatrixLiveDaemonInfo attaches Engine /info to the
// same resolved request CreateContainer preflights. It does not start a game
// container. Skip only when Docker is absent; the table above always runs.
func TestRuntimeQualificationMatrixLiveDaemonInfo(t *testing.T) {
	log := logger.New()
	client, err := NewClient("", log)
	if err != nil {
		t.Skipf("docker client unavailable: %v", err)
	}
	ctx := context.Background()
	if _, err := client.Ping(ctx); err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	enforcement, err := client.DaemonEnforcement(ctx)
	if err != nil {
		t.Fatalf("DaemonEnforcement: %v", err)
	}

	spec := resolvedMinecraftContainer(nil, false)
	spec.Daemon = &enforcement
	owner := 0
	spec.DataOwnerUID = &owner

	beforePriv := spec.HostConfig.Privileged
	beforeNNP := hasNoNewPrivileges(spec.HostConfig.SecurityOpt)
	beforeRO := spec.HostConfig.ReadonlyRootfs

	profile := DescribeRuntime(spec)
	err = PreflightRuntime(spec)
	assertQualificationDidNotBroadenPrivileges(t, spec, profile, beforePriv, beforeNNP, beforeRO)

	if enforcement.Rootless {
		if !errors.Is(err, ErrRuntimeIncompatible) {
			t.Fatalf("rootless daemon with host-root /data: PreflightRuntime() = %v, want fatal", err)
		}
		if !hasIssue(profile, RuntimeIssueRootlessHostRootData) {
			t.Fatalf("issues = %v, want rootless_host_root_data", issueCodes(profile))
		}
		return
	}
	if err != nil {
		t.Fatalf("rootful live daemon refused the default resolved request: %v (issues=%v)", err, issueCodes(profile))
	}
}

func assertQualificationDidNotBroadenPrivileges(t *testing.T, spec RuntimeSpec, profile RuntimeProfile, privileged, noNewPrivileges, readOnlyRoot bool) {
	t.Helper()
	if spec.HostConfig.Privileged != privileged || profile.Privileged != privileged {
		t.Fatalf("qualification changed privileged: host=%t profile=%t before=%t", spec.HostConfig.Privileged, profile.Privileged, privileged)
	}
	if hasNoNewPrivileges(spec.HostConfig.SecurityOpt) != noNewPrivileges || profile.NoNewPrivileges != noNewPrivileges {
		t.Fatalf("qualification changed no-new-privileges: host=%t profile=%t before=%t", hasNoNewPrivileges(spec.HostConfig.SecurityOpt), profile.NoNewPrivileges, noNewPrivileges)
	}
	if spec.HostConfig.ReadonlyRootfs != readOnlyRoot || profile.ReadOnlyRootfs != readOnlyRoot {
		t.Fatalf("qualification changed root writability: hostRO=%t profileRO=%t beforeRO=%t", spec.HostConfig.ReadonlyRootfs, profile.ReadOnlyRootfs, readOnlyRoot)
	}
	if !privileged && (profile.Privileged || spec.HostConfig.Privileged) {
		t.Fatal("qualification auto-enabled privileged to recover")
	}
	if noNewPrivileges && (!profile.NoNewPrivileges || !hasNoNewPrivileges(spec.HostConfig.SecurityOpt)) {
		t.Fatal("qualification disabled no-new-privileges to recover")
	}
	if readOnlyRoot && (!profile.ReadOnlyRootfs || !spec.HostConfig.ReadonlyRootfs) {
		t.Fatal("qualification made the root writable to recover")
	}
}
