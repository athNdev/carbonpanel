// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/runtimeprofile"
	"github.com/moby/moby/api/types/container"
)

// The runtime profile and storage checks live in pkg/runtimeprofile so Cloud
// can import them. This package is the panel caller: it builds the spec from
// the server record and runs the public contract before the daemon is asked
// to create a container.

type (
	RuntimeIssueSeverity = runtimeprofile.RuntimeIssueSeverity
	RuntimeIssueCode     = runtimeprofile.RuntimeIssueCode
	RuntimeIssue         = runtimeprofile.RuntimeIssue
	RuntimeSpec          = runtimeprofile.RuntimeSpec
	ContainerLimits      = runtimeprofile.ContainerLimits
	RuntimeProfile       = runtimeprofile.RuntimeProfile
)

const (
	RuntimeFatal                             = runtimeprofile.RuntimeFatal
	RuntimeWarning                           = runtimeprofile.RuntimeWarning
	RuntimeIssueDataMountMissing             = runtimeprofile.RuntimeIssueDataMountMissing
	RuntimeIssueDataMountReadOnly            = runtimeprofile.RuntimeIssueDataMountReadOnly
	RuntimeIssueRootfsReadOnlyNsswitch       = runtimeprofile.RuntimeIssueRootfsReadOnlyNsswitch
	RuntimeIssueBootstrapCapabilitiesMissing = runtimeprofile.RuntimeIssueBootstrapCapabilitiesMissing
	RuntimeIssueNoNewPrivilegesDisabled      = runtimeprofile.RuntimeIssueNoNewPrivilegesDisabled
	RuntimeIssuePrivilegedMode               = runtimeprofile.RuntimeIssuePrivilegedMode
	RuntimeIssueWritableRootfs               = runtimeprofile.RuntimeIssueWritableRootfs
	RuntimeIssueNonNumericUser               = runtimeprofile.RuntimeIssueNonNumericUser
	RuntimeIssueCustomEntrypoint             = runtimeprofile.RuntimeIssueCustomEntrypoint
	RuntimeIssueTmpfsMissing                 = runtimeprofile.RuntimeIssueTmpfsMissing
	RuntimeIssueTmpfsTooSmall                = runtimeprofile.RuntimeIssueTmpfsTooSmall
	RuntimeIssueTmpfsExec                    = runtimeprofile.RuntimeIssueTmpfsExec
	RuntimeIssueHostNetwork                  = runtimeprofile.RuntimeIssueHostNetwork
	RuntimeIssueHostDevice                   = runtimeprofile.RuntimeIssueHostDevice
	RuntimeIssueDataOwnerMismatch            = runtimeprofile.RuntimeIssueDataOwnerMismatch
	RuntimeIssueMemoryLimitUnset             = runtimeprofile.RuntimeIssueMemoryLimitUnset
	RuntimeIssueSwapEnabled                  = runtimeprofile.RuntimeIssueSwapEnabled
	RuntimeIssuePidsLimitUnset               = runtimeprofile.RuntimeIssuePidsLimitUnset
	RuntimeIssueWritableLayerUnbounded       = runtimeprofile.RuntimeIssueWritableLayerUnbounded
	RuntimeIssueStorageQuotaIsNotAWorldQuota = runtimeprofile.RuntimeIssueStorageQuotaIsNotAWorldQuota
	RuntimeIssueWorldQuotaExceeded           = runtimeprofile.RuntimeIssueWorldQuotaExceeded
	RuntimeIssueWorldQuotaUnmeasured         = runtimeprofile.RuntimeIssueWorldQuotaUnmeasured
	RuntimeIssueWritableLayerCapIgnored      = runtimeprofile.RuntimeIssueWritableLayerCapIgnored
	RuntimeIssueMemoryLimitUnsupported       = runtimeprofile.RuntimeIssueMemoryLimitUnsupported
	RuntimeIssueSwapLimitUnsupported         = runtimeprofile.RuntimeIssueSwapLimitUnsupported
	RuntimeIssuePidsLimitUnsupported         = runtimeprofile.RuntimeIssuePidsLimitUnsupported
	RuntimeIssueRootlessCgroupUnqualified    = runtimeprofile.RuntimeIssueRootlessCgroupUnqualified
	RuntimeIssueWritableLayerCapUnsupported  = runtimeprofile.RuntimeIssueWritableLayerCapUnsupported
	RuntimeIssueImageUnpinned                = runtimeprofile.RuntimeIssueImageUnpinned
	RuntimeIssueRootlessHostRootData         = runtimeprofile.RuntimeIssueRootlessHostRootData
	RuntimeIssueReadOnlyRootUserMod          = runtimeprofile.RuntimeIssueReadOnlyRootUserMod
	RuntimeIssueSensitiveMount               = runtimeprofile.RuntimeIssueSensitiveMount
	RuntimeIssueTmpfsReplaced                = runtimeprofile.RuntimeIssueTmpfsReplaced
)

var ErrRuntimeIncompatible = runtimeprofile.ErrRuntimeIncompatible

func DescribeRuntime(spec RuntimeSpec) RuntimeProfile {
	return runtimeprofile.DescribeRuntime(spec)
}

func PreflightRuntime(spec RuntimeSpec) error {
	return runtimeprofile.PreflightRuntime(spec)
}

func PreflightProfile(profile RuntimeProfile) error {
	return runtimeprofile.PreflightProfile(profile)
}

func IsMinecraftImage(image string) bool {
	return runtimeprofile.IsMinecraftImage(image)
}

func hasNoNewPrivileges(opts []string) bool {
	return runtimeprofile.HasNoNewPrivileges(opts)
}

// runtimeSpecFor assembles the qualification input for a resolved container
// request, including the world directory's owner when the platform reports it.
func runtimeSpecFor(image, dataPath string, server *db.Server, config *container.Config, hostConfig *container.HostConfig) RuntimeSpec {
	spec := RuntimeSpec{
		Image:          image,
		DataPath:       dataPath,
		MinecraftImage: IsMinecraftImage(image),
		Config:         config,
		HostConfig:     hostConfig,
	}
	if uid, ok := dataOwnerUID(dataPath); ok {
		spec.DataOwnerUID = &uid
	}
	return spec
}
