// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"errors"
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// runtimeMatrixCase is one supported or unsupported effective profile. Each row
// builds the resolved container request the same way CreateContainer does and
// asserts the named qualification outcome (OSS-238 / D34–D36).
type runtimeMatrixCase struct {
	name       string
	overrides  *v1.DockerOverrides
	dataRO     bool
	ownerUID   *int
	rootless   bool
	// mutate adjusts the resolved spec after CreateContainer helpers run.
	mutate     func(*RuntimeSpec)
	wantFatal  bool
	mustHave   []RuntimeIssueCode
	mustNot    []RuntimeIssueCode
}

func TestRuntimeQualificationMatrix(t *testing.T) {
	owner := func(uid int) *int { return &uid }

	cases := []runtimeMatrixCase{
		{
			name:      "default_minecraft_rootful",
			wantFatal: false,
			mustNot:   []RuntimeIssueCode{RuntimeIssueDataOwnerMismatch, RuntimeIssueRootlessHostRootData},
		},
		{
			name:      "pinned_digest_no_unpinned_fatal",
			wantFatal: false,
			overrides: nil,
		},
		{
			name:      "non_root_matching_owner",
			overrides: &v1.DockerOverrides{User: "1000:1000"},
			ownerUID:  owner(1000),
			wantFatal: false,
			mustNot:   []RuntimeIssueCode{RuntimeIssueDataOwnerMismatch},
		},
		{
			name:      "non_root_owner_mismatch",
			overrides: &v1.DockerOverrides{User: "1000:1000"},
			ownerUID:  owner(0),
			wantFatal: true,
			mustHave:  []RuntimeIssueCode{RuntimeIssueDataOwnerMismatch},
		},
		{
			name:      "rootless_bootstrap_on_host_root_data",
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
			name:      "rootless_non_root_user_matching",
			overrides: &v1.DockerOverrides{User: "1000:1000"},
			rootless:  true,
			ownerUID:  owner(1000),
			wantFatal: false,
			mustNot:   []RuntimeIssueCode{RuntimeIssueRootlessHostRootData, RuntimeIssueDataOwnerMismatch},
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
			name: "cap_drop_all_bootstrap",
			mutate: func(spec *RuntimeSpec) {
				spec.HostConfig.CapAdd = nil
				spec.HostConfig.CapDrop = []string{"ALL"}
			},
			wantFatal: true,
			mustHave:  []RuntimeIssueCode{RuntimeIssueBootstrapCapabilitiesMissing},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := resolvedMinecraftContainer(tc.overrides, tc.dataRO)
			spec.DataOwnerUID = tc.ownerUID
			if tc.rootless {
				spec.Daemon = &DaemonEnforcement{Rootless: true, CgroupVersion: "2"}
			}
			if tc.mutate != nil {
				tc.mutate(&spec)
			}
			profile := DescribeRuntime(spec)
			err := PreflightProfile(profile)

			if tc.wantFatal {
				if !errors.Is(err, ErrRuntimeIncompatible) {
					t.Fatalf("PreflightProfile() = %v, want ErrRuntimeIncompatible", err)
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
		})
	}
}
