// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"errors"
	"strings"
	"testing"

	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// resolvedMinecraftContainer builds the container request the same way
// CreateContainer does: defaults, then operator overrides, then the rootfs
// rules. Every test starts here so the assertions describe the configuration
// the daemon would actually receive rather than a helper's defaults.
func resolvedMinecraftContainer(overrides *v1.DockerOverrides, dataReadOnly bool) RuntimeSpec {
	image := "itzg/minecraft-server:java21"
	config := &container.Config{Image: image}
	pids := int64(512)
	memory := int64(2048 << 20)
	hostConfig := &container.HostConfig{
		Mounts: []mount.Mount{{
			Type:     mount.TypeBind,
			Source:   "/srv/worlds/world-a",
			Target:   "/data",
			ReadOnly: dataReadOnly,
		}},
		// The limits CreateContainer derives from the server record.
		Resources: container.Resources{Memory: memory, MemorySwap: memory},
	}
	applyContainerHardening(hostConfig)
	hostConfig.PidsLimit = &pids
	ApplyOverrides(overrides, config, hostConfig)
	applyServerRootfs(hostConfig, config, overrides)
	return RuntimeSpec{
		Image:          image,
		DataPath:       "/srv/worlds/world-a",
		MinecraftImage: true,
		Overrides:      overrides,
		Config:         config,
		HostConfig:     hostConfig,
	}
}

func issueCodes(profile RuntimeProfile) []string {
	codes := make([]string, 0, len(profile.Issues))
	for _, issue := range profile.Issues {
		codes = append(codes, string(issue.Code))
	}
	return codes
}

func hasIssue(profile RuntimeProfile, code RuntimeIssueCode) bool {
	for _, issue := range profile.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestPreflightAcceptsTheDefaultMinecraftContainer(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	profile := DescribeRuntime(spec)

	if err := PreflightProfile(profile); err != nil {
		t.Fatalf("default container refused: %v", err)
	}
	if profile.Fatal() {
		t.Fatalf("default container has fatal issues: %v", issueCodes(profile))
	}
	if !profile.ReadOnlyRootfs {
		t.Error("default root filesystem is writable")
	}
	if !profile.DataWritable {
		t.Error("default /data mount is not writable")
	}
	if !profile.NoNewPrivileges {
		t.Error("default container lost no-new-privileges")
	}
	if got := profile.Tmpfs["/tmp"]; !strings.Contains(got, "noexec") {
		t.Errorf("/tmp tmpfs = %q, want noexec", got)
	}
	if len(profile.CapAdd) == 0 {
		t.Error("default container has no bootstrap capabilities")
	}
	if len(profile.Issues) != 0 {
		t.Errorf("default container reported issues: %v", issueCodes(profile))
	}
}

func TestPreflightRejectsReadOnlyDataMount(t *testing.T) {
	// The resolver forces /data writable, so this guards the rule itself: a
	// future change that stops forcing it must still refuse the boot.
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.Mounts[0].ReadOnly = true
	profile := DescribeRuntime(spec)

	err := PreflightProfile(profile)
	if !errors.Is(err, ErrRuntimeIncompatible) {
		t.Fatalf("error = %v, want ErrRuntimeIncompatible", err)
	}
	if !strings.Contains(err.Error(), string(RuntimeIssueDataMountReadOnly)) {
		t.Fatalf("error = %v, want the data_mount_read_only reason", err)
	}
}

func TestPreflightRejectsMissingDataMount(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.Mounts = nil

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueDataMountMissing) {
		t.Fatalf("issues = %v, want data_mount_missing", issueCodes(profile))
	}
	if !profile.Fatal() {
		t.Fatal("a container with no /data mount is not fatal")
	}
}

func TestPreflightRejectsReadOnlyRootThatBlocksNsswitchRewrite(t *testing.T) {
	// Reproduce a stored row whose environment lost the skip flag while the
	// root stayed read-only: the entrypoint retries the rewrite and loops.
	spec := resolvedMinecraftContainer(nil, false)
	spec.Config.Env = nil

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueRootfsReadOnlyNsswitch) {
		t.Fatalf("issues = %v, want read_only_root_blocks_nsswitch", issueCodes(profile))
	}
	if err := PreflightProfile(profile); !errors.Is(err, ErrRuntimeIncompatible) {
		t.Fatalf("error = %v, want ErrRuntimeIncompatible", err)
	}
}

func TestPreflightRejectsBootstrapCapabilitiesRemoved(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.CapAdd = nil
	spec.HostConfig.CapDrop = []string{"ALL"}

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueBootstrapCapabilitiesMissing) {
		t.Fatalf("issues = %v, want bootstrap_capabilities_missing", issueCodes(profile))
	}
	if err := PreflightProfile(profile); !errors.Is(err, ErrRuntimeIncompatible) {
		t.Fatalf("error = %v, want ErrRuntimeIncompatible", err)
	}
}

