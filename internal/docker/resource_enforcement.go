// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// ResourceEnforcementView is the API/console view of requested versus
// effective limits. MemoryEnforced is false until /info qualification says
// the cap applies. WorldDiskEnforced is never set from storage-opt.
type ResourceEnforcementView struct {
	RequestedMemoryBytes     int64
	EffectiveMemoryBytes     int64
	RequestedWritableLayerMB int64
	EffectiveWritableLayerMB int64
	WorldDiskEnforced        bool
	WorldQuotaAdmission      bool
	MemoryEnforced           bool
	Warnings                 []string
}

// DescribeConfiguredResourceLimits builds a RuntimeProfile for the configured
// memory/writable-layer/world-quota request against optional daemon /info.
func DescribeConfiguredResourceLimits(requestedMemoryMB, writableLayerMB, worldQuotaMB, worldBytes int64, daemon *DaemonEnforcement) RuntimeProfile {
	hc := &container.HostConfig{}
	if requestedMemoryMB > 0 {
		mem := requestedMemoryMB << 20
		hc.Memory = mem
		hc.MemorySwap = mem
	}
	ApplyDiskQuota(hc, writableLayerMB)
	hc.Mounts = []mount.Mount{{
		Type:   mount.TypeBind,
		Source: "/srv/world",
		Target: "/data",
	}}
	return DescribeRuntime(RuntimeSpec{
		RequestedMemoryMB: requestedMemoryMB,
		WritableLayerMB:   writableLayerMB,
		WorldQuotaMB:      worldQuotaMB,
		WorldBytes:        worldBytes,
		WorldBytesKnown:   true,
		HostConfig:        hc,
		Daemon:            daemon,
	})
}

// ViewResourceEnforcement maps a runtime profile onto the console contract.
// daemonQualified is true only when Docker /info was read for this view.
func ViewResourceEnforcement(profile RuntimeProfile, daemonQualified bool, worldQuotaMB int64) ResourceEnforcementView {
	view := ResourceEnforcementView{
		RequestedMemoryBytes:     profile.Limits.RequestedMemoryBytes,
		EffectiveMemoryBytes:     profile.Limits.MemoryBytes,
		RequestedWritableLayerMB: profile.Limits.RequestedWritableLayerMB,
		EffectiveWritableLayerMB: profile.Limits.WritableLayerMB,
		WorldDiskEnforced:        profile.Limits.WorldDiskEnforced,
		WorldQuotaAdmission:      worldQuotaMB > 0,
		Warnings:                 profileWarningCodes(profile),
	}
	if daemonQualified && view.EffectiveMemoryBytes > 0 && !hasWarning(view.Warnings, string(RuntimeIssueMemoryLimitUnsupported)) && !hasWarning(view.Warnings, string(RuntimeIssueRootlessCgroupUnqualified)) {
		view.MemoryEnforced = true
	}
	return view
}

func profileWarningCodes(profile RuntimeProfile) []string {
	codes := make([]string, 0, len(profile.Issues))
	for _, issue := range profile.Issues {
		if !resourceWarningCode(issue.Code) {
			continue
		}
		codes = append(codes, string(issue.Code))
	}
	return codes
}

func resourceWarningCode(code RuntimeIssueCode) bool {
	switch code {
	case RuntimeIssueMemoryLimitUnset,
		RuntimeIssueMemoryLimitUnsupported,
		RuntimeIssueSwapEnabled,
		RuntimeIssueSwapLimitUnsupported,
		RuntimeIssuePidsLimitUnset,
		RuntimeIssuePidsLimitUnsupported,
		RuntimeIssueWritableLayerUnbounded,
		RuntimeIssueWritableLayerCapUnsupported,
		RuntimeIssueWritableLayerCapIgnored,
		RuntimeIssueStorageQuotaIsNotAWorldQuota,
		RuntimeIssueWorldQuotaExceeded,
		RuntimeIssueWorldQuotaUnmeasured,
		RuntimeIssueRootlessCgroupUnqualified:
		return true
	default:
		return false
	}
}

func hasWarning(codes []string, want string) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}