func TestPreflightRejectsDataOwnerMismatch(t *testing.T) {
	spec := resolvedMinecraftContainer(&v1.DockerOverrides{User: "1000:1000"}, false)
	owner := 0
	spec.DataOwnerUID = &owner

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueDataOwnerMismatch) {
		t.Fatalf("issues = %v, want data_owner_mismatch", issueCodes(profile))
	}
	if !profile.Fatal() {
		t.Fatal("a uid that cannot write the world is not fatal")
	}
	if profile.RootStartDropUser {
		t.Fatal("a container with an explicit non-root user is still treated as root-then-drop")
	}
}

func TestPreflightAcceptsASupportedNonRootUser(t *testing.T) {
	spec := resolvedMinecraftContainer(&v1.DockerOverrides{User: "1000:1000"}, false)
	owner := 1000
	spec.DataOwnerUID = &owner

	profile := DescribeRuntime(spec)
	if err := PreflightProfile(profile); err != nil {
		t.Fatalf("supported non-root container refused: %v", err)
	}
	if hasIssue(profile, RuntimeIssueDataOwnerMismatch) {
		t.Fatalf("issues = %v, want no owner mismatch when the world is owned by the runtime user", issueCodes(profile))
	}
}

func TestPreflightWarnsWhenAnOverrideDropsNoNewPrivileges(t *testing.T) {
	overrides := &v1.DockerOverrides{SecurityOpt: []string{"seccomp=unconfined"}}
	profile := DescribeRuntime(resolvedMinecraftContainer(overrides, false))

	if !hasIssue(profile, RuntimeIssueNoNewPrivilegesDisabled) {
		t.Fatalf("issues = %v, want no_new_privileges_disabled", issueCodes(profile))
	}
	if profile.Fatal() {
		t.Fatalf("a confinement warning stopped creation: %v", issueCodes(profile))
	}
	if err := PreflightProfile(profile); err != nil {
		t.Fatalf("a warning must not refuse creation: %v", err)
	}
}

func TestPreflightWarnsOnSmallOrExecutableTmpfs(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.Tmpfs["/tmp"] = "rw,exec,size=8m"

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueTmpfsTooSmall) {
		t.Errorf("issues = %v, want tmpfs_too_small", issueCodes(profile))
	}
	if !hasIssue(profile, RuntimeIssueTmpfsExec) {
		t.Errorf("issues = %v, want tmpfs_exec", issueCodes(profile))
	}
	if profile.Fatal() {
		t.Fatalf("tmpfs warnings stopped creation: %v", issueCodes(profile))
	}
}

func TestPreflightWarnsWithoutTmpfsOnAReadOnlyRoot(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.Tmpfs = nil

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueTmpfsMissing) {
		t.Fatalf("issues = %v, want tmpfs_missing", issueCodes(profile))
	}
	if profile.Fatal() {
		t.Fatalf("a missing tmpfs stopped creation: %v", issueCodes(profile))
	}
}

func TestPreflightRejectsHostNetworkAndHostDevices(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.NetworkMode = "host"
	spec.HostConfig.Devices = append(spec.HostConfig.Devices, container.DeviceMapping{
		PathOnHost: "/dev/kvm", PathInContainer: "/dev/kvm", CgroupPermissions: "rwm",
	})

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueHostNetwork) {
		t.Errorf("issues = %v, want host_network", issueCodes(profile))
	}
	if !hasIssue(profile, RuntimeIssueHostDevice) {
		t.Errorf("issues = %v, want host_device", issueCodes(profile))
	}
	err := PreflightProfile(profile)
	if !errors.Is(err, ErrRuntimeIncompatible) {
		t.Fatalf("error = %v, want ErrRuntimeIncompatible", err)
	}
	if !strings.Contains(err.Error(), string(RuntimeIssueHostDevice)) {
		t.Errorf("error = %v, want the host device reason", err)
	}
}

func TestPreflightAcceptsAWritableRootfsForAnImageThatNeedsIt(t *testing.T) {
	overrides := &v1.DockerOverrides{
		Environment: map[string]string{writableRootfsEnv: "1"},
	}
	spec := resolvedMinecraftContainer(overrides, false)
	// A writable root removes the need for the nsswitch skip.
	spec.Config.Env = nil

	profile := DescribeRuntime(spec)
	if profile.ReadOnlyRootfs {
		t.Fatal("CARBON_WRITABLE_ROOTFS did not make the root writable")
	}
	if err := PreflightProfile(profile); err != nil {
		t.Fatalf("a supported writable-root image was refused: %v", err)
	}
	if !hasIssue(profile, RuntimeIssueWritableRootfs) {
		t.Errorf("issues = %v, want a writable_rootfs warning", issueCodes(profile))
	}
}

func TestDescribeRuntimeSummaryNamesTheEffectiveSettings(t *testing.T) {
	profile := DescribeRuntime(resolvedMinecraftContainer(nil, false))
	got := profile.Description()
	for _, want := range []string{"itzg/minecraft-server", "rootfs=read-only", "/srv/worlds/world-a (rw)", "nnp=true"} {
		if !strings.Contains(got, want) {
			t.Errorf("Description() = %q, want %q", got, want)
		}
	}
}

func TestIsMinecraftImage(t *testing.T) {
	cases := map[string]bool{
		"itzg/minecraft-server:java21":           true,
		"docker.io/itzg/minecraft-server:latest": true,
		"ITZG/Minecraft-Server:stable":           true,
		"itzg/minecraft-bedrock-server:latest":   false,
		"eclipse-temurin:21-jre":                 false,
		"":                                       false,
	}
	for image, want := range cases {
		if got := IsMinecraftImage(image); got != want {
			t.Errorf("IsMinecraftImage(%q) = %t, want %t", image, got, want)
		}
	}
}

func TestDescribeRuntimeReportsTheAppliedLimits(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.RequestedMemoryMB = 2048
	profile := DescribeRuntime(spec)

	if profile.Limits.MemoryBytes != 2048<<20 {
		t.Errorf("MemoryBytes = %d, want 2048 MB", profile.Limits.MemoryBytes)
	}
	if !profile.Limits.SwapDisabled {
		t.Error("SwapDisabled = false, want the 0-swap policy")
	}
	if profile.Limits.PidsLimit != 512 {
		t.Errorf("PidsLimit = %d, want 512", profile.Limits.PidsLimit)
	}
	if profile.Limits.WritableLayerMB != 0 {
		t.Errorf("WritableLayerMB = %d, want 0 on a read-only root", profile.Limits.WritableLayerMB)
	}
	if profile.Limits.WorldDiskEnforced {
		t.Error("WorldDiskEnforced = true, want false: a bind-mounted world is never quota-limited by the daemon")
	}
	if hasIssue(profile, RuntimeIssueMemoryLimitUnset) || hasIssue(profile, RuntimeIssuePidsLimitUnset) {
		t.Errorf("issues = %v, want the applied limits to be recognised", issueCodes(profile))
	}
}

func TestPreflightWarnsWhenTheMemoryLimitIsMissing(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.Memory = 0
	spec.HostConfig.MemorySwap = 0

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueMemoryLimitUnset) {
		t.Fatalf("issues = %v, want memory_limit_unset", issueCodes(profile))
	}
	if profile.Fatal() {
		t.Fatalf("an unlimited container stopped creation: %v", issueCodes(profile))
	}
}

func TestPreflightWarnsWhenSwapIsReEnabled(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.MemorySwap = 4096 << 20

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueSwapEnabled) {
		t.Fatalf("issues = %v, want swap_enabled", issueCodes(profile))
	}
	if profile.Fatal() {
		t.Fatalf("an overridden swap policy stopped creation: %v", issueCodes(profile))
	}
}

func TestPreflightWarnsWhenThePidsLimitIsMissing(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.HostConfig.PidsLimit = nil

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssuePidsLimitUnset) {
		t.Fatalf("issues = %v, want pids_limit_unset", issueCodes(profile))
	}
}

func TestPreflightSaysAWritableLayerCapDoesNotBoundTheWorld(t *testing.T) {
	spec := resolvedMinecraftContainer(&v1.DockerOverrides{
		Environment: map[string]string{writableRootfsEnv: "1"},
	}, false)
	ApplyDiskQuota(spec.HostConfig, 1024)
	spec.WritableLayerMB = 1024

	profile := DescribeRuntime(spec)
	if profile.Limits.WritableLayerMB != 1024 {
		t.Fatalf("WritableLayerMB = %d, want 1024", profile.Limits.WritableLayerMB)
	}
	if !hasIssue(profile, RuntimeIssueStorageQuotaIsNotAWorldQuota) {
		t.Fatalf("issues = %v, want storage_quota_is_not_a_world_quota", issueCodes(profile))
	}
	if hasIssue(profile, RuntimeIssueWritableLayerUnbounded) {
		t.Fatalf("issues = %v, want the cap to be recognised for the writable layer", issueCodes(profile))
	}
	if profile.Limits.WorldDiskEnforced {
		t.Error("WorldDiskEnforced = true, want false")
	}
}

func TestPreflightWarnsThatAWritableLayerCapIsIgnoredOnAReadOnlyRoot(t *testing.T) {
	spec := resolvedMinecraftContainer(nil, false)
	spec.WritableLayerMB = 1024

	profile := DescribeRuntime(spec)
	if !hasIssue(profile, RuntimeIssueWritableLayerCapIgnored) {
		t.Fatalf("issues = %v, want writable_layer_cap_ignored", issueCodes(profile))
	}
	if profile.Limits.WritableLayerMB != 0 {
		t.Errorf("WritableLayerMB = %d, want 0 when the root is read-only", profile.Limits.WritableLayerMB)
	}
}
